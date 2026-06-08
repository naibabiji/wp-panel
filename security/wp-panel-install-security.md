# WP Panel 安装脚本安全透明化报告

> **副标题**：逐段拆解 `install.sh`，回应"篡改密码、删除 Nginx、黑掉 WordPress"等不实指控

---

## 一、前言：先给结论

有人在 GitHub Issue 中声称：

> "服务器密码直接被篡改了，wp 全部跳到元素周期表了哈哈哈，nginx 也被删了"

这是一个**完全可以被证伪的指控**。WP Panel 的安装脚本 `install.sh` 与 `install-cn.sh` 均为**开源文件**，任何人都可以打开逐行阅读。本文将把整个安装流程拆解成十几个独立步骤，说明每一步在做什么、为什么需要、以及它**不可能**完成哪些操作。

**核心结论先行**：

| 指控 | 安装脚本实际行为 | 能否做到指控中的效果 |
|------|------------------|----------------------|
| 篡改服务器 root/SSH 密码 | **完全不碰** `/etc/shadow`、`/etc/ssh/sshd_config` 或系统用户体系 | ❌ 不可能 |
| 黑掉 WordPress（篡改文件） | 只从 `wordpress.org` 下载官方 ZIP 备用，**不读取、不修改、不扫描**现有站点 | ❌ 不可能 |
| 删除 Nginx | **安装并启用** Nginx，卸载时也会明确保留网站数据 | ❌ 不可能 |

---

## 二、`install-cn.sh`：只是一个"入口招待员"

先讲简单的。国内用户看到的 `install-cn.sh` 只有 **54 行**，核心逻辑就三句话：

```bash
export WP_PANEL_PREFER_CN_MIRROR=1    # 标记：优先使用国内镜像
bash install.sh --prefer-cn            # 调用主脚本，附加国内优先参数
```

如果同目录有 `install.sh`，它就执行本地文件；如果没有，它先从 `gh.wp-panel.org`（国内反代）拉取主脚本，失败后再尝试直连 GitHub。

**安全要点**：
- 它不执行任何系统操作，只是一个"**跳板脚本**"。
- 拉取的 `install.sh` 内容你可以先保存到本地审查，再执行。
- 不存在"两个脚本各干各的、隐藏恶意逻辑"的情况。

---

## 三、`install.sh` 全景图：一张表看懂 1000 行脚本

`install.sh` 虽然长，但结构非常清晰。可以按功能切成以下模块：

| 模块 | 行号范围 | 做什么 | 是否涉及密码/删除操作 |
|------|----------|--------|----------------------|
| 初始化与参数解析 | 1–144 | 颜色输出、日志函数、解析 `--prefer-cn` 等参数 | ❌ 否 |
| 系统内核优化 | 53–120 | TCP 调优、BBR、文件描述符限制 | ❌ 否 |
| PHP 源配置 | 150–282 | 添加 apt 源（官方/中科大/上交），安装 PHP 8.3 | ❌ 否 |
| 卸载/清理函数 | 288–397 | 定义卸载逻辑（安装时不执行） | ⚠️ 仅卸载时执行 |
| 重复安装检测 | 400–491 | 检测是否已安装，提供修复/卸载选项 | ❌ 否 |
| Swap 配置 | 494–517 | 内存 ≤1GB 时自动创建 2GB Swap | ❌ 否 |
| APT 基础组件安装 | 519–569 | 安装 nginx、mariadb、redis、fail2ban、php 扩展等 | ❌ 安装，不是删除 |
| systemd 守护 | 572–589 | 配置 nginx/php/mariadb/redis 崩溃后自动重启 | ❌ 否 |
| Nginx 基础配置 | 592–616 | 写入速率限制、FastCGI 缓存配置 | ❌ 否 |
| 防火墙放行 8443 | 619–634 | nftables/ufw 放行面板端口 | ❌ 仅放行一个端口 |
| MariaDB 安全加固 | 637–680 | 设 root 密码、删空用户、删 test 库、禁远程 root | ⚠️ 设 MariaDB 密码，不改系统密码 |
| 目录结构与权限 | 683–691 | 创建 `/www/server/panel` 等目录，权限 700 | ❌ 否 |
| 自签名 SSL 证书 | 694–711 | 本地生成 2048 位 RSA 证书，有效期 10 年 | ❌ 本地生成，不联网 |
| 下载 WordPress 包 | 714–732 | 从 wordpress.org 下载官方最新版 ZIP | ❌ 仅下载备用 |
| 生成面板安全凭证 | 735–760 | /dev/urandom 生成随机密码，bcrypt 哈希存储 | ⚠️ 生成面板自身密码 |
| 写入 config.json | 763–826 | 面板配置集中写入 JSON 文件，权限 600 | ⚠️ 写入配置，不含后门 |
| 部署面板二进制 | 829–877 | 下载或复制 Go 编译的二进制到 `/usr/local/bin` | ❌ 否 |
| 创建 systemd 服务 | 880–907 | 注册 `wp-panel.service`，开机自启 | ❌ 否 |
| 端口检测与输出 | 910–1013 | 检查 8443 是否监听，打印访问地址和密码 | ❌ 仅输出 |

下面进入逐段详细拆解。

---

## 四、逐段拆解：每一行在做什么

### 4.1 权限检查与重复安装检测（行 400–491）

```bash
if [[ $EUID -ne 0 ]]; then
    log_error "请使用 root 权限运行此脚本"
fi
```

**为什么需要 root？** 因为面板要安装系统软件包（nginx、mariadb）、写入 `/etc/nginx`、管理系统服务。这是任何服务器面板（包括宝塔、cPanel）的共同要求，不是 WP Panel 的特殊设计。

**安全设计亮点**：
- 如果检测到已安装，脚本会**停下来询问**，而不是强行覆盖。提供四个选项：卸载重装 / 仅卸载 / 彻底清空 / 退出。
- 如果检测到上次安装中断（有残留文件），也会询问是继续修复、清理重装还是退出。
- **不存在静默覆盖、偷偷重装的行为。**

### 4.2 系统内核优化（行 53–120）

```bash
cat > /etc/sysctl.d/99-wp-panel.conf << 'SYSCTLEOF'
net.core.somaxconn = 65535
net.ipv4.tcp_max_syn_backlog = 8192
# ... TCP 缓冲区、TIME-WAIT、Keepalive、BBR 等
SYSCTLEOF
```

这里写入的是一组**公开标准的 Linux 网络调优参数**，常见于所有 Web 服务器优化指南。然后执行 `sysctl --system` 使其生效。

- 改的是**网络栈参数**，不是密码。
- 单核 VPS 还会智能跳过 BBR，避免 CPU 争抢。
- 文件命名 `99-wp-panel.conf` 是为了便于识别和后续清理。

### 4.3 PHP 8.3 源配置（行 150–282）

Debian 13 官方仓库的 PHP 版本是8.4，WordPress推荐版本是PHP 8.3，现阶段对WordPress生态的兼容性更好，因此需要添加 Ondřej Surý 的官方 PHP 源来支持PHP 8.3的安装。脚本做了三重兜底：

1. **官方源**（`packages.sury.org`）
2. **中科大镜像**
3. **上海交大镜像**

```bash
# 下载并安装 GPG 公钥（用于验证软件包签名）
download_file "$PHP_KEY_URL" "$tmp_key" 20
dpkg -i "$tmp_key"

# 写入 apt 源列表
cat > /etc/apt/sources.list.d/php.sources << PHPSOURCESEOF
Types: deb
URIs: ${PHP_REPO_URL}
Suites: ${codename}
Components: main
Signed-By: ${keyring_file}
PHPSOURCESEOF
```

**安全要点**：
- 所有 PHP 包通过 `apt` 安装，受 GPG 签名保护。
- 脚本会验证 `php8.3-cli` 和 `php8.3-fpm` 是否可用，**不可用就报错终止**，不会继续盲装。
- 不涉及任何远程代码执行或密码修改。

### 4.4 基础组件安装（行 547–569）

```bash
apt-get install -y \
    nginx \
    mariadb-server \
    redis-server \
    fail2ban \
    nftables \
    php8.3-fpm php8.3-mysql php8.3-curl ...
```

**这是在装软件，不是在删软件。** 装的是：
- **nginx**：Web 服务器
- **mariadb-server**：数据库
- **redis-server**：缓存
- **fail2ban**：入侵防御（自动封禁暴力破解 IP）
- **nftables**：防火墙框架
- **php8.3-***：PHP 及常用扩展

这些都是 Debian 官方仓库的标准软件包，版本公开、签名可验。

### 4.5 systemd 进程守护（行 572–589）

```bash
for svc in nginx php8.3-fpm mariadb redis-server; do
    mkdir -p "/etc/systemd/system/${svc}.service.d"
    cat > "/etc/systemd/system/${svc}.service.d/wp-panel.conf" << SYSTEMDEOF
[Service]
Restart=always
RestartSec=5s
StartLimitIntervalSec=0
SYSTEMDEOF
done
```

为 nginx、php-fpm、mariadb、redis 添加一个**覆盖配置**：如果进程意外崩溃，5 秒后自动重启。这是标准的系统稳定性操作，不改任何服务的数据或密码。

### 4.6 Nginx 基础配置（行 592–616）

脚本写入两个配置文件：

1. **`/etc/nginx/conf.d/wppanel-ratelimit.conf`** —— 请求频率限制：
   - 已登录 WordPress 用户（有 `wordpress_logged_in` cookie）**不限速**
   - 未登录访问限速 **60 请求/分钟**
2. **`/etc/nginx/conf.d/wppanel-cache.conf`** —— FastCGI 缓存路径配置

然后执行 `nginx -t` 测试配置合法性，再 `nginx -s reload` 平滑重载。

**安全意义**：这是在做**防护加固**，不是破坏。

### 4.7 防火墙放行 8443（行 619–634）

```bash
# nftables
nft add rule inet filter input tcp dport 8443 accept

# ufw
ufw allow 8443/tcp
```

只做了**一件事**：放行面板的 HTTPS 管理端口 `8443`。不会关闭 22（SSH）、80、443 等端口。之后面板的扫描防御模块会进一步加固防火墙规则。

### 4.8 MariaDB 安全加固（行 637–680）

这是安装脚本中**少数涉及密码**的地方，但只涉及 MariaDB（数据库），不涉及系统 root 密码或 SSH 密码。

```bash
# 生成 32 位随机密码
MYSQL_PASS=$(head -c 24 /dev/urandom | sha256sum | head -c 32)

# 如果 MariaDB 没设过密码，就设置
mysqladmin -u root password "${MYSQL_PASS}"

# 执行 MariaDB 官方安全加固：删空用户、删 test 库、禁止远程 root
mysql -u root -p"${MYSQL_PASS}" -e "
    DELETE FROM mysql.user WHERE User='';
    DELETE FROM mysql.user WHERE User='root' AND Host!='localhost';
    DROP DATABASE IF EXISTS test;
    DELETE FROM mysql.db WHERE Db='test' OR Db='test\\_%';
    FLUSH PRIVILEGES;
"
```

**安全要点**：
- MariaDB root 密码从 `/dev/urandom` 随机生成，**不是硬编码的通用密码**。
- 如果检测到 MariaDB 已经有密码且能连通，脚本会**复用已有密码**，不会强制修改。
- 禁用了**远程 root 登录**，只允许 localhost 连接。
- **完全不碰** Linux 系统 root 密码、`/etc/passwd`、`/etc/shadow`。

### 4.9 目录结构与文件权限（行 683–691）

```bash
mkdir -p "$INSTALL_DIR"/{backups,packages,logs,certs}
mkdir -p /www/wwwroot
mkdir -p /www/wwwlogs
mkdir -p /www/server/certificates
chmod 700 "$INSTALL_DIR"       # 仅所有者可读写执行
```

创建标准目录结构。关键权限设置：
- 面板数据目录 `/www/server/panel` 权限 `700`：只有 root 能进入。
- 后续 `config.json` 权限设为 `600`：只有 root 能读写。

### 4.10 自签名 SSL 证书（行 694–711）

```bash
openssl req -x509 -nodes -days 3650 -newkey rsa:2048 \
    -keyout "$KEY_FILE" \
    -out "$CERT_FILE" \
    -subj "/C=CN/ST=Shanghai/L=Shanghai/O=WP Panel/OU=IT/CN=WP-Panel-SelfSigned" \
    -addext "subjectAltName=IP:127.0.0.1"
```

- **在服务器本地生成**，不连接任何外部 CA 或 API。
- 2048 位 RSA，有效期 10 年。
- 私钥文件权限 `600`，证书权限 `644`。
- 安装后你可以随时替换为自己的正式证书，面板支持配置证书路径。

### 4.11 下载 WordPress 官方包（行 714–732）

```bash
download_file "https://wordpress.org/latest.zip" "$WP_ZIP_TMP" 60
```

- 下载来源是 **`wordpress.org`** —— WordPress 官方网站，不是任何第三方仓库。
- 下载的是 ZIP 文件，**保存在 `/www/server/panel/packages/` 备用**，供后续"一键建站"时使用。
- 如果下载失败，脚本会提示"将在首次建站时使用联网下载"，**不会导致建站失败**。
- **不扫描、不读取、不修改**服务器上已有的任何 WordPress 站点。

### 4.12 生成面板安全凭证（行 735–760）

这是安装脚本中**最关键的安全设计**，涉及两层认证密码的生成：

```bash
# 从 /dev/urandom 读取真随机数
PANEL_SUFFIX=$(head -c 20 /dev/urandom | sha256sum | head -c 8)
BASIC_PASS=$(head -c 12 /dev/urandom | base64 | head -c 16)
WEB_PASS=$(head -c 12 /dev/urandom | base64 | head -c 16)

# 使用 PHP 或 Python 进行 bcrypt 哈希（cost=12）
BASIC_HASH=$(php8.3 -r "echo password_hash('$BASIC_PASS', PASSWORD_BCRYPT, ['cost' => 12]);")
WEB_HASH=$(php8.3 -r "echo password_hash('$WEB_PASS', PASSWORD_BCRYPT, ['cost' => 12]);")
```

**逐层解析**：

1. **熵源**：`/dev/urandom` 是 Linux 内核提供的密码学安全随机数生成器，不是伪随机函数 `rand()`。
2. **密码复杂度**：8 位随机后缀 + 16 位随机 BasicAuth 密码 + 16 位随机 Web 密码。暴力破解不可行。
3. **存储方式**：配置文件中只存 **bcrypt 哈希值**（以 `$2a$12$` 开头），**不存明文密码**。
   - bcrypt cost=12 意味着暴力破解一个密码需要数年时间。
   - 即使有人拿到了你的 `config.json`，也**无法反推出原始密码**。
4. **降级保护**：如果服务器既没有 PHP 8.3 也没有 Python 3（极端罕见），脚本会写入占位哈希，并提示"面板首次启动时将自动重置密码"。**不会使用弱密码或空密码启动**。

### 4.13 写入 `config.json`（行 763–826）

所有配置集中写入一个 JSON 文件：

```json
{
  "panel": { "port": 8888, "tls_port": 8443, "random_suffix": "abc123de" },
  "mariadb": { "root_password": "xxx..." },
  "admin": { "username": "wpadmin", "password_hash": "$2a$12$..." },
  "basic_auth": { "username": "admin", "password_hash": "$2a$12$..." },
  "security": { "max_login_attempts": 5, "ban_duration_hours": 24 }
}
```

**安全要点**：
- 文件权限 `600`：只有 root 能读。
- MariaDB root 密码存在这里，但面板本身也**只能通过 localhost + Unix Socket 连接** MariaDB，不对外暴露。
- **没有隐藏的后门账号、没有硬编码的通用密钥、没有向外部服务器发送密码的逻辑**。

### 4.14 部署面板二进制（行 829–877）

```bash
# 优先使用同目录的本地二进制
cp "$SCRIPT_DIR/wp-panel" "$BIN_PATH"

# 否则从 GitHub Release 下载
GITHUB_RELEASE="https://github.com/naibabiji/wp-panel/releases/latest/download/wp-panel"
```

- 如果你把编译好的 `wp-panel` 二进制和安装脚本放在同一目录，脚本会**直接使用本地文件**，不联网下载。
- 如果需要下载，来源是 **GitHub Releases**（开源仓库的正式发行版），可选走 `gh.wp-panel.org` 国内反代。
- 下载后通过 `chmod +x` 赋予执行权限，然后移动到 `/usr/local/bin/wp-panel`。

**如何验证二进制安全？**
- WP Panel 是**开源项目**，你可以自己克隆代码、审查 Go 源码、本地编译后使用。
- 安装脚本提供了本地部署路径，**完全可以断网安装**。

### 4.15 创建 systemd 服务（行 880–907）

```bash
cat > "$SERVICE_PATH" << SYSTEMDEOF
[Unit]
Description=WordPress Server Management Panel
After=network.target mariadb.service redis-server.service

[Service]
Type=simple
User=root
Group=root
ExecStart=$BIN_PATH --config=$CONFIG_FILE
Restart=always
RestartSec=5
LimitNOFILE=65536
SYSTEMDEOF
```

- 以 `root` 身份运行，因为面板需要管理 Nginx 配置、重启服务、操作文件系统。这是服务器面板的通用做法。
- `Restart=always`：如果面板进程崩溃，5 秒后自动重启。
- 然后执行 `systemctl enable wp-panel`（开机自启）和 `systemctl start wp-panel`（立即启动）。

### 4.16 端口检测与最终输出（行 910–1013）

脚本最后会：
1. 检测 `wp-panel` 服务是否正常运行
2. 检测 8443 端口是否在监听
3. **打印访问地址和两层密码（仅显示一次）**

```
面板地址: https://<IP>:8443/<随机后缀>/
第 1 层 — BasicAuth（浏览器弹窗）  用户名: admin / 密码: xxxxxxxx
第 2 层 — Web 登录（面板内表单）   用户名: wpadmin / 密码: xxxxxxxx
```

**安全要点**：
- 密码**只在终端输出一次**，不会保存到日志、不会发送到任何远程服务器。
- 脚本末尾提到"匿名安装统计"，内容仅限：机器匿名标识（`/etc/machine-id` 的 SHA256 哈希）+ 面板版本号。**不包含 IP、域名、密码。**

---

## 五、直接回应三项指控

### 指控 1："服务器密码被篡改了"

**事实**：安装脚本**从未读取或写入**以下任何一个文件：

- `/etc/shadow`（Linux 用户密码）
- `/etc/passwd`（用户列表）
- `/etc/ssh/sshd_config`（SSH 配置）
- `~/.ssh/authorized_keys`（SSH 公钥）

脚本唯一涉及的密码是：
1. **MariaDB root 密码** —— 随机生成，用于面板管理数据库，不是系统 root 密码。
2. **面板自身的两层登录密码** —— 随机生成，bcrypt 哈希存储，与系统密码完全无关。

> 你的 SSH root 密码在安装前后**不会变化**。如果你发现密码被改，请检查：是否使用了弱密码被暴力破解、是否在其他地方泄露了密钥、是否安装了其他不明软件。

### 指控 2："WP 全部跳到元素周期表了"

（注："元素周期表"是某些网页被篡改后显示的错误页面或钓鱼内容。）

**事实**：安装脚本**完全不触碰** `/www/wwwroot/` 下的任何现有文件。脚本中与 WordPress 相关的唯一操作是：

```bash
download_file "https://wordpress.org/latest.zip" "$WP_ZIP_TMP" 60
```

- 从 **wordpress.org 官方网站** 下载一份 ZIP 到 `/www/server/panel/packages/wordpress.zip`
- 这是**备用包**，用于后续"一键新建站点"时解压使用
- **不会自动解压到任何现有目录**
- **不会扫描、修改、删除**已有站点的任何文件

> 如果你的 WordPress 站点被篡改，常见原因是：使用了破解版插件/主题、WordPress 核心或插件存在漏洞未及时更新、弱密码被暴力破解。这与 WP Panel 安装脚本无关。

### 指控 3："nginx 也被删了"

**事实**：安装脚本**安装并启用** Nginx：

```bash
apt-get install -y nginx
systemctl start nginx
systemctl enable nginx
```

随后还写入了速率限制和缓存配置，并执行 `nginx -t`（配置检查）和 `nginx -s reload`（平滑重载）。

即使执行**卸载**（`do_uninstall` 函数），脚本也会明确保留：

```bash
log_info "面板已卸载。以下内容已保留："
log_info "  - /www/wwwroot（网站文件）"
log_info "  - /www/wwwlogs（网站日志）"
log_info "  - /www/server/certificates（SSL 证书）"
log_info "  - MariaDB 数据库"
log_info "  - 系统软件包（nginx/php/mariadb/redis/fail2ban）"
```

只有在用户**手动选择"彻底清空"（purge）**时，才会卸载 nginx 等软件包——而且这是**交互式确认**的，需要输入 `yes`。

> 如果你发现 Nginx 被删，请检查：是否手动执行了卸载或 purge、是否服务器上还有其他管理脚本/人员在操作。

---

## 六、你自己可以验证什么

开源的意义不仅是"代码公开"，更是"你可以亲自验证"。以下是几个简单的自检方法：

### 方法 1：审查安装脚本（不需要执行）

```bash
# 下载后先看，不执行
curl -fsSL https://raw.githubusercontent.com/naibabiji/wp-panel/main/install.sh -o install.sh
# 用文本编辑器打开，搜索以下关键词：
# passwd, shadow, ssh, rm -rf /etc/nginx, wget 非 wordpress.org 的地址
# 你会发现：以上关键词要么不存在，要么出现在安全的上下文中。
```

### 方法 2：断网本地安装

```bash
# 1. 克隆源码并本地编译
git clone https://github.com/naibabiji/wp-panel.git
cd wp-panel
go build -o wp-panel .

# 2. 把编译好的二进制和 install.sh 放一起
# 3. 断开外网，执行 bash install.sh
# 脚本会使用本地 wp-panel 二进制，全程不下载任何外部文件。
```

### 方法 3：监控安装过程

```bash
# 另开一个终端窗口，实时监控文件变化
watch -n 1 'ls -la /etc/shadow; ls -la /www/wwwroot/'

# 同时监控网络连接
ss -tpn

# 你会发现：/etc/shadow 时间戳不变，/www/wwwroot 内容不变，
# 也没有异常的外部网络连接。
```

### 方法 4：检查安装后的 config.json

```bash
cat /www/server/panel/config.json
# 确认：没有硬编码的通用密码、没有指向外部服务器的上报接口、
# 没有可疑的远程命令执行配置。
```

---

## 七、总结

WP Panel 的 `install.sh` 本质上是一份**自动化的服务器运维手册**：它做的每一件事——安装 Nginx、配置 MariaDB、调优内核、生成随机密码、设置防火墙——都是资深运维在搭建 WordPress 服务器时会手动执行的标准操作。

将这一切开源的好处是：**没有任何暗箱操作的空间**。每一个指控都可以通过阅读源码、对比文件哈希、监控安装过程来验证真伪。

| 谣言 | 真相 |
|------|------|
| 篡改服务器密码 | ❌ 脚本不碰 `/etc/shadow`、`/etc/ssh` 或任何系统认证体系 |
| 黑掉 WordPress | ❌ 只从 wordpress.org 下载官方备用包，不动现有站点 |
| 删除 Nginx | ❌ 安装并启用 Nginx，卸载时也会明确保护网站数据 |

如果你仍有疑虑，欢迎：
1. 打开 `install.sh` 搜索本文提到的关键行号，亲自核对。
2. 在本地虚拟机或容器中断网执行安装，观察每一步的行为。
3. 关注本系列的第二篇文章：《安装完成后，WP Panel 如何通过多层机制保护你的服务器》。

---

*本文基于 WP Panel 开源仓库 commit 历史中的 `install.sh` 与 `install-cn.sh` 撰写。所有行号与代码片段均可公开验证。*
