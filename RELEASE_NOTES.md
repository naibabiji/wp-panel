## v1.2.0-rc8

### 安全审计（25 项修复）

**漏洞修复（12 项）**：
- SQL LIKE `%`/`_` 通配符跨站越权：`CacheHelperHandler` 6 处 LIKE 查询的 domain 参数未转义，攻击者可越权修改其他站点配置
- 二阶 SQL 注入：`ClearDatabase` 的 `TABLE_NAME` 反引号未转义，恶意表名可注入 SQL 语句以 root 权限执行
- `/tmp` 固定路径符号链接 ×4：面板更新、备份上传、远程备份测试、重装 WordPress 的临时文件使用可预测路径
- 上传合并临时文件缺少 `O_EXCL`：大文件分片合并时符号链接可被预占
- 压缩文件路径穿越：单项 `Compress` 缺少 `isPathWithin` 越权检查
- `wp-config.php` 权限 0644 泄露数据库凭据
- SMTP 邮件头注入：`subject`/`to` 未过滤 `\r\n`
- `configPath` 硬编码忽略 `--config` 命令行参数
- nftables 初始化 data race

**健壮性修复（13 项）**：
- goroutine 无 panic recover ×12 处：`GoSafe` 封装统一保护
- `db.Exec` 错误静默忽略 ×5 处：网站暂停/启用/删除、优化设置
- `DB.QueryRow`/`Exec` 错误忽略 ×7 处：版本升级模块
- 日志全量加载 OOM ×2 处：cron 日志、软件日志改用 `tailFile`
- `tailFile` 切片 prepend O(N²) 内存分配修复
- HTTP 接口内同步 `apt-get install sshpass` 移到启动阶段
- 网站监控串行 curl 改为并发 `http.Client`，20 站点 200s → ~10s

### 业务逻辑审查（19 项修复）

**备份系统（6 项）**：
- 上传恢复 `os.CreateTemp` 丢失扩展名导致 100% 失败
- `restoreFromGz` gunzip 失败时 mysql 进程泄漏，数据库被部分修改
- 备份文件 DB 记录写入失败仍返回成功，产生孤立文件
- `ClearDatabase` `StdinPipe`/`Start` 错误忽略可致 nil panic
- `cleanOldBackups` 未区分全量/增量文件，删除全量后增量不可恢复
- 增量备份不再自动清理（备份链为单一逻辑单元）

**网站生命周期（5 项）**：
- 域名变更 PHP-FPM Pool 回滚不重建旧配置，回滚后站点 502
- Nginx 回滚不恢复 `sites-enabled` 软链接
- 改密码先改 MariaDB 再写 `wp-config.php`，失败时不回滚
- `dropMariaDBDatabase` 忽略所有错误
- `CREATE USER IF NOT EXISTS` 导致重装时密码不一致

**定时任务（5 项）**：
- `wp_cron` 手动执行将域名当 shell 命令执行，100% 失败
- Update 接口 `enabled` 缺失时默认 1，意外启用已禁用任务
- Update 切换 `wp_cron` 类型未处理 `DISABLE_WP_CRON`
- 站点删除后 `site_id=NULL` 导致备份任务用无效 ID 执行
- Run 接口 check-and-set 非原子，双击可并发执行两次

**登录认证（3 项）**：
- 登录成功不清除 IP 失败计数，合法用户可被误封 24 小时
- SessionStore 无过期清理，新增 30 分钟定时 GC
- `banIP` 不检查已有封禁产生重复记录，解封不完全

**告警监控（4 项）**：
- `checkWebsiteExpiry` 去重 `alert_type` 前缀不匹配，每 60 秒重复告警
- SMTP "none" 加密跳过 AUTH 认证，邮件发送失败
- `checkSSL` 排除已过期证书，过期后无任何告警
- `alert_log.resolved` 从未更新，所有告警永远显示"未恢复"

### UX 改进
- 系统更新页新增超时警告：弹窗提示 + 黄色常驻文案，防止管理员重复点击

---
