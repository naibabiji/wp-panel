<?php
/**
 * WP Panel Optimizer — 面板 API 通信模块
 *
 * 配置加载（面板地址/API Key）、与面板 REST API 的全部 HTTP 通信、
 * 文件锁状态同步、插件自身版本检查。
 */

if (!defined('ABSPATH')) exit;

trait WPP_Optimizer_Config_Trait {

    private static function is_path_allowed_by_open_basedir($path) {
        $openBasedir = ini_get('open_basedir');
        if (!$openBasedir) {
            return true;
        }

        $path = str_replace('\\', '/', $path);
        foreach (explode(PATH_SEPARATOR, $openBasedir) as $allowed) {
            $allowed = trim($allowed);
            if ($allowed === '') {
                continue;
            }
            if ($allowed === '.' && defined('ABSPATH')) {
                $allowed = ABSPATH;
            }
            $allowed = str_replace('\\', '/', $allowed);
            if ($allowed === '/') {
                return true;
            }
            $allowed = rtrim($allowed, '/');
            if ($allowed === '') {
                continue;
            }
            if ($path === $allowed || strpos($path, $allowed . '/') === 0) {
                return true;
            }
        }

        return false;
    }

    private static function load_config() {
        static $loaded = false;
        static $cached = null;

        if ($loaded) {
            return $cached;
        }
        $loaded = true;

        $base = '/var/wp-panel/site-secrets/';
        $files = array();

        // Current WP Panel PHP-FPM pools provide the exact identity path. This
        // remains correct when the panel domain and WordPress home URL differ.
        $runtimeFile = getenv('WP_PANEL_CONFIG_PATH');
        if (is_string($runtimeFile)) {
            $runtimeFile = str_replace('\\', '/', trim($runtimeFile));
            if (preg_match('#^/var/wp-panel/site-secrets/[A-Za-z0-9.-]+/wp-panel-config\.json$#', $runtimeFile)) {
                $files[] = $runtimeFile;
            }
        }

        // Compatibility for pools generated before WP_PANEL_CONFIG_PATH was
        // introduced. These candidates can be removed after the legacy support
        // window ends; callers that require an explicit identity validate it
        // before reaching this general-purpose loader.
        $domain = wp_parse_url(home_url(), PHP_URL_HOST);
        if ($domain) {
            $domain = strtolower(trim($domain));
            $domains = array($domain);
            if (strpos($domain, 'www.') === 0) {
                $domains[] = substr($domain, 4);
            } else {
                $domains[] = 'www.' . $domain;
            }
            foreach ($domains as $candidateDomain) {
                $files[] = $base . $candidateDomain . '/wp-panel-config.json';
            }
        }

        foreach (array_unique($files) as $file) {
            if (!self::is_path_allowed_by_open_basedir($file)) {
                continue;
            }
            if (file_exists($file)) {
                $json = file_get_contents($file);
                if ($json === false) {
                    continue;
                }
                $cached = json_decode($json, true);
                return $cached;
            }
        }
        return null;
    }

    private static function current_site_domain($domain = null) {
        if (is_string($domain) && $domain !== '') {
            return strtolower(trim($domain));
        }
        return strtolower(trim((string) wp_parse_url(home_url(), PHP_URL_HOST)));
    }

    private static function get_panel_url() {
        $cfg = self::load_config();
        return $cfg ? $cfg['panel_url'] : '';
    }

    private static function get_api_key() {
        $cfg = self::load_config();
        return $cfg ? $cfg['api_key'] : '';
    }

    private static function fetch_panel_state() {
        $domain = wp_parse_url(home_url(), PHP_URL_HOST);
        $resp = self::api_request('GET', '/api/sites/find?domain=' . urlencode($domain));
        if (!$resp || is_wp_error($resp)) return null;
        $data = json_decode($resp, true);
        return !empty($data['success']) ? ($data['data'] ?? null) : null;
    }

    private static function sync_file_lock_state($force = false) {
        if (!$force) {
            $cached = get_transient(self::FILE_LOCK_STATE_TRANSIENT);
            if ($cached !== false) {
                return $cached === '1';
            }
        }
        $panelState = self::fetch_panel_state();
        if (is_array($panelState)) {
            return self::update_file_lock_state_option($panelState) === '1';
        }
        $current = get_option(self::OPTION_FILE_LOCK_ENABLED, '0') === '1' ? '1' : '0';
        set_transient(self::FILE_LOCK_STATE_TRANSIENT, $current, self::FILE_LOCK_STATE_TTL);
        return $current === '1';
    }

    private static function update_file_lock_state_option($panelState) {
        $value = !empty($panelState['file_lock_enabled']) ? '1' : '0';
        update_option(self::OPTION_FILE_LOCK_ENABLED, $value);
        set_transient(self::FILE_LOCK_STATE_TRANSIENT, $value, self::FILE_LOCK_STATE_TTL);
        return $value;
    }

    private static function push_optimizer_settings($fcacheEnabled, $fcacheTTL, $noUpdates, $noFileEdit, $wpDebug = false, $postRevisions = -1, $memoryLimit = '', $fileLockSafeOnly = false) {
        $domain = wp_parse_url(home_url(), PHP_URL_HOST);
        $resp = self::api_request('PUT', '/api/sites/optimizer-settings', [
            'domain'               => $domain,
            'enabled'              => $fcacheEnabled,
            'ttl'                  => $fcacheTTL,
            'disable_wp_updates'   => $noUpdates,
            'disable_file_editing' => $noFileEdit,
            'wp_debug_enabled'     => $wpDebug,
            'wp_post_revisions'    => $postRevisions,
            'wp_memory_limit'      => $memoryLimit,
            'file_lock_safe_only'  => $fileLockSafeOnly,
        ]);
        if (is_wp_error($resp)) return $resp;
        $data = json_decode($resp, true);
        if (empty($data['success'])) {
            return new \WP_Error('api_error', $data['message'] ?? __('API returned an error', 'wp-panel-optimizer'));
        }
        return true;
    }

    private static function do_clear($domain = null) {
        $domain = self::current_site_domain($domain);
        if ($domain === '') {
            return ['success' => false, 'code' => 'config_missing', 'message' => __('Unable to determine the current site identity from WP_PANEL_CONFIG_PATH.', 'wp-panel-optimizer')];
        }
        $resp = self::api_request('DELETE', '/api/sites/clear-cache', ['domain' => $domain]);
        if (is_wp_error($resp)) {
            $data = $resp->get_error_data();
            $payload = is_array($data) && isset($data['payload']) && is_array($data['payload']) ? $data['payload'] : [];
            return [
                'success' => false,
                'code' => !empty($payload['error_code']) ? (string) $payload['error_code'] : (string) $resp->get_error_code(),
                'message' => $resp->get_error_message(),
                'scope' => $payload['scope'] ?? null,
                'not_cleared' => $payload['not_cleared'] ?? [],
            ];
        }
        $data = json_decode($resp, true);
        if (!is_array($data)) {
            return ['success' => false, 'code' => 'invalid_response', 'message' => __('The panel returned an invalid cache-clear response.', 'wp-panel-optimizer')];
        }
        $success = !empty($data['success']);
        return [
            'success' => $success,
            'code' => !$success && !empty($data['error_code']) ? (string) $data['error_code'] : ($success ? 'ok' : 'operation_failed'),
            'message' => $data['message'] ?? ($data['data']['message'] ?? ''),
            'scope' => $data['data']['scope'] ?? $data['scope'] ?? null,
            'not_cleared' => $data['data']['not_cleared'] ?? $data['not_cleared'] ?? [],
        ];
    }

    public static function api_request_public($method, $path, $body = null) {
        return self::api_request($method, $path, $body);
    }

    private static function api_request($method, $path, $body = null, $timeout = 10) {
        $baseUrl = self::get_panel_url();
        $apiKey  = self::get_api_key();
        if (!$baseUrl || !$apiKey) {
            return new \WP_Error('config_missing', __('Unable to read the WP Panel configuration. The site domain or runtime configuration may have changed; rebuild the companion plugin configuration from the site details page in WP Panel.', 'wp-panel-optimizer'));
        }

        $args = [
            'method'    => $method,
            'headers'   => [
                'X-WP-Panel-Key' => $apiKey,
                'Content-Type'   => 'application/json',
            ],
            'timeout'   => max(1, min(30, intval($timeout))),
            'sslverify' => false,
        ];

        if ($body) {
            $args['body'] = json_encode($body);
        }

        $response = wp_remote_request($baseUrl . $path, $args);
        if (is_wp_error($response)) {
            return $response;
        }

        $code = wp_remote_retrieve_response_code($response);
        if ($code >= 400) {
            $body = wp_remote_retrieve_body($response);
            // 面板错误响应是 {"success":false,"message":"..."} 这样的 JSON，取出 message
            // 字段展示给用户；不是这个形状（例如反代/网关的错误页）才退回原始响应体。
            $decoded = $body ? json_decode($body, true) : null;
            if (is_array($decoded) && !empty($decoded['message'])) {
                $msg = $decoded['message'];
            } else {
                $msg = $body ?: "HTTP $code";
            }
            return new \WP_Error('api_error', $msg, ['http_status' => $code, 'payload' => is_array($decoded) ? $decoded : []]);
        }

        return wp_remote_retrieve_body($response);
    }
}
