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

package auth

import (
	"errors"
	"fmt"
	"path/filepath"
)

// MultiTenantConfig defines the configuration for multi-tenant support
type MultiTenantConfig struct {
	// Enable multi-tenant mode
	Enabled bool `json:"enabled"`
	// Default storage backend type for new users
	DefaultBackendType string `json:"default_backend_type"`
	// Base path for user isolation
	BasePath string `json:"base_path"`
	// Storage quotas (in bytes, 0 = unlimited)
	DefaultQuota int64 `json:"default_quota"`
}

// UserStorageConfig defines storage configuration for a specific user
type UserStorageConfig struct {
	// Storage backend type: posix, cephfs, nfs, lustre, minio, rustfs
	BackendType string `json:"backend_type"`
	// Backend-specific configuration
	BackendConfig map[string]interface{} `json:"backend_config"`
	// Storage path or mount point
	StoragePath string `json:"storage_path"`
	// Storage quota in bytes (0 = unlimited)
	Quota int64 `json:"quota"`
	// Used space tracking
	UsedSpace int64 `json:"used_space"`
	// Mount status
	Mounted bool `json:"mounted"`
	// Additional metadata
	Metadata map[string]string `json:"metadata"`
}

// Enhanced Account structure with storage configuration
type EnhancedAccount struct {
	Account
	// Storage configuration for this user
	StorageConfig UserStorageConfig `json:"storage_config"`
	// Tenant ID for isolation
	TenantID string `json:"tenant_id"`
	// Allowed operations
	Permissions []string `json:"permissions"`
}

// MultiTenantManager manages multi-tenant operations
type MultiTenantManager interface {
	// GetUserStorageConfig returns storage configuration for a user
	GetUserStorageConfig(userID string) (*UserStorageConfig, error)
	// SetUserStorageConfig sets storage configuration for a user
	SetUserStorageConfig(userID string, config *UserStorageConfig) error
	// GetUserBasePath returns the isolated base path for a user
	GetUserBasePath(userID string) (string, error)
	// CreateUserNamespace creates isolated namespace for a user
	CreateUserNamespace(userID string, config *UserStorageConfig) error
	// DeleteUserNamespace removes user namespace
	DeleteUserNamespace(userID string) error
	// MountUserStorage mounts storage for a user
	MountUserStorage(userID string) error
	// UnmountUserStorage unmounts storage for a user
	UnmountUserStorage(userID string) error
	// CheckQuota checks if user is within quota limits
	CheckQuota(userID string, additionalSize int64) error
	// UpdateUsedSpace updates the used space for a user
	UpdateUsedSpace(userID string, delta int64) error
}

var (
	ErrUserStorageNotFound = errors.New("user storage configuration not found")
	ErrQuotaExceeded       = errors.New("storage quota exceeded")
	ErrMountFailed         = errors.New("failed to mount user storage")
	ErrUnmountFailed       = errors.New("failed to unmount user storage")
	ErrInvalidBackendType  = errors.New("invalid backend type")
)

// DefaultMultiTenantManager implements MultiTenantManager
type DefaultMultiTenantManager struct {
	config         MultiTenantConfig
	userConfigs    map[string]*UserStorageConfig
	backendFactory BackendFactory
}

// BackendFactory creates backend instances for different storage types
type BackendFactory interface {
	CreateBackend(backendType string, config map[string]interface{}) (interface{}, error)
}

// NewMultiTenantManager creates a new multi-tenant manager
func NewMultiTenantManager(config MultiTenantConfig, factory BackendFactory) *DefaultMultiTenantManager {
	return &DefaultMultiTenantManager{
		config:         config,
		userConfigs:    make(map[string]*UserStorageConfig),
		backendFactory: factory,
	}
}

// GetUserStorageConfig returns storage configuration for a user
func (m *DefaultMultiTenantManager) GetUserStorageConfig(userID string) (*UserStorageConfig, error) {
	config, exists := m.userConfigs[userID]
	if !exists {
		return nil, ErrUserStorageNotFound
	}
	return config, nil
}

// SetUserStorageConfig sets storage configuration for a user
func (m *DefaultMultiTenantManager) SetUserStorageConfig(userID string, config *UserStorageConfig) error {
	if config == nil {
		return errors.New("config cannot be nil")
	}

	// Validate backend type
	validBackends := []string{"posix", "cephfs", "nfs", "lustre", "minio", "rustfs"}
	valid := false
	for _, backend := range validBackends {
		if config.BackendType == backend {
			valid = true
			break
		}
	}
	if !valid {
		return ErrInvalidBackendType
	}

	m.userConfigs[userID] = config
	return nil
}

// GetUserBasePath returns the isolated base path for a user
func (m *DefaultMultiTenantManager) GetUserBasePath(userID string) (string, error) {
	if !m.config.Enabled {
		return "", errors.New("multi-tenant mode not enabled")
	}

	// Create user-specific path
	userPath := filepath.Join(m.config.BasePath, "users", userID)
	return userPath, nil
}

// CreateUserNamespace creates isolated namespace for a user
func (m *DefaultMultiTenantManager) CreateUserNamespace(userID string, config *UserStorageConfig) error {
	basePath, err := m.GetUserBasePath(userID)
	if err != nil {
		return err
	}

	// Set default configuration if not provided
	if config == nil {
		config = &UserStorageConfig{
			BackendType:   m.config.DefaultBackendType,
			BackendConfig: make(map[string]interface{}),
			StoragePath:   basePath,
			Quota:         m.config.DefaultQuota,
			UsedSpace:     0,
			Mounted:       false,
			Metadata:      make(map[string]string),
		}
	}

	// Ensure storage path is set
	if config.StoragePath == "" {
		config.StoragePath = basePath
	}

	// Store user configuration
	m.userConfigs[userID] = config

	return nil
}

// DeleteUserNamespace removes user namespace
func (m *DefaultMultiTenantManager) DeleteUserNamespace(userID string) error {
	// Unmount storage first
	if err := m.UnmountUserStorage(userID); err != nil {
		return fmt.Errorf("failed to unmount storage before deletion: %w", err)
	}

	// Remove from memory
	delete(m.userConfigs, userID)

	return nil
}

// MountUserStorage mounts storage for a user
func (m *DefaultMultiTenantManager) MountUserStorage(userID string) error {
	config, err := m.GetUserStorageConfig(userID)
	if err != nil {
		return err
	}

	if config.Mounted {
		return nil // Already mounted
	}

	// Create backend instance
	backend, err := m.backendFactory.CreateBackend(config.BackendType, config.BackendConfig)
	if err != nil {
		return fmt.Errorf("failed to create backend: %w", err)
	}

	// Perform mounting logic based on backend type
	switch config.BackendType {
	case "posix", "cephfs", "nfs", "lustre":
		// For filesystem-based backends, ensure directory exists
		// Implementation would depend on specific backend requirements
	case "minio", "rustfs":
		// For object storage backends, initialize client connections
		// Implementation would depend on specific backend requirements
	default:
		return ErrInvalidBackendType
	}

	config.Mounted = true
	return nil
}

// UnmountUserStorage unmounts storage for a user
func (m *DefaultMultiTenantManager) UnmountUserStorage(userID string) error {
	config, err := m.GetUserStorageConfig(userID)
	if err != nil {
		return err
	}

	if !config.Mounted {
		return nil // Already unmounted
	}

	// Perform unmounting logic based on backend type
	// Implementation would depend on specific backend requirements

	config.Mounted = false
	return nil
}

// CheckQuota checks if user is within quota limits
func (m *DefaultMultiTenantManager) CheckQuota(userID string, additionalSize int64) error {
	config, err := m.GetUserStorageConfig(userID)
	if err != nil {
		return err
	}

	// No quota limit
	if config.Quota == 0 {
		return nil
	}

	// Check if adding additional size would exceed quota
	if config.UsedSpace+additionalSize > config.Quota {
		return ErrQuotaExceeded
	}

	return nil
}

// UpdateUsedSpace updates the used space for a user
func (m *DefaultMultiTenantManager) UpdateUsedSpace(userID string, delta int64) error {
	config, err := m.GetUserStorageConfig(userID)
	if err != nil {
		return err
	}

	config.UsedSpace += delta

	// Ensure used space doesn't go negative
	if config.UsedSpace < 0 {
		config.UsedSpace = 0
	}

	return nil
}

// Helper functions for tenant isolation

// IsTenantIsolationEnabled checks if tenant isolation is enabled
func IsTenantIsolationEnabled(manager MultiTenantManager) bool {
	if mgr, ok := manager.(*DefaultMultiTenantManager); ok {
		return mgr.config.Enabled
	}
	return false
}

// GetTenantID extracts tenant ID from user access key or context
func GetTenantID(userID string) string {
	// For now, use userID as tenantID
	// In more complex scenarios, this could extract from a different source
	return userID
}

// ValidateUserAccess checks if user has access to a specific resource
func ValidateUserAccess(userID, resourcePath string, manager MultiTenantManager) error {
	if !IsTenantIsolationEnabled(manager) {
		return nil // No isolation, allow access
	}

	userBasePath, err := manager.GetUserBasePath(userID)
	if err != nil {
		return err
	}

	// Check if resource is within user's allowed path
	if !isSubPath(userBasePath, resourcePath) {
		return errors.New("access denied: resource outside user namespace")
	}

	return nil
}

// isSubPath checks if path is under basePath
func isSubPath(basePath, path string) bool {
	relPath, err := filepath.Rel(basePath, path)
	if err != nil {
		return false
	}

	// Path is under basePath if relative path doesn't start with ".."
	return !filepath.IsAbs(relPath) && !filepath.HasPrefix(relPath, "..")
}
