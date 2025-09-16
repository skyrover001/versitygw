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

package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/urfave/cli/v2"
	"github.com/versity/versitygw/auth"
	"github.com/versity/versitygw/backend"
	"github.com/versity/versitygw/config"
)

var (
	// Multi-tenant specific flags
	multiTenantEnabled   bool
	multiTenantConfigDir string
	multiTenantBasePath  string
	defaultBackendType   string
	enableDynamicMount   bool
	enableUserIsolation  bool
	maxConcurrentUsers   int
	userIdleTimeout      string
)

// multiTenantCommand creates the multi-tenant command
func multiTenantCommand() *cli.Command {
	return &cli.Command{
		Name:  "multitenant",
		Usage: "run S3 gateway with multi-tenant support",
		Description: `This runs the S3 gateway with advanced multi-tenant capabilities including:
- User isolation with dedicated storage backends
- Dynamic mounting of CephFS, NFS, Lustre, and other storage systems  
- Per-user quota and bandwidth management
- Lustre striping optimization for parallel I/O
- Support for MinIO and other object storage backends`,
		Action: runMultiTenant,
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:        "config-dir",
				Usage:       "multi-tenant configuration directory",
				EnvVars:     []string{"VGW_MT_CONFIG_DIR"},
				Value:       "/etc/versitygw/multitenant",
				Destination: &multiTenantConfigDir,
			},
			&cli.StringFlag{
				Name:        "base-path",
				Usage:       "base path for user storage mounts",
				EnvVars:     []string{"VGW_MT_BASE_PATH"},
				Value:       "/var/lib/versitygw/mounts",
				Destination: &multiTenantBasePath,
			},
			&cli.StringFlag{
				Name:        "default-backend",
				Usage:       "default backend type for new users (posix, cephfs, nfs, lustre, minio)",
				EnvVars:     []string{"VGW_MT_DEFAULT_BACKEND"},
				Value:       "posix",
				Destination: &defaultBackendType,
			},
			&cli.BoolFlag{
				Name:        "enable-dynamic-mount",
				Usage:       "enable dynamic mounting of storage backends",
				EnvVars:     []string{"VGW_MT_DYNAMIC_MOUNT"},
				Value:       true,
				Destination: &enableDynamicMount,
			},
			&cli.BoolFlag{
				Name:        "enable-user-isolation",
				Usage:       "enable strict user isolation",
				EnvVars:     []string{"VGW_MT_USER_ISOLATION"},
				Value:       true,
				Destination: &enableUserIsolation,
			},
			&cli.IntFlag{
				Name:        "max-concurrent-users",
				Usage:       "maximum number of concurrent users",
				EnvVars:     []string{"VGW_MT_MAX_USERS"},
				Value:       1000,
				Destination: &maxConcurrentUsers,
			},
			&cli.StringFlag{
				Name:        "user-idle-timeout",
				Usage:       "timeout for unmounting idle user storage",
				EnvVars:     []string{"VGW_MT_IDLE_TIMEOUT"},
				Value:       "30m",
				Destination: &userIdleTimeout,
			},
		},
	}
}

// runMultiTenant runs the multi-tenant S3 gateway
func runMultiTenant(ctx *cli.Context) error {
	fmt.Println("Starting Versity S3 Gateway with Multi-Tenant Support")
	fmt.Printf("Config Directory: %s\n", multiTenantConfigDir)
	fmt.Printf("Base Mount Path: %s\n", multiTenantBasePath)
	fmt.Printf("Default Backend: %s\n", defaultBackendType)
	fmt.Printf("Dynamic Mount: %v\n", enableDynamicMount)
	fmt.Printf("User Isolation: %v\n", enableUserIsolation)

	// Initialize configuration manager
	configManager := config.NewConfigManager(multiTenantConfigDir)

	// Load or create global configuration
	if err := configManager.LoadGlobalConfig(); err != nil {
		return fmt.Errorf("failed to load global config: %w", err)
	}

	globalConfig := configManager.GetGlobalConfig()
	fmt.Printf("Multi-tenant mode: %v\n", globalConfig.Enabled)

	// Initialize multi-tenant manager
	mtConfig := auth.MultiTenantConfig{
		Enabled:            globalConfig.Enabled,
		DefaultBackendType: defaultBackendType,
		BasePath:           multiTenantBasePath,
		DefaultQuota:       globalConfig.Defaults.StorageQuota,
	}

	// Create backend factory
	backendFactory := NewMultiTenantBackendFactory(configManager)

	// Initialize multi-tenant manager
	mtManager := auth.NewMultiTenantManager(mtConfig, backendFactory)

	// Initialize dynamic backend manager
	dynamicConfig := backend.DynamicBackendConfig{
		BaseMountPath:   multiTenantBasePath,
		DefaultBackend:  defaultBackendType,
		BackendDefaults: globalConfig.Defaults.BackendConfig,
		MountTimeout:    globalConfig.ResourceLimits.MountTimeout,
		UnmountTimeout:  globalConfig.ResourceLimits.UnmountTimeout,
		EnableQuota:     true,
		EnableMetrics:   globalConfig.Monitoring.EnableMetrics,
	}

	dynamicManager := backend.NewDynamicBackendManager(dynamicConfig, mtManager)

	// Create multi-tenant backend wrapper
	mtBackend := NewMultiTenantBackend(dynamicManager, mtManager, configManager)

	// Initialize IAM with multi-tenant support
	iamOpts := &auth.Opts{
		RootAccount: auth.Account{
			Access: rootUserAccess,
			Secret: rootUserSecret,
			Role:   auth.RoleAdmin,
		},
		Dir: iamDir,
		// Add other IAM configuration as needed
	}

	iam, err := auth.New(iamOpts)
	if err != nil {
		return fmt.Errorf("failed to initialize IAM: %w", err)
	}

	// Create enhanced IAM service with multi-tenant support
	enhancedIAM := NewMultiTenantIAMService(iam, mtManager, configManager)

	// Run the gateway with multi-tenant backend
	return runGateway(ctx.Context, mtBackend, enhancedIAM)
}

// MultiTenantBackendFactory creates backends for multi-tenant environment
type MultiTenantBackendFactory struct {
	configManager *config.ConfigManager
}

// NewMultiTenantBackendFactory creates a new backend factory
func NewMultiTenantBackendFactory(configManager *config.ConfigManager) *MultiTenantBackendFactory {
	return &MultiTenantBackendFactory{
		configManager: configManager,
	}
}

// CreateBackend creates a backend instance based on type and configuration
func (f *MultiTenantBackendFactory) CreateBackend(backendType string, config map[string]interface{}) (interface{}, error) {
	template, err := f.configManager.GetBackendTemplate(backendType)
	if err != nil {
		return nil, fmt.Errorf("backend template not found: %w", err)
	}

	if !template.Enabled {
		return nil, fmt.Errorf("backend type %s is disabled", backendType)
	}

	// Merge template config with user config
	mergedConfig := make(map[string]interface{})
	for k, v := range template.Config {
		mergedConfig[k] = v
	}
	for k, v := range config {
		mergedConfig[k] = v
	}

	switch backendType {
	case "posix":
		return f.createPosixBackend(mergedConfig)
	case "cephfs":
		return f.createCephFSBackend(mergedConfig)
	case "nfs":
		return f.createNFSBackend(mergedConfig)
	case "lustre":
		return f.createLustreBackend(mergedConfig)
	case "minio":
		return f.createMinIOBackend(mergedConfig)
	default:
		return nil, fmt.Errorf("unsupported backend type: %s", backendType)
	}
}

// Backend creation methods (simplified implementations)
func (f *MultiTenantBackendFactory) createPosixBackend(config map[string]interface{}) (interface{}, error) {
	// Implementation would create and configure a POSIX backend
	return nil, fmt.Errorf("POSIX backend creation not implemented")
}

func (f *MultiTenantBackendFactory) createCephFSBackend(config map[string]interface{}) (interface{}, error) {
	// Implementation would create and configure a CephFS backend
	return nil, fmt.Errorf("CephFS backend creation not implemented")
}

func (f *MultiTenantBackendFactory) createNFSBackend(config map[string]interface{}) (interface{}, error) {
	// Implementation would create and configure an NFS backend
	return nil, fmt.Errorf("NFS backend creation not implemented")
}

func (f *MultiTenantBackendFactory) createLustreBackend(config map[string]interface{}) (interface{}, error) {
	// Implementation would create and configure a Lustre backend with striping
	return nil, fmt.Errorf("Lustre backend creation not implemented")
}

func (f *MultiTenantBackendFactory) createMinIOBackend(config map[string]interface{}) (interface{}, error) {
	// Implementation would create and configure a MinIO backend
	return nil, fmt.Errorf("MinIO backend creation not implemented")
}

// MultiTenantBackend wraps backend operations with multi-tenant logic
type MultiTenantBackend struct {
	dynamicManager *backend.DynamicBackendManager
	mtManager      auth.MultiTenantManager
	configManager  *config.ConfigManager
}

// NewMultiTenantBackend creates a new multi-tenant backend wrapper
func NewMultiTenantBackend(
	dynamicManager *backend.DynamicBackendManager,
	mtManager auth.MultiTenantManager,
	configManager *config.ConfigManager,
) *MultiTenantBackend {
	return &MultiTenantBackend{
		dynamicManager: dynamicManager,
		mtManager:      mtManager,
		configManager:  configManager,
	}
}

// MultiTenantIAMService enhances IAM with multi-tenant support
type MultiTenantIAMService struct {
	baseIAM       auth.IAMService
	mtManager     auth.MultiTenantManager
	configManager *config.ConfigManager
}

// NewMultiTenantIAMService creates an enhanced IAM service
func NewMultiTenantIAMService(
	baseIAM auth.IAMService,
	mtManager auth.MultiTenantManager,
	configManager *config.ConfigManager,
) *MultiTenantIAMService {
	return &MultiTenantIAMService{
		baseIAM:       baseIAM,
		mtManager:     mtManager,
		configManager: configManager,
	}
}

// GetUserAccount retrieves user account with multi-tenant enhancements
func (m *MultiTenantIAMService) GetUserAccount(access string) (auth.Account, error) {
	// Get base account from IAM
	account, err := m.baseIAM.GetUserAccount(access)
	if err != nil {
		return account, err
	}

	// Load user configuration if available
	userConfig, err := m.configManager.LoadUserConfig(access)
	if err != nil {
		// User doesn't have multi-tenant config yet, create default
		tenantID := auth.GetTenantID(access)
		userConfig, err = m.configManager.CreateUserConfig(access, tenantID, "posix")
		if err != nil {
			log.Printf("Warning: Failed to create user config for %s: %v", access, err)
			return account, nil
		}

		if err := m.configManager.SaveUserConfig(userConfig); err != nil {
			log.Printf("Warning: Failed to save user config for %s: %v", access, err)
		}
	}

	// Update last accessed time
	if err := m.configManager.UpdateUserStatus(access, "active"); err != nil {
		log.Printf("Warning: Failed to update user status for %s: %v", access, err)
	}

	return account, nil
}

// CreateAccount creates a new account with multi-tenant setup
func (m *MultiTenantIAMService) CreateAccount(account auth.Account) error {
	// Create base account
	if err := m.baseIAM.CreateAccount(account); err != nil {
		return err
	}

	// Create multi-tenant configuration
	tenantID := auth.GetTenantID(account.Access)
	globalConfig := m.configManager.GetGlobalConfig()

	userConfig, err := m.configManager.CreateUserConfig(
		account.Access,
		tenantID,
		globalConfig.Defaults.BackendType,
	)
	if err != nil {
		return fmt.Errorf("failed to create user config: %w", err)
	}

	// Save user configuration
	if err := m.configManager.SaveUserConfig(userConfig); err != nil {
		return fmt.Errorf("failed to save user config: %w", err)
	}

	// Create user namespace
	storageConfig := &auth.UserStorageConfig{
		BackendType:   userConfig.BackendType,
		BackendConfig: userConfig.BackendConfig,
		StoragePath:   userConfig.StoragePath,
		Quota:         userConfig.StorageQuota,
		UsedSpace:     0,
		Mounted:       false,
		Metadata:      userConfig.Metadata,
	}

	if err := m.mtManager.CreateUserNamespace(account.Access, storageConfig); err != nil {
		return fmt.Errorf("failed to create user namespace: %w", err)
	}

	fmt.Printf("Created multi-tenant user: %s with backend: %s\n",
		account.Access, userConfig.BackendType)

	return nil
}

// Implement other IAM methods by delegating to base IAM
func (m *MultiTenantIAMService) UpdateUserAccount(access string, props auth.MutableProps) error {
	return m.baseIAM.UpdateUserAccount(access, props)
}

func (m *MultiTenantIAMService) DeleteUserAccount(access string) error {
	// Delete user namespace first
	if err := m.mtManager.DeleteUserNamespace(access); err != nil {
		log.Printf("Warning: Failed to delete user namespace for %s: %v", access, err)
	}

	// Delete user configuration
	if err := m.configManager.DeleteUserConfig(access); err != nil {
		log.Printf("Warning: Failed to delete user config for %s: %v", access, err)
	}

	// Delete base account
	return m.baseIAM.DeleteUserAccount(access)
}

func (m *MultiTenantIAMService) ListUserAccounts() ([]auth.Account, error) {
	return m.baseIAM.ListUserAccounts()
}

func (m *MultiTenantIAMService) Shutdown() error {
	return m.baseIAM.Shutdown()
}

// Enhanced runGateway function with multi-tenant support
func runGateway(ctx context.Context, be backend.Backend, iam auth.IAMService) error {
	// This would be similar to the existing runGateway function
	// but with enhanced multi-tenant backend and IAM
	fmt.Println("Multi-tenant S3 Gateway starting...")

	// For now, return a placeholder
	// In the full implementation, this would start the S3 API server
	// with the multi-tenant backend and enhanced IAM

	return fmt.Errorf("multi-tenant gateway implementation in progress")
}

// Utility functions for managing user storage

// createUserStorageDirectory creates the storage directory for a user
func createUserStorageDirectory(basePath, userID string) (string, error) {
	userPath := filepath.Join(basePath, "users", userID)

	if err := os.MkdirAll(userPath, 0755); err != nil {
		return "", fmt.Errorf("failed to create user directory: %w", err)
	}

	// Create subdirectories for different purposes
	subdirs := []string{"buckets", "metadata", "temp"}
	for _, subdir := range subdirs {
		subdirPath := filepath.Join(userPath, subdir)
		if err := os.MkdirAll(subdirPath, 0755); err != nil {
			return "", fmt.Errorf("failed to create subdirectory %s: %w", subdir, err)
		}
	}

	return userPath, nil
}

// validateUserQuota checks if a user operation would exceed their quota
func validateUserQuota(configManager *config.ConfigManager, userID string, additionalSize int64) error {
	userConfig, err := configManager.LoadUserConfig(userID)
	if err != nil {
		return err
	}

	if userConfig.StorageQuota == 0 {
		return nil // No quota limit
	}

	if userConfig.UsedStorage+additionalSize > userConfig.StorageQuota {
		return fmt.Errorf("operation would exceed storage quota")
	}

	return nil
}
