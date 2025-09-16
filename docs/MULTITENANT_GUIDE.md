# Versity S3 Gateway - 多用户隔离部署指南

## 概述

基于 Versity S3 Gateway 项目，我们实现了一个支持多用户隔离、动态挂载和多种后端存储的增强版 S3 协议服务。

### 主要特性

- **多用户隔离**: 每个用户拥有独立的存储命名空间和配额管理
- **动态挂载**: 支持为不同用户动态挂载不同的存储后端
- **多种后端存储**: 支持 CephFS、NFS、Local、Lustre 等 POSIX 文件系统，以及 MinIO、RustFS 等对象存储
- **Lustre 条带化优化**: 基于 Lustre 条带化的并行读写加速
- **配置热重载**: 支持动态配置用户存储映射
- **配额和权限管理**: 细粒度的资源控制和访问权限

## 架构设计

```
┌─────────────────────────────────────────────────────────────┐
│                    S3 API Layer                            │
├─────────────────────────────────────────────────────────────┤
│                Multi-Tenant Manager                        │
├─────────────────────────────────────────────────────────────┤
│              Dynamic Backend Manager                       │
├─────────────────────────────────────────────────────────────┤
│  POSIX  │ CephFS │   NFS   │ Lustre │ MinIO │ RustFS │
│ Backend │Backend │ Backend │Backend │Backend│Backend │
└─────────────────────────────────────────────────────────────┘
```

## 安装部署

### 系统要求

- Linux 操作系统 (建议 Ubuntu 20.04+ 或 CentOS 8+)
- Go 1.19+
- 对于 Lustre: 需要安装 Lustre 客户端
- 对于 CephFS: 需要安装 Ceph 客户端
- 对于 NFS: 需要安装 NFS 客户端工具

### 编译安装

```bash
# 克隆项目
git clone https://github.com/versity/versitygw.git
cd versitygw

# 编译
make build

# 或者使用 Go 直接编译
go build -o versitygw cmd/versitygw/*.go
```

### 配置文件

1. 创建配置目录：
```bash
sudo mkdir -p /etc/versitygw/multitenant
sudo mkdir -p /var/lib/versitygw/mounts
sudo mkdir -p /var/log/versitygw
```

2. 复制配置文件：
```bash
sudo cp config/multitenant.json /etc/versitygw/multitenant/
```

3. 根据实际环境修改配置文件 `/etc/versitygw/multitenant/multitenant.json`

## 配置说明

### 全局配置

配置文件位置: `/etc/versitygw/multitenant/multitenant.json`

```json
{
  "enabled": true,
  "base_mount_path": "/var/lib/versitygw/mounts",
  "defaults": {
    "backend_type": "posix",
    "storage_quota": 107374182400,  // 100GB
    "max_buckets": 100,
    "permissions": ["read", "write", "delete"]
  }
}
```

### 后端存储配置

#### CephFS 配置
```json
"cephfs": {
  "type": "cephfs",
  "enabled": true,
  "config": {
    "monitor_addresses": ["mon1:6789", "mon2:6789", "mon3:6789"],
    "username": "admin",
    "secret_key": "your-ceph-secret-key",
    "filesystem": "cephfs"
  }
}
```

#### Lustre 配置
```json
"lustre": {
  "type": "lustre",
  "enabled": true,
  "config": {
    "mgs_nodes": ["mgs1", "mgs2"],
    "filesystem": "lustre",
    "stripe_count": 4,
    "stripe_size": 1048576
  }
}
```

#### NFS 配置
```json
"nfs": {
  "type": "nfs",
  "enabled": true,
  "config": {
    "server_address": "nfs-server.example.com",
    "export_path": "/exports",
    "version": "nfs4",
    "options": ["rw", "sync"]
  }
}
```

#### MinIO 配置
```json
"minio": {
  "type": "minio",
  "enabled": true,
  "config": {
    "endpoint": "http://minio.example.com:9000",
    "access_key": "minioadmin",
    "secret_key": "minioadmin",
    "region": "us-east-1",
    "bucket_prefix": "vgw"
  }
}
```

## 启动服务

### 基本启动
```bash
# 设置环境变量
export ROOT_ACCESS_KEY="admin"
export ROOT_SECRET_KEY="admin123"

# 启动多租户模式
sudo ./versitygw multitenant \
  --port :7070 \
  --config-dir /etc/versitygw/multitenant \
  --base-path /var/lib/versitygw/mounts \
  --default-backend posix \
  --enable-dynamic-mount \
  --enable-user-isolation
```

### 使用 systemd 服务

1. 创建服务文件 `/etc/systemd/system/versitygw.service`：
```ini
[Unit]
Description=Versity S3 Gateway Multi-Tenant
After=network.target

[Service]
Type=simple
User=versitygw
Group=versitygw
Environment=ROOT_ACCESS_KEY=admin
Environment=ROOT_SECRET_KEY=admin123
ExecStart=/usr/local/bin/versitygw multitenant \
  --port :7070 \
  --config-dir /etc/versitygw/multitenant \
  --base-path /var/lib/versitygw/mounts \
  --default-backend posix \
  --enable-dynamic-mount \
  --enable-user-isolation
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
```

2. 启动服务：
```bash
sudo systemctl daemon-reload
sudo systemctl enable versitygw
sudo systemctl start versitygw
```

## 用户管理

### 创建用户

```bash
# 使用 admin API 创建用户
curl -X POST http://localhost:7070/admin/users \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "access_key": "user1",
    "secret_key": "user1secret",
    "backend_type": "lustre",
    "storage_quota": 53687091200,
    "permissions": ["read", "write"]
  }'
```

### 配置用户后端

用户配置文件位置: `/etc/versitygw/multitenant/users/user1.json`

```json
{
  "user_id": "user1",
  "tenant_id": "user1",
  "backend_type": "lustre",
  "storage_path": "/var/lib/versitygw/mounts/users/user1",
  "backend_config": {
    "mgs_nodes": ["mgs1", "mgs2"],
    "filesystem": "lustre",
    "stripe_count": 8,
    "stripe_size": 2097152
  },
  "storage_quota": 53687091200,
  "max_buckets": 50,
  "permissions": ["read", "write", "delete"],
  "status": "active"
}
```

## 使用示例

### S3 客户端配置

```bash
# 配置 AWS CLI
aws configure set aws_access_key_id user1
aws configure set aws_secret_access_key user1secret
aws configure set default.region us-east-1
aws configure set default.s3.signature_version s3v4

# 使用自定义端点
aws --endpoint-url http://localhost:7070 s3 ls
```

### Python 客户端示例

```python
import boto3

# 创建 S3 客户端
s3_client = boto3.client(
    's3',
    endpoint_url='http://localhost:7070',
    aws_access_key_id='user1',
    aws_secret_access_key='user1secret',
    region_name='us-east-1'
)

# 创建存储桶
s3_client.create_bucket(Bucket='my-bucket')

# 上传文件
s3_client.upload_file('local-file.txt', 'my-bucket', 'remote-file.txt')

# 下载文件
s3_client.download_file('my-bucket', 'remote-file.txt', 'downloaded-file.txt')
```

## 高级功能

### Lustre 条带化优化

对于大文件，系统会自动应用 Lustre 条带化优化：

- 文件大小 < 1MB: 单条带
- 1MB ≤ 文件大小 < 100MB: 2条带
- 100MB ≤ 文件大小 < 1GB: 4条带  
- 文件大小 ≥ 1GB: 8条带

可以通过用户配置自定义条带参数：
```json
"backend_config": {
  "stripe_count": 16,
  "stripe_size": 4194304
}
```

### 动态挂载管理

```bash
# 查看用户挂载状态
curl http://localhost:7070/admin/mounts

# 手动挂载用户存储
curl -X POST http://localhost:7070/admin/mounts/user1 \
  -H "Authorization: Bearer $ADMIN_TOKEN"

# 卸载用户存储
curl -X DELETE http://localhost:7070/admin/mounts/user1 \
  -H "Authorization: Bearer $ADMIN_TOKEN"
```

### 配额监控

```bash
# 查看用户配额使用情况
curl http://localhost:7070/admin/users/user1/quota \
  -H "Authorization: Bearer $ADMIN_TOKEN"

# 更新用户配额
curl -X PUT http://localhost:7070/admin/users/user1/quota \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"storage_quota": 107374182400}'
```

## 监控和日志

### 指标监控

访问 `http://localhost:7070/metrics` 获取 Prometheus 格式的监控指标。

主要指标包括：
- `versitygw_active_users`: 活跃用户数
- `versitygw_storage_usage`: 存储使用量
- `versitygw_mount_points`: 挂载点数量
- `versitygw_requests_total`: 请求总数

### 日志配置

```json
"monitoring": {
  "log_level": "info",
  "enable_audit_log": true,
  "audit_log_path": "/var/log/versitygw/audit.log"
}
```

## 故障排除

### 常见问题

1. **挂载失败**
   - 检查存储后端是否可访问
   - 验证认证信息是否正确
   - 查看系统日志: `journalctl -u versitygw`

2. **权限问题**
   - 确保 versitygw 用户有足够权限
   - 检查挂载点权限设置

3. **配额超限**
   - 查看用户配额使用情况
   - 调整用户配额或清理不需要的数据

### 调试模式

```bash
# 启用调试日志
./versitygw multitenant --debug --log-level debug
```

## 性能优化

### CephFS 优化
- 使用多个 Monitor 节点
- 配置适当的缓存大小
- 启用压缩以减少网络传输

### Lustre 优化
- 根据工作负载调整条带参数
- 使用 OST 池进行负载均衡
- 配置适当的 I/O 缓冲区大小

### 网络优化
- 使用万兆以太网或 InfiniBand
- 调整 TCP 缓冲区大小
- 启用多路径以提高带宽

## 安全建议

1. **网络安全**
   - 使用 HTTPS/TLS 加密传输
   - 配置防火墙规则
   - 限制访问来源 IP

2. **认证安全**
   - 使用强密码策略
   - 定期轮换访问密钥
   - 启用审计日志

3. **存储安全**
   - 启用存储加密
   - 配置适当的文件权限
   - 定期备份重要数据

## 支持和维护

- GitHub Issues: https://github.com/versity/versitygw/issues
- 文档: https://github.com/versity/versitygw/wiki
- 社区论坛: [待建设]

定期更新系统和依赖包以获得最新的安全补丁和功能改进。