#!/bin/bash
set -e

# ============================================================
# WP Panel 安装脚本（国内优化版）
# 自动使用上海交大镜像源，解决 GitHub/packages.sury.org 国内无法访问的问题
# 适用于 Debian 13 (Trixie)，建议使用纯净系统
# ============================================================

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BOLD='\033[1m'
NC='\033[0m'

INSTALL_DIR="/www/server/panel"
CONFIG_FILE="$INSTALL_DIR/config.json"
DB_PATH="$INSTALL_DIR/panel.db"
BIN_PATH="/usr/local/bin/wp-panel"
SERVICE_PATH="/etc/systemd/system/wp-panel.service"
PANEL_PORT=8888
MYSQL_PASS=""

# 国内镜像
MIRROR="https://mirror.sjtu.edu.cn"

log_info()  { echo -e "${GREEN}[INFO]${NC} $1"; }
log_warn()  { echo -e "${YELLOW}[WARN]${NC} $1"; }
log_error() { echo -e "${RED}[ERROR]${NC} $1"; exit 1; }

# ============================================================
# 卸载函数（定义在前，兼容管道执行）
# ============================================================

do_uninstall() {
    echo ""
    echo -e "${BOLD}正在卸载面板，请稍候...${NC}"

    echo -e "  → 停止面板服务..."
    systemctl stop wp-panel 2>/dev/null || true
    systemctl disable wp-panel 2>/dev/null || true
    rm -f /etc/systemd/system/wp-panel.service
    systemctl daemon-reload
    echo -e "  ${GREEN}✓${NC} 面板服务已停止"

    echo -e "  → 删除面板文件..."
    rm -f "$BIN_PATH"
    rm -f /usr/local/bin/wp
    rm -rf "$INSTALL_DIR"
    echo -e "  ${GREEN}✓${NC} 面板文件已删除"

    echo -e "  → 清理 Nginx 面板配置..."
    rm -f /etc/nginx/conf.d/wppanel-ratelimit.conf
    rm -f /etc/nginx/conf.d/wppanel-cache.conf
    rm -f /etc/nginx/conf.d/wppanel-log.conf
    nginx -s reload 2>/dev/null || true
    echo -e "  ${GREEN}✓${NC} Nginx 配置已清理"

    echo ""
    log_info "面板已卸载。以下内容已保留："
    log_info "  - /www/wwwroot（网站文件）"
    log_info "  - /www/wwwlogs（网站日志）"
    log_info "  - /www/server/certificates（SSL 证书）"
    log_info "  - MariaDB 数据库"
    log_info "  - 系统软件包（nginx/php/mariadb/redis/fail2ban）"
}

do_purge() {
    echo ""
    echo -e "${RED}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
    echo -e "${RED}  警告：将删除所有网站数据和系统软件！${NC}"
    echo -e "${RED}  此操作不可逆，请谨慎选择。${NC}"
    echo -e "${RED}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
    echo ""
    echo -e "  输入 ${BOLD}yes${NC} 确认，直接回车取消。"

    confirm=""
    read -p "  > " confirm < /dev/tty 2>/dev/null || true
    if [[ "$confirm" != "yes" ]]; then
        log_info "已取消"
        return 1
    fi

    echo ""
    echo -e "${BOLD}正在清空，请耐心等待...${NC}"

    echo -e "  → 停止所有服务..."
    systemctl stop wp-panel 2>/dev/null || true
    systemctl stop nginx 2>/dev/null || true
    systemctl stop php8.3-fpm 2>/dev/null || true
    systemctl stop mariadb 2>/dev/null || true
    systemctl stop redis-server 2>/dev/null || true
    systemctl stop fail2ban 2>/dev/null || true
    echo -e "  ${GREEN}✓${NC} 服务已停止"

    echo -e "  → 清理网站 Nginx 和 PHP-FPM 配置..."
    rm -f /etc/nginx/sites-enabled/*
    rm -f /etc/nginx/sites-available/*
    rm -f /etc/php/8.3/fpm/pool.d/*.conf
    echo -e "  ${GREEN}✓${NC} 配置已清理"

    echo -e "  → 卸载软件包（可能需要 1-2 分钟）..."
    DEBIAN_FRONTEND=noninteractive apt-get purge -y nginx nginx-common mariadb-server mariadb-common redis-server fail2ban php8.3-* 2>/dev/null || true
    DEBIAN_FRONTEND=noninteractive apt-get autoremove -y 2>/dev/null || true
    echo -e "  ${GREEN}✓${NC} 软件包已卸载"

    echo -e "  → 清理 systemd 配置..."
    systemctl disable wp-panel 2>/dev/null || true
    rm -f /etc/systemd/system/wp-panel.service
    for svc in nginx php8.3-fpm mariadb redis-server; do
        rm -rf "/etc/systemd/system/${svc}.service.d/wp-panel.conf"
    done
    systemctl daemon-reload
    echo -e "  ${GREEN}✓${NC} systemd 已清理"

    echo -e "  → 删除面板文件..."
    rm -f "$BIN_PATH"
    rm -f /usr/local/bin/wp
    rm -rf "$INSTALL_DIR"
    echo -e "  ${GREEN}✓${NC} 面板文件已删除"

    echo -e "  → 删除网站数据..."
    rm -rf /www/wwwroot /www/wwwlogs /www/server/certificates
    rm -f /etc/nginx/conf.d/wppanel-*.conf
    rm -rf /var/cache/nginx/fastcgi
    echo -e "  ${GREEN}✓${NC} 网站数据已删除"

    if grep -q "/swapfile" /etc/fstab 2>/dev/null; then
        echo -e "  → 清理 Swap 文件..."
        swapoff /swapfile 2>/dev/null || true
        rm -f /swapfile
        sed -i '/\/swapfile/d' /etc/fstab
        echo -e "  ${GREEN}✓${NC} Swap 已删除"
    fi

    echo ""
    log_info "全部清除完成，系统已恢复安装前状态"
}

# ============================================================
# 权限检查
# ============================================================
if [[ $EUID -ne 0 ]]; then
    log_error "请使用 root 权限运行此脚本"
fi
log_info "权限检查通过"

# ============================================================
# 重复安装检测
# ============================================================
if [[ -f "$CONFIG_FILE" ]] && [[ -f "$BIN_PATH" ]]; then
    echo ""
    echo -e "${YELLOW}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
    echo -e "${YELLOW}  检测到 WP Panel 已安装${NC}"
    echo -e "${YELLOW}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
    echo ""
    echo -e "  1) 卸载面板（${GREEN}保留网站/数据库/SSL/软件${NC}）"
    echo -e "  2) 彻底清空（${RED}删除所有数据并卸载软件${NC}）"
    echo -e "  3) 退出"
    echo ""
    echo -e "  输入数字后回车进行选择。"

    read -p "  > " choice < /dev/tty 2>/dev/null || read choice

    case "${choice:-3}" in
        1)
            do_uninstall
            ;;
        2)
            do_purge
            ;;
        *)
            echo -e "${GREEN}已取消，面板保持现有状态${NC}"
            ;;
    esac
    exit 0
fi

# ============================================================
# 系统检测与Swap配置
# ============================================================
if ! grep -qi "debian" /etc/os-release 2>/dev/null; then
    log_error "此脚本仅支持 Debian 系统"
fi

TOTAL_MEM_KB=$(grep MemTotal /proc/meminfo | awk '{print $2}')
TOTAL_MEM_MB=$((TOTAL_MEM_KB / 1024))
log_info "物理内存: ${TOTAL_MEM_MB}MB"

if [[ $TOTAL_MEM_MB -le 1024 ]]; then
    log_info "内存 <= 1GB，创建 2GB Swap 分区..."
    SWAP_FILE="/swapfile"
    if [[ ! -f "$SWAP_FILE" ]]; then
        dd if=/dev/zero of=$SWAP_FILE bs=1M count=2048 status=progress
        chmod 600 $SWAP_FILE
        mkswap $SWAP_FILE
        swapon $SWAP_FILE
        echo "$SWAP_FILE none swap sw 0 0" >> /etc/fstab
        log_info "Swap 分区创建完成"
    else
        log_info "Swap 分区已存在，跳过"
    fi
fi

# ============================================================
# APT 源配置
# ============================================================
log_info "配置 APT 源..."
export DEBIAN_FRONTEND=noninteractive

DEBIAN_CODENAME=$(lsb_release -sc 2>/dev/null || grep VERSION_CODENAME /etc/os-release | cut -d= -f2)
log_info "Debian 版本: ${DEBIAN_CODENAME}"

# 保持系统默认源（腾讯云/阿里云等国内服务器已内置高速镜像）
apt-get update

# 安装基础依赖
apt-get install -y curl wget unzip ca-certificates gnupg lsb-release

# Ondřej Surý PHP 8.3 源（使用上海交大镜像，支持 trixie）
KEYRING="/usr/share/keyrings/debsuryorg-archive-keyring.gpg"

if [[ ! -f "$KEYRING" ]]; then
    log_info "下载 PHP Sury GPG 公钥..."
    KEY_OK=false
    for i in 1 2 3; do
        if curl -sSL --connect-timeout 10 -o /tmp/debsuryorg-archive-keyring.deb \
            "${MIRROR}/sury/debsuryorg-archive-keyring.deb" 2>/dev/null; then
            if dpkg -i /tmp/debsuryorg-archive-keyring.deb 2>/dev/null; then
                KEY_OK=true
                break
            fi
        fi
        log_warn "获取 keyring 失败，重试 ($i/3)..."
        sleep 2
    done
    rm -f /tmp/debsuryorg-archive-keyring.deb

    if ! $KEY_OK; then
        log_error "无法获取 PHP Sury GPG 公钥，请检查网络连接"
    fi
fi

# 始终写入（覆盖 install.sh 可能残留的 packages.sury.org 配置）
cat > /etc/apt/sources.list.d/php.sources << PHPSOURCESEOF
Types: deb
URIs: ${MIRROR}/sury/php/
Suites: ${DEBIAN_CODENAME}
Components: main
Signed-By: ${KEYRING}
PHPSOURCESEOF

apt-get update

# ============================================================
# 安装基础组件
# ============================================================
log_info "安装系统组件..."

apt-get install -y \
    nginx \
    mariadb-server \
    redis-server \
    fail2ban \
    nftables \
    rsyslog \
    cron \
    php8.3-fpm \
    php8.3-mysql \
    php8.3-curl \
    php8.3-gd \
    php8.3-mbstring \
    php8.3-xml \
    php8.3-zip \
    php8.3-intl \
    php8.3-redis \
    php8.3-opcache \
    php8.3-cli

log_info "基础组件安装完成"

# ============================================================
# systemd 进程守护配置
# ============================================================
log_info "配置 systemd 进程守护..."

for svc in nginx php8.3-fpm mariadb redis-server; do
    DROPDIR="/etc/systemd/system/${svc}.service.d"
    mkdir -p "$DROPDIR"
    cat > "$DROPDIR/wp-panel.conf" << SYSTEMDEOF
[Service]
Restart=always
RestartSec=5s
StartLimitIntervalSec=0
SYSTEMDEOF
done

systemctl daemon-reload
log_info "systemd 进程守护配置完成"

# ============================================================
# Nginx 基础配置
# ============================================================
log_info "配置 Nginx 基础..."

mkdir -p /etc/nginx/conf.d

cat > /etc/nginx/conf.d/wppanel-ratelimit.conf << 'RATELIMITEOF'
# WP Panel — 请求频率限制
# 已登录 WordPress 用户不限速
map $http_cookie $wp_rate_limit_key {
    ~*wordpress_logged_in "";
    default $binary_remote_addr;
}

limit_req_zone $wp_rate_limit_key zone=wp_req_limit:10m rate=60r/m;
RATELIMITEOF

# FastCGI 缓存
mkdir -p /var/cache/nginx/fastcgi
cat > /etc/nginx/conf.d/wppanel-cache.conf << 'CACHEEOF'
fastcgi_cache_path /var/cache/nginx/fastcgi levels=1:2 keys_zone=WP_CACHE:200m inactive=60m max_size=2g;
CACHEEOF

nginx -t && nginx -s reload 2>/dev/null || true
log_info "Nginx 基础配置完成"

# ============================================================
# 防火墙放行 8443 面板端口
# ============================================================
log_info "放行面板端口 8443..."

# nftables
if command -v nft &>/dev/null && nft list ruleset 2>/dev/null | grep -q "hook input"; then
    nft add rule inet filter input tcp dport 8443 accept 2>/dev/null || \
    nft add rule ip filter input tcp dport 8443 accept 2>/dev/null || true
    log_info "nftables 已放行 8443"
fi

# ufw
if command -v ufw &>/dev/null && ufw status 2>/dev/null | grep -q "Status: active"; then
    ufw allow 8443/tcp 2>/dev/null || true
    log_info "ufw 已放行 8443"
fi

# ============================================================
# MariaDB 安全加固
# ============================================================
log_info "配置 MariaDB..."

systemctl start mariadb
systemctl enable mariadb

# 优先读取已有密码（防止上次安装中断导致密码不一致）
MYSQL_PASS=$(grep -o '"root_password": "[^"]*"' "$CONFIG_FILE" 2>/dev/null | cut -d'"' -f4)
if [[ -z "$MYSQL_PASS" ]]; then
    MYSQL_PASS=$(head -c 24 /dev/urandom | sha256sum | head -c 32)
fi

if mysql -u root -p"${MYSQL_PASS}" -e "SELECT 1" 2>/dev/null; then
    log_info "MariaDB root 密码已验证"
elif mysql -u root -e "SELECT 1" 2>/dev/null; then
    mysqladmin -u root password "${MYSQL_PASS}" 2>/dev/null
    log_info "MariaDB root 密码已设置"
else
    log_warn "MariaDB 密码状态异常，面板首次启动时将自动修复"
fi

mysql -u root -p"${MYSQL_PASS}" -e "
    DELETE FROM mysql.user WHERE User='';
    DELETE FROM mysql.user WHERE User='root' AND Host!='localhost';
    DROP DATABASE IF EXISTS test;
    DELETE FROM mysql.db WHERE Db='test' OR Db='test\\_%';
    FLUSH PRIVILEGES;
" 2>/dev/null || log_warn "部分安全加固跳过(密码可能已设置)"

if [[ $TOTAL_MEM_MB -le 1024 ]]; then
    log_info "低内存环境，优化 MariaDB 配置..."
    cat > /etc/mysql/mariadb.conf.d/99-wp-panel.cnf << 'MARIADBEOF'
[mysqld]
innodb_buffer_pool_size = 128M
innodb_log_buffer_size = 8M
table_open_cache = 128
max_connections = 30
performance_schema = OFF
MARIADBEOF
    systemctl restart mariadb
fi

# ============================================================
# 目录结构创建
# ============================================================
log_info "创建目录结构..."

mkdir -p "$INSTALL_DIR"/{backups,packages,logs,certs}
mkdir -p /www/wwwroot
mkdir -p /www/wwwlogs
mkdir -p /www/server/certificates
chmod 700 "$INSTALL_DIR"

# ============================================================
# 生成自签名 SSL 证书（有效期 10 年）
# ============================================================
log_info "生成自签名 SSL 证书..."

CERT_DIR="$INSTALL_DIR/certs"
CERT_FILE="$CERT_DIR/panel.crt"
KEY_FILE="$CERT_DIR/panel.key"

openssl req -x509 -nodes -days 3650 -newkey rsa:2048 \
    -keyout "$KEY_FILE" \
    -out "$CERT_FILE" \
    -subj "/C=CN/ST=Shanghai/L=Shanghai/O=WP Panel/OU=IT/CN=WP-Panel-SelfSigned" \
    -addext "subjectAltName=IP:127.0.0.1" \
    2>/dev/null

chmod 600 "$KEY_FILE"
chmod 644 "$CERT_FILE"
log_info "自签名证书已生成（有效期 10 年）"

# ============================================================
# 下载 WordPress 备用包
# ============================================================
log_info "下载 WordPress 备用包..."
WP_ZIP="$INSTALL_DIR/packages/wordpress.zip"
for i in 1 2 3; do
    if wget -q -T 60 -O "$WP_ZIP" "https://wordpress.org/latest.zip" 2>/dev/null; then
        log_info "WordPress 下载完成"
        break
    fi
    log_warn "下载失败，重试 ($i/3)..."
    sleep 3
done
if [[ ! -f "$WP_ZIP" ]]; then
    log_warn "WordPress 下载失败，将在首次建站时使用联网下载"
fi

# ============================================================
# 生成面板安全凭证
# ============================================================
log_info "生成安全凭证..."

PANEL_SUFFIX=$(head -c 20 /dev/urandom | sha256sum | head -c 8)

BASIC_USER="admin"
BASIC_PASS=$(head -c 12 /dev/urandom | base64 | head -c 16)
WEB_USER="wpadmin"
WEB_PASS=$(head -c 12 /dev/urandom | base64 | head -c 16)

BASIC_HASH=""
WEB_HASH=""
if command -v php8.3 &>/dev/null; then
    BASIC_HASH=$(php8.3 -r "echo password_hash('$BASIC_PASS', PASSWORD_BCRYPT, ['cost' => 12]);" 2>/dev/null)
    WEB_HASH=$(php8.3 -r "echo password_hash('$WEB_PASS', PASSWORD_BCRYPT, ['cost' => 12]);" 2>/dev/null)
fi
if [[ -z "$BASIC_HASH" ]] && command -v python3 &>/dev/null; then
    BASIC_HASH=$(python3 -c "import bcrypt; print(bcrypt.hashpw(b'$BASIC_PASS', bcrypt.gensalt(12)).decode())" 2>/dev/null)
    WEB_HASH=$(python3 -c "import bcrypt; print(bcrypt.hashpw(b'$WEB_PASS', bcrypt.gensalt(12)).decode())" 2>/dev/null)
fi
if [[ -z "$BASIC_HASH" ]]; then
    log_warn "无法生成 bcrypt 哈希，面板首次启动时将自动重置密码"
    BASIC_HASH='$2a$12$00000000000000000000000000000000000000000000000000000'
    WEB_HASH='$2a$12$00000000000000000000000000000000000000000000000000000'
fi

# ============================================================
# 写入 config.json
# ============================================================
log_info "写入配置文件..."

cat > "$CONFIG_FILE" << CONFIGEOF
{
  "panel": {
    "version": "1.0.0-mvp",
    "port": $PANEL_PORT,
    "tls_port": 8443,
    "tls_cert_path": "$CERT_FILE",
    "tls_key_path": "$KEY_FILE",
    "random_suffix": "$PANEL_SUFFIX",
    "data_dir": "$INSTALL_DIR",
    "backup_dir": "$INSTALL_DIR/backups",
    "log_dir": "$INSTALL_DIR/logs"
  },
  "sqlite": {
    "path": "$DB_PATH"
  },
  "mariadb": {
    "host": "localhost",
    "port": 3306,
    "socket": "/run/mysqld/mysqld.sock",
    "root_user": "root",
    "root_password": "$MYSQL_PASS"
  },
  "admin": {
    "username": "$WEB_USER",
    "password_hash": "$WEB_HASH"
  },
  "basic_auth": {
    "username": "$BASIC_USER",
    "password_hash": "$BASIC_HASH"
  },
  "paths": {
    "www_root": "/www/wwwroot",
    "www_logs": "/www/wwwlogs",
    "nginx_sites_available": "/etc/nginx/sites-available",
    "nginx_sites_enabled": "/etc/nginx/sites-enabled",
    "php_fpm_pool": "/etc/php/8.3/fpm/pool.d",
    "php_fpm_sock": "/run/php",
    "certificates": "/www/server/certificates",
    "wordpress_package": "$INSTALL_DIR/packages/wordpress.zip",
    "cron_file": "/etc/cron.d/wp_panel_cron"
  },
  "security": {
    "basic_auth_enabled": true,
    "max_login_attempts": 5,
    "attempt_window_minutes": 5,
    "ban_duration_hours": 24,
    "auto_whitelist_enabled": true,
    "core_ports": [22, $PANEL_PORT, 80, 443, 8443]
  },
  "systemd": {
    "service_name": "wp-panel",
    "service_path": "$SERVICE_PATH",
    "binary_path": "$BIN_PATH"
  }
}
CONFIGEOF

chmod 600 "$CONFIG_FILE"

# ============================================================
# 部署 Go 二进制
# ============================================================
log_info "部署面板二进制..."

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
GITHUB_RELEASE="https://github.com/naibabiji/wp-panel/releases/latest/download/wp-panel"
GHPROXY="https://gh.wp-panel.org"

if [[ -f "$SCRIPT_DIR/wp-panel" ]]; then
    cp "$SCRIPT_DIR/wp-panel" "$BIN_PATH"
    chmod +x "$BIN_PATH"
    log_info "面板二进制已部署（本地文件）"
else
    log_info "下载面板二进制（优先 ghproxy 镜像）..."
    DOWNLOAD_OK=false

    # 1) ghproxy 反代（国内可用）
    if wget -q -T 60 -O "$BIN_PATH" "${GHPROXY}/${GITHUB_RELEASE}" 2>/dev/null; then
        chmod +x "$BIN_PATH"
        log_info "面板二进制下载完成（ghproxy 镜像）"
        DOWNLOAD_OK=true
    fi

    # 2) 直连 GitHub（备用，国内大概率失败）
    if ! $DOWNLOAD_OK; then
        log_warn "ghproxy 下载失败，尝试直连 GitHub..."
        if wget -q -T 30 -O "$BIN_PATH" "$GITHUB_RELEASE" 2>/dev/null; then
            chmod +x "$BIN_PATH"
            log_info "面板二进制下载完成（GitHub 直连）"
            DOWNLOAD_OK=true
        fi
    fi

    if ! $DOWNLOAD_OK; then
        log_error "无法获取面板二进制。解决方案：
  1. 在本机挂代理下载 wp-panel 二进制，上传到服务器
  2. 将 wp-panel 文件和 install-cn.sh 放在同一目录，重新运行脚本
  3. 或在本机编译后上传：go build -o wp-panel ."
    fi
fi

# ============================================================
# 创建 systemd 服务
# ============================================================
log_info "创建 systemd 服务..."

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
StandardOutput=journal
StandardError=journal
SyslogIdentifier=wp-panel
LimitNOFILE=65536

[Install]
WantedBy=multi-user.target
SYSTEMDEOF

systemctl daemon-reload
systemctl enable wp-panel
systemctl start wp-panel

# ============================================================
# 端口监听检测
# ============================================================
PORT_OK=false
if systemctl is-active --quiet wp-panel; then
    sleep 3
    for i in 1 2 3 4 5 6 7 8; do
        if ss -tlnp 2>/dev/null | grep -q ":8443"; then
            PORT_OK=true
            break
        fi
        sleep 2
    done
fi

# ============================================================
# 最终输出
# ============================================================
if systemctl is-active --quiet wp-panel; then
    STATUS="${GREEN}运行中${NC}"
else
    STATUS="${RED}未运行${NC}"
fi

LOCAL_IP=$(hostname -I 2>/dev/null | awk '{print $1}')
[[ -z "$LOCAL_IP" ]] && LOCAL_IP="<未知>"

PUBLIC_IP=$(curl -s --connect-timeout 5 ip.sb 2>/dev/null || curl -s --connect-timeout 5 ifconfig.me 2>/dev/null)
[[ -z "$PUBLIC_IP" ]] && PUBLIC_IP="<未知>"

echo ""
echo -e "${BOLD}============================================${NC}"
echo -e "${BOLD}  WP Panel 安装完成（国内镜像版）${NC}"
echo -e "${BOLD}============================================${NC}"
echo ""
echo -e "公网 IP:     ${BOLD}${PUBLIC_IP}${NC}"
echo -e "内网 IP:     ${BOLD}${LOCAL_IP}${NC}"
echo ""
echo -e "面板地址:    ${BOLD}https://${LOCAL_IP}:8443/${PANEL_SUFFIX}/${NC}"
if [[ "$PUBLIC_IP" != "<未知>" ]]; then
    echo -e "            ${BOLD}https://${PUBLIC_IP}:8443/${PANEL_SUFFIX}/${NC}"
fi
echo -e "面板状态:    ${STATUS}"
if $PORT_OK; then
    echo -e "端口监听:    ${GREEN}8443 已监听${NC}"
else
    echo -e "端口监听:    ${YELLOW}8443 未检测到监听，请查看日志: journalctl -u wp-panel -n 20${NC}"
fi
echo ""
echo -e "  ┌─────────────────────────────────────────┐"
echo -e "  │  第 1 层 — BasicAuth（浏览器弹窗）       │"
echo -e "  ├─────────────────────────────────────────┤"
echo -e "  │  用户名:  ${BOLD}${BASIC_USER}${NC}"
echo -e "  │  密  码:  ${BOLD}${BASIC_PASS}${NC}"
echo -e "  └─────────────────────────────────────────┘"
echo ""
echo -e "  ┌─────────────────────────────────────────┐"
echo -e "  │  第 2 层 — Web 登录（面板内表单）         │"
echo -e "  ├─────────────────────────────────────────┤"
echo -e "  │  用户名:  ${BOLD}${WEB_USER}${NC}"
echo -e "  │  密  码:  ${BOLD}${WEB_PASS}${NC}"
echo -e "  └─────────────────────────────────────────┘"
echo ""
echo -e "  ${BOLD}登录流程：${NC}"
echo -e "  1. 浏览器打开上方地址 → 弹出 BasicAuth 对话框"
echo -e "     → 输入 ${BOLD}第 1 层${NC} 的用户名和密码"
echo -e "  2. 通过后看到登录页面 → 输入 ${BOLD}第 2 层${NC} 的用户名和密码"
echo -e "  3. 进入控制台"
echo ""
echo -e "${YELLOW}⚠ 当前使用自签名证书，浏览器会提示「不安全」${NC}"
echo -e "${YELLOW}  请点击「高级」→「继续访问」即可进入面板${NC}"
echo -e "${YELLOW}  面板使用 8443 端口（HTTPS），与 Nginx 网站 443 端口不冲突${NC}"
echo ""
echo -e "${BOLD}无法访问？${NC}"
echo -e "  1. 云服务器请检查${YELLOW}安全组/防火墙${NC}是否放行 8443 端口"
echo -e "  2. 检查本地防火墙: ${BOLD}nft list ruleset${NC}"
echo -e "  3. 查看面板日志: ${BOLD}journalctl -u wp-panel -f${NC}"
echo ""
echo -e "${BOLD}软件安装路径:${NC}"
echo -e "  Nginx:      /etc/nginx/"
echo -e "  PHP-FPM:    /etc/php/8.3/fpm/"
echo -e "  MariaDB:    /etc/mysql/"
echo -e "  Redis:      /etc/redis/"
echo -e "  面板程序:   /usr/local/bin/wp-panel"
echo -e "  面板数据:   /www/server/panel/"
echo -e "  SSL 证书:   ${CERT_DIR}/"
echo ""
echo -e "${BOLD}面板 CLI (wp):${NC}"
echo -e "  wp              查看面板信息"
echo -e "  wp restart      重启面板"
echo -e "  wp password     一键重置管理员密码"
echo -e "  wp unban        一键清空所有IP封禁"
echo -e "  wp status       查看运行状态"
echo ""
echo -e "${YELLOW}请立即保存以上凭据，此信息仅显示一次${NC}"
echo ""
echo -e "${BOLD}匿名安装统计${NC}"
echo -e "  面板会每天上报一次匿名安装统计，内容仅包含："
echo -e "  机器匿名标识（/etc/machine-id 的 SHA256 哈希）"
echo -e "  面板版本号"
echo -e "  不会上报 IP、域名、网站信息等任何敏感数据。"
echo -e "  如需关闭，请在面板安全设置中关闭。"
echo ""
