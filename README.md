# WP Panel

WordPress 专用服务器管理面板。一行命令，纯净 Debian 13 变身 WordPress 托管平台。

[![License](https://img.shields.io/badge/license-GPL--3.0-blue.svg)](LICENSE)
[![Go](https://img.shields.io/badge/Go-1.22%2B-00ADD8.svg)](https://go.dev/)

---

## 定位

通用 Linux 面板臃肿、复杂、与 WordPress 无关的功能太多。

WP Panel 只做一件事：**在 VPS 上高效管理 WordPress 网站**。不做 Docker、不做邮件系统、不做 FTP、不做 Java/Python/Node 运行环境。

## 功能模块

| 模块 | 说明 |
|------|------|
| **网站管理** | 一键建站（自动创建隔离用户/目录/Nginx/PHP-FPM/数据库）、暂停/启用/删除、重装 WordPress |
| **SSL 证书** | Let's Encrypt 自动申请、到期前 30 天自动续签、手动替换、自签名证书 |
| **FastCGI 缓存** | Nginx 全站静态化缓存，配套 WordPress 插件一键清除 |
| **安全防御** | Fail2ban + nftables 双机制渐进封禁、Cloudflare/Google/Bing 官方白名单、全局限速 |
| **数据库管理** | MariaDB 密码修改、数据库备份/恢复/上传恢复/自动备份 |
| **计划任务** | 可视化 Cron 管理、WP Cron 替代、文件增量备份、系统任务查看 |
| **文件管理器** | 上传/下载/删除/重命名/压缩/解压/剪切/复制/粘贴/多选、分片上传+断点续传 |
| **仪表盘** | CPU/内存/磁盘/负载实时监控、24h/7d/15d 历史趋势图 |
| **告警通知** | SMTP 邮件告警、CPU/内存/磁盘/服务/SSL/网站到期/系统更新/面板更新规则独立开关 |
| **软件管理** | PHP/Nginx/MariaDB/Redis 配置修改、进程守护、日志查看 |
| **面板安全** | 随机入口 + BasicAuth + Web 双重认证、bcrypt 密码哈希、登录失败封禁 |
| **版本更新** | 面板内一键检查更新、SHA256 + Ed25519 双重校验、失败自动回滚、国内可配置反代地址 |

## 一键安装

```bash
apt-get update && apt-get install -y wget ca-certificates && wget -qO- https://raw.githubusercontent.com/naibabiji/wp-panel/main/install.sh | bash
```

**国内服务器**：GitHub 无法访问时，使用国内优化版脚本：

```bash
apt-get update && apt-get install -y wget ca-certificates && wget -qO- https://gh.wp-panel.org/https://raw.githubusercontent.com/naibabiji/wp-panel/main/install-cn.sh | bash
```

安装完成后输出面板地址和两层登录凭据（BasicAuth + Web 登录）。

> 自签名证书首次访问浏览器提示不安全，点击「高级」→「继续访问」即可。

## 安全性

**一句话：只要登录地址和账号密码不泄露，别人进不来。**

面板有四层防御：
- 第零层：**扫描防御** — 非浏览器请求触碰 8443 端口直接被 Nftables 丢弃数据包，封禁 30 天
- 第一层：随机入口 — 8 位随机路径（如 `/26858785`），扫描器不可能猜到
- 第二层：BasicAuth — 浏览器弹窗要求输入账号密码
- 第三层：Web 登录 — 网页表单要求输入面板登录密码

四层全通过的人才能进入面板。任何一层失败 5 次即被封禁。

---

更详细的安全机制：

**访问防护**
- **扫描防御**：面板 8443 端口自动检测非浏览器请求（curl、脚本、扫描器），网络层直接封禁
- 随机入口地址（62^8 ≈ 218 万亿种组合，暴力扫描不可行）
- BasicAuth + Web 登录双重认证
- 纯 HTTPS 加密通信，仅对外开放 8443 一个端口
- API 错误信息不泄露内部路径和命令输出

**防爆破**
- 任一认证层连续 5 次失败 → Nftables 网络层封禁 24 小时
- 多级渐进封禁：10分钟 → 24小时 → 30天 → 永久

**站点隔离**
- 每个网站运行在独立的系统用户和 PHP-FPM Pool 下
- 每个网站使用独立的 MariaDB 数据库
- 一个网站出问题不影响其他网站

**WordPress 专项防护**
- 自动检测并封禁 wp-login.php 暴力破解和 xmlrpc.php 恶意请求
- 敏感文件扫描检测（.env、.git、压缩包等） → 自动封禁
- 404 泛滥检测：30 次/60 秒判定为目录扫描
- Nginx 拒绝未知域名的 HTTPS 连接，避免证书信息泄露
- 已登录 WordPress 用户自动豁免限速，不影响后台操作

**更新安全**
- 面板更新 SHA256 + Ed25519 双重校验，攻击者即使篡改 GitHub Release 也无法伪造签名
- 更新失败自动回滚到旧版本，不影响面板正常运行

**代码透明**
- 100% 开源（GPL-3.0），代码可审查
- 不收集任何数据，不连接任何外部服务器
- 无 Web Shell、无在线代码编辑功能
- 密码 bcrypt 12 轮哈希存储，不留明文
- 三轮 AI 安全审计已修复 44 项潜在问题

## 安全测试

欢迎白帽和安全研究人员对本项目进行安全测试。如果你发现安全漏洞，请通过以下方式反馈：

- **公开反馈**：提交 [GitHub Issue](https://github.com/naibabiji/wp-panel/issues)，在标题标注 `[安全]`
- **私下反馈**：通过 GitHub Security 标签页提交 Private Vulnerability Report
- 有效漏洞会在修复后于 Release Notes 中向报告者致谢

## 系统要求

| 项目 | 要求 |
|------|------|
| 操作系统 | Debian 13 (Trixie) |
| CPU | 1 核及以上 |
| 内存 | 1 GB 及以上（低于自动创建 Swap） |
| 架构 | x86_64 |

> 各云厂商魔改镜像可能导致未知问题。安装遇到困难时，建议使用 [bin456789/reinstall](https://github.com/bin456789/reinstall) 重装为纯净 Debian 13 后重试。

## 为什么选择这些技术方案

**为什么是 Debian 13？**

Debian 是服务器领域稳定性最高的发行版之一。Trixie（Debian 13）在面板开发启动时是最新稳定版，拥有最新内核、较新的软件包版本，同时保持 Debian 一贯的保守稳定策略。选择这个版本意味着面板可以享受长周期的安全更新支持，用户无需频繁升级系统。

**为什么锁定 PHP 8.3？**

WordPress 官方推荐 PHP 8.3 或更高版本。8.3 在 WordPress 生态中经过了最广泛的生产环境验证，拥有活跃支持周期，性能与安全性持续改进。固定版本意味着所有用户运行相同的 PHP 环境，问题可复现、可排查，避免因 PHP 版本差异导致的兼容性怪病。

**为什么是 MariaDB 而非 MySQL？**

WordPress 官方推荐 MariaDB 10.6 或更高版本。Debian 12/13 自带的 MariaDB 均满足此要求。Oracle MySQL 存在许可证和功能限制风险，MariaDB 是完全兼容的 GPL 分支，由社区驱动。
Oracle MySQL 存在许可证和功能限制风险。MariaDB 是 MySQL 的 GPL 分支，完全兼容且由社区驱动。Debian 源自带的 MariaDB LTS 版本提供到 2028 年的安全更新，无需添加第三方仓库。

**为什么是自己编的 Go 二进制，不用 Docker/PM2？**

单一二进制文件，0 依赖，`systemd` 守护。占用十几 MB 内存，适合 1G 小 VPS。不与 Nginx 共用端口，各自独立提供 HTTPS。没有容器层，没有运行时开销。

## 运行组件

所有组件通过 APT 包管理器安装，面板不自行编译：

| 组件 | 说明 |
|------|------|
| PHP 8.3 | Ondřej Surý 源，独立 FPM Pool 隔离 |
| MariaDB | Debian 自带 LTS 版本 |
| Nginx | Debian 自带稳定版 |
| Redis | Debian 自带 |
| Fail2ban + nftables | Debian 自带 |

## 技术架构

- **后端**：Go + Gin Web 框架，SQLite (WAL 模式)，端口 8443 (HTTPS/TLS)
- **前端**：HTML 模板 + TailwindCSS + Alpine.js + Chart.js
- **分发**：单一二进制文件（前端资源通过 `//go:embed` 编译内嵌），约 20 MB
- **安全**：面板不与 Nginx 反向代理耦合，独立 TLS 加密

## SSH 管理命令

安装后面板提供 `wp` 命令行工具：

| 命令 | 说明 |
|------|------|
| `wp` | 查看面板信息 |
| `wp restart` | 重启面板 |
| `wp password` | 一键重置管理员账号密码 |
| `wp info` | 查看版本/端口/入口 |
| `wp status` | 查看运行状态 |
| `wp unban` | 清空所有 IP 封禁（管理员被误封时紧急恢复） |

## 面板数据库备份与恢复

面板使用 SQLite 存储数据，每天凌晨 2:30 自动备份到 `/www/server/panel/backups/panel-db/`，保留最近 7 份。

### 面板正常时

在「面板设置」页面可以：
- 手动创建备份
- 下载备份文件到本地
- 从备份恢复（恢复前自动创建安全备份，恢复后面板自动重启）
- 删除备份

### 面板无法启动时的恢复步骤

如果面板恢复数据库后无法启动，或数据库损坏导致面板无法运行，请通过 SSH 手动恢复：

```bash
# 1. 查看可用备份
ls -lh /www/server/panel/backups/panel-db/

# 2. 停止面板
systemctl stop wp-panel

# 3. 备份当前损坏的数据库（以防万一）
cp /www/server/panel/panel.db /www/server/panel/panel.db.broken

# 4. 用备份替换当前数据库（替换为实际的备份文件名）
cp /www/server/panel/backups/panel-db/panel_20260107_023000.db /www/server/panel/panel.db

# 5. 启动面板
systemctl start wp-panel

# 6. 检查是否正常
systemctl status wp-panel
journalctl -u wp-panel -n 20
```

### 重装面板后导入备份

如果需要完全重装面板并恢复数据：

```bash
# 1. 先保存备份文件到安全位置
cp -r /www/server/panel/backups/panel-db/ /root/panel-db-backup/

# 2. 重装面板（选择"卸载后重新安装"，保留网站数据）

# 3. 安装完成后停止面板
systemctl stop wp-panel

# 4. 用备份替换新数据库
cp /root/panel-db-backup/panel_20260107_023000.db /www/server/panel/panel.db

# 5. 启动面板（自动执行数据库升级）
systemctl start wp-panel
```

> **注意**：旧版备份可能缺少新版数据库字段，面板启动时会自动通过升级链补齐。

## 项目结构

```
├── main.go               # 程序入口
├── config/               # 全局配置管理
├── database/             # SQLite 连接与迁移
├── models/               # 数据结构
├── router/               # 路由 + 页面分发
├── middleware/            # BasicAuth / Session / CSRF / 登录限流
├── handlers/             # HTTP 处理器
├── executor/             # 任务执行器
├── collector/            # 系统指标采集
├── templates/            # HTML 模板
├── static/               # JS
├── input.css             # TailwindCSS 源文件
├── install.sh            # 一键安装脚本
├── install-cn.sh         # 国内优化版安装脚本
├── docs/                 # 部署文档
└── wp-panel-optimizer/   # WordPress 配套插件
```

## License

GPL-3.0
