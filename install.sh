#!/bin/bash
set -e
set -o pipefail

# ============================================================
# WP Panel 安装脚本 — 适用于 Debian 13 (Trixie)，建议使用纯净系统
# 自动为 PHP 8.3 源选择官方源或国内镜像源，兼容海外和国内 VPS
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
GHPROXY="https://gh.wp-panel.org"
PREFER_CN=false
PHP_SOURCE_MODE="${WP_PANEL_PHP_SOURCE:-auto}"
REPAIR_MODE=false
REPAIR_BACKUP_DIR=""
REPAIR_SERVICE_WAS_ACTIVE=false
REPAIR_COMMITTED=false
PANEL_CANDIDATE=""
REPAIR_BIN_EXISTED=false
REPAIR_UNIT_EXISTED=false
REPAIR_TLS_EXISTED=false
REPAIR_MUTATED=false

if [[ "${WP_PANEL_PREFER_CN_MIRROR:-0}" == "1" ]] || [[ "${WP_PANEL_PREFER_CN_MIRROR:-}" == "true" ]]; then
    PREFER_CN=true
fi

log_info()  { echo -e "${GREEN}[INFO]${NC} $1"; }
log_warn()  { echo -e "${YELLOW}[WARN]${NC} $1"; }
log_error() { echo -e "${RED}[ERROR]${NC} $1"; exit 1; }

systemctl_enable_best_effort() {
    local svc="$1"
    if ! systemctl enable "$svc"; then
        log_warn "${svc} 开机自启设置失败，继续安装。安装后可手动检查: systemctl enable ${svc}"
    fi
}

systemctl_start_required() {
    local svc="$1"
    if ! systemctl start "$svc"; then
        journalctl -u "$svc" -n 20 --no-pager 2>/dev/null || true
        log_error "${svc} 启动失败，请根据上方日志排查"
    fi
}

# ============================================================
# 系统内核优化（BBR+FQ、TCP 缓冲、连接队列、文件描述符）
# ============================================================
apply_system_tuning() {
    log_info "应用系统内核优化..."

    SYSCTL_FILE="/etc/sysctl.d/99-wp-panel.conf"
    CPU_CORES=$(nproc)

    cat > "$SYSCTL_FILE" << 'SYSCTLEOF'
# WP Panel — 网络与内核优化

# ── 连接队列 ──
net.core.somaxconn = 65535
net.ipv4.tcp_max_syn_backlog = 8192
net.core.netdev_max_backlog = 16384

# ── TCP 缓冲区 ──
net.core.rmem_default = 262144
net.core.wmem_default = 262144
net.core.rmem_max = 16777216
net.core.wmem_max = 16777216
net.ipv4.tcp_rmem = 4096 87380 16777216
net.ipv4.tcp_wmem = 4096 65536 16777216

# ── TIME-WAIT 优化 ──
net.ipv4.tcp_tw_reuse = 1
net.ipv4.tcp_fin_timeout = 15
net.ipv4.ip_local_port_range = 1024 65535

# ── Keepalive ──
net.ipv4.tcp_keepalive_time = 300
net.ipv4.tcp_keepalive_intvl = 30
net.ipv4.tcp_keepalive_probes = 5

# ── BBR 辅助参数 ──
net.ipv4.tcp_slow_start_after_idle = 0
net.ipv4.tcp_notsent_lowat = 16384

# ── 基础安全 ──
net.ipv4.tcp_syncookies = 1
net.ipv4.tcp_sack = 1
net.ipv4.tcp_timestamps = 1
SYSCTLEOF

    # BBR + FQ: 仅 2 核及以上机器开启（单核 VPS CPU 争抢时 BBR 吞吐量会暴跌）
    if [[ $CPU_CORES -ge 2 ]]; then
        cat >> "$SYSCTL_FILE" << 'BBREOF'

# ── BBR 拥塞控制 + FQ 调度 ──
net.core.default_qdisc = fq
net.ipv4.tcp_congestion_control = bbr
BBREOF
        modprobe tcp_bbr 2>/dev/null || true
        log_info "BBR + FQ 已启用（${CPU_CORES} 核 CPU）"
    else
        log_info "单核 CPU，跳过 BBR（避免 CPU 争抢副作用）"
    fi

    sysctl --system >/dev/null 2>&1

    # 文件描述符限制
    if ! grep -q "nofile 65535" /etc/security/limits.conf 2>/dev/null; then
        cat >> /etc/security/limits.conf << 'LIMITSEOF'
* soft nofile 65535
* hard nofile 65535
LIMITSEOF
    fi

    log_info "系统内核优化完成"
}

while [[ $# -gt 0 ]]; do
    case "$1" in
        --prefer-cn|--cn)
            PREFER_CN=true
            shift
            ;;
        --php-source)
            if [[ $# -lt 2 ]]; then
                log_error "--php-source 需要指定 official、ustc、sjtu 或 auto"
            fi
            PHP_SOURCE_MODE="$2"
            shift 2
            ;;
        --php-source=*)
            PHP_SOURCE_MODE="${1#*=}"
            shift
            ;;
        *)
            log_warn "未知参数已忽略: $1"
            shift
            ;;
    esac
done

# 异常退出时显示友好反馈提示
trap 'e=$?; rm -f "${PANEL_CANDIDATE:-}"; if [[ $e -ne 0 ]]; then repair_rollback; echo -e "${RED}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"; echo -e "${RED}  安装未完成 / Installation incomplete${NC}"; echo -e "${RED}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"; echo -e "  安装失败通常与系统环境不纯净有关。"; echo -e "  Installation failures are commonly caused by a non-clean system environment."; echo -e "  建议使用纯净版 Debian 13 重装系统后再安装，或联系面板开发者协助排查。"; echo -e "  Please retry on a clean Debian 13 system, or contact the developer for help."; echo ""; echo -e "  请将上方错误信息截图发送至 / Send a screenshot of the error above to:"; echo -e "  blog@naibabiji.com"; echo -e "  微信 / WeChat: vv15_zhi"; echo ""; fi' EXIT

# ============================================================
# PHP 8.3 源选择（官方源 + 国内镜像多重兜底）
# ============================================================

set_php_source_meta() {
    case "$1" in
        official)
            PHP_SOURCE_LABEL="Ondřej Surý 官方源"
            PHP_KEY_URL="https://packages.sury.org/debsuryorg-archive-keyring.deb"
            PHP_REPO_URL="https://packages.sury.org/php/"
            ;;
        ustc)
            PHP_SOURCE_LABEL="中科大 PHP Sury 镜像"
            PHP_KEY_URL="https://mirrors.ustc.edu.cn/sury/debsuryorg-archive-keyring.deb"
            PHP_REPO_URL="https://mirrors.ustc.edu.cn/sury/php/"
            ;;
        sjtu)
            PHP_SOURCE_LABEL="上海交大 PHP Sury 镜像"
            PHP_KEY_URL="https://mirror.sjtu.edu.cn/sury/debsuryorg-archive-keyring.deb"
            PHP_REPO_URL="https://mirror.sjtu.edu.cn/sury/php/"
            ;;
        *)
            return 1
            ;;
    esac
}

download_file() {
    local url="$1"
    local output="$2"
    local timeout="${3:-30}"

    rm -f "$output"
    if command -v curl &>/dev/null; then
        curl -fsSL --connect-timeout "$timeout" -o "$output" "$url" 2>/dev/null && [[ -s "$output" ]] && return 0
    fi
    if command -v wget &>/dev/null; then
        wget -q -T "$timeout" -O "$output" "$url" 2>/dev/null && [[ -s "$output" ]] && return 0
    fi
    rm -f "$output"
    return 1
}

prepare_panel_candidate() {
    local script_dir=""
    local github_release="https://github.com/naibabiji/wp-panel/releases/latest/download/wp-panel"
    local proxy_release="${GHPROXY}/${github_release}"

    [[ -n "$PANEL_CANDIDATE" ]] && [[ -x "$PANEL_CANDIDATE" ]] && return 0
    PANEL_CANDIDATE="/tmp/wp-panel.$$.candidate"
    script_dir="$(cd "$(dirname "$0")" && pwd)"

    if [[ -s "$script_dir/wp-panel" ]]; then
        cp "$script_dir/wp-panel" "$PANEL_CANDIDATE"
    elif $PREFER_CN; then
        download_file "$proxy_release" "$PANEL_CANDIDATE" 60 || \
            download_file "$github_release" "$PANEL_CANDIDATE" 60 || \
            log_error "无法获取用于安装校验的面板二进制"
    else
        download_file "$github_release" "$PANEL_CANDIDATE" 60 || \
            download_file "$proxy_release" "$PANEL_CANDIDATE" 60 || \
            log_error "无法获取用于安装校验的面板二进制"
    fi
    chmod 0755 "$PANEL_CANDIDATE"
}

create_repair_backup() {
    local timestamp=""
    timestamp=$(date -u +%Y%m%dT%H%M%SZ)
    REPAIR_BACKUP_DIR="$INSTALL_DIR/backups/install-repair/${timestamp}-$$"
    [[ -s "$BIN_PATH" ]] && REPAIR_BIN_EXISTED=true
    [[ -f "$SERVICE_PATH" ]] && REPAIR_UNIT_EXISTED=true
    if [[ -f "$INSTALL_DIR/certs/panel.crt" ]] && [[ -f "$INSTALL_DIR/certs/panel.key" ]]; then
        REPAIR_TLS_EXISTED=true
    fi
    install -d -m 0700 "$REPAIR_BACKUP_DIR"

    install -m 0600 "$CONFIG_FILE" "$REPAIR_BACKUP_DIR/config.json"
    if [[ -f "$DB_PATH" ]]; then
        sqlite3 "$DB_PATH" ".timeout 10000" ".backup $REPAIR_BACKUP_DIR/panel.db"
        [[ "$(sqlite3 "$REPAIR_BACKUP_DIR/panel.db" 'PRAGMA integrity_check;')" == "ok" ]] || \
            log_error "repair备份SQLite完整性检查失败"
    fi
    if $REPAIR_BIN_EXISTED; then
        install -m 0755 "$BIN_PATH" "$REPAIR_BACKUP_DIR/wp-panel"
    fi
    if $REPAIR_UNIT_EXISTED; then
        install -m 0644 "$SERVICE_PATH" "$REPAIR_BACKUP_DIR/wp-panel.service"
    fi
    if $REPAIR_TLS_EXISTED; then
        install -m 0644 "$INSTALL_DIR/certs/panel.crt" "$REPAIR_BACKUP_DIR/panel.crt"
        install -m 0600 "$INSTALL_DIR/certs/panel.key" "$REPAIR_BACKUP_DIR/panel.key"
    fi
    find "$REPAIR_BACKUP_DIR" -type f ! -name SHA256SUMS -print0 | sort -z | xargs -0 sha256sum > "$REPAIR_BACKUP_DIR/SHA256SUMS"
    sha256sum -c "$REPAIR_BACKUP_DIR/SHA256SUMS" >/dev/null || log_error "repair备份校验失败"
}

repair_rollback() {
    [[ "$REPAIR_MODE" == true ]] || return 0
    [[ "$REPAIR_COMMITTED" == false ]] || return 0
    [[ "$REPAIR_MUTATED" == true ]] || return 0
    [[ -n "$REPAIR_BACKUP_DIR" ]] && [[ -d "$REPAIR_BACKUP_DIR" ]] || return 0

    log_warn "repair未完成，正在恢复本次替换的面板文件"
    if $REPAIR_BIN_EXISTED; then
        install -m 0755 "$REPAIR_BACKUP_DIR/wp-panel" "$BIN_PATH"
    else
        rm -f "$BIN_PATH"
    fi
    if $REPAIR_UNIT_EXISTED; then
        install -m 0644 "$REPAIR_BACKUP_DIR/wp-panel.service" "$SERVICE_PATH"
    else
        rm -f "$SERVICE_PATH"
    fi
    if $REPAIR_TLS_EXISTED; then
        install -m 0644 "$REPAIR_BACKUP_DIR/panel.crt" "$INSTALL_DIR/certs/panel.crt"
        install -m 0600 "$REPAIR_BACKUP_DIR/panel.key" "$INSTALL_DIR/certs/panel.key"
    elif [[ "${REPAIR_TLS_ACTION:-}" == "generate" ]]; then
        rm -f "$INSTALL_DIR/certs/panel.crt" "$INSTALL_DIR/certs/panel.key"
    fi
    systemctl daemon-reload 2>/dev/null || true
    if $REPAIR_SERVICE_WAS_ACTIVE; then
        systemctl start wp-panel 2>/dev/null || true
    else
        systemctl stop wp-panel 2>/dev/null || true
    fi
}

apt_package_available() {
    local pkg="$1"
    local candidate=""

    candidate=$(LC_ALL=C apt-cache policy "$pkg" 2>/dev/null | awk '/Candidate:/ {print $2; exit}' || true)
    if [[ -n "$candidate" ]] && [[ "$candidate" != "(none)" ]]; then
        return 0
    fi

    LC_ALL=C apt-cache show "$pkg" >/dev/null 2>&1
}

php_package_available() {
    local pkg="$1"

    apt_package_available "$pkg"
}

set_debian_source_meta() {
    case "$1" in
        nju)
            DEBIAN_SOURCE_LABEL="南京大学 Debian 镜像"
            DEBIAN_REPO_URL="http://mirror.nju.edu.cn/debian"
            DEBIAN_SECURITY_URL="http://mirror.nju.edu.cn/debian-security"
            ;;
        ustc)
            DEBIAN_SOURCE_LABEL="中科大 Debian 镜像"
            DEBIAN_REPO_URL="http://mirrors.ustc.edu.cn/debian"
            DEBIAN_SECURITY_URL="http://mirrors.ustc.edu.cn/debian-security"
            ;;
        tuna)
            DEBIAN_SOURCE_LABEL="清华大学 Debian 镜像"
            DEBIAN_REPO_URL="http://mirrors.tuna.tsinghua.edu.cn/debian"
            DEBIAN_SECURITY_URL="http://mirrors.tuna.tsinghua.edu.cn/debian-security"
            ;;
        official)
            DEBIAN_SOURCE_LABEL="Debian 官方源"
            DEBIAN_REPO_URL="http://deb.debian.org/debian"
            DEBIAN_SECURITY_URL="http://security.debian.org/debian-security"
            ;;
        *)
            return 1
            ;;
    esac
}

backup_default_debian_sources() {
    local source_file=""

    mkdir -p /etc/apt/sources.list.d

    if [[ -f /etc/apt/sources.list.d/debian.sources ]]; then
        if [[ ! -f /etc/apt/sources.list.d/debian.sources.wp-panel.bak ]]; then
            cp /etc/apt/sources.list.d/debian.sources /etc/apt/sources.list.d/debian.sources.wp-panel.bak
        fi
        mv /etc/apt/sources.list.d/debian.sources /etc/apt/sources.list.d/debian.sources.wp-panel.disabled
    fi

    for source_file in /etc/apt/sources.list /etc/apt/sources.list.d/*.list; do
        [[ -f "$source_file" ]] || continue
        if [[ ! -f "${source_file}.wp-panel.bak" ]]; then
            cp "$source_file" "${source_file}.wp-panel.bak"
        fi
        sed -i -E '/^[[:space:]]*deb(-src)?[[:space:]].*(\/debian-security|\/debian([[:space:]\/]|$)|deb\.debian\.org|security\.debian\.org)/ s/^/# disabled by WP Panel: /' "$source_file"
    done
}

write_debian_sources() {
    local codename="$1"

    cat > /etc/apt/sources.list.d/wp-panel-debian.sources << DEBIANSOURCESEOF
Types: deb
URIs: ${DEBIAN_REPO_URL}
Suites: ${codename} ${codename}-updates
Components: main contrib non-free non-free-firmware
Signed-By: /usr/share/keyrings/debian-archive-keyring.gpg

Types: deb
URIs: ${DEBIAN_SECURITY_URL}
Suites: ${codename}-security
Components: main contrib non-free non-free-firmware
Signed-By: /usr/share/keyrings/debian-archive-keyring.gpg
DEBIANSOURCESEOF
}

debian_packages_available() {
    local packages=(ca-certificates wget curl gnupg lsb-release nginx mariadb-server redis-server)
    local pkg=""

    for pkg in "${packages[@]}"; do
        if ! apt_package_available "$pkg"; then
            log_warn "APT 源缺少关键包候选版本: ${pkg}"
            return 1
        fi
    done
    return 0
}

configure_debian_source() {
    local source_id="$1"
    local codename="$2"
    local apt_log="/tmp/wp-panel-debian-apt-update.log"

    set_debian_source_meta "$source_id" || return 1
    log_info "尝试 Debian 源: ${DEBIAN_SOURCE_LABEL}"
    write_debian_sources "$codename"

    if apt-get update > "$apt_log" 2>&1 && debian_packages_available; then
        rm -f "$apt_log"
        log_info "Debian 源可用: ${DEBIAN_SOURCE_LABEL}"
        return 0
    fi

    log_warn "${DEBIAN_SOURCE_LABEL} 不可用或同步不完整，准备尝试下一个 Debian 源"
    if [[ -f "$apt_log" ]]; then
        tail -n 8 "$apt_log" 2>/dev/null || true
    fi
    rm -f "$apt_log"
    return 1
}

select_debian_source() {
    local codename="$1"
    local candidates=()
    local source_id=""

    if $PREFER_CN; then
        candidates=(nju ustc tuna official)
        backup_default_debian_sources
    else
        log_info "使用系统默认 Debian APT 源"
        apt-get update
        debian_packages_available || log_error "系统默认 APT 源缺少关键包，请检查 /etc/apt/sources.list 或 /etc/apt/sources.list.d/"
        return 0
    fi

    for source_id in "${candidates[@]}"; do
        if configure_debian_source "$source_id" "$codename"; then
            if [[ "$source_id" == "official" ]]; then
                log_warn "国内镜像同步可能延迟，已回退官方源"
            fi
            return 0
        fi
    done

    log_error "所有 Debian APT 源均不可用。请检查网络、DNS、系统时间，或手动配置可用镜像源后重试。"
}

configure_php_source() {
    local source_id="$1"
    local codename="$2"
    local keyring_file="/usr/share/keyrings/debsuryorg-archive-keyring.gpg"
    local tmp_key="/tmp/debsuryorg-archive-keyring.deb"
    local apt_log="/tmp/wp-panel-apt-update.log"

    set_php_source_meta "$source_id" || return 1
    log_info "尝试 PHP 源: ${PHP_SOURCE_LABEL}"

    if download_file "$PHP_KEY_URL" "$tmp_key" 20; then
        if ! dpkg -i "$tmp_key" >/dev/null 2>&1; then
            rm -f "$tmp_key"
            log_warn "${PHP_SOURCE_LABEL} GPG key 安装失败"
            return 1
        fi
        rm -f "$tmp_key"
    else
        if [[ -f "$keyring_file" ]]; then
            log_warn "${PHP_SOURCE_LABEL} GPG key 下载失败，将复用本机已有 keyring"
        else
            log_warn "${PHP_SOURCE_LABEL} GPG key 下载失败"
            return 1
        fi
    fi

    cat > /etc/apt/sources.list.d/php.sources << PHPSOURCESEOF
Types: deb
URIs: ${PHP_REPO_URL}
Suites: ${codename}
Components: main
Signed-By: ${keyring_file}
PHPSOURCESEOF

    if apt-get update > "$apt_log" 2>&1 && \
        php_package_available php8.3-cli && \
        php_package_available php8.3-fpm; then
        rm -f "$apt_log"
        log_info "PHP 源可用: ${PHP_SOURCE_LABEL}"
        return 0
    fi

    log_warn "${PHP_SOURCE_LABEL} 不可用，准备尝试下一个 PHP 源"
    if [[ -f "$apt_log" ]]; then
        tail -n 8 "$apt_log" 2>/dev/null || true
    fi
    rm -f "$apt_log"
    return 1
}

select_php_source() {
    local codename="$1"
    local candidates=()

    case "$PHP_SOURCE_MODE" in
        auto|"")
            if $PREFER_CN; then
                candidates=(ustc sjtu official)
            else
                candidates=(official ustc sjtu)
            fi
            ;;
        official|ustc|sjtu)
            candidates=("$PHP_SOURCE_MODE")
            ;;
        *)
            log_warn "未知 PHP 源模式 ${PHP_SOURCE_MODE}，回退到 auto"
            candidates=(official ustc sjtu)
            ;;
    esac

    for source_id in "${candidates[@]}"; do
        if configure_php_source "$source_id" "$codename"; then
            return 0
        fi
    done

    log_error "所有 PHP 8.3 源均不可用。请检查网络、DNS、证书时间，或稍后重试。"
}

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
    rm -f /usr/local/bin/wpp
    grep -qx '# WP Panel CLI — wp' /usr/local/bin/wp 2>/dev/null && rm -f /usr/local/bin/wp
    rm -rf "$INSTALL_DIR"
    echo -e "  ${GREEN}✓${NC} 面板文件已删除"

    echo -e "  → 清理 Nginx 面板配置..."
    rm -f /etc/nginx/conf.d/wppanel-ratelimit.conf
    rm -f /etc/nginx/conf.d/wppanel-botlimit.conf
    rm -f /etc/nginx/conf.d/wppanel-limit-status.conf
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
        return 0
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

    echo -e "  → 恢复系统内核参数..."
    rm -f /etc/sysctl.d/99-wp-panel.conf
    sysctl --system >/dev/null 2>&1
    sed -i '/nofile 65535/d' /etc/security/limits.conf 2>/dev/null || true
    echo -e "  ${GREEN}✓${NC} 内核参数已恢复"

    echo -e "  → 删除面板文件..."
    rm -f "$BIN_PATH"
    rm -f /usr/local/bin/wpp
    grep -qx '# WP Panel CLI — wp' /usr/local/bin/wp 2>/dev/null && rm -f /usr/local/bin/wp
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
exec 9>/run/lock/wp-panel-install.lock
flock -n 9 || log_error "另一个WP Panel安装或repair进程正在运行"
log_info "权限检查通过"

# ============================================================
# 重复安装/残留安装检测
# ============================================================
INSTALL_COMPLETE=false
INSTALL_TRACES=false

if [[ -f "$CONFIG_FILE" ]] && [[ -s "$BIN_PATH" ]] && [[ -x "$BIN_PATH" ]]; then
    INSTALL_COMPLETE=true
fi

if [[ -e "$CONFIG_FILE" ]] || [[ -e "$BIN_PATH" ]] || [[ -d "$INSTALL_DIR" ]] || [[ -f "$SERVICE_PATH" ]]; then
    INSTALL_TRACES=true
fi

if $INSTALL_COMPLETE; then
    echo ""
    echo -e "${YELLOW}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
    echo -e "${YELLOW}  检测到 WP Panel 已安装${NC}"
    echo -e "${YELLOW}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
    echo ""
    echo -e "  1) 继续/修复安装（${GREEN}保留面板配置、凭据和TLS身份${NC}）"
    echo -e "  2) 卸载后重新安装（${YELLOW}重建面板身份和状态${NC}）"
    echo -e "  3) 仅卸载面板（${GREEN}保留网站/数据库/SSL/软件${NC}）"
    echo -e "  4) 彻底清空（${RED}删除所有数据并卸载软件${NC}）"
    echo -e "  5) 退出"
    echo ""
    echo -e "  输入数字后回车进行选择。"

    read -p "  > " choice < /dev/tty 2>/dev/null || read choice

    case "${choice:-5}" in
        1)
            REPAIR_MODE=true
            log_info "继续/修复安装：将保留现有面板身份和配置"
            ;;
        2)
            do_uninstall
            log_info "开始重新安装..."
            ;;
        3)
            do_uninstall
            exit 0
            ;;
        4)
            do_purge
            exit 0
            ;;
        *)
            echo -e "${GREEN}已取消，面板保持现有状态${NC}"
            exit 0
            ;;
    esac
elif $INSTALL_TRACES; then
    echo ""
    echo -e "${YELLOW}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
    echo -e "${YELLOW}  检测到 WP Panel 上次安装未完成或存在残留${NC}"
    echo -e "${YELLOW}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
    echo ""
    if [[ -f "$CONFIG_FILE" ]]; then
        echo -e "  1) 继续/修复安装（${GREEN}保留面板配置、凭据和TLS身份${NC}）"
        echo -e "  2) 清理面板残留后重新安装（${YELLOW}重建面板身份和状态${NC}）"
        echo -e "  3) 仅卸载面板残留（${GREEN}保留网站/数据库/SSL/软件${NC}）"
        echo -e "  4) 彻底清空（${RED}删除所有数据并卸载软件${NC}）"
        echo -e "  5) 退出"
        echo ""
        echo -e "  直接回车将继续/修复安装。"
    else
        echo -e "${RED}  检测到面板状态但缺少config.json，无法安全repair。${NC}"
        echo -e "  1) 清理面板残留后重新安装（${YELLOW}重建面板身份和状态${NC}）"
        echo -e "  2) 仅卸载面板残留（${GREEN}保留网站/数据库/SSL/软件${NC}）"
        echo -e "  3) 彻底清空（${RED}删除所有数据并卸载软件${NC}）"
        echo -e "  4) 退出"
    fi

    read -p "  > " choice < /dev/tty 2>/dev/null || read choice

    if [[ -f "$CONFIG_FILE" ]]; then
        case "${choice:-1}" in
            1) REPAIR_MODE=true; log_info "继续/修复安装：将保留现有面板身份和配置" ;;
            2) do_uninstall; log_info "开始重新安装..." ;;
            3) do_uninstall; exit 0 ;;
            4) do_purge; exit 0 ;;
            *) echo -e "${GREEN}已取消，系统保持现有状态${NC}"; exit 0 ;;
        esac
    else
        case "${choice:-4}" in
            1) do_uninstall; log_info "开始重新安装..." ;;
            2) do_uninstall; exit 0 ;;
            3) do_purge; exit 0 ;;
            *) echo -e "${GREEN}已取消，系统保持现有状态${NC}"; exit 0 ;;
        esac
    fi
fi

if $REPAIR_MODE; then
    for required_cmd in openssl systemctl sha256sum; do
        command -v "$required_cmd" >/dev/null 2>&1 || log_error "repair缺少必要命令: ${required_cmd}"
    done
    prepare_panel_candidate
    repair_check=$($PANEL_CANDIDATE --repair-config-check --config "$CONFIG_FILE") || \
        log_error "现有config.json未通过repair安全校验，未修改服务器状态"
    case "$repair_check" in
        *'"tls_action":"preserve"'*) REPAIR_TLS_ACTION="preserve" ;;
        *'"tls_action":"generate"'*) REPAIR_TLS_ACTION="generate" ;;
        *) log_error "repair配置检查返回未知TLS状态" ;;
    esac
    case "$repair_check" in
        *'"tls_certificate_expired"'*) log_warn "现有面板TLS证书已过期；repair将保留证书身份，请另行更新" ;;
        *'"tls_certificate_expires_soon"'*) log_warn "现有面板TLS证书将在30天内到期；repair将保留证书身份" ;;
    esac
    if ! command -v sqlite3 >/dev/null 2>&1; then
        log_warn "repair需要sqlite3创建面板数据库在线备份，正在自动安装"
        command -v apt-get >/dev/null 2>&1 || log_error "repair缺少apt-get，无法自动安装sqlite3"
        DEBIAN_FRONTEND=noninteractive apt-get install -y sqlite3 || \
            log_error "repair自动安装sqlite3失败，请检查APT后重试"
        command -v sqlite3 >/dev/null 2>&1 || log_error "repair安装sqlite3后仍无法找到该命令"
    fi
    if systemctl is-active --quiet wp-panel 2>/dev/null; then
        REPAIR_SERVICE_WAS_ACTIVE=true
    fi
    create_repair_backup
    log_info "repair预检与备份完成"
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

if ! $REPAIR_MODE && [[ $TOTAL_MEM_MB -le 8192 ]]; then
    SWAP_FILE="/swapfile"
    SWAP_SIZE_BYTES=$((2 * 1024 * 1024 * 1024))
    ROOT_STATS=$(df -P -B1 / | awk 'NR == 2 {print $2, $3, $4}')
    read -r ROOT_TOTAL ROOT_USED ROOT_AVAILABLE <<< "$ROOT_STATS"

    if awk 'NR > 1 && NF {found=1} END {exit !found}' /proc/swaps; then
        log_info "系统已有启用的 Swap，跳过自动创建"
    elif [[ -e "$SWAP_FILE" ]]; then
        log_warn "${SWAP_FILE} 已存在但未启用，跳过自动创建"
    elif [[ -z "$ROOT_TOTAL" || -z "$ROOT_USED" || -z "$ROOT_AVAILABLE" ]]; then
        log_warn "无法读取根分区空间，跳过自动创建 Swap"
    elif [[ $ROOT_TOTAL -le 0 ]]; then
        log_warn "根分区总容量异常，跳过自动创建 Swap"
    elif [[ $ROOT_AVAILABLE -lt $((8 * 1024 * 1024 * 1024)) ]]; then
        log_warn "根分区可用空间不足 8GB，跳过自动创建 Swap"
    elif [[ $(((ROOT_USED + SWAP_SIZE_BYTES) * 100 / ROOT_TOTAL)) -gt 85 ]]; then
        log_warn "创建 Swap 后根分区使用率将超过 85%，跳过自动创建"
    else
        log_info "创建 2GB Swap 安全缓冲..."
        if dd if=/dev/zero of="$SWAP_FILE" bs=1M count=2048 status=progress &&
           chmod 600 "$SWAP_FILE" &&
           mkswap "$SWAP_FILE" &&
           swapon "$SWAP_FILE"; then
            if {
                echo ""
                echo "# WP Panel managed swap"
                echo "$SWAP_FILE none swap sw 0 0"
            } >> /etc/fstab; then
                if ! cat > /etc/sysctl.d/99-wp-panel-swap.conf << 'SWAPSYSCTLEOF'
# WP Panel managed swap
vm.swappiness = 10
SWAPSYSCTLEOF
                then
                    log_warn "Swap 已启用，但写入 vm.swappiness 配置失败"
                else
                    sysctl -p /etc/sysctl.d/99-wp-panel-swap.conf >/dev/null 2>&1 || \
                        log_warn "Swap 已启用，但应用 vm.swappiness=10 失败"
                fi
                log_info "2GB Swap 创建完成"
            else
                swapoff "$SWAP_FILE" 2>/dev/null || true
                rm -f "$SWAP_FILE"
                log_warn "写入 /etc/fstab 失败，已回滚 Swap 并继续安装"
            fi
        else
            swapoff "$SWAP_FILE" 2>/dev/null || true
            rm -f "$SWAP_FILE"
            log_warn "Swap 创建失败，已清理临时文件并继续安装"
        fi
    fi
fi

# ============================================================
# APT 源配置
# ============================================================
log_info "配置 APT 源..."
export DEBIAN_FRONTEND=noninteractive
DEBIAN_CODENAME=""
if command -v lsb_release &>/dev/null; then
    DEBIAN_CODENAME=$(lsb_release -sc 2>/dev/null || true)
fi
if [[ -z "$DEBIAN_CODENAME" ]] && [[ -f /etc/os-release ]]; then
    DEBIAN_CODENAME=$(grep '^VERSION_CODENAME=' /etc/os-release 2>/dev/null | cut -d= -f2 || true)
fi
if [[ -z "$DEBIAN_CODENAME" ]]; then
    log_error "无法识别 Debian 版本代号"
fi
log_info "Debian 版本: ${DEBIAN_CODENAME}"

# 国内模式会优先选择 Debian 镜像，并同时覆盖 debian-security / debian-updates。
if ! $REPAIR_MODE; then
select_debian_source "$DEBIAN_CODENAME"

# 安装基础依赖
apt-get install -y curl wget unzip ca-certificates gnupg lsb-release

# PHP 8.3 源单独做多重兜底，国内模式优先中科大 / 上交镜像。
select_php_source "$DEBIAN_CODENAME"

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
    sshpass \
    rsyslog \
    cron \
    php8.3-fpm \
    php8.3-mysql \
    php8.3-curl \
    php8.3-gd \
    php8.3-exif \
    jpegoptim \
    optipng \
    php8.3-mbstring \
    php8.3-xml \
    php8.3-zip \
    php8.3-intl \
    php8.3-redis \
    php8.3-opcache \
    php8.3-cli

log_info "基础组件安装完成"
else
    log_info "repair模式保留APT源和现有软件包，不执行安装或升级"
fi

# ============================================================
# systemd 进程守护配置
# ============================================================
log_info "配置 systemd 进程守护..."

if ! $REPAIR_MODE; then
for svc in nginx php8.3-fpm mariadb redis-server; do
    DROPDIR="/etc/systemd/system/${svc}.service.d"
    mkdir -p "$DROPDIR"
    cat > "$DROPDIR/wp-panel.conf" << SYSTEMDEOF
[Unit]
StartLimitIntervalSec=0

[Service]
Restart=always
RestartSec=5s
SYSTEMDEOF
done

systemctl daemon-reload
log_info "systemd 进程守护配置完成"

systemctl_start_required php8.3-fpm
systemctl_start_required nginx
else
    log_info "repair模式保留Nginx、PHP-FPM、MariaDB和Redis的systemd配置与状态"
fi

# ============================================================
# Nginx 基础配置
# ============================================================
log_info "配置 Nginx 基础..."

if ! $REPAIR_MODE; then
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

cat > /etc/nginx/conf.d/wppanel-limit-status.conf << 'LIMITSTATUSEOF'
# WP Panel Generated - shared limit_req status
limit_req_status 429;
LIMITSTATUSEOF

# FastCGI 缓存
mkdir -p /var/cache/nginx/fastcgi
cat > /etc/nginx/conf.d/wppanel-cache.conf << 'CACHEEOF'
fastcgi_cache_path /var/cache/nginx/fastcgi levels=1:2 keys_zone=WP_CACHE:200m inactive=60m max_size=2g;
CACHEEOF

nginx -t && nginx -s reload 2>/dev/null || true
log_info "Nginx 基础配置完成"
else
    log_info "repair模式不改写或reload Nginx配置"
fi

# ============================================================
# 防火墙放行 8443 面板端口
# ============================================================
log_info "放行面板端口 8443..."

if ! $REPAIR_MODE; then
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
else
    log_info "repair模式保留现有防火墙规则"
fi

# ============================================================
# MariaDB 安全加固
# ============================================================
log_info "配置 MariaDB..."

if ! $REPAIR_MODE; then
    systemctl_start_required mariadb
    systemctl_enable_best_effort mariadb
fi

if $REPAIR_MODE; then
    log_info "repair模式保留现有MariaDB身份与配置"
else
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
        systemctl restart mariadb || systemctl_start_required mariadb
    fi
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
log_info "检查面板 TLS 证书..."

CERT_DIR="$INSTALL_DIR/certs"
CERT_FILE="$CERT_DIR/panel.crt"
KEY_FILE="$CERT_DIR/panel.key"

if $REPAIR_MODE && [[ "$REPAIR_TLS_ACTION" == "preserve" ]]; then
    log_info "repair模式保留现有TLS证书与私钥"
else
    TLS_TMP_DIR=$(mktemp -d "$CERT_DIR/.repair-tls.XXXXXX")
    openssl req -x509 -nodes -days 3650 -newkey rsa:2048 \
        -keyout "$TLS_TMP_DIR/panel.key" \
        -out "$TLS_TMP_DIR/panel.crt" \
        -subj "/C=CN/ST=Shanghai/L=Shanghai/O=WP Panel/OU=IT/CN=WP-Panel-SelfSigned" \
        -addext "subjectAltName=IP:127.0.0.1" \
        2>/dev/null
    openssl x509 -in "$TLS_TMP_DIR/panel.crt" -noout >/dev/null
    openssl pkey -in "$TLS_TMP_DIR/panel.key" -noout >/dev/null
    chmod 600 "$TLS_TMP_DIR/panel.key"
    chmod 644 "$TLS_TMP_DIR/panel.crt"
    if $REPAIR_MODE; then REPAIR_MUTATED=true; fi
    mv "$TLS_TMP_DIR/panel.key" "$KEY_FILE"
    mv "$TLS_TMP_DIR/panel.crt" "$CERT_FILE"
    rmdir "$TLS_TMP_DIR"
    log_info "自签名证书已生成（有效期 10 年）"
fi

# ============================================================
# 下载 WordPress 备用包
# ============================================================
log_info "检查 WordPress 备用包..."
WP_ZIP="$INSTALL_DIR/packages/wordpress.zip"
WP_ZIP_TMP="${WP_ZIP}.download"
if $REPAIR_MODE && [[ -s "$WP_ZIP" ]]; then
    log_info "repair模式保留现有WordPress备用包"
else
    for i in 1 2 3; do
        if download_file "https://wordpress.org/latest.zip" "$WP_ZIP_TMP" 60; then
            mv "$WP_ZIP_TMP" "$WP_ZIP"
            log_info "WordPress 下载完成"
            break
        fi
        log_warn "下载失败，重试 ($i/3)..."
        sleep 3
    done
    rm -f "$WP_ZIP_TMP"
    if [[ ! -s "$WP_ZIP" ]]; then
        rm -f "$WP_ZIP"
        log_warn "WordPress 下载失败，将在首次建站时使用联网下载"
    fi
fi

# ============================================================
# 生成面板安全凭证
# ============================================================
log_info "检查安全凭证..."

if ! $REPAIR_MODE; then
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
else
    log_info "repair模式保留现有登录凭据和安全入口"
fi

# ============================================================
# 写入 config.json
# ============================================================
log_info "检查配置文件..."

if ! $REPAIR_MODE; then
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
else
    log_info "repair模式保持config.json字节不变"
fi

# ============================================================
# 部署 Go 二进制
# ============================================================
log_info "部署面板二进制..."

prepare_panel_candidate
BIN_TMP="${BIN_PATH}.repair.$$"
install -m 0755 "$PANEL_CANDIDATE" "$BIN_TMP"
if $REPAIR_MODE; then REPAIR_MUTATED=true; fi
mv "$BIN_TMP" "$BIN_PATH"
log_info "面板二进制已原子部署"

# ============================================================
# 创建 systemd 服务
# ============================================================
log_info "检查 systemd 服务..."

if ! $REPAIR_MODE || [[ ! -f "$SERVICE_PATH" ]]; then
if $REPAIR_MODE; then REPAIR_MUTATED=true; fi
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
else
    log_info "repair模式保留现有systemd unit"
fi

systemctl daemon-reload
if $REPAIR_MODE; then
    if $REPAIR_SERVICE_WAS_ACTIVE; then
        systemctl restart wp-panel || log_error "wp-panel重启失败"
    else
        log_info "repair前wp-panel未运行，保持inactive状态"
    fi
else
    systemctl_enable_best_effort wp-panel
    systemctl_start_required wp-panel
    apply_system_tuning
fi

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

if $REPAIR_MODE; then
    if $REPAIR_SERVICE_WAS_ACTIVE; then
        systemctl is-active --quiet wp-panel || log_error "repair后面板服务未运行"
    elif systemctl is-active --quiet wp-panel; then
        log_error "repair改变了面板服务原始inactive状态"
    fi
    "$BIN_PATH" --repair-config-check --config "$CONFIG_FILE" >/dev/null || \
        log_error "repair后配置复核失败"
    REPAIR_COMMITTED=true
fi

LOCAL_IP=$(hostname -I 2>/dev/null | awk '{print $1}')
[[ -z "$LOCAL_IP" ]] && LOCAL_IP="<未知>"

PUBLIC_IP=$(curl -s --connect-timeout 5 ip.sb 2>/dev/null || curl -s --connect-timeout 5 ifconfig.me 2>/dev/null)
[[ -z "$PUBLIC_IP" ]] && PUBLIC_IP="<未知>"

echo ""
echo -e "${BOLD}============================================${NC}"
echo -e "${BOLD}  WP Panel 安装完成 / Installation Complete${NC}"
echo -e "${BOLD}============================================${NC}"
echo ""
echo -e "${BOLD}官方来源 / Official Sources:${NC}"
echo -e "  官网 / Website: ${BOLD}https://wp-panel.org${NC}"
echo -e "  GitHub:     ${BOLD}https://github.com/naibabiji/wp-panel${NC}"
echo -e "  其他域名均非本项目官网，与本项目无关。"
echo -e "  Other domains are not official WP Panel websites."
echo ""
echo -e "公网 IP / Public IP:  ${BOLD}${PUBLIC_IP}${NC}"
echo -e "内网 IP / Private IP: ${BOLD}${LOCAL_IP}${NC}"
echo ""
if $REPAIR_MODE; then
    echo -e "面板地址和两层登录凭据已保持不变，本次repair不重新显示秘密。"
    echo -e "Panel URL and both login credentials are unchanged. Repair mode does not print secrets again."
elif [[ "$PUBLIC_IP" != "<未知>" ]]; then
    echo -e "面板地址 / Panel URL: ${BOLD}https://${PUBLIC_IP}:8443/${PANEL_SUFFIX}/${NC}"
    if [[ "$LOCAL_IP" != "<未知>" && "$LOCAL_IP" != "$PUBLIC_IP" ]]; then
        echo -e "内网地址 / LAN URL:   ${BOLD}https://${LOCAL_IP}:8443/${PANEL_SUFFIX}/${NC}"
    fi
else
    echo -e "面板地址 / Panel URL: ${BOLD}https://${LOCAL_IP}:8443/${PANEL_SUFFIX}/${NC}"
fi
echo -e "面板状态 / Panel Status: ${STATUS}"
if $PORT_OK; then
    echo -e "端口监听 / Port:         ${GREEN}8443 is listening${NC}"
else
    echo -e "端口监听 / Port:         ${YELLOW}8443 is not listening. Check logs: journalctl -u wp-panel -n 20${NC}"
fi
if ! $REPAIR_MODE; then
    echo ""
    echo -e "  ┌─────────────────────────────────────────┐"
    echo -e "  │  第 1 层 / Layer 1 — BasicAuth          │"
    echo -e "  ├─────────────────────────────────────────┤"
    echo -e "  │  用户名 / Username: ${BOLD}${BASIC_USER}${NC}"
    echo -e "  │  密码 / Password:   ${BOLD}${BASIC_PASS}${NC}"
    echo -e "  └─────────────────────────────────────────┘"
    echo ""
    echo -e "  ┌─────────────────────────────────────────┐"
    echo -e "  │  第 2 层 / Layer 2 — Web Login          │"
    echo -e "  ├─────────────────────────────────────────┤"
    echo -e "  │  用户名 / Username: ${BOLD}${WEB_USER}${NC}"
    echo -e "  │  密码 / Password:   ${BOLD}${WEB_PASS}${NC}"
    echo -e "  └─────────────────────────────────────────┘"
    echo ""
    echo -e "  ${BOLD}登录流程 / Login Steps:${NC}"
    echo -e "  1. 打开上方地址，输入第 1 层 BasicAuth 凭据"
    echo -e "     Open the URL above and enter the Layer 1 BasicAuth credentials"
    echo -e "  2. 进入登录页后，输入第 2 层 Web 登录凭据"
    echo -e "     On the login page, enter the Layer 2 Web Login credentials"
    echo -e "  3. 进入控制台 / Enter the dashboard"
fi
echo ""
echo -e "${YELLOW}⚠ 当前使用自签名证书，浏览器会提示「不安全」${NC}"
echo -e "${YELLOW}  A self-signed certificate is used, so your browser may show a warning.${NC}"
echo -e "${YELLOW}  请点击「高级」→「继续访问」即可进入面板${NC}"
echo -e "${YELLOW}  Click Advanced, then continue to the site to open the panel.${NC}"
echo -e "${YELLOW}  面板使用 8443 端口（HTTPS），与 Nginx 网站 443 端口不冲突${NC}"
echo -e "${YELLOW}  The panel uses HTTPS port 8443 and does not conflict with Nginx port 443.${NC}"
echo ""
echo -e "${BOLD}无法访问？ / Cannot Access?${NC}"
echo -e "  1. 云服务器请检查安全组/防火墙是否放行 8443 端口"
echo -e "     For cloud servers, allow port 8443 in the security group/firewall"
echo -e "  2. 检查本地防火墙 / Check local firewall: ${BOLD}nft list ruleset${NC}"
echo -e "  3. 查看面板日志 / View panel logs: ${BOLD}journalctl -u wp-panel -f${NC}"
echo ""
echo -e "${BOLD}软件安装路径 / Installed Paths:${NC}"
echo -e "  Nginx:      /etc/nginx/"
echo -e "  PHP-FPM:    /etc/php/8.3/fpm/"
echo -e "  MariaDB:    /etc/mysql/"
echo -e "  Redis:      /etc/redis/"
echo -e "  面板程序 / Panel binary: /usr/local/bin/wp-panel"
echo -e "  面板数据 / Panel data:   /www/server/panel/"
echo -e "  SSL 证书 / SSL certs:    ${CERT_DIR}/"
echo ""
echo -e "${BOLD}面板 CLI (wpp):${NC}"
echo -e "  wpp              查看面板信息 / Show panel info"
echo -e "  wpp restart      重启面板 / Restart panel"
echo -e "  wpp password     一键重置管理员密码 / Reset admin password"
echo -e "  wpp unban        一键清空所有IP封禁 / Clear all IP bans"
echo -e "  wpp status       查看运行状态 / Show runtime status"
echo ""
if ! $REPAIR_MODE; then
    echo -e "${YELLOW}请立即保存以上凭据，此信息仅显示一次${NC}"
    echo -e "${YELLOW}Save these credentials now. They are shown only once.${NC}"
fi
echo ""
echo -e "${BOLD}匿名安装统计 / Anonymous Install Telemetry${NC}"
echo -e "  面板会每天上报一次匿名安装统计，内容仅包含："
echo -e "  The panel reports anonymous install telemetry once per day. It includes only:"
echo -e "  机器匿名标识（/etc/machine-id 的 SHA256 哈希）"
echo -e "  Anonymous machine ID (SHA256 hash of /etc/machine-id)"
echo -e "  面板版本号 / Panel version"
echo -e "  不会上报 IP、域名、网站信息等任何敏感数据。"
echo -e "  It does not report IPs, domains, website data, or other sensitive information."
echo -e "  如需关闭，请在面板安全设置中关闭。"
echo -e "  You can disable it in the panel security settings."
echo ""
