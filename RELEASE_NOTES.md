## v1.2.0-rc1 候选发布版

### 安全加固
- API Key 配置文件移出 Web 目录至 /var/wp-panel/site-secrets/
- 插件 API 仅允许本机调用，RemoteAddr 防 X-Forwarded-For 伪造
- Gin SetTrustedProxies(nil) 全局禁用代理头信任
- WordPress + PHP 站点 Nginx 模板均拦截 wp-panel-config.json 公网访问
- nftables 持久封禁改用参数调用，消除 shell 命令注入风险
- 文件管理器修复软链接逃逸漏洞
- 计划任务增加全面输入校验防注入
- Commander 命令执行器收紧白名单，拒绝 shell 元字符
- 安全设置和告警设置增加字段白名单 + 范围/格式校验

### Bug 修复
- SSL 启用/关闭/续期不再丢失站点其他 Nginx 配置
- HTTPS Nginx 模板补充自定义 include
- PHP-FPM 配置写入前语法检查，失败自动回滚
- 速率限制配置修改前 nginx -t 检查，失败自动回滚
- 关闭限速时同步清理站点配置中的 limit_req 行，避免 Nginx reload 失败
- Nginx 模板按限速开关动态生成 limit_req 规则
- 域名变更时 Nginx 配置不再丢失已有字段
- 插件配置读取兼容 www/non-www 域名
- ApplyFail2banSettings 失败不再静默，错误返回前端

### 升级说明
- 旧站点启动时自动迁移配置文件、更新插件、重建 Nginx/PHP-FPM 配置
- bootstrap.sh 已删除，统一使用 install.sh + release 二进制
