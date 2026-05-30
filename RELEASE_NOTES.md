## v1.2.0-rc9

### 安全审计（25 项修复）

**漏洞修复（12 项）**：
- SQL LIKE `%`/`_` 通配符跨站越权：`website.go` 6 处 LIKE 查询的 domain 参数未转义
- `/tmp` 固定路径符号链接 ×4：面板更新、备份上传、远程备份测试、重装 WordPress 临时文件
- 上传合并临时文件缺少 `O_EXCL`：大文件分片合并可被符号链接预占
- 压缩文件路径穿越：单项 `Compress` 缺少 `isPathWithin` 越权检查
- 二阶 SQL 注入：`ClearDatabase` 的 `TABLE_NAME` 反引号未转义
- SMTP 邮件头注入：`subject`/`to` 未过滤 `\r\n`
- `wp-config.php` 权限 0644→0600 防凭据泄露
- `configPath` 硬编码忽略 `--config` 命令行参数
- nftables 初始化 data race→`sync.Once`

**健壮性修复（12 项）**：
- goroutine panic recover：`GoSafe` 封装统一保护 ×12 处
- `db.Exec` 错误静默忽略 ×5 处
- `DB.QueryRow`/`Exec` 错误忽略 ×7 处
- 日志全量加载 OOM→`tailFile` ×2 处
- `tailFile` O(N²)→O(N)
- `config.json` WriteFile 错误检查
- HTTP 内 `apt-get install sshpass`→启动阶段安装
- `EnsureWPCommand` 写入错误记录日志
- 网站监控串行 curl→并发 `http.Client`

**UX 改进（1 项）**：
- 系统更新页超时警告 + 勿重复点击提示

### 补充安全修复 — Codex 审计（8 项）

基于外部 Codex Security 安全审计报告修复：

- **Cron 命令注入**：名称拒绝反引号/美元符/反斜杠；WP Cron 域名格式验证；无 `runAsUser` 的命令用 `bash -c '...'` 单引号包裹；executor 层 `sanitizeCronArg` 防御深度
- **软件配置注入**：Key 白名单（每软件仅允许 UI 展示的 key）；Value 拒绝换行；Nginx 配置拒绝分号
- **Nginx 别名注入**：更新域名时验证别名格式；`buildServerNames` 防御性过滤无效别名；`isValidDomain` 导出统一验证
- **DB 密码未转义**：`phpSingleQuoteEscape` 对 `\` 和 `'` 转义，`generateWPConfig`/`FixWPConfigCredentials`/`ChangeDBPassword` 三处全覆盖
- **公告栏 XSS**：远程 ANNOUNCEMENT.md 渲染 `x-html`→`x-text`，切断"GitHub 被黑→服务器被黑"攻击链
- **SSH 远程备份主机密钥**：专用 `known_hosts` 文件 + `ssh-keyscan` 预扫描 + 返回指纹供管理员核对 + sync 用 `StrictHostKeyChecking=yes`
- **Webhook SSRF**：`isSafeWebhookURL` DNS 解析后拒绝回环/内网/链路本地 IP；`safeWebhookClient` 连接时二次校验防 DNS rebinding
- **WordPress 优化器权限**：缓存清除 `admin_bar_button` + `handle_clear` 加 `current_user_can('manage_options')`

### 补充修复 — GLM 交叉审核（2 项）

基于 GLM AI 对以上 Commit 的交叉审核修复：

- **handler/executor 域名验证不一致**：删除 `domainOrIPRe`，handler 统一使用 `executor.IsValidDomain`，避免 IP 地址 WP Cron 被静默丢弃且无错误提示
- **Webhook DNS rebinding**：在 `http.Transport.DialContext` 中连接时再次解析并校验目标 IP，覆盖"校验通过后 DNS 修改"和"HTTP 重定向到内网"两种 SSRF 路径

---

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
