package executor

const phpHTTPTemplate = `# WP Panel Generated — {{.TemplateVer}}
# Site: {{.Domain}} (PHP)
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

    location ~* /wp-panel-config\.json$ {
        return 404;
    }

    location ^~ /.well-known/acme-challenge/ {
        try_files $uri =404;
    }

    location ~ /\. {
        return 404;
    }
}
`

const phpHTTPSTemplate = `# WP Panel Generated — {{.TemplateVer}}
# Site: {{.Domain}} (PHP)
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

    location ~ \.php$ {
        include /etc/nginx/fastcgi_params;
        fastcgi_pass {{.PHPProxy}};
        fastcgi_index index.php;
        fastcgi_param SCRIPT_FILENAME $document_root$fastcgi_script_name;
        fastcgi_param HTTPS on;
        fastcgi_read_timeout 300;
	    {{if .FCacheEnabled}}
	    set $wp_skip_cache 0;
	    if ($request_method = POST) { set $wp_skip_cache 1; }
	    if ($query_string != "") { set $wp_skip_cache 1; }
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

    location ~* /wp-panel-config\.json$ {
        return 404;
    }

    location ^~ /.well-known/acme-challenge/ {
        try_files $uri =404;
    }

    location ~ /\. {
        return 404;
    }
}
`
