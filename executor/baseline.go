package executor

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
)

func EnsureWordPressBaseline() {
	ensurePHPBaseline()
	ensureNginxBaseline()
	ensureNginxCacheBypassMap()
	ensureNginxSSLDefaultServer()
	ensureMariaDBBaseline()
	ensureRedisBaseline()
}

func ensurePHPBaseline() {
	changed, err := EnsurePHPRuntimeConfigFile()
	if err == nil && changed {
		exec.Command("systemctl", "reload", "php8.3-fpm").Run()
	}
}

func ensureNginxBaseline() {
	path := "/etc/nginx/conf.d/wppanel.conf"
	data, err := os.ReadFile(path)

	if err != nil {
		// 文件不存在，创建完整配置
		content := `# WP Panel — WordPress 安全基线 (安装时自动生成)
client_max_body_size 64m;
server_names_hash_bucket_size 128;
`
		os.WriteFile(path, []byte(content), 0644)
		exec.Command("nginx", "-s", "reload").Run()
		return
	}

	// 文件已存在，检查是否缺少 server_names_hash_bucket_size
	content := string(data)
	if !strings.Contains(content, "server_names_hash_bucket_size") {
		content = strings.TrimRight(content, "\n") + "\nserver_names_hash_bucket_size 128;\n"
		os.WriteFile(path, []byte(content), 0644)
		exec.Command("nginx", "-s", "reload").Run()
	}
}

// ensureNginxCacheBypassMap 定义 $wp_cache_skip_args，供每个站点的 FastCGI 缓存绕过
// 判断复用（站点模板里引用了这个全局变量，不能在每个站点自己的配置文件里各定义一份——
// map 指令只能在 http{} 顶层出现一次，两个站点各定义一份同名变量会导致 nginx -t 报重复定义）。
//
// 语义：只要查询字符串完全由已知的营销/统计追踪参数组成（utm_*/fbclid/gclid 等），就不算
// "真实查询参数"，仍然允许命中缓存；只要出现任何一个不在名单里的参数，$wp_cache_skip_args
// 就会保留原始 $args（非空），触发跳过缓存——这样从广告点击进来的流量不会把缓存打穿。
func ensureNginxCacheBypassMap() {
	path := "/etc/nginx/conf.d/wppanel-cache-bypass.conf"
	if _, err := os.Stat(path); err == nil {
		return
	}
	content := `# WP Panel — FastCGI 缓存例外：营销追踪参数不算"真实查询参数"
map $args $wp_cache_skip_args {
    default $args;
    "~^(?:(?:utm_source|utm_medium|utm_campaign|utm_term|utm_content|fbclid|gclid|msclkid|mc_cid|mc_eid)=[^&]*&?)+$" "";
}
`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		log.Printf("[WP-Panel] 写入缓存例外配置失败 %s: %v", path, err)
		return
	}
	if out, err := exec.Command("nginx", "-t").CombinedOutput(); err != nil {
		log.Printf("[WP-Panel] Nginx 配置语法错误，跳过重载: %s", string(out))
		return
	}
	exec.Command("nginx", "-s", "reload").Run()
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
	poolSize := fmt.Sprintf("%dM", RecommendInnoDBBufferPoolSizeMB(CollectSystemFacts()))
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

	maxmem := fmt.Sprintf("%dmb", RecommendRedisMaxmemoryMB(CollectSystemFacts()))

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
