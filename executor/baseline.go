package executor

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

func EnsureWordPressBaseline() {
	ensurePHPBaseline()
	ensureNginxBaseline()
	ensureNginxSSLDefaultServer()
	ensureMariaDBBaseline()
	ensureRedisBaseline()
}

func ensurePHPBaseline() {
	path := "/etc/php/8.3/fpm/conf.d/99-wppanel.ini"
	if _, err := os.Stat(path); err == nil {
		return
	}
	content := `; WP Panel — WordPress 安全基线 (安装时自动生成)
; 可在面板「软件管理」中修改这些值
memory_limit = 256M
upload_max_filesize = 64M
post_max_size = 64M
max_execution_time = 300
max_input_vars = 2000
`
	os.WriteFile(path, []byte(content), 0644)
	exec.Command("systemctl", "reload", "php8.3-fpm").Run()
}

func ensureNginxBaseline() {
	path := "/etc/nginx/conf.d/wppanel.conf"
	if _, err := os.Stat(path); err == nil {
		return
	}
	content := `# WP Panel — WordPress 安全基线 (安装时自动生成)
client_max_body_size 64m;
`
	os.WriteFile(path, []byte(content), 0644)
}

func ensureNginxSSLDefaultServer() {
	confPath := "/etc/nginx/conf.d/wppanel-ssl-default.conf"
	content := `# WP Panel — 默认 SSL 服务器，拒绝未知域名的 TLS 握手，防止证书跨站泄露
server {
    listen 443 ssl default_server;
    listen [::]:443 ssl default_server;
    http2 on;
    ssl_reject_handshake on;
}
`
	os.WriteFile(confPath, []byte(content), 0644)

	if out, err := exec.Command("nginx", "-t").CombinedOutput(); err != nil {
		fmt.Printf("[WP-Panel] Nginx 配置语法错误，跳过重载: %s\n", string(out))
		return
	}
	exec.Command("nginx", "-s", "reload").Run()
}

func ensureMariaDBBaseline() {
	path := "/etc/mysql/mariadb.conf.d/99-wppanel.cnf"
	if _, err := os.Stat(path); err == nil {
		return
	}
	totalMemKB := getTotalMemoryKB()
	var poolSize string
	switch {
	case totalMemKB <= 1048576:
		poolSize = "128M"
	case totalMemKB <= 2097152:
		poolSize = "256M"
	default:
		poolSize = "512M"
	}
	content := fmt.Sprintf(`# WP Panel — WordPress 安全基线 (安装时自动生成)
[mysqld]
innodb_buffer_pool_size = %s
`, poolSize)
	os.WriteFile(path, []byte(content), 0644)
	exec.Command("systemctl", "restart", "mariadb").Run()
}

func ensureRedisBaseline() {
	// Redis doesn't have conf.d, check if already set
	path := "/etc/redis/redis.conf"
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	if strings.Contains(string(data), "maxmemory ") && !strings.Contains(string(data), "# maxmemory ") {
		return
	}

	totalMemKB := getTotalMemoryKB()
	var maxmem string
	switch {
	case totalMemKB <= 1048576:
		maxmem = "64mb"
	case totalMemKB <= 2097152:
		maxmem = "128mb"
	default:
		maxmem = "256mb"
	}

	// Find commented maxmemory line and uncomment it
	content := string(data)
	replaced := false
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "# maxmemory") {
			lines[i] = "maxmemory " + maxmem
			replaced = true
			break
		}
	}
	if !replaced {
		lines = append(lines, "", "# WP Panel — WordPress 安全基线", "maxmemory "+maxmem)
	}

	os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0644)
	exec.Command("systemctl", "restart", "redis-server").Run()
}

func getTotalMemoryKB() int64 {
	out, err := exec.Command("bash", "-c", "grep MemTotal /proc/meminfo | awk '{print $2}'").CombinedOutput()
	if err != nil {
		return 2097152 // default 2GB fallback
	}
	var kb int64
	fmt.Sscanf(strings.TrimSpace(string(out)), "%d", &kb)
	return kb
}
