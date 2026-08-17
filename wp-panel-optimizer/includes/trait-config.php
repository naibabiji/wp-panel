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

        $domain = wp_parse_url(home_url(), PHP_URL_HOST);
        if (!$domain) return null;
        $domain = strtolower(trim($domain));

        $base = '/var/wp-panel/site-secrets/';
        $candidates = array($domain);
        if (strpos($domain, 'www.') === 0) {
            $candidates[] = substr($domain, 4);
        } else {
            $candidates[] = 'www.' . $domain;
        }

        foreach ($candidates as $d) {
            $file = $base . $d . '/wp-panel-config.json';
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

    private static function push_optimizer_settings($fcacheEnabled, $fcacheTTL, $noUpdates, $noFileEdit, $wpDebug = false, $postRevisions = -1, $memoryLimit = '') {
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
        ]);
        if (is_wp_error($resp)) return $resp;
        $data = json_decode($resp, true);
        if (empty($data['success'])) {
            return new \WP_Error('api_error', $data['message'] ?? 'API 返回错误');
        }
        return true;
    }

    private static function do_clear() {
        $domain = wp_parse_url(home_url(), PHP_URL_HOST);
        $resp = self::api_request('DELETE', '/api/sites/clear-cache', ['domain' => $domain]);
        if (is_wp_error($resp)) {
            return ['success' => false, 'message' => $resp->get_error_message()];
        }
        $data = json_decode($resp, true);
        return ['success' => !empty($data['success']), 'message' => $data['message'] ?? ''];
    }

    public static function api_request_public($method, $path, $body = null) {
        return self::api_request($method, $path, $body);
    }

    private static function api_request($method, $path, $body = null) {
        $baseUrl = self::get_panel_url();
        $apiKey  = self::get_api_key();
        if (!$baseUrl || !$apiKey) {
            return new \WP_Error('config_missing', '面板地址或 API Key 未配置');
        }

        $args = [
            'method'    => $method,
            'headers'   => [
                'X-WP-Panel-Key' => $apiKey,
                'Content-Type'   => 'application/json',
            ],
            'timeout'   => 10,
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
            $msg = wp_remote_retrieve_body($response);
            $msg = $msg ?: "HTTP $code";
            return new \WP_Error('api_error', $msg);
        }

        return wp_remote_retrieve_body($response);
    }
}
