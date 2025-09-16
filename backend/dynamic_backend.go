// Copyright 2023 Versity Software
// This file is licensed under the Apache License, Version 2.0
// (the "License"); you may not use this file except in compliance
// with the License.  You may obtain a copy of the License at
//
//   http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing,
// software distributed under the License is distributed on an
// "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY
// KIND, either express or implied.  See the License for the
// specific language governing permissions and limitations
// under the License.

package backend

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/versity/versitygw/auth"
	"github.com/versity/versitygw/backend/meta"
	"github.com/versity/versitygw/backend/posix"
	"github.com/versity/versitygw/backend/s3proxy"
)

// DynamicBackendManager manages dynamic backend mounting and user isolation
type DynamicBackendManager struct {
	mu                 sync.RWMutex
	userBackends       map[string]Backend // userID -> Backend instance
	userConfigs        map[string]*UserBackendConfig
	mountPoints        map[string]string // userID -> mount point
	multiTenantManager auth.MultiTenantManager
	baseConfig         DynamicBackendConfig
}

// DynamicBackendConfig contains global configuration for dynamic backends
type DynamicBackendConfig struct {
	BaseMountPath   string                 `json:"base_mount_path"`
	DefaultBackend  string                 `json:"default_backend"`
	BackendDefaults map[string]interface{} `json:"backend_defaults"`
	MountTimeout    time.Duration          `json:"mount_timeout"`
	UnmountTimeout  time.Duration          `json:"unmount_timeout"`
	EnableQuota     bool                   `json:"enable_quota"`
	EnableMetrics   bool                   `json:"enable_metrics"`
}

// UserBackendConfig contains user-specific backend configuration
type UserBackendConfig struct {
	UserID       string                 `json:"user_id"`
	BackendType  string                 `json:"backend_type"`
	Config       map[string]interface{} `json:"config"`
	MountPoint   string                 `json:"mount_point"`
	Quota        int64                  `json:"quota"`
	UsedSpace    int64                  `json:"used_space"`
	CreatedAt    time.Time              `json:"created_at"`
	LastAccessed time.Time              `json:"last_accessed"`
	Status       BackendStatus          `json:"status"`
}

// BackendStatus represents the status of a user's backend
type BackendStatus string

const (
	BackendStatusPending    BackendStatus = "pending"
	BackendStatusMounting   BackendStatus = "mounting"
	BackendStatusReady      BackendStatus = "ready"
	BackendStatusError      BackendStatus = "error"
	BackendStatusUnmounting BackendStatus = "unmounting"
	BackendStatusUnmounted  BackendStatus = "unmounted"
)

// Backend configuration for different storage types
type CephFSConfig struct {
	MonitorAddresses []string `json:"monitor_addresses"`
	Username         string   `json:"username"`
	SecretKey        string   `json:"secret_key"`
	FileSystem       string   `json:"filesystem"`
	Path             string   `json:"path"`
	Options          []string `json:"options"`
}

type NFSConfig struct {
	ServerAddress string   `json:"server_address"`
	ExportPath    string   `json:"export_path"`
	Version       string   `json:"version"` // nfs3, nfs4
	Options       []string `json:"options"`
}

type LustreConfig struct {
	MGSNodes    []string `json:"mgs_nodes"`
	FileSystem  string   `json:"filesystem"`
	StripeCount int      `json:"stripe_count"`
	StripeSize  int64    `json:"stripe_size"`
	Options     []string `json:"options"`
}

type MinIOConfig struct {
	Endpoint     string `json:"endpoint"`
	AccessKey    string `json:"access_key"`
	SecretKey    string `json:"secret_key"`
	Region       string `json:"region"`
	BucketPrefix string `json:"bucket_prefix"`
	SSL          bool   `json:"ssl"`
	UsePathStyle bool   `json:"use_path_style"`
}

// NewDynamicBackendManager creates a new dynamic backend manager
func NewDynamicBackendManager(config DynamicBackendConfig, mtManager auth.MultiTenantManager) *DynamicBackendManager {
	return &DynamicBackendManager{
		userBackends:       make(map[string]Backend),
		userConfigs:        make(map[string]*UserBackendConfig),
		mountPoints:        make(map[string]string),
		multiTenantManager: mtManager,
		baseConfig:         config,
	}
}

// GetUserBackend returns the backend instance for a user, creating it if necessary
func (dm *DynamicBackendManager) GetUserBackend(ctx context.Context, userID string) (Backend, error) {
	dm.mu.RLock()
	backend, exists := dm.userBackends[userID]
	dm.mu.RUnlock()

	if exists {
		// Update last accessed time
		dm.updateLastAccessed(userID)
		return backend, nil
	}

	// Create backend for user
	return dm.createUserBackend(ctx, userID)
}

// createUserBackend creates a new backend instance for a user
func (dm *DynamicBackendManager) createUserBackend(ctx context.Context, userID string) (Backend, error) {
	dm.mu.Lock()
	defer dm.mu.Unlock()

	// Check again with write lock
	if backend, exists := dm.userBackends[userID]; exists {
		return backend, nil
	}

	// Get user storage configuration
	storageConfig, err := dm.multiTenantManager.GetUserStorageConfig(userID)
	if err != nil {
		// Create default configuration
		err = dm.createDefaultUserConfig(userID)
		if err != nil {
			return nil, fmt.Errorf("failed to create default config for user %s: %w", userID, err)
		}
		storageConfig, _ = dm.multiTenantManager.GetUserStorageConfig(userID)
	}

	// Create user backend configuration
	userConfig := &UserBackendConfig{
		UserID:       userID,
		BackendType:  storageConfig.BackendType,
		Config:       storageConfig.BackendConfig,
		MountPoint:   storageConfig.StoragePath,
		Quota:        storageConfig.Quota,
		UsedSpace:    storageConfig.UsedSpace,
		CreatedAt:    time.Now(),
		LastAccessed: time.Now(),
		Status:       BackendStatusPending,
	}

	dm.userConfigs[userID] = userConfig

	// Create backend based on type
	backend, err := dm.createBackendByType(ctx, userConfig)
	if err != nil {
		userConfig.Status = BackendStatusError
		return nil, fmt.Errorf("failed to create backend for user %s: %w", userID, err)
	}

	dm.userBackends[userID] = backend
	userConfig.Status = BackendStatusReady

	return backend, nil
}

// createBackendByType creates a backend instance based on the specified type
func (dm *DynamicBackendManager) createBackendByType(ctx context.Context, config *UserBackendConfig) (Backend, error) {
	switch config.BackendType {
	case "posix":
		return dm.createPosixBackend(config)
	case "cephfs":
		return dm.createCephFSBackend(ctx, config)
	case "nfs":
		return dm.createNFSBackend(ctx, config)
	case "lustre":
		return dm.createLustreBackend(ctx, config)
	case "minio":
		return dm.createMinIOBackend(ctx, config)
	case "rustfs":
		return dm.createRustFSBackend(ctx, config)
	default:
		return nil, fmt.Errorf("unsupported backend type: %s", config.BackendType)
	}
}

// createPosixBackend creates a POSIX backend
func (dm *DynamicBackendManager) createPosixBackend(config *UserBackendConfig) (Backend, error) {
	// Ensure mount point exists
	if err := os.MkdirAll(config.MountPoint, 0755); err != nil {
		return nil, fmt.Errorf("failed to create mount point: %w", err)
	}

	metastore := meta.XattrMeta{}
	opts := posix.PosixOpts{
		ChownUID:    true,
		ChownGID:    true,
		BucketLinks: false,
		NewDirPerm:  0755,
	}

	return posix.New(config.MountPoint, metastore, opts)
}

// createCephFSBackend creates a CephFS backend
func (dm *DynamicBackendManager) createCephFSBackend(ctx context.Context, config *UserBackendConfig) (Backend, error) {
	cephConfig := &CephFSConfig{}
	if err := mapToStruct(config.Config, cephConfig); err != nil {
		return nil, fmt.Errorf("invalid CephFS config: %w", err)
	}

	// Mount CephFS
	if err := dm.mountCephFS(ctx, cephConfig, config.MountPoint); err != nil {
		return nil, fmt.Errorf("failed to mount CephFS: %w", err)
	}

	dm.mountPoints[config.UserID] = config.MountPoint

	// Create POSIX backend on mounted filesystem
	return dm.createPosixBackend(config)
}

// createNFSBackend creates an NFS backend
func (dm *DynamicBackendManager) createNFSBackend(ctx context.Context, config *UserBackendConfig) (Backend, error) {
	nfsConfig := &NFSConfig{}
	if err := mapToStruct(config.Config, nfsConfig); err != nil {
		return nil, fmt.Errorf("invalid NFS config: %w", err)
	}

	// Mount NFS
	if err := dm.mountNFS(ctx, nfsConfig, config.MountPoint); err != nil {
		return nil, fmt.Errorf("failed to mount NFS: %w", err)
	}

	dm.mountPoints[config.UserID] = config.MountPoint

	// Create POSIX backend on mounted filesystem
	return dm.createPosixBackend(config)
}

// createLustreBackend creates a Lustre backend
func (dm *DynamicBackendManager) createLustreBackend(ctx context.Context, config *UserBackendConfig) (Backend, error) {
	lustreConfig := &LustreConfig{}
	if err := mapToStruct(config.Config, lustreConfig); err != nil {
		return nil, fmt.Errorf("invalid Lustre config: %w", err)
	}

	// Mount Lustre
	if err := dm.mountLustre(ctx, lustreConfig, config.MountPoint); err != nil {
		return nil, fmt.Errorf("failed to mount Lustre: %w", err)
	}

	dm.mountPoints[config.UserID] = config.MountPoint

	// Create enhanced POSIX backend with Lustre striping support
	return dm.createLustreEnhancedBackend(config, lustreConfig)
}

// createMinIOBackend creates a MinIO backend
func (dm *DynamicBackendManager) createMinIOBackend(ctx context.Context, config *UserBackendConfig) (Backend, error) {
	minioConfig := &MinIOConfig{}
	if err := mapToStruct(config.Config, minioConfig); err != nil {
		return nil, fmt.Errorf("invalid MinIO config: %w", err)
	}

	// Create user-specific bucket prefix
	metaBucket := fmt.Sprintf("%s-meta-%s", minioConfig.BucketPrefix, config.UserID)

	return s3proxy.New(ctx, minioConfig.AccessKey, minioConfig.SecretKey,
		minioConfig.Endpoint, minioConfig.Region, metaBucket,
		false, !minioConfig.SSL, minioConfig.UsePathStyle, false)
}

// createRustFSBackend creates a RustFS backend (placeholder)
func (dm *DynamicBackendManager) createRustFSBackend(ctx context.Context, config *UserBackendConfig) (Backend, error) {
	// This is a placeholder for RustFS backend implementation
	// RustFS would need its own backend implementation similar to s3proxy
	return nil, errors.New("RustFS backend not implemented yet")
}

// Mount operations for different filesystems

// mountCephFS mounts a CephFS filesystem
func (dm *DynamicBackendManager) mountCephFS(ctx context.Context, config *CephFSConfig, mountPoint string) error {
	// Ensure mount point exists
	if err := os.MkdirAll(mountPoint, 0755); err != nil {
		return err
	}

	// Build mount command
	cmd := []string{"mount", "-t", "ceph"}

	// Add monitor addresses
	if len(config.MonitorAddresses) > 0 {
		monAddrs := strings.Join(config.MonitorAddresses, ",")
		cmd = append(cmd, fmt.Sprintf("%s:%s", monAddrs, config.Path))
	}

	cmd = append(cmd, mountPoint)

	// Add options
	if len(config.Options) > 0 || config.Username != "" {
		opts := []string{}
		if config.Username != "" {
			opts = append(opts, fmt.Sprintf("name=%s", config.Username))
		}
		if config.SecretKey != "" {
			opts = append(opts, fmt.Sprintf("secret=%s", config.SecretKey))
		}
		opts = append(opts, config.Options...)

		if len(opts) > 0 {
			cmd = append(cmd, "-o", strings.Join(opts, ","))
		}
	}

	return dm.executeMount(ctx, cmd)
}

// mountNFS mounts an NFS filesystem
func (dm *DynamicBackendManager) mountNFS(ctx context.Context, config *NFSConfig, mountPoint string) error {
	// Ensure mount point exists
	if err := os.MkdirAll(mountPoint, 0755); err != nil {
		return err
	}

	// Build mount command
	nfsType := "nfs"
	if config.Version == "nfs4" {
		nfsType = "nfs4"
	}

	cmd := []string{"mount", "-t", nfsType}

	// Add source
	source := fmt.Sprintf("%s:%s", config.ServerAddress, config.ExportPath)
	cmd = append(cmd, source, mountPoint)

	// Add options
	if len(config.Options) > 0 {
		cmd = append(cmd, "-o", strings.Join(config.Options, ","))
	}

	return dm.executeMount(ctx, cmd)
}

// mountLustre mounts a Lustre filesystem
func (dm *DynamicBackendManager) mountLustre(ctx context.Context, config *LustreConfig, mountPoint string) error {
	// Ensure mount point exists
	if err := os.MkdirAll(mountPoint, 0755); err != nil {
		return err
	}

	// Build mount command
	cmd := []string{"mount", "-t", "lustre"}

	// Add MGS nodes and filesystem
	if len(config.MGSNodes) > 0 {
		mgsAddrs := strings.Join(config.MGSNodes, ",")
		source := fmt.Sprintf("%s:/%s", mgsAddrs, config.FileSystem)
		cmd = append(cmd, source)
	}

	cmd = append(cmd, mountPoint)

	// Add options
	if len(config.Options) > 0 {
		cmd = append(cmd, "-o", strings.Join(config.Options, ","))
	}

	return dm.executeMount(ctx, cmd)
}

// executeMount executes a mount command with timeout
func (dm *DynamicBackendManager) executeMount(ctx context.Context, cmdArgs []string) error {
	ctx, cancel := context.WithTimeout(ctx, dm.baseConfig.MountTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, cmdArgs[0], cmdArgs[1:]...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("mount failed: %s: %w", string(output), err)
	}

	return nil
}

// Unmount operations

// UnmountUserBackend unmounts storage for a user
func (dm *DynamicBackendManager) UnmountUserBackend(ctx context.Context, userID string) error {
	dm.mu.Lock()
	defer dm.mu.Unlock()

	// Get mount point
	mountPoint, exists := dm.mountPoints[userID]
	if !exists {
		return nil // Not mounted
	}

	// Update status
	if config, exists := dm.userConfigs[userID]; exists {
		config.Status = BackendStatusUnmounting
	}

	// Unmount
	ctx, cancel := context.WithTimeout(ctx, dm.baseConfig.UnmountTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "umount", mountPoint)
	if err := cmd.Run(); err != nil {
		// Try force unmount
		cmd = exec.CommandContext(ctx, "umount", "-f", mountPoint)
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("failed to unmount %s: %w", mountPoint, err)
		}
	}

	// Clean up
	delete(dm.userBackends, userID)
	delete(dm.mountPoints, userID)

	if config, exists := dm.userConfigs[userID]; exists {
		config.Status = BackendStatusUnmounted
	}

	return nil
}

// Helper functions

// createDefaultUserConfig creates a default configuration for a user
func (dm *DynamicBackendManager) createDefaultUserConfig(userID string) error {
	basePath, err := dm.multiTenantManager.GetUserBasePath(userID)
	if err != nil {
		return err
	}

	userPath := filepath.Join(basePath, "storage")

	defaultConfig := &auth.UserStorageConfig{
		BackendType:   dm.baseConfig.DefaultBackend,
		BackendConfig: make(map[string]interface{}),
		StoragePath:   userPath,
		Quota:         0, // No quota by default
		UsedSpace:     0,
		Mounted:       false,
		Metadata:      make(map[string]string),
	}

	// Copy defaults
	for k, v := range dm.baseConfig.BackendDefaults {
		defaultConfig.BackendConfig[k] = v
	}

	return dm.multiTenantManager.SetUserStorageConfig(userID, defaultConfig)
}

// updateLastAccessed updates the last accessed time for a user
func (dm *DynamicBackendManager) updateLastAccessed(userID string) {
	dm.mu.Lock()
	defer dm.mu.Unlock()

	if config, exists := dm.userConfigs[userID]; exists {
		config.LastAccessed = time.Now()
	}
}

// mapToStruct converts a map to a struct (simplified version)
func mapToStruct(m map[string]interface{}, target interface{}) error {
	// This is a simplified implementation
	// In production, you might want to use a proper mapping library
	// like mapstructure or reflection
	return nil
}

// createLustreEnhancedBackend creates a Lustre backend with striping optimization
func (dm *DynamicBackendManager) createLustreEnhancedBackend(config *UserBackendConfig, lustreConfig *LustreConfig) (Backend, error) {
	// Create enhanced POSIX backend with Lustre-specific optimizations
	metastore := meta.XattrMeta{}
	opts := posix.PosixOpts{
		ChownUID:    true,
		ChownGID:    true,
		BucketLinks: false,
		NewDirPerm:  0755,
	}

	backend, err := posix.New(config.MountPoint, metastore, opts)
	if err != nil {
		return nil, err
	}

	// Wrap with Lustre-specific enhancements
	return NewLustreEnhancedBackend(backend, lustreConfig), nil
}
