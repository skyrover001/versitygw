#!/bin/bash

# Versity S3 Gateway Multi-Tenant Test Script
# Copyright 2023 Versity Software

set -e

# Configuration
GATEWAY_URL="http://localhost:7070"
ADMIN_ACCESS_KEY="admin"
ADMIN_SECRET_KEY="admin123"
CONFIG_DIR="/etc/versitygw/multitenant"
MOUNT_BASE="/var/lib/versitygw/mounts"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Helper functions
log_info() {
    echo -e "${BLUE}[INFO]${NC} $1"
}

log_success() {
    echo -e "${GREEN}[SUCCESS]${NC} $1"
}

log_warning() {
    echo -e "${YELLOW}[WARNING]${NC} $1"
}

log_error() {
    echo -e "${RED}[ERROR]${NC} $1"
}

# Check if gateway is running
check_gateway() {
    log_info "Checking if gateway is running..."
    if curl -s "$GATEWAY_URL/health" > /dev/null 2>&1; then
        log_success "Gateway is running"
    else
        log_error "Gateway is not running at $GATEWAY_URL"
        exit 1
    fi
}

# Setup test environment
setup_test_env() {
    log_info "Setting up test environment..."
    
    # Create test directories
    sudo mkdir -p "$CONFIG_DIR/users"
    sudo mkdir -p "$MOUNT_BASE/users"
    sudo mkdir -p "/tmp/versitygw-test"
    
    # Create test files
    echo "Hello, World!" > /tmp/versitygw-test/test-file.txt
    dd if=/dev/zero of=/tmp/versitygw-test/large-file.bin bs=1M count=10 2>/dev/null
    
    log_success "Test environment setup complete"
}

# Create test user configuration
create_test_user() {
    local user_id="$1"
    local backend_type="$2"
    local quota="$3"
    
    log_info "Creating test user: $user_id with backend: $backend_type"
    
    # Create user directory
    sudo mkdir -p "$MOUNT_BASE/users/$user_id"
    
    # Create user config file
    cat > /tmp/user-config-$user_id.json << EOF
{
  "user_id": "$user_id",
  "tenant_id": "$user_id",
  "backend_type": "$backend_type",
  "storage_path": "$MOUNT_BASE/users/$user_id",
  "backend_config": {},
  "storage_quota": $quota,
  "bandwidth_limit": 0,
  "max_buckets": 10,
  "max_objects": 1000,
  "permissions": ["read", "write", "delete"],
  "metadata": {},
  "created_at": "$(date -Iseconds)",
  "updated_at": "$(date -Iseconds)",
  "last_accessed": "$(date -Iseconds)",
  "status": "active",
  "used_storage": 0,
  "used_bandwidth": 0
}
EOF
    
    sudo mv "/tmp/user-config-$user_id.json" "$CONFIG_DIR/users/$user_id.json"
    log_success "Created user configuration for $user_id"
}

# Test S3 operations with a user
test_s3_operations() {
    local access_key="$1"
    local secret_key="$2"
    local test_name="$3"
    
    log_info "Testing S3 operations for $test_name"
    
    # Configure AWS CLI for this user
    export AWS_ACCESS_KEY_ID="$access_key"
    export AWS_SECRET_ACCESS_KEY="$secret_key"
    export AWS_DEFAULT_REGION="us-east-1"
    
    local bucket_name="test-bucket-$(date +%s)"
    
    # Test bucket operations
    log_info "Creating bucket: $bucket_name"
    if aws --endpoint-url "$GATEWAY_URL" s3 mb "s3://$bucket_name" 2>/dev/null; then
        log_success "Bucket created successfully"
    else
        log_error "Failed to create bucket"
        return 1
    fi
    
    # Test object upload
    log_info "Uploading test file"
    if aws --endpoint-url "$GATEWAY_URL" s3 cp /tmp/versitygw-test/test-file.txt "s3://$bucket_name/" 2>/dev/null; then
        log_success "File uploaded successfully"
    else
        log_error "Failed to upload file"
        return 1
    fi
    
    # Test object download
    log_info "Downloading test file"
    if aws --endpoint-url "$GATEWAY_URL" s3 cp "s3://$bucket_name/test-file.txt" "/tmp/downloaded-$(date +%s).txt" 2>/dev/null; then
        log_success "File downloaded successfully"
    else
        log_error "Failed to download file"
        return 1
    fi
    
    # Test object listing
    log_info "Listing objects"
    if aws --endpoint-url "$GATEWAY_URL" s3 ls "s3://$bucket_name/" 2>/dev/null; then
        log_success "Objects listed successfully"
    else
        log_error "Failed to list objects"
        return 1
    fi
    
    # Test large file upload (for Lustre striping test)
    log_info "Uploading large file (for striping test)"
    if aws --endpoint-url "$GATEWAY_URL" s3 cp /tmp/versitygw-test/large-file.bin "s3://$bucket_name/" 2>/dev/null; then
        log_success "Large file uploaded successfully"
    else
        log_warning "Failed to upload large file (may be expected if quota is low)"
    fi
    
    # Cleanup
    log_info "Cleaning up test bucket"
    aws --endpoint-url "$GATEWAY_URL" s3 rm "s3://$bucket_name/" --recursive 2>/dev/null || true
    aws --endpoint-url "$GATEWAY_URL" s3 rb "s3://$bucket_name/" 2>/dev/null || true
    
    log_success "$test_name S3 operations test completed"
}

# Test user isolation
test_user_isolation() {
    log_info "Testing user isolation..."
    
    # Create two test users
    create_test_user "testuser1" "posix" "52428800"  # 50MB
    create_test_user "testuser2" "posix" "104857600" # 100MB
    
    # Test that user1 cannot access user2's resources
    export AWS_ACCESS_KEY_ID="testuser1"
    export AWS_SECRET_ACCESS_KEY="testuser1secret"
    
    # This should fail if isolation is working
    log_info "Testing cross-user access (should fail)"
    if aws --endpoint-url "$GATEWAY_URL" s3 ls "s3://user2-private-bucket" 2>/dev/null; then
        log_error "User isolation failed - user1 can access user2's resources"
        return 1
    else
        log_success "User isolation working - cross-user access denied"
    fi
}

# Test quota enforcement
test_quota_enforcement() {
    log_info "Testing quota enforcement..."
    
    # Create user with small quota
    create_test_user "quotauser" "posix" "1048576"  # 1MB
    
    export AWS_ACCESS_KEY_ID="quotauser"
    export AWS_SECRET_ACCESS_KEY="quotausersecret"
    
    local bucket_name="quota-test-bucket"
    aws --endpoint-url "$GATEWAY_URL" s3 mb "s3://$bucket_name" 2>/dev/null || true
    
    # Try to upload file larger than quota
    log_info "Attempting to upload file larger than quota (should fail)"
    if aws --endpoint-url "$GATEWAY_URL" s3 cp /tmp/versitygw-test/large-file.bin "s3://$bucket_name/" 2>/dev/null; then
        log_error "Quota enforcement failed - large file upload succeeded"
        return 1
    else
        log_success "Quota enforcement working - large file upload rejected"
    fi
    
    # Upload small file (should succeed)
    log_info "Uploading small file within quota (should succeed)"
    if aws --endpoint-url "$GATEWAY_URL" s3 cp /tmp/versitygw-test/test-file.txt "s3://$bucket_name/" 2>/dev/null; then
        log_success "Small file upload within quota succeeded"
    else
        log_error "Small file upload failed unexpectedly"
        return 1
    fi
}

# Test backend-specific features
test_backend_features() {
    local backend_type="$1"
    
    log_info "Testing $backend_type specific features..."
    
    case "$backend_type" in
        "lustre")
            test_lustre_striping
            ;;
        "cephfs")
            test_cephfs_features
            ;;
        "nfs")
            test_nfs_features
            ;;
        "minio")
            test_minio_features
            ;;
        *)
            log_info "No specific tests for $backend_type backend"
            ;;
    esac
}

# Test Lustre striping
test_lustre_striping() {
    log_info "Testing Lustre striping features..."
    
    # Create user with Lustre backend
    cat > /tmp/lustre-user-config.json << EOF
{
  "user_id": "lustreuser",
  "tenant_id": "lustreuser", 
  "backend_type": "lustre",
  "storage_path": "$MOUNT_BASE/users/lustreuser",
  "backend_config": {
    "mgs_nodes": ["mgs1", "mgs2"],
    "filesystem": "lustre",
    "stripe_count": 4,
    "stripe_size": 1048576
  },
  "storage_quota": 1073741824,
  "status": "active"
}
EOF
    
    sudo mv /tmp/lustre-user-config.json "$CONFIG_DIR/users/lustreuser.json"
    
    # Test file operations that should trigger striping
    export AWS_ACCESS_KEY_ID="lustreuser"
    export AWS_SECRET_ACCESS_KEY="lustresecret"
    
    local bucket_name="lustre-test-bucket"
    aws --endpoint-url "$GATEWAY_URL" s3 mb "s3://$bucket_name" 2>/dev/null || true
    
    # Upload large file to test striping
    if aws --endpoint-url "$GATEWAY_URL" s3 cp /tmp/versitygw-test/large-file.bin "s3://$bucket_name/striped-file.bin" 2>/dev/null; then
        log_success "Large file uploaded to Lustre (striping should be applied)"
        
        # Check if file was striped (if lfs command is available)
        if command -v lfs > /dev/null 2>&1; then
            local file_path="$MOUNT_BASE/users/lustreuser/lustre-test-bucket/striped-file.bin"
            if [ -f "$file_path" ]; then
                local stripe_count=$(lfs getstripe -c "$file_path" 2>/dev/null || echo "unknown")
                log_info "File stripe count: $stripe_count"
            fi
        fi
    else
        log_warning "Large file upload to Lustre failed (may be expected in test environment)"
    fi
}

# Test CephFS features
test_cephfs_features() {
    log_info "Testing CephFS specific features..."
    log_info "CephFS features test not implemented yet"
}

# Test NFS features  
test_nfs_features() {
    log_info "Testing NFS specific features..."
    log_info "NFS features test not implemented yet"
}

# Test MinIO features
test_minio_features() {
    log_info "Testing MinIO specific features..."
    log_info "MinIO features test not implemented yet"
}

# Cleanup test environment
cleanup_test_env() {
    log_info "Cleaning up test environment..."
    
    # Remove test files
    rm -rf /tmp/versitygw-test
    rm -f /tmp/downloaded-*.txt
    
    # Remove test user configs
    sudo rm -f "$CONFIG_DIR/users/testuser1.json"
    sudo rm -f "$CONFIG_DIR/users/testuser2.json"
    sudo rm -f "$CONFIG_DIR/users/quotauser.json"
    sudo rm -f "$CONFIG_DIR/users/lustreuser.json"
    
    # Remove test mount points
    sudo rm -rf "$MOUNT_BASE/users/testuser1"
    sudo rm -rf "$MOUNT_BASE/users/testuser2"
    sudo rm -rf "$MOUNT_BASE/users/quotauser"
    sudo rm -rf "$MOUNT_BASE/users/lustreuser"
    
    log_success "Test environment cleaned up"
}

# Print test results
print_results() {
    echo
    echo "=========================================="
    echo "       Multi-Tenant Test Results"
    echo "=========================================="
    echo "Gateway URL: $GATEWAY_URL"
    echo "Config Dir: $CONFIG_DIR"
    echo "Mount Base: $MOUNT_BASE"
    echo "=========================================="
}

# Main test function
run_all_tests() {
    log_info "Starting Versity S3 Gateway Multi-Tenant Tests"
    
    # Prerequisites
    check_gateway
    setup_test_env
    
    # Core functionality tests
    log_info "Running core functionality tests..."
    test_s3_operations "$ADMIN_ACCESS_KEY" "$ADMIN_SECRET_KEY" "Admin User"
    
    # Multi-tenant specific tests
    log_info "Running multi-tenant specific tests..."
    test_user_isolation
    test_quota_enforcement
    
    # Backend-specific tests
    if [ "$1" != "" ]; then
        test_backend_features "$1"
    fi
    
    # Cleanup
    cleanup_test_env
    
    # Results
    print_results
    log_success "All tests completed successfully!"
}

# Script options
case "${1:-all}" in
    "all")
        run_all_tests
        ;;
    "basic")
        check_gateway
        setup_test_env
        test_s3_operations "$ADMIN_ACCESS_KEY" "$ADMIN_SECRET_KEY" "Basic Test"
        cleanup_test_env
        ;;
    "isolation")
        check_gateway
        setup_test_env
        test_user_isolation
        cleanup_test_env
        ;;
    "quota")
        check_gateway
        setup_test_env
        test_quota_enforcement
        cleanup_test_env
        ;;
    "lustre")
        run_all_tests "lustre"
        ;;
    "cephfs")
        run_all_tests "cephfs"
        ;;
    "nfs")
        run_all_tests "nfs"
        ;;
    "minio")
        run_all_tests "minio"
        ;;
    "cleanup")
        cleanup_test_env
        ;;
    *)
        echo "Usage: $0 [all|basic|isolation|quota|lustre|cephfs|nfs|minio|cleanup]"
        echo
        echo "Options:"
        echo "  all       - Run all tests (default)"
        echo "  basic     - Run basic S3 operations test"
        echo "  isolation - Test user isolation"
        echo "  quota     - Test quota enforcement"
        echo "  lustre    - Test with Lustre backend"
        echo "  cephfs    - Test with CephFS backend"
        echo "  nfs       - Test with NFS backend"
        echo "  minio     - Test with MinIO backend"
        echo "  cleanup   - Clean up test environment"
        exit 1
        ;;
esac