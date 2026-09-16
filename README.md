# WP Panel

WordPress 专用服务器管理面板。一行命令，纯净 Debian 13 变身 WordPress 托管平台。

WordPress server management panel for Debian 13 VPS environments, focused on site isolation, SSL, backups, security, and day-to-day WordPress hosting operations.

## English Documentation

The full English project guide is available here: [README.en.md](README.en.md).

[![License](https://img.shields.io/badge/license-GPL--3.0-blue.svg)](LICENSE)
[![Go](https://img.shields.io/badge/Go-1.26-00ADD8.svg)](https://go.dev/)

---

## 官方来源

- 官方网站：<https://wp-panel.org>
- 帮助中心：<https://wp-panel.org/help/>
- GitHub 项目地址：<https://github.com/naibabiji/wp-panel>

除 `wp-panel.org` 和本 GitHub 仓库外，其他域名均不是 WP Panel 官方网站，与本项目无关。

---

## 定位

通用 Linux 面板臃肿、复杂、与 WordPress 无关的功能太多。

WP Panel 只做一件事：**在 VPS 上高效管理 WordPress 网站**。不做 Docker、不做邮件系统、不做 FTP、不做 Java/Python/Node 运行环境。

## 功能模块

| 模块 | 说明 |
|------|------|
| **网站管理** | 一键创建 WordPress 网站，也可暂停、启用、删除或重装；每个网站相互隔离，一个网站出问题时不容易影响其他网站 |
| **网站搬家** | 在两台相同版本的 WP Panel 服务器之间搬迁网站，可一次选择多个网站，查看各自进度并重试失败任务 |
| **WordPress 更新管理** | 在更新核心、插件或主题前先确认内容并自动备份；更新后检查网站是否正常，失败时自动恢复，插件还可批量更新 |
| **WordPress 站点总览** | 在一个页面查看所有 WordPress 网站的版本、插件、主题和待更新项目，不必逐个登录后台检查 |
| **WordPress 维护保护** | 更新或维护网站时自动显示维护页面，结束后恢复访问；临时维护到期后也会自动恢复，减少忘记开启网站的风险 |
| **SSL 证书** | Let's Encrypt 自动申请、到期前 30 天自动续签、手动替换、自签名证书 |
| **网站加速** | 提供页面缓存，并可从 WordPress 后台一键清理；上传图片时也可按设置自动优化，减少图片占用和加载时间 |
| **安全防御** | 自动拦截常见的登录爆破、恶意扫描和高频爬虫，并整理可疑访问记录，方便判断是否需要处理 |
| **密码找回保护** | 可按网站决定是否允许找回密码，也可以只禁止管理员账号通过公开页面找回，降低账号被试探的风险 |
| **数据库管理** | 修改数据库密码，手动或自动备份数据库，并可上传备份进行恢复；需要临时管理时可按需开启管理工具 |
| **计划任务** | 用页面管理定时任务，可用更可靠的系统任务替代 WordPress 自带定时任务，并查看备份等任务是否正常运行 |
| **文件管理器** | 像使用电脑文件管理器一样上传、下载、复制、移动、压缩、解压和搜索文件；大文件支持断点续传 |
| **仪表盘** | CPU/内存/磁盘/负载实时监控、24h/7d/15d 历史趋势图 |
| **系统稳定性防护** | 内存不足时自动增加缓冲空间；重要服务异常、内存耗尽或资源使用异常时保留线索并发出提醒 |
| **网站异常监测** | 关注管理员账号、内容、重要设置、应用密码和异常文件变化，帮助尽早发现网站被篡改的迹象 |
| **AI 诊断** | 一键汇总网站日志和服务状态，用对话方式继续追问问题；只提供分析建议，不会自行修改网站 |
| **告警通知** | 可通过邮件接收资源不足、服务异常、证书到期、网站到期和可用更新等提醒，每类提醒可单独开关 |
| **软件与运行环境** | 在面板中管理 PHP、Nginx、MariaDB 和 Redis，查看日志，并按网站调整 PHP 或 Nginx 设置 |
| **面板安全** | 使用不公开的登录入口和两次登录验证；连续输错密码或频繁扫描错误地址时会自动限制来源 |
| **安全更新** | 面板和 Debian 系统软件都可检查更新；面板更新会验证文件是否可信，失败时自动恢复旧版本 |
| **备份与异地保存** | 自动备份网站和面板数据，并可把网站备份同步到另一台服务器或对象存储，减少单机故障造成的损失 |

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

## 网站搬家

WP Panel 支持在两台相同版本的面板之间搬迁 WordPress 或通用 PHP 网站。升级两台服务器后，从「网站管理」进入「网站搬家」，建立面板连接并选择需要迁移的网站。

- 可迁移网站文件、数据库、域名与别名、SSL 证书，以及主要的 PHP、Nginx、监控、计划任务和 WordPress 运行设置。
- 备份历史、访问日志、安全事件历史、服务器级远程备份凭据和自定义命令类计划任务不会迁移。
- 搬家期间源网站会进入 HTTP 503 维护状态，避免迁移过程中继续产生新数据。
- 接收端不会覆盖同域名网站；迁移完成后仍需管理员自行检查业务并调整 DNS/CDN 解析。
- 搬家不是同一瞬间完成的整机快照。建议选择访问量较低的时段操作，并在切换域名解析前检查前台、后台、表单和订单等关键功能。

开始操作前，请前往[帮助中心](https://wp-panel.org/help/)阅读“两台 WP Panel 之间的网站搬家”，了解准备条件、迁移步骤和完成后的处理方式。

## 安全性

**一句话：在服务器和用户电脑没有先被攻破、登录入口与两组登录信息没有泄露、面板保持更新的前提下，外部人员仅靠公网扫描或猜测，几乎无法进入 WP Panel。**

这是因为正常登录必须同时知道每台服务器独有的随机入口，并依次通过浏览器弹窗和网页登录。反复寻找入口或猜测密码还会触发自动限制。没有任何联网软件能承诺绝对不会被攻破，但 WP Panel 不会把安全只押在一个密码上。

---

更详细的安全机制：

**访问防护**
- 每台服务器都有独立的随机入口，能提高陌生扫描者找到登录页的难度
- 直接使用扫描工具试探面板，或在一分钟内访问 10 个不同的错误地址，会触发自动限制
- 即使找到入口，仍需依次通过浏览器弹窗和网页登录
- 登录过程使用 HTTPS 加密，错误提示也会尽量避免暴露服务器内部信息

**防爆破**
- 浏览器弹窗或网页登录在短时间内连续失败 5 次，会限制该来源 24 小时
- 网站登录和 SSH 也有独立保护；重复攻击时限制时间会逐步延长，最长 7 天

**站点隔离**
- 每个网站运行在独立的系统用户和 PHP-FPM Pool 下
- 每个网站使用独立的 MariaDB 数据库
- 一个网站出问题不影响其他网站

**WordPress 专项防护**
- 自动识别反复尝试登录、批量寻找常见敏感文件和短时间访问大量不存在页面的行为
- 拒绝使用陌生域名访问服务器，减少网站和证书信息被探测的机会
- 将可疑访问按风险高低整理，并给出可疑来源、访问目标和处理建议；默认只分析，不会仅凭分析结果自动封禁
- 监测网站目录中新出现的可疑 PHP 文件和异常高频访问，留下安全事件记录
- 可单独限制高频爬虫，尽量减少对普通访客和日常后台操作的影响

**AI 运维诊断**
- 一次收集与网站故障有关的日志和运行状态，并可继续追问，减少新手来回寻找信息的困难
- 诊断只给出分析和排查建议，不会自动修改文件、数据库或服务器设置

**备份与异地保存**
- 网站备份可同步到另一台服务器或 S3 兼容存储；同步失败时可按设置保留本地副本

**更新安全**
- 更新前会验证安装包确实来自 WP Panel，避免使用被替换或损坏的文件
- 更新失败会自动恢复旧版本，尽量让面板继续可用

**代码透明**
- 100% 开源（GPL-3.0），代码可审查
- 不收集敏感业务数据，匿名统计（仅版本号）可在面板中一键关闭
- 更新检查仅连接 GitHub，不连接其他外部服务
- 无 Web Shell、无在线代码编辑功能
- 密码 bcrypt 12 轮哈希存储，不留明文

### 📖 安全深度解读

- **[安装脚本安全透明化报告](security/wp-panel-install-security.md)** — 逐段拆解 install.sh，回应"篡改密码、删除 Nginx、黑掉 WordPress"等指控
- **[运行时安全：多层防护机制](security/wp-panel-runtime-security.md)** — 源码层面解析六层纵深防御、更新签名校验、软件漏洞管理

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

WordPress 官方推荐 MariaDB 10.6 或更高版本。Debian 自带的 MariaDB 满足此要求。MariaDB 是由社区驱动的 GPL 分支，兼容 MySQL，并可直接获得 Debian 软件源提供的安全更新，无需添加第三方数据库仓库。

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

安装后面板提供 `wpp` 命令行工具：

| 命令 | 说明 |
|------|------|
| `wpp` | 查看面板信息 |
| `wpp restart` | 重启面板 |
| `wpp password` | 一键重置管理员账号密码 |
| `wpp info` | 查看版本/端口/入口 |
| `wpp status` | 查看运行状态 |
| `wpp unban` | 清空所有 IP 封禁（管理员被误封时紧急恢复） |

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
├── security/             # 安全说明文档
└── wp-panel-optimizer/   # WordPress 配套插件
```

## License

GPL-3.0
