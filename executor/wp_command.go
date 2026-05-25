package executor

import "os"

const wpScript = `#!/bin/bash
# WP Panel CLI — wp

BIN=/usr/local/bin/wp-panel
CFG=/www/server/panel/config.json
SVC=wp-panel

red()  { echo -e "\033[31m$*\033[0m"; }
green(){ echo -e "\033[32m$*\033[0m"; }
dim()  { echo -e "\033[2m$*\033[0m"; }

diag() {
    local issues=0

    # 1. 二进制
    if [ -x "$BIN" ]; then
        green "✓ 二进制: $BIN"
    elif [ -f "$BIN" ]; then
        red "✗ 二进制无执行权限: $BIN"
        echo "   → 修复: chmod +x $BIN"
        issues=$((issues+1))
    else
        red "✗ 二进制不存在: $BIN"
        echo "   → 面板可能未安装或安装不完整，请重新运行 install.sh"
        issues=$((issues+1))
        return $issues
    fi

    # 2. 配置文件
    if [ -f "$CFG" ]; then
        if python3 -c "import json; json.load(open('$CFG'))" 2>/dev/null; then
            green "✓ 配置文件: $CFG"
        else
            red "✗ 配置文件 JSON 格式错误: $CFG"
            echo "   → 修复: 检查文件内容或从备份恢复"
            issues=$((issues+1))
        fi
    else
        red "✗ 配置文件不存在: $CFG"
        echo "   → 面板可能未安装，请重新运行 install.sh"
        issues=$((issues+1))
        return $issues
    fi

    # 3. 数据库
    DB=$(python3 -c "import json; d=json.load(open('$CFG')); print(d.get('sqlite',{}).get('path',''))" 2>/dev/null)
    if [ -n "$DB" ] && [ -f "$DB" ]; then
        green "✓ 数据库: $DB"
    elif [ -n "$DB" ]; then
        red "✗ 数据库文件不存在: $DB"
        echo "   → 数据库文件丢失，检查磁盘空间或从备份恢复"
        issues=$((issues+1))
    else
        dim "? 未能读取数据库路径"
    fi

    # 4. systemd 服务文件
    if [ -f "/etc/systemd/system/${SVC}.service" ]; then
        green "✓ systemd 服务文件: /etc/systemd/system/${SVC}.service"
    else
        red "✗ systemd 服务文件缺失"
        echo "   → 修复: 重新运行 install.sh"
        issues=$((issues+1))
        return $issues
    fi

    # 5. 端口
    PORT=$(python3 -c "import json; d=json.load(open('$CFG')); print(d['panel'].get('tls_port', d['panel']['port']))" 2>/dev/null)
    if [ -n "$PORT" ]; then
        if ss -tlnp 2>/dev/null | grep -q ":${PORT} "; then
            green "✓ 端口 ${PORT} 已监听"
        else
            dim "? 端口 ${PORT} 未监听（面板未在运行）"
        fi
    fi

    # 6. systemd 状态
    if systemctl is-active --quiet "$SVC"; then
        green "✓ 服务状态: 运行中"
    else
        red "✗ 服务状态: 未运行"
        issues=$((issues+1))
        echo ""
        echo "── 最近的错误日志 ──"
        journalctl -u "$SVC" -n 20 --no-pager --lines=6 2>/dev/null | tail -20
        echo "── 日志结束 ──"
        echo ""
        echo "→ 查看完整日志: journalctl -u $SVC -n 50 --no-pager"
    fi

    if [ $issues -eq 0 ]; then
        echo ""
        green "所有检查通过"
    else
        echo ""
        red "发现 ${issues} 个问题"
    fi
}

case "${1:-}" in
    restart)
        echo "正在重启面板..."
        if systemctl restart "$SVC" 2>/dev/null; then
            sleep 2
            if systemctl is-active --quiet "$SVC"; then
                green "WP Panel 已重启，运行中"
            else
                red "WP Panel 重启后未能启动"
                echo ""
                echo "── 最近日志 ──"
                journalctl -u "$SVC" -n 20 --no-pager 2>/dev/null | tail -20
                echo "── 结束 ──"
                echo ""
                echo "→ 运行 'wp status' 进行完整诊断"
            fi
        else
            red "systemctl restart 失败，服务可能未安装"
            echo "→ 运行 'wp status' 进行诊断"
        fi
        ;;
    password)
        $BIN --reset-admin
        ;;
    info)
        $BIN --info
        ;;
    unban)
        $BIN --unban-all
        ;;
    status|check)
        echo "WP Panel 诊断检查"
        echo "=================="
        echo ""
        diag
        ;;
    log)
        journalctl -u "$SVC" -n "${2:-30}" --no-pager 2>/dev/null
        ;;
    *)
        $BIN --info 2>/dev/null
        echo ""
        if [ -f "$CFG" ]; then
            PORT=$(python3 -c "import json; d=json.load(open('$CFG')); print(d['panel']['port'])" 2>/dev/null)
            SUFFIX=$(python3 -c "import json; d=json.load(open('$CFG')); print(d['panel']['random_suffix'])" 2>/dev/null)
            IP=$(hostname -I 2>/dev/null | awk '{print $1}')
            TLS_PORT=$(python3 -c "import json; d=json.load(open('$CFG')); print(d['panel'].get('tls_port', d['panel']['port']))" 2>/dev/null)
            [ -n "$TLS_PORT" ] && [ -n "$SUFFIX" ] && [ -n "$IP" ] && echo "面板地址: https://$IP:$TLS_PORT/$SUFFIX"
        fi
        if systemctl is-active --quiet "$SVC"; then
            green "运行状态: 运行中"
        else
            red "运行状态: 未运行"
            echo ""
            echo "── 自动诊断 ──"
            diag
        fi
        echo ""
        echo "用法: wp <命令>"
        echo "  wp restart     重启面板"
        echo "  wp status      完整诊断检查"
        echo "  wp log [N]     查看最近 N 条日志（默认30）"
        echo "  wp password    一键重置管理员账号密码"
        echo "  wp unban       一键清空所有IP封禁"
        ;;
esac
`

func EnsureWPCommand() {
	path := "/usr/local/bin/wp"
	os.WriteFile(path, []byte(wpScript), 0755)
}
