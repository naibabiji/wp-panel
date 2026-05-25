## v1.0.0-beta10

**新功能**
- SSH 暴力破解防护：fail2ban 新增 `wppanel-sshd` jail，监控 `/var/log/auth.log`，自动封禁 SSH 登录攻击，封禁记录同步到面板后台

**Bug 修复**
- 修复解封 API 写死 jail 名称，导致 SSH 封禁无法解封
- 修复封禁等级映射不一致，永久封禁记录显示为"临时30天"
- 修复 `countLevel3` 无时间窗口，历史已过期封禁被误计入导致错误永久封禁
- 修复任务队列 `QueueLength` 永远返回 0
- 修复 fail2ban nftables 不同步时错误解封数据库记录
- 修复 session 滑动续期数据竞争

**安全加固**
- sshpass 密码传递从命令行 `-p` 改为环境变量 `SSHPASS`，避免暴露在进程列表
- 服务器设置时区/主机名输入校验从黑名单改为白名单正则，防止命令注入绕过
- 前端 5 处 API 调用修复 Content-Type 丢失

**代码质量**
- 移除 `isDirEmpty`、`pruneNginxBackups`、`getCSRFToken` 等未使用函数
- 移除 `scan_defense` `isBrowserLike` 中无效的 Accept 头检查代码
- `RemovePersistBan` 补充 IP 格式校验，对齐 `AddPersistBan`
- 防火墙页面来源标签和颜色优化，支持 Web/404/SSH 三种防护区分
