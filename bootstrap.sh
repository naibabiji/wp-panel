#!/bin/bash
# ============================================================
# WP Panel 一键部署脚本
# 适用: bin456789/reinstall 重装后的纯净 Debian 13
# 用法:
#   在线安装:  bash bootstrap.sh https://github.com/naibabiji/wppanel.git
#   本地安装:  bash bootstrap.sh
# ============================================================
set -e

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BOLD='\033[1m'
NC='\033[0m'

log_info()  { echo -e "${GREEN}[INFO]${NC}  $1"; }
log_warn()  { echo -e "${YELLOW}[WARN]${NC}  $1"; }
log_error() { echo -e "${RED}[FATAL]${NC} $1"; exit 1; }
log_step()  { echo -e "\n${BOLD}>>> 步骤 $1: $2${NC}"; }

# ============================================================
# 0. 环境检测
# ============================================================
if [[ $EUID -ne 0 ]]; then
    log_error "请使用 root 权限运行此脚本: sudo bash bootstrap.sh"
fi

if ! grep -qi "debian" /etc/os-release 2>/dev/null; then
    log_error "此脚本仅支持 Debian 系统"
fi

ARCH=$(uname -m)
case $ARCH in
    x86_64)  GO_ARCH="amd64" ;;
    aarch64) GO_ARCH="arm64" ;;
    *)       log_error "不支持的 CPU 架构: $ARCH" ;;
esac

REPO_URL="${1:-}"
WORK_DIR="/opt/wppanel"

# ============================================================
# 1. 安装最小工具箱（裸系统可能没有 curl/wget/git/gnupg）
# ============================================================
log_step "1" "安装最小工具箱"

# 先更新 apt 源（裸系统这一步也可能失败，多试几次）
for i in 1 2 3; do
    if apt-get update -qq 2>/dev/null; then
        break
    fi
    log_warn "apt update 失败，重试 ($i/3)..."
    sleep 3
done

# 最小依赖：裸系统可能连 ca-certificates 都没有
# 分两批装，第一批确保 https 下载能力
log_info "安装基础工具 (ca-certificates curl wget gnupg git)..."
DEBIAN_FRONTEND=noninteractive apt-get install -y -qq \
    ca-certificates curl wget gnupg git unzip \
    2>/dev/null || {
    log_warn "部分包安装失败，逐个重试..."
    for pkg in ca-certificates curl wget gnupg git unzip; do
        DEBIAN_FRONTEND=noninteractive apt-get install -y -qq "$pkg" 2>/dev/null || true
    done
}

# 验证关键工具
for cmd in curl wget git; do
    if ! command -v $cmd &>/dev/null; then
        log_error "核心工具 $cmd 安装失败，请检查 apt 源配置"
    fi
done
log_info "最小工具箱就绪 (curl wget git gnupg unzip)"

# ============================================================
# 2. 获取源码
# ============================================================
log_step "2" "获取源码"

if [[ -n "$REPO_URL" ]]; then
    log_info "从 GitHub 克隆: $REPO_URL"
    rm -rf "$WORK_DIR" 2>/dev/null || true
    git clone "$REPO_URL" "$WORK_DIR"
    log_info "仓库克隆完成 → $WORK_DIR"
elif [[ -f "./go.mod" ]] && [[ -f "./main.go" ]]; then
    WORK_DIR="$(pwd)"
    log_info "使用当前目录: $WORK_DIR"
elif [[ -f "/opt/wppanel/go.mod" ]]; then
    WORK_DIR="/opt/wppanel"
    log_info "使用已有目录: $WORK_DIR"
else
    log_error "请提供 GitHub 仓库 URL: bash bootstrap.sh https://github.com/naibabiji/wppanel.git"
fi

cd "$WORK_DIR"

# ============================================================
# 3. 安装 Go（如未安装）
# ============================================================
log_step "3" "安装 Go"

GO_VERSION="1.21.13"
if command -v go &>/dev/null; then
    log_info "Go 已安装: $(go version)"
else
    log_info "下载 Go $GO_VERSION ($GO_ARCH)..."
    GO_TAR="go${GO_VERSION}.linux-${GO_ARCH}.tar.gz"
    curl -sSL "https://go.dev/dl/${GO_TAR}" -o "/tmp/${GO_TAR}"
    rm -rf /usr/local/go
    tar -C /usr/local -xzf "/tmp/${GO_TAR}"
    rm -f "/tmp/${GO_TAR}"

    cat >> /etc/profile << 'GOENV'
export PATH="/usr/local/go/bin:$PATH"
export GOPATH="/root/go"
GOENV

    export PATH="/usr/local/go/bin:$PATH"
    export GOPATH="/root/go"
    log_info "Go 安装完成: $(go version)"
fi

export PATH="/usr/local/go/bin:$PATH"
export GOPATH="/root/go"

# ============================================================
# 4. 下载前端资源 + 编译 TailwindCSS
# ============================================================
log_step "4" "准备前端资源"

mkdir -p bin static/js static/css

# TailwindCSS Standalone CLI
if [[ ! -f bin/tailwindcss ]]; then
    log_info "下载 TailwindCSS CLI..."
    TAILWIND_VERSION="v3.4.4"
    curl -sSL "https://github.com/tailwindlabs/tailwindcss/releases/download/${TAILWIND_VERSION}/tailwindcss-linux-x64" -o bin/tailwindcss
    chmod +x bin/tailwindcss
    log_info "TailwindCSS CLI 就绪"
fi

# Alpine.js
if [[ ! -f static/js/alpine.min.js ]]; then
    log_info "下载 Alpine.js..."
    curl -sSL "https://cdn.jsdelivr.net/npm/alpinejs@3.13.7/dist/cdn.min.js" -o static/js/alpine.min.js
fi

# Chart.js
if [[ ! -f static/js/chart.min.js ]]; then
    log_info "下载 Chart.js..."
    curl -sSL "https://cdn.jsdelivr.net/npm/chart.js@4.4.1/dist/chart.umd.min.js" -o static/js/chart.min.js
fi

# 编译 TailwindCSS
if [[ -f input.css ]]; then
    log_info "编译 TailwindCSS..."
    bin/tailwindcss -i input.css -o static/css/main.css --minify 2>/dev/null || true
    if [[ -f static/css/main.css ]]; then
        CSS_SIZE=$(wc -c < static/css/main.css)
        log_info "main.css 编译完成 (${CSS_SIZE} bytes)"
    else
        log_warn "TailwindCSS 编译失败，将使用占位样式"
    fi
fi

log_info "前端资源准备完成"

# ============================================================
# 5. 编译 Go 二进制
# ============================================================
log_step "5" "编译 Go 二进制"

rm -f go.sum

log_info "下载 Go 依赖 (go mod tidy)..."
set +e
go mod tidy 2>&1
TIDY_EXIT=$?
set -e
if [[ $TIDY_EXIT -ne 0 ]]; then
    log_warn "go mod tidy 失败，清理缓存重试..."
    go clean -modcache 2>/dev/null || true
    rm -f go.sum
    go mod tidy 2>&1 || log_error "go mod tidy 失败，请检查网络连接"
fi

VERSION=$(git describe --tags --always 2>/dev/null || echo "dev")
BUILD_TIME=$(date -u '+%Y-%m-%dT%H:%M:%SZ')
LDFLAGS="-s -w -X main.Version=${VERSION} -X main.BuildTime=${BUILD_TIME}"

log_info "静态检查 (go vet)..."
go vet ./...

log_info "编译 wp-panel (CGO_ENABLED=0 静态链接)..."
set +e
go build -ldflags="${LDFLAGS}" -o wp-panel . 2>&1
BUILD_EXIT=$?
set -e

if [[ $BUILD_EXIT -ne 0 ]]; then
    log_warn "编译失败 (exit=$BUILD_EXIT)，重试一次..."
    go mod tidy 2>&1 || true
    go build -ldflags="${LDFLAGS}" -o wp-panel . 2>&1 || log_error "编译再次失败"
fi

if [[ ! -f wp-panel ]]; then
    log_error "编译失败，wp-panel 二进制未生成。请将上方错误信息反馈给开发者"
fi

BIN_SIZE=$(du -h wp-panel | cut -f1)
log_info "编译完成: wp-panel (${BIN_SIZE})"

# ============================================================
# 6. 系统环境安装
# ============================================================
log_step "6" "安装系统组件"

INSTALL_DIR="/www/server/panel"
CONFIG_FILE="$INSTALL_DIR/config.json"
DB_PATH="$INSTALL_DIR/panel.db"
BIN_PATH="/usr/local/bin/wp-panel"
SERVICE_PATH="/etc/systemd/system/wp-panel.service"
PANEL_PORT=8888

TOTAL_MEM_KB=$(grep MemTotal /proc/meminfo | awk '{print $2}')
TOTAL_MEM_MB=$((TOTAL_MEM_KB / 1024))
log_info "物理内存: ${TOTAL_MEM_MB}MB"

# Swap（小内存保护）
if [[ $TOTAL_MEM_MB -le 1024 ]]; then
    SWAP_FILE="/swapfile"
    if [[ ! -f "$SWAP_FILE" ]]; then
        log_info "内存 ≤ 1GB，创建 2GB Swap..."
        dd if=/dev/zero of=$SWAP_FILE bs=1M count=2048 status=none
        chmod 600 $SWAP_FILE
        mkswap $SWAP_FILE >/dev/null
        swapon $SWAP_FILE
        echo "$SWAP_FILE none swap sw 0 0" >> /etc/fstab
        log_info "Swap 创建完成"
    fi
fi

# APT 源 + 组件安装
export DEBIAN_FRONTEND=noninteractive

# Ondřej Surý PHP 8.3 源
if [[ ! -f /etc/apt/sources.list.d/php.list ]]; then
    log_info "添加 PHP 8.3 源..."
    curl -sSL https://packages.sury.org/php/apt.gpg | gpg --dearmor -o /etc/apt/trusted.gpg.d/php.gpg 2>/dev/null || true
    echo "deb https://packages.sury.org/php/ $(lsb_release -sc 2>/dev/null || echo 'trixie') main" > /etc/apt/sources.list.d/php.list
    apt-get update -qq
fi

log_info "安装 Nginx / MariaDB / PHP 8.3 / Redis / Fail2ban / nftables..."
apt-get install -y -qq \
    nginx \
    mariadb-server \
    redis-server \
    fail2ban \
    nftables \
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
    php8.3-cli \
    2>/dev/null || {
    log_warn "批量安装有失败，逐个重试..."
    PKGS="nginx mariadb-server redis-server fail2ban nftables cron php8.3-fpm php8.3-mysql php8.3-curl php8.3-gd php8.3-mbstring php8.3-xml php8.3-zip php8.3-intl php8.3-redis php8.3-opcache php8.3-cli"
    for pkg in $PKGS; do
        apt-get install -y -qq "$pkg" 2>/dev/null || log_warn "跳过 $pkg"
    done
}

log_info "系统组件安装完成"

# 确保关键服务启动
log_info "启动核心服务..."
systemctl start nginx 2>/dev/null || true
systemctl enable nginx 2>/dev/null || true
systemctl start php8.3-fpm 2>/dev/null || true
systemctl enable php8.3-fpm 2>/dev/null || true
systemctl start redis-server 2>/dev/null || true
systemctl enable redis-server 2>/dev/null || true
systemctl start fail2ban 2>/dev/null || true
systemctl enable fail2ban 2>/dev/null || true
mkdir -p /run/php

# ============================================================
# 7. MariaDB 安全加固
# ============================================================
log_step "7" "MariaDB 安全加固"

systemctl start mariadb 2>/dev/null || true
systemctl enable mariadb 2>/dev/null || true

MYSQL_PASS=$(head -c 24 /dev/urandom | sha256sum | head -c 32)

# 尝试设置密码
if mysqladmin -u root password "${MYSQL_PASS}" 2>/dev/null; then
    log_info "MariaDB root 密码已设置"
else
    log_warn "MariaDB root 密码可能已存在或 socket 认证，尝试绕过..."
    # 某些 MariaDB 版本使用 unix_socket 认证，需要先切换到 mysql_native_password
    mysql -u root -e "ALTER USER 'root'@'localhost' IDENTIFIED BY '${MYSQL_PASS}'; FLUSH PRIVILEGES;" 2>/dev/null || true
fi

mysql -u root -p"${MYSQL_PASS}" -e "
    DELETE FROM mysql.user WHERE User='';
    DELETE FROM mysql.user WHERE User='root' AND Host!='localhost';
    DROP DATABASE IF EXISTS test;
    DELETE FROM mysql.db WHERE Db='test' OR Db='test\\_%';
    FLUSH PRIVILEGES;
" 2>/dev/null || log_warn "安全加固部分跳过（密码验证方式不同）"

# 低内存优化
if [[ $TOTAL_MEM_MB -le 1024 ]]; then
    log_info "低内存环境，应用 MariaDB 优化..."
    mkdir -p /etc/mysql/mariadb.conf.d
    cat > /etc/mysql/mariadb.conf.d/99-wp-panel.cnf << 'MARIADBEOF'
[mysqld]
innodb_buffer_pool_size = 128M
innodb_log_buffer_size = 8M
table_open_cache = 128
max_connections = 30
performance_schema = OFF
MARIADBEOF
    systemctl restart mariadb 2>/dev/null || true
fi

# ============================================================
# 8. 目录 + WordPress 备用包 + 凭证
# ============================================================
log_step "8" "初始化面板数据"

log_info "创建目录结构..."
mkdir -p "$INSTALL_DIR"/{backups,packages,logs}
mkdir -p /www/wwwroot /www/wwwlogs /www/server/certificates
chmod 700 "$INSTALL_DIR"

log_info "下载 WordPress 备用包..."
WP_ZIP="$INSTALL_DIR/packages/wordpress.zip"
for i in 1 2 3; do
    if wget -q -T 60 -O "$WP_ZIP" "https://wordpress.org/latest.zip" 2>/dev/null; then
        log_info "WordPress 下载完成"
        break
    fi
    log_warn "重试 ($i/3)..."
    sleep 3
done
if [[ ! -f "$WP_ZIP" ]]; then
    log_warn "WordPress 下载失败，首次建站时将自动联网下载"
fi

# 生成凭证
log_info "生成安全凭证..."
PANEL_SUFFIX=$(head -c 20 /dev/urandom | sha256sum | head -c 32)

# BasicAuth 凭据（第一层：浏览器弹窗认证）
BASIC_USER="admin"
BASIC_PASS=$(head -c 12 /dev/urandom | base64 | head -c 16)

# Web 登录凭据（第二层：面板内登录表单）
ADMIN_USER="wpadmin"
ADMIN_PASS=$(head -c 12 /dev/urandom | base64 | head -c 16)

BASIC_HASH=""
ADMIN_HASH=""
if command -v php8.3 &>/dev/null; then
    BASIC_HASH=$(php8.3 -r "echo password_hash('$BASIC_PASS', PASSWORD_BCRYPT, ['cost' => 12]);" 2>/dev/null)
    ADMIN_HASH=$(php8.3 -r "echo password_hash('$ADMIN_PASS', PASSWORD_BCRYPT, ['cost' => 12]);" 2>/dev/null)
fi
if [[ -z "$BASIC_HASH" ]]; then
    log_warn "无法通过 PHP 生成 bcrypt，面板首次启动时将自动重置密码"
    BASIC_HASH='$2a$12$00000000000000000000000000000000000000000000000000000'
    ADMIN_HASH='$2a$12$00000000000000000000000000000000000000000000000000000'
fi

# ============================================================
# 9. 写入配置 + 部署 + 启动
# ============================================================
log_step "9" "部署面板"

cat > "$CONFIG_FILE" << CONFIGEOF
{
  "panel": {
    "version": "1.0.0-mvp",
    "port": $PANEL_PORT,
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
    "username": "$ADMIN_USER",
    "password_hash": "$ADMIN_HASH"
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
    "core_ports": [22, $PANEL_PORT, 80, 443]
  },
  "systemd": {
    "service_name": "wp-panel",
    "service_path": "$SERVICE_PATH",
    "binary_path": "$BIN_PATH"
  }
}
CONFIGEOF

chmod 600 "$CONFIG_FILE"
log_info "配置文件已写入: $CONFIG_FILE"

cp wp-panel "$BIN_PATH"
chmod +x "$BIN_PATH"
log_info "二进制已部署: $BIN_PATH"

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

sleep 2

# ============================================================
# 10. 安装完成
# ============================================================
if systemctl is-active --quiet wp-panel; then
    STATUS="${GREEN}● 运行中${NC}"
else
    STATUS="${RED}● 未运行 — 查看日志: journalctl -u wp-panel -n 30${NC}"
fi

SERVER_IP=$(hostname -I 2>/dev/null | awk '{print $1}')
[[ -z "$SERVER_IP" ]] && SERVER_IP="<服务器IP>"

echo ""
echo -e "${BOLD}╔══════════════════════════════════════════════╗${NC}"
echo -e "${BOLD}║         WP Panel 安装完成                    ║${NC}"
echo -e "${BOLD}╚══════════════════════════════════════════════╝${NC}"
echo ""
echo -e "  面板地址:  ${BOLD}http://${SERVER_IP}:${PANEL_PORT}/${PANEL_SUFFIX}/${NC}"
echo ""
echo -e "  ┌─────────────────────────────────────────┐"
echo -e "  │  第 1 层 — BasicAuth（浏览器弹窗）      │"
echo -e "  ├─────────────────────────────────────────┤"
echo -e "  │  用户名:  ${BOLD}${BASIC_USER}${NC}"
echo -e "  │  密  码:  ${BOLD}${BASIC_PASS}${NC}"
echo -e "  └─────────────────────────────────────────┘"
echo ""
echo -e "  ┌─────────────────────────────────────────┐"
echo -e "  │  第 2 层 — Web 登录（面板内表单）        │"
echo -e "  ├─────────────────────────────────────────┤"
echo -e "  │  用户名:  ${BOLD}${ADMIN_USER}${NC}"
echo -e "  │  密  码:  ${BOLD}${ADMIN_PASS}${NC}"
echo -e "  └─────────────────────────────────────────┘"
echo ""
echo -e "  ${BOLD}登录流程：${NC}"
echo -e "  1. 浏览器打开上方地址 → 弹出 BasicAuth 对话框"
echo -e "     → 输入 ${BOLD}第 1 层${NC} 的用户名和密码"
echo -e "  2. 通过后看到登录页面 → 输入 ${BOLD}第 2 层${NC} 的用户名和密码"
echo -e "  3. 进入控制台"
echo ""
echo -e "  面板状态:  ${STATUS}"
echo ""
echo -e "  ${YELLOW}▲ 直接访问 http://${SERVER_IP}:${PANEL_PORT} 将返回 404（入口隐藏）${NC}"
echo -e "  ${YELLOW}▲ 凭据仅显示一次！也可查看: cat /www/server/panel/config.json${NC}"
echo ""
echo -e "  查看日志:  ${BOLD}journalctl -u wp-panel -f${NC}"
echo -e "  重启面板:  ${BOLD}systemctl restart wp-panel${NC}"
echo ""
