# 更新说明

## v1.2.24

### 新增：S3 兼容对象存储远程备份

- 远程备份新增「S3 兼容对象存储」后端，可用于 Cloudflare R2、AWS S3、MinIO、Backblaze B2 等兼容服务。
- 保留原有 SSH / rsync 远程备份方式，已配置 rsync 的服务器升级后默认行为不变。
- 设置页新增备份目标选择，可在 SSH / rsync 与 S3 兼容对象存储之间切换。
- S3 配置支持 Endpoint、Bucket、Region、Access Key ID、Secret Access Key 和路径前缀。
- Cloudflare R2 可使用 `auto` 作为 Region，并使用账号专属 R2 Endpoint。

### 改进：大文件备份上传

- S3 后端支持 multipart upload，大文件会自动分片上传，上传完成后在对象存储中仍显示为一个完整备份对象。
- 单次分片上传失败时会自动 abort multipart upload，减少远程未完成分片残留。
- 小文件继续使用单次 PUT 上传，避免不必要的分片流程。
- S3 文件上传超时调整为更适合备份场景的长超时，连接测试仍保持短超时。

### 安全与兼容

- S3 Endpoint 必须使用 HTTPS。
- Bucket、Region、Access Key ID、路径前缀等配置均增加格式校验。
- Secret Access Key 和 SSH 密码在接口返回时继续脱敏显示。
- 本地上传路径仍限制在面板备份目录下，避免任意文件被同步。
- S3 上传使用标准 SigV4 签名，不依赖 rclone 或额外系统命令。

### 数据库升级

- `remote_backup_settings` 新增 S3 远程备份配置字段。
- 新装数据库和老版本升级路径均已处理。
- 老用户升级后默认 `backup_type` 为 `rsync`，不会自动切换备份后端。

### 测试

- 增加远程备份类型和 S3 参数校验测试。
- 增加新装和老版本升级的数据库字段测试。
- 增加 mock S3 服务测试，覆盖连接探测、multipart 成功上传和失败 abort。
- 增加 S3 XML complete 请求测试，确认 `Content-Type: application/xml` 和 ETag XML 格式。
