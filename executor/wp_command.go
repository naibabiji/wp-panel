package executor

import "os"

const wpScript = `#!/bin/bash
# WP Panel CLI — wp

BIN=/usr/local/bin/wp-panel

case "${1:-}" in
    restart)
        systemctl restart wp-panel
        echo "WP Panel 已重启"
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
    *)
        $BIN --info 2>/dev/null
        echo ""
        CFG=/www/server/panel/config.json
        if [ -f "$CFG" ]; then
            PORT=$(python3 -c "import json; d=json.load(open('$CFG')); print(d['panel']['port'])" 2>/dev/null)
            SUFFIX=$(python3 -c "import json; d=json.load(open('$CFG')); print(d['panel']['random_suffix'])" 2>/dev/null)
            IP=$(hostname -I 2>/dev/null | awk '{print $1}')
            TLS_PORT=$(python3 -c "import json; d=json.load(open('$CFG')); print(d['panel'].get('tls_port', d['panel']['port']))" 2>/dev/null)
            [ -n "$TLS_PORT" ] && [ -n "$SUFFIX" ] && [ -n "$IP" ] && echo "面板地址: https://$IP:$TLS_PORT/$SUFFIX"
        fi
        if systemctl is-active --quiet wp-panel; then
            echo "运行状态: 运行中"
        else
            echo "运行状态: 未运行"
        fi
        echo ""
        echo "用法: wp <命令>"
        echo "  wp restart     重启面板"
        echo "  wp password    一键重置管理员账号密码"
        echo "  wp unban       一键清空所有IP封禁"
        ;;
esac
`

func EnsureWPCommand() {
	path := "/usr/local/bin/wp"
	os.WriteFile(path, []byte(wpScript), 0755)
}
