# 更新说明

## v1.2.25

### 新增：WordPress 文件锁定

- WordPress 站点详情页新增「文件锁定」开关，可一键锁定站点代码目录。
- 开启后，站点仍可正常发布文章、编辑页面、上传媒体、生成缓存、写入翻译文件和安全插件日志等运行数据。
- 插件、主题、核心文件、`wp-config.php` 等代码和配置文件修改会被阻止。
- 解锁时会自动移除面板托管的 `DISALLOW_FILE_MODS` 设置，并恢复常规站点权限。
- 如果用户自己在 `wp-config.php` 中定义了 `DISALLOW_FILE_MODS=true`，解锁后会明确提示仍存在用户自定义限制。

### 安全基线

- WordPress 默认 Nginx 模板新增 `wp-content` 运行数据区 PHP 执行拦截，阻止 `plugins`、`themes`、`mu-plugins` 之外的 `.php`、`.phtml`、`.phar`、`php数字后缀` 文件被直接执行。
- 默认阻止直接访问 `wp-config.php`、`wordfence-waf.php`、`php.ini` 等根目录敏感文件。
- 该规则作为 WordPress 默认安全基线生效，不依赖是否开启文件锁定。
- 面板启动时会重建全部站点 Nginx 配置，升级后已有 WordPress 站点也会获得该规则。

### 文件安全事件

- 安全防御页新增「文件安全事件」，记录运行数据目录 PHP 执行尝试和本机扫描发现的可疑 PHP 文件。
- Nginx 安全日志会记录 `wp-content` 运行数据区 PHP 访问尝试，便于管理员结合 IP 来源手动判断。
- 面板可扫描 `uploads`、`cache`、`languages`、`wflogs` 下的 PHP 可执行文件，仅展示和告警，不自动封禁、不删除文件。
- 扫描发现的可疑文件移除后，事件会标记为已处理。

### 面板文件操作防护

- 文件管理器、远程导入、上传、删除、重命名、压缩、解压、复制、移动等写入操作增加锁定校验。
- 锁定状态下允许写入 `wp-content` 下已存在或面板内置的运行数据目录，例如 `uploads`、`cache`、`languages`、`wflogs`，但禁止写入 PHP 可执行文件。
- `wp-content/upgrade` 和 `wp-content/upgrade-temp-backup` 属于 WordPress 更新临时目录，锁定状态下不作为运行数据目录开放。
- 面板安装/更新配套优化插件、保存会写入 `wp-config.php` 的 WordPress 优化项、重装 WordPress、同步数据库配置等维护操作会要求先解除锁定。
- 已开启文件锁定的站点会跳过面板启动时的配套优化插件自动更新，避免自动写入插件代码。

### 配套 WordPress 插件

- `WP Panel Optimizer` 升级到 `1.1.7`。
- 插件后台会显示醒目的文件锁定提示，避免管理员误以为无法安装或修改插件主题是网站故障。
- 普通后台页面的锁定状态同步增加 5 分钟缓存，避免每个后台页面加载都实时请求面板。
- 插件设置页仍会强制刷新锁定状态，确保保存前判断准确。

### 数据库升级与兼容

- `websites` 表新增 `file_lock_enabled` 字段。
- 新增 `file_security_events` 表，用于保存 WordPress 文件安全事件。
- 新安装和老用户升级路径均已处理，已有站点默认不启用文件锁定。
- 文件锁定仅支持 WordPress 站点，不影响通用 PHP 站点。

### 验证

- 新增 Linux / WSL 验证脚本 `scripts/verify.sh`，作为 `scripts\verify.ps1` 的等价脚本。
- 已覆盖数据库升级、Nginx 模板、`wp-config.php` 托管块、文件锁定写入守卫等测试。
