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

package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// MultiTenantConfig defines the overall multi-tenant configuration
type MultiTenantConfig struct {
	// General settings
	Enabled       bool   `json:"enabled" yaml:"enabled"`
	ConfigDir     string `json:"config_dir" yaml:"config_dir"`
	BaseMountPath string `json:"base_mount_path" yaml:"base_mount_path"`

	// Default settings for new users
	Defaults UserDefaults `json:"defaults" yaml:"defaults"`

	// Backend configurations
	Backends map[string]BackendConfig `json:"backends" yaml:"backends"`

	// Resource management
	ResourceLimits ResourceLimits `json:"resource_limits" yaml:"resource_limits"`

	// Security settings
	Security SecurityConfig `json:"security" yaml:"security"`

	// Monitoring and logging
	Monitoring MonitoringConfig `json:"monitoring" yaml:"monitoring"`
}

// UserDefaults contains default settings for new users
type UserDefaults struct {
	BackendType    string                 `json:"backend_type" yaml:"backend_type"`
	StorageQuota   int64                  `json:"storage_quota" yaml:"storage_quota"`
	BandwidthLimit int64                  `json:"bandwidth_limit" yaml:"bandwidth_limit"`
	MaxBuckets     int                    `json:"max_buckets" yaml:"max_buckets"`
	MaxObjects     int64                  `json:"max_objects" yaml:"max_objects"`
	BackendConfig  map[string]interface{} `json:"backend_config" yaml:"backend_config"`
	Permissions    []string               `json:"permissions" yaml:"permissions"`
}

// BackendConfig contains configuration for a specific backend type
type BackendConfig struct {
	Type        string                 `json:"type" yaml:"type"`
	Name        string                 `json:"name" yaml:"name"`
	Description string                 `json:"description" yaml:"description"`
	Enabled     bool                   `json:"enabled" yaml:"enabled"`
	Config      map[string]interface{} `json:"config" yaml:"config"`

	// Resource constraints
	MaxUsers     int   `json:"max_users" yaml:"max_users"`
	MaxStorage   int64 `json:"max_storage" yaml:"max_storage"`
	MaxBandwidth int64 `json:"max_bandwidth" yaml:"max_bandwidth"`

	// Performance settings
	Performance PerformanceConfig `json:"performance" yaml:"performance"`
}

// PerformanceConfig contains performance-related settings
type PerformanceConfig struct {
	ReadBufferSize    int           `json:"read_buffer_size" yaml:"read_buffer_size"`
	WriteBufferSize   int           `json:"write_buffer_size" yaml:"write_buffer_size"`
	MaxConcurrency    int           `json:"max_concurrency" yaml:"max_concurrency"`
	CacheSize         int64         `json:"cache_size" yaml:"cache_size"`
	CacheTTL          time.Duration `json:"cache_ttl" yaml:"cache_ttl"`
	EnableCompression bool          `json:"enable_compression" yaml:"enable_compression"`
}

// ResourceLimits defines system resource limits
type ResourceLimits struct {
	MaxConcurrentUsers int           `json:"max_concurrent_users" yaml:"max_concurrent_users"`
	MaxMountPoints     int           `json:"max_mount_points" yaml:"max_mount_points"`
	MountTimeout       time.Duration `json:"mount_timeout" yaml:"mount_timeout"`
	UnmountTimeout     time.Duration `json:"unmount_timeout" yaml:"unmount_timeout"`
	IdleTimeout        time.Duration `json:"idle_timeout" yaml:"idle_timeout"`
}

// SecurityConfig contains security-related settings
type SecurityConfig struct {
	EnableEncryption    bool     `json:"enable_encryption" yaml:"enable_encryption"`
	EncryptionAlgorithm string   `json:"encryption_algorithm" yaml:"encryption_algorithm"`
	RequireSSL          bool     `json:"require_ssl" yaml:"require_ssl"`
	AllowedNetworks     []string `json:"allowed_networks" yaml:"allowed_networks"`
	MaxRequestSize      int64    `json:"max_request_size" yaml:"max_request_size"`
	EnableAuditLog      bool     `json:"enable_audit_log" yaml:"enable_audit_log"`
	AuditLogPath        string   `json:"audit_log_path" yaml:"audit_log_path"`
}

// MonitoringConfig contains monitoring and metrics settings
type MonitoringConfig struct {
	EnableMetrics     bool          `json:"enable_metrics" yaml:"enable_metrics"`
	MetricsInterval   time.Duration `json:"metrics_interval" yaml:"metrics_interval"`
	MetricsEndpoint   string        `json:"metrics_endpoint" yaml:"metrics_endpoint"`
	EnableHealthCheck bool          `json:"enable_health_check" yaml:"enable_health_check"`
	HealthCheckPath   string        `json:"health_check_path" yaml:"health_check_path"`
	LogLevel          string        `json:"log_level" yaml:"log_level"`
}

// UserConfig contains configuration for a specific user
type UserConfig struct {
	UserID        string                 `json:"user_id" yaml:"user_id"`
	TenantID      string                 `json:"tenant_id" yaml:"tenant_id"`
	BackendType   string                 `json:"backend_type" yaml:"backend_type"`
	StoragePath   string                 `json:"storage_path" yaml:"storage_path"`
	BackendConfig map[string]interface{} `json:"backend_config" yaml:"backend_config"`

	// Resource allocation
	StorageQuota   int64 `json:"storage_quota" yaml:"storage_quota"`
	BandwidthLimit int64 `json:"bandwidth_limit" yaml:"bandwidth_limit"`
	MaxBuckets     int   `json:"max_buckets" yaml:"max_buckets"`
	MaxObjects     int64 `json:"max_objects" yaml:"max_objects"`

	// Permissions and access
	Permissions []string          `json:"permissions" yaml:"permissions"`
	Metadata    map[string]string `json:"metadata" yaml:"metadata"`

	// Status and tracking
	CreatedAt     time.Time `json:"created_at" yaml:"created_at"`
	UpdatedAt     time.Time `json:"updated_at" yaml:"updated_at"`
	LastAccessed  time.Time `json:"last_accessed" yaml:"last_accessed"`
	Status        string    `json:"status" yaml:"status"`
	UsedStorage   int64     `json:"used_storage" yaml:"used_storage"`
	UsedBandwidth int64     `json:"used_bandwidth" yaml:"used_bandwidth"`
}

// ConfigManager manages multi-tenant configuration
type ConfigManager struct {
	configPath       string
	globalConfig     *MultiTenantConfig
	userConfigs      map[string]*UserConfig
	backendTemplates map[string]*BackendConfig
}

// NewConfigManager creates a new configuration manager
func NewConfigManager(configPath string) *ConfigManager {
	return &ConfigManager{
		configPath:       configPath,
		userConfigs:      make(map[string]*UserConfig),
		backendTemplates: make(map[string]*BackendConfig),
	}
}

// LoadGlobalConfig loads the global multi-tenant configuration
func (cm *ConfigManager) LoadGlobalConfig() error {
	configFile := filepath.Join(cm.configPath, "multitenant.json")

	data, err := os.ReadFile(configFile)
	if err != nil {
		if os.IsNotExist(err) {
			// Create default configuration
			cm.globalConfig = cm.createDefaultConfig()
			return cm.SaveGlobalConfig()
		}
		return fmt.Errorf("failed to read config file: %w", err)
	}

	var config MultiTenantConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return fmt.Errorf("failed to parse config file: %w", err)
	}

	cm.globalConfig = &config

	// Load backend templates
	for name, backend := range config.Backends {
		cm.backendTemplates[name] = &backend
	}

	return nil
}

// SaveGlobalConfig saves the global configuration
func (cm *ConfigManager) SaveGlobalConfig() error {
	configFile := filepath.Join(cm.configPath, "multitenant.json")

	// Ensure config directory exists
	if err := os.MkdirAll(cm.configPath, 0755); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}

	data, err := json.MarshalIndent(cm.globalConfig, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	return os.WriteFile(configFile, data, 0644)
}

// LoadUserConfig loads configuration for a specific user
func (cm *ConfigManager) LoadUserConfig(userID string) (*UserConfig, error) {
	// Check cache first
	if config, exists := cm.userConfigs[userID]; exists {
		return config, nil
	}

	configFile := filepath.Join(cm.configPath, "users", userID+".json")

	data, err := os.ReadFile(configFile)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("user config not found for user %s", userID)
		}
		return nil, fmt.Errorf("failed to read user config: %w", err)
	}

	var config UserConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("failed to parse user config: %w", err)
	}

	// Cache the config
	cm.userConfigs[userID] = &config

	return &config, nil
}

// SaveUserConfig saves configuration for a specific user
func (cm *ConfigManager) SaveUserConfig(config *UserConfig) error {
	userDir := filepath.Join(cm.configPath, "users")
	if err := os.MkdirAll(userDir, 0755); err != nil {
		return fmt.Errorf("failed to create user config directory: %w", err)
	}

	configFile := filepath.Join(userDir, config.UserID+".json")

	// Update timestamp
	config.UpdatedAt = time.Now()

	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal user config: %w", err)
	}

	if err := os.WriteFile(configFile, data, 0644); err != nil {
		return fmt.Errorf("failed to write user config: %w", err)
	}

	// Update cache
	cm.userConfigs[config.UserID] = config

	return nil
}

// CreateUserConfig creates a new user configuration based on defaults
func (cm *ConfigManager) CreateUserConfig(userID, tenantID, backendType string) (*UserConfig, error) {
	if cm.globalConfig == nil {
		return nil, fmt.Errorf("global config not loaded")
	}

	// Get backend template
	backend, exists := cm.backendTemplates[backendType]
	if !exists {
		return nil, fmt.Errorf("backend type %s not found", backendType)
	}

	// Create user-specific storage path
	storagePath := filepath.Join(cm.globalConfig.BaseMountPath, "users", userID)

	config := &UserConfig{
		UserID:         userID,
		TenantID:       tenantID,
		BackendType:    backendType,
		StoragePath:    storagePath,
		BackendConfig:  make(map[string]interface{}),
		StorageQuota:   cm.globalConfig.Defaults.StorageQuota,
		BandwidthLimit: cm.globalConfig.Defaults.BandwidthLimit,
		MaxBuckets:     cm.globalConfig.Defaults.MaxBuckets,
		MaxObjects:     cm.globalConfig.Defaults.MaxObjects,
		Permissions:    cm.globalConfig.Defaults.Permissions,
		Metadata:       make(map[string]string),
		CreatedAt:      time.Now(),
		UpdatedAt:      time.Now(),
		Status:         "active",
		UsedStorage:    0,
		UsedBandwidth:  0,
	}

	// Copy backend-specific configuration
	for k, v := range backend.Config {
		config.BackendConfig[k] = v
	}

	// Copy default backend configuration
	for k, v := range cm.globalConfig.Defaults.BackendConfig {
		if _, exists := config.BackendConfig[k]; !exists {
			config.BackendConfig[k] = v
		}
	}

	return config, nil
}

// ListUsers returns a list of all configured users
func (cm *ConfigManager) ListUsers() ([]string, error) {
	userDir := filepath.Join(cm.configPath, "users")

	entries, err := os.ReadDir(userDir)
	if err != nil {
		if os.IsNotExist(err) {
			return []string{}, nil
		}
		return nil, fmt.Errorf("failed to read user directory: %w", err)
	}

	var users []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		name := entry.Name()
		if filepath.Ext(name) == ".json" {
			userID := name[:len(name)-5] // Remove .json extension
			users = append(users, userID)
		}
	}

	return users, nil
}

// DeleteUserConfig deletes configuration for a user
func (cm *ConfigManager) DeleteUserConfig(userID string) error {
	configFile := filepath.Join(cm.configPath, "users", userID+".json")

	if err := os.Remove(configFile); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to delete user config: %w", err)
	}

	// Remove from cache
	delete(cm.userConfigs, userID)

	return nil
}

// GetGlobalConfig returns the global configuration
func (cm *ConfigManager) GetGlobalConfig() *MultiTenantConfig {
	return cm.globalConfig
}

// GetBackendTemplate returns a backend template by name
func (cm *ConfigManager) GetBackendTemplate(name string) (*BackendConfig, error) {
	template, exists := cm.backendTemplates[name]
	if !exists {
		return nil, fmt.Errorf("backend template %s not found", name)
	}
	return template, nil
}

// UpdateUserStatus updates the status of a user
func (cm *ConfigManager) UpdateUserStatus(userID, status string) error {
	config, err := cm.LoadUserConfig(userID)
	if err != nil {
		return err
	}

	config.Status = status
	return cm.SaveUserConfig(config)
}

// UpdateUserUsage updates usage statistics for a user
func (cm *ConfigManager) UpdateUserUsage(userID string, storageUsed, bandwidthUsed int64) error {
	config, err := cm.LoadUserConfig(userID)
	if err != nil {
		return err
	}

	config.UsedStorage = storageUsed
	config.UsedBandwidth = bandwidthUsed
	config.LastAccessed = time.Now()

	return cm.SaveUserConfig(config)
}

// createDefaultConfig creates a default multi-tenant configuration
func (cm *ConfigManager) createDefaultConfig() *MultiTenantConfig {
	return &MultiTenantConfig{
		Enabled:       true,
		ConfigDir:     cm.configPath,
		BaseMountPath: "/var/lib/versitygw/mounts",
		Defaults: UserDefaults{
			BackendType:    "posix",
			StorageQuota:   100 * 1024 * 1024 * 1024, // 100GB
			BandwidthLimit: 0,                        // No limit
			MaxBuckets:     100,
			MaxObjects:     1000000,
			BackendConfig:  make(map[string]interface{}),
			Permissions:    []string{"read", "write", "delete"},
		},
		Backends: map[string]BackendConfig{
			"posix": {
				Type:        "posix",
				Name:        "POSIX File System",
				Description: "Local POSIX-compliant file system",
				Enabled:     true,
				Config:      make(map[string]interface{}),
				MaxUsers:    1000,
				MaxStorage:  10 * 1024 * 1024 * 1024 * 1024, // 10TB
				Performance: PerformanceConfig{
					ReadBufferSize:    64 * 1024,
					WriteBufferSize:   64 * 1024,
					MaxConcurrency:    10,
					CacheSize:         100 * 1024 * 1024,
					CacheTTL:          5 * time.Minute,
					EnableCompression: false,
				},
			},
			"cephfs": {
				Type:        "cephfs",
				Name:        "Ceph File System",
				Description: "Ceph distributed file system",
				Enabled:     true,
				Config: map[string]interface{}{
					"monitor_addresses": []string{"mon1:6789", "mon2:6789", "mon3:6789"},
					"username":          "admin",
					"filesystem":        "cephfs",
				},
				MaxUsers:   500,
				MaxStorage: 100 * 1024 * 1024 * 1024 * 1024, // 100TB
				Performance: PerformanceConfig{
					ReadBufferSize:    1024 * 1024,
					WriteBufferSize:   1024 * 1024,
					MaxConcurrency:    20,
					CacheSize:         1024 * 1024 * 1024,
					CacheTTL:          10 * time.Minute,
					EnableCompression: true,
				},
			},
			"lustre": {
				Type:        "lustre",
				Name:        "Lustre File System",
				Description: "High-performance parallel file system",
				Enabled:     true,
				Config: map[string]interface{}{
					"mgs_nodes":    []string{"mgs1", "mgs2"},
					"filesystem":   "lustre",
					"stripe_count": 4,
					"stripe_size":  1048576,
				},
				MaxUsers:   200,
				MaxStorage: 500 * 1024 * 1024 * 1024 * 1024, // 500TB
				Performance: PerformanceConfig{
					ReadBufferSize:    4 * 1024 * 1024,
					WriteBufferSize:   4 * 1024 * 1024,
					MaxConcurrency:    50,
					CacheSize:         4 * 1024 * 1024 * 1024,
					CacheTTL:          15 * time.Minute,
					EnableCompression: false,
				},
			},
			"nfs": {
				Type:        "nfs",
				Name:        "Network File System",
				Description: "NFS network attached storage",
				Enabled:     true,
				Config: map[string]interface{}{
					"server_address": "nfs-server.example.com",
					"export_path":    "/exports",
					"version":        "nfs4",
					"options":        []string{"rw", "sync"},
				},
				MaxUsers:   300,
				MaxStorage: 50 * 1024 * 1024 * 1024 * 1024, // 50TB
				Performance: PerformanceConfig{
					ReadBufferSize:    512 * 1024,
					WriteBufferSize:   512 * 1024,
					MaxConcurrency:    15,
					CacheSize:         512 * 1024 * 1024,
					CacheTTL:          5 * time.Minute,
					EnableCompression: false,
				},
			},
			"minio": {
				Type:        "minio",
				Name:        "MinIO Object Storage",
				Description: "MinIO S3-compatible object storage",
				Enabled:     true,
				Config: map[string]interface{}{
					"endpoint":       "http://minio.example.com:9000",
					"access_key":     "minioadmin",
					"secret_key":     "minioadmin",
					"region":         "us-east-1",
					"bucket_prefix":  "vgw",
					"ssl":            false,
					"use_path_style": true,
				},
				MaxUsers:   1000,
				MaxStorage: 1000 * 1024 * 1024 * 1024 * 1024, // 1PB
				Performance: PerformanceConfig{
					ReadBufferSize:    1024 * 1024,
					WriteBufferSize:   1024 * 1024,
					MaxConcurrency:    30,
					CacheSize:         2 * 1024 * 1024 * 1024,
					CacheTTL:          10 * time.Minute,
					EnableCompression: true,
				},
			},
		},
		ResourceLimits: ResourceLimits{
			MaxConcurrentUsers: 1000,
			MaxMountPoints:     500,
			MountTimeout:       30 * time.Second,
			UnmountTimeout:     15 * time.Second,
			IdleTimeout:        30 * time.Minute,
		},
		Security: SecurityConfig{
			EnableEncryption:    true,
			EncryptionAlgorithm: "AES-256-GCM",
			RequireSSL:          false,
			AllowedNetworks:     []string{"0.0.0.0/0"},
			MaxRequestSize:      5 * 1024 * 1024 * 1024, // 5GB
			EnableAuditLog:      true,
			AuditLogPath:        "/var/log/versitygw/audit.log",
		},
		Monitoring: MonitoringConfig{
			EnableMetrics:     true,
			MetricsInterval:   30 * time.Second,
			MetricsEndpoint:   "/metrics",
			EnableHealthCheck: true,
			HealthCheckPath:   "/health",
			LogLevel:          "info",
		},
	}
}
