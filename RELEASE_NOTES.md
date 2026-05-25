## v1.1.0-beta3

**Bug 修复**
- 修复全新安装后 fail2ban 无法启动（四重兜底）：
  - 遍历网站目录自动补建 access.log/error.log
  - 自动补建 /var/log/auth.log（SSH jail）
  - 零网站时创建占位日志目录确保 jail glob 不报错
  - install.sh 新增 rsyslog 依赖
