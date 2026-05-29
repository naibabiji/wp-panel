package executor

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"text/template"
	"time"
)

type NginxSiteData struct {
	Domain           string
	Aliases          []string
	ServerNames      string
	WebRoot          string
	LogDir           string
	SystemUser       string
	UseSSL           bool
	SSLCertPath      string
	SSLKeyPath       string
	PHPProxy         string
	TemplateVer      string
	AccessLogMode    string
	FCacheEnabled    bool
	FCacheTTL        int
	FCacheKey        string
	SiteType         string
	RateLimitEnabled bool
	RateLimitBurst   int
	XMLRPCEnabled    bool
}

type PHPFPMPoolData struct {
	Domain     string
	SystemUser string
	WebRoot    string
	SocketPath string
}

type TemplateEngine struct {
	BackupDir string
}

func EnsureLogMap() {
	confDir := "/etc/nginx/conf.d"
	confPath := confDir + "/wppanel-log.conf"
	os.MkdirAll(confDir, 0755)
	content := `# WP Panel — 日志条件变量 (勿手动修改)
map $status $wp_loggable {
    ~^[45]  1;
    default 0;
}

map $arg_wp_hc $wp_hc_loggable {
    ""      1;
    default 0;
}
`
	os.WriteFile(confPath, []byte(content), 0644)
	exec.Command("nginx", "-s", "reload").Run()
}

func NewTemplateEngine(backupDir string) *TemplateEngine {
	os.MkdirAll(backupDir, 0755)
	return &TemplateEngine{BackupDir: backupDir}
}

func (e *TemplateEngine) RenderNginxConfig(data *NginxSiteData) (string, error) {
	data.RateLimitEnabled, _, data.RateLimitBurst = GetRateLimitSettings()
	tmplName := "nginx_http"
	if data.UseSSL {
		tmplName = "nginx_https"
	}

	tmpl, err := template.New(tmplName).Parse(getNginxTemplate(data.UseSSL, data.SiteType))
	if err != nil {
		return "", fmt.Errorf("模板解析失败: %w", err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("模板渲染失败: %w", err)
	}

	return buf.String(), nil
}

func (e *TemplateEngine) RenderPHPFPMPool(data *PHPFPMPoolData) (string, error) {
	tmpl, err := template.New("php_fpm_pool").Parse(phpFPMPoolTemplate)
	if err != nil {
		return "", fmt.Errorf("模板解析失败: %w", err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("模板渲染失败: %w", err)
	}

	return buf.String(), nil
}

func (e *TemplateEngine) ApplyNginxConfig(configContent string, targetPath string, enabledPath string) error {
	ts := fmt.Sprintf("%d", time.Now().UnixNano())
	serverTmp := "/tmp/nginx_server_" + ts + ".conf"
	mainTmp := "/tmp/nginx_main_" + ts + ".conf"

	if err := os.WriteFile(serverTmp, []byte(configContent), 0644); err != nil {
		return fmt.Errorf("写入临时配置失败: %w", err)
	}
	defer os.Remove(serverTmp)

	customDir := "/www/server/panel/nginx-custom"
	os.MkdirAll(customDir, 0755)
	for _, line := range strings.Split(configContent, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "include "+customDir+"/") {
			incPath := strings.TrimPrefix(line, "include ")
			incPath = strings.TrimRight(incPath, ";")
			incPath = strings.TrimSpace(incPath)
			if _, err := os.Stat(incPath); os.IsNotExist(err) {
				os.WriteFile(incPath, []byte{}, 0644)
			}
		}
	}

	wrapper := "events { worker_connections 1024; }\nhttp {\n    include /etc/nginx/mime.types;\n    include /etc/nginx/conf.d/*.conf;\n    include " + serverTmp + ";\n}\n"
	if err := os.WriteFile(mainTmp, []byte(wrapper), 0644); err != nil {
		return fmt.Errorf("写入临时主配置失败: %w", err)
	}
	defer os.Remove(mainTmp)

	preCheckCmd := exec.Command("nginx", "-t", "-c", mainTmp)
	preCheckOut, err := preCheckCmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("Nginx 语法检查失败:\n%s", string(preCheckOut))
	}

	if _, err := os.Stat(targetPath); err == nil {
		nginxBackupDir := e.BackupDir + "/nginx"
		os.MkdirAll(nginxBackupDir, 0755)
		backupPath := nginxBackupDir + "/" + fmt.Sprintf("%s.bak.%d", getConfBaseName(targetPath), time.Now().Unix())
		if err := os.Rename(targetPath, backupPath); err != nil {
			return fmt.Errorf("备份旧配置失败: %w", err)
		}
	}

	if err := os.WriteFile(targetPath, []byte(configContent), 0644); err != nil {
		return fmt.Errorf("写入配置文件失败: %w", err)
	}

	_ = os.Remove(enabledPath)
	if err := os.Symlink(targetPath, enabledPath); err != nil {
		return fmt.Errorf("创建软链接失败: %w", err)
	}

	reloadCmd := exec.Command("nginx", "-s", "reload")
	reloadOut, err := reloadCmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("Nginx 重载失败: %s", string(reloadOut))
	}

	return nil
}

func (e *TemplateEngine) ApplyPHPFPMPool(configContent string, targetPath string, logDir string) error {
	os.MkdirAll(logDir, 0755)

	oldContent, oldErr := os.ReadFile(targetPath)
	hadOld := oldErr == nil

	if err := os.WriteFile(targetPath, []byte(configContent), 0644); err != nil {
		return fmt.Errorf("写入PHP-FPM配置失败: %w", err)
	}

	testCmd := exec.Command("php-fpm8.3", "-t")
	testOut, err := testCmd.CombinedOutput()
	if err != nil {
		if hadOld {
			_ = os.WriteFile(targetPath, oldContent, 0644)
		} else {
			_ = os.Remove(targetPath)
		}
		return fmt.Errorf("PHP-FPM 配置检查失败，已回滚\n%s", string(testOut))
	}

	// 尝试 reload，失败则 restart，再失败则 start
	reloadCmd := exec.Command("systemctl", "reload", "php8.3-fpm")
	if _, err := reloadCmd.CombinedOutput(); err != nil {
		restartCmd := exec.Command("systemctl", "restart", "php8.3-fpm")
		if _, err := restartCmd.CombinedOutput(); err != nil {
			startCmd := exec.Command("systemctl", "start", "php8.3-fpm")
			if _, err := startCmd.CombinedOutput(); err != nil {
				if hadOld {
					_ = os.WriteFile(targetPath, oldContent, 0644)
				} else {
					_ = os.Remove(targetPath)
				}
				_ = exec.Command("systemctl", "restart", "php8.3-fpm").Run()
				return fmt.Errorf("PHP-FPM 启动失败，请检查: systemctl status php8.3-fpm")
			}
		}
		// 重启后等待 socket 就绪
		sockPath := "/run/php/php8.3-fpm.sock"
		for i := 0; i < 30; i++ {
			if _, err := os.Stat(sockPath); err == nil {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
	}

	return nil
}

func (e *TemplateEngine) RemoveNginxConfig(targetPath string, enabledPath string) error {
	_ = os.Remove(enabledPath)
	_ = os.Remove(targetPath)

	reloadCmd := exec.Command("nginx", "-s", "reload")
	reloadOut, err := reloadCmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("Nginx 重载失败: %s", string(reloadOut))
	}
	return nil
}

func getConfBaseName(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' {
			return path[i+1:]
		}
	}
	return path
}

func getNginxTemplate(useSSL bool, siteType string) string {
	if siteType == "php" {
		if useSSL {
			return phpHTTPSTemplate
		}
		return phpHTTPTemplate
	}
	if useSSL {
		return nginxHTTPSTemplate
	}
	return nginxHTTPTemplate
}

const nginxHTTPTemplate = `# WP Panel Generated — {{.TemplateVer}}
# Site: {{.Domain}}
server {
    listen 80;
    listen [::]:80;

    server_name {{.ServerNames}};

    if ($wppanel_banned_ip) { return 444; }

    {{if .RateLimitEnabled}}
    limit_req zone=wp_req_limit burst={{.RateLimitBurst}} nodelay;
    limit_req_status 429;
    {{end}}

    set $wp_cache_ver "{{.FCacheKey}}";

    include /www/server/panel/nginx-custom/{{.Domain}}.pre.conf;

    root {{.WebRoot}};
    index index.php index.html index.htm;

    {{if eq .AccessLogMode "full"}}
	    access_log /www/wwwlogs/{{.Domain}}/access.log combined if=$wp_hc_loggable;
	    {{else if eq .AccessLogMode "error_only"}}
	    access_log /www/wwwlogs/{{.Domain}}/access.log combined if=$wp_loggable;
	    {{else}}
	    access_log off;
	    {{end}}

    include /www/server/panel/nginx-custom/{{.Domain}}.conf;

    location / {
        try_files $uri $uri/ /index.php?$args;
    }

    {{if not .XMLRPCEnabled}}
    location ~ ^/+xmlrpc\.php$ {
        return 403;
    }

    {{end}}
    location ~ \.php$ {
        include /etc/nginx/fastcgi_params;
        fastcgi_pass {{.PHPProxy}};
        fastcgi_index index.php;
        fastcgi_param SCRIPT_FILENAME $document_root$fastcgi_script_name;
        fastcgi_read_timeout 300;
	    {{if .FCacheEnabled}}
	    set $wp_skip_cache 0;
	    if ($request_method = POST) { set $wp_skip_cache 1; }
	    if ($query_string != "") { set $wp_skip_cache 1; }
	    if ($http_cookie ~* "wordpress_logged_in|comment_author|woocommerce|wp_woocommerce_session|wp-resetpass") { set $wp_skip_cache 1; }
	    if ($request_uri ~* "/wp-admin/|/wp-login.php|/cart/|/checkout/|/my-account/") { set $wp_skip_cache 1; }
	    fastcgi_cache WP_CACHE;
	    fastcgi_cache_key "$scheme$request_method$host$request_uri$wp_cache_ver";
	    fastcgi_cache_valid 200 301 {{.FCacheTTL}}s;
	    fastcgi_cache_use_stale error timeout updating invalid_header http_500;
	    fastcgi_cache_bypass $wp_skip_cache;
	    fastcgi_no_cache $wp_skip_cache;
	    fastcgi_cache_lock on;
	    add_header X-FastCGI-Cache $upstream_cache_status;
	    {{end}}
    }

    location ~* \.(js|css|png|jpg|jpeg|gif|ico|svg|woff|woff2|ttf|eot)$ {
        expires 30d;
        add_header Cache-Control "public, immutable";
    }

    location ~* \.(env|git|config\.bak|sql|tar|gz|zip|old|swp|save)$ {
        return 404;
    }

	    location ~* /wp-panel-config.json$ {
	        return 404;
	    }

    location ^~ /.well-known/acme-challenge/ {
        try_files $uri =404;
    }

    location ~ /\. {
        return 404;
    }

    location = /wp-login.php {
        include /etc/nginx/fastcgi_params;
        fastcgi_pass {{.PHPProxy}};
        fastcgi_index index.php;
        fastcgi_param SCRIPT_FILENAME $document_root$fastcgi_script_name;
        fastcgi_read_timeout 300;
    }

    {{if .XMLRPCEnabled}}
    location = /xmlrpc.php {
        include /etc/nginx/fastcgi_params;
        fastcgi_pass {{.PHPProxy}};
        fastcgi_index index.php;
        fastcgi_param SCRIPT_FILENAME $document_root$fastcgi_script_name;
        fastcgi_read_timeout 300;
    }
    {{end}}
}
`

const nginxHTTPSTemplate = `# WP Panel Generated — {{.TemplateVer}}
# Site: {{.Domain}}
server {
    listen 80;
    listen [::]:80;
    server_name {{.ServerNames}};

    if ($wppanel_banned_ip) { return 444; }

    {{if .RateLimitEnabled}}
    limit_req zone=wp_req_limit burst={{.RateLimitBurst}} nodelay;
    limit_req_status 429;
    {{end}}

    set $wp_cache_ver "{{.FCacheKey}}";

    return 301 https://$host$request_uri;
}

server {
    listen 443 ssl http2;
    listen [::]:443 ssl http2;

    server_name {{.ServerNames}};

    if ($wppanel_banned_ip) { return 444; }

    {{if .RateLimitEnabled}}
    limit_req zone=wp_req_limit burst={{.RateLimitBurst}} nodelay;
    limit_req_status 429;
    {{end}}

    set $wp_cache_ver "{{.FCacheKey}}";

    ssl_certificate {{.SSLCertPath}};
    ssl_certificate_key {{.SSLKeyPath}};
    ssl_protocols TLSv1.2 TLSv1.3;
    ssl_ciphers ECDHE-ECDSA-AES128-GCM-SHA256:ECDHE-RSA-AES128-GCM-SHA256:ECDHE-ECDSA-AES256-GCM-SHA384:ECDHE-RSA-AES256-GCM-SHA384:ECDHE-ECDSA-CHACHA20-POLY1305:ECDHE-RSA-CHACHA20-POLY1305;
    ssl_prefer_server_ciphers off;
    ssl_session_cache shared:SSL:10m;
    ssl_session_timeout 10m;

    add_header Strict-Transport-Security "max-age=31536000; includeSubDomains" always;

    include /www/server/panel/nginx-custom/{{.Domain}}.pre.conf;

    root {{.WebRoot}};
    index index.php index.html index.htm;

    {{if eq .AccessLogMode "full"}}
	    access_log /www/wwwlogs/{{.Domain}}/access.log combined if=$wp_hc_loggable;
	    {{else if eq .AccessLogMode "error_only"}}
	    access_log /www/wwwlogs/{{.Domain}}/access.log combined if=$wp_loggable;
	    {{else}}
	    access_log off;
	    {{end}}

    include /www/server/panel/nginx-custom/{{.Domain}}.conf;

    location / {
        try_files $uri $uri/ /index.php?$args;
    }

    {{if not .XMLRPCEnabled}}
    location ~ ^/+xmlrpc\.php$ {
        return 403;
    }

    {{end}}
    location ~ \.php$ {
        include /etc/nginx/fastcgi_params;
        fastcgi_pass {{.PHPProxy}};
        fastcgi_index index.php;
        fastcgi_param SCRIPT_FILENAME $document_root$fastcgi_script_name;
        fastcgi_param HTTPS on;
        fastcgi_read_timeout 300;
    }

	    {{if .FCacheEnabled}}
	    set $wp_skip_cache 0;
	    if ($request_method = POST) { set $wp_skip_cache 1; }
	    if ($query_string != "") { set $wp_skip_cache 1; }
	    if ($http_cookie ~* "wordpress_logged_in|comment_author|woocommerce|wp_woocommerce_session|wp-resetpass") { set $wp_skip_cache 1; }
	    if ($request_uri ~* "/wp-admin/|/wp-login.php|/cart/|/checkout/|/my-account/") { set $wp_skip_cache 1; }
	    fastcgi_cache WP_CACHE;
	    fastcgi_cache_key "$scheme$request_method$host$request_uri$wp_cache_ver";
	    fastcgi_cache_valid 200 301 {{.FCacheTTL}}s;
	    fastcgi_cache_use_stale error timeout updating invalid_header http_500;
	    fastcgi_cache_bypass $wp_skip_cache;
	    fastcgi_no_cache $wp_skip_cache;
	    fastcgi_cache_lock on;
	    add_header X-FastCGI-Cache $upstream_cache_status;
	    {{end}}
    location ~* \.(js|css|png|jpg|jpeg|gif|ico|svg|woff|woff2|ttf|eot)$ {
        expires 30d;
        add_header Cache-Control "public, immutable";
    }

    location ~* \.(env|git|config\.bak|sql|tar|gz|zip|old|swp|save)$ {
        return 404;
    }

	    location ~* /wp-panel-config.json$ {
	        return 404;
	    }

    location ^~ /.well-known/acme-challenge/ {
        try_files $uri =404;
    }

    location ~ /\. {
        return 404;
    }

    location = /wp-login.php {
        include /etc/nginx/fastcgi_params;
        fastcgi_pass {{.PHPProxy}};
        fastcgi_index index.php;
        fastcgi_param SCRIPT_FILENAME $document_root$fastcgi_script_name;
        fastcgi_param HTTPS on;
        fastcgi_read_timeout 300;
    }

    {{if .XMLRPCEnabled}}
    location = /xmlrpc.php {
        include /etc/nginx/fastcgi_params;
        fastcgi_pass {{.PHPProxy}};
        fastcgi_index index.php;
        fastcgi_param SCRIPT_FILENAME $document_root$fastcgi_script_name;
        fastcgi_param HTTPS on;
        fastcgi_read_timeout 300;
    }
    {{end}}
}
`

const phpFPMPoolTemplate = `; WP Panel Generated — v1.0
; Site: {{.Domain}}

[{{.Domain}}]
user = {{.SystemUser}}
group = {{.SystemUser}}

listen = {{.SocketPath}}/{{.Domain}}.sock
listen.owner = www-data
listen.group = www-data
listen.mode = 0660

pm = ondemand
pm.max_children = 10
pm.start_servers = 2
pm.min_spare_servers = 1
pm.max_spare_servers = 5
pm.process_idle_timeout = 10s
pm.max_requests = 500

php_admin_value[open_basedir] = {{.WebRoot}}:/tmp:/usr/share/php:/var/wp-panel/site-secrets/{{.Domain}}
php_admin_value[upload_max_filesize] = 64M
php_admin_value[post_max_size] = 64M
php_admin_value[max_execution_time] = 300
php_admin_value[max_input_time] = 300
php_admin_value[memory_limit] = 256M
php_admin_value[disable_functions] = exec,passthru,shell_exec,system,proc_open,popen,show_source
php_admin_flag[allow_url_fopen] = On
php_admin_flag[allow_url_include] = Off

slowlog = /www/wwwlogs/{{.Domain}}/php-slow.log
request_slowlog_timeout = 30s

php_flag[display_errors] = Off
php_flag[log_errors] = On
php_value[error_log] = /www/wwwlogs/{{.Domain}}/php-error.log
`
