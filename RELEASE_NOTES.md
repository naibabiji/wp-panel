**Bug 修复**
- 修复全新安装后 fail2ban 拒绝启动（三个缺失场景）：
  - 网站目录存在但日志文件未生成 → 自动 touch access.log/error.log
  - SSH jail 日志 /var/log/auth.log 不存在 → 自动 touch
  - 零网站场景 → 创建占位日志目录确保 jail glob 不报错
