## v1.1.0-beta9 更新内容

### 安全加固
- API Key 配置文件移出 Web 目录，存放于 /var/wp-panel/site-secrets/，不再暴露在 wp-content 下
- 插件 API 仅允许本机（127.0.0.1/::1）调用，外部无法通过 API Key 访问面板
- Nginx 配置自动拦截 wp-panel-config.json 公网访问请求（返回 404）
- 文件管理器修复软链接绕过目录限制的漏洞
- 计划任务增加全面输入校验，防止非法命令注入

### Bug 修复
- 修复开启/关闭 SSL 后 Nginx 自定义配置丢失的问题（HTTPS 模板补全了 include 指令）
- PHP-FPM 配置写入前增加语法检查，错误配置自动回滚不影响服务
- 修复 SSL 证书续期后 Nginx 未加载新证书的问题
- 修复域名变更时 Nginx 配置部分字段丢失的 bug
- 插件配置读取兼容 www/non-www 域名

### 升级说明
- 旧站点启动时自动迁移配置文件、更新插件、重建 Nginx/PHP-FPM 配置
- bootstrap.sh 已删除，正式版安装请使用 install.sh
