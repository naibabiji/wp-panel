<?php
/**
 * Plugin Name: WP Panel Optimizer
 * Plugin URI:  https://github.com/naibabiji/wp-panel
 * Description: 与 WP Panel 面板配合，管理 FastCGI 缓存、禁止检测更新、文件编辑等优化项。发布/更新文章自动清除缓存。
 * Version:     1.0.0
 * Author:      WP Panel
 * Author URI:  https://blog.naibabiji.com
 * License:     GPL-2.0+
 */

if (!defined('ABSPATH')) exit;

register_uninstall_hook(__FILE__, 'wpp_optimizer_uninstall');
function wpp_optimizer_uninstall() {
    delete_option('wpp_optimizer_fcache_enabled');
    delete_option('wpp_optimizer_fcache_ttl');
    delete_option('wpp_optimizer_no_updates');
    delete_option('wpp_optimizer_no_file_edit');
    delete_option('wpp_optimizer_verified');
    delete_option('wpp_optimizer_log');
}

class WP_Panel_Optimizer {

    const VERSION = '1.0.0';

    const OPTION_FCACHE_ENABLED = 'wpp_optimizer_fcache_enabled';
    const OPTION_FCACHE_TTL     = 'wpp_optimizer_fcache_ttl';
    const OPTION_NO_UPDATES     = 'wpp_optimizer_no_updates';
    const OPTION_NO_FILE_EDIT   = 'wpp_optimizer_no_file_edit';
    const OPTION_VERIFIED       = 'wpp_optimizer_verified';
    const OPTION_LOG            = 'wpp_optimizer_log';

    private static function load_config() {
        $domain = wp_parse_url(home_url(), PHP_URL_HOST);
        $file = '/www/server/panel/site-secrets/' . $domain . '/wp-panel-config.json';
        if (!file_exists($file)) return null;
        return json_decode(file_get_contents($file), true);
    }

    private static function get_panel_url() {
        $cfg = self::load_config();
        return $cfg ? $cfg['panel_url'] : '';
    }

    private static function get_api_key() {
        $cfg = self::load_config();
        return $cfg ? $cfg['api_key'] : '';
    }

    public static function init() {
        add_action('admin_bar_menu', [__CLASS__, 'admin_bar_button'], 100);
        add_action('admin_menu', [__CLASS__, 'settings_page']);
        add_action('admin_post_wpp_cache_clear', [__CLASS__, 'handle_clear']);
        add_action('save_post', [__CLASS__, 'auto_clear'], 99, 1);
        add_action('deleted_post', [__CLASS__, 'auto_clear'], 99, 1);
        add_action('wp_update_comment_count', [__CLASS__, 'auto_comment_clear']);
        add_filter('plugin_action_links_' . plugin_basename(__FILE__), [__CLASS__, 'action_links']);
        add_action('admin_notices', [__CLASS__, 'clear_notice']);
        add_action('wp_ajax_wpp_optimizer_check_update', [__CLASS__, 'ajax_check_update']);

        // 禁止检测更新：完全屏蔽更新提示和通知
        if (get_option(self::OPTION_NO_UPDATES, '0') === '1') {
            add_action('admin_init', [__CLASS__, 'suppress_updates']);
        }
    }

    public static function suppress_updates() {
        remove_action('admin_notices', 'update_nag', 3);
        remove_action('network_admin_notices', 'update_nag', 3);
        remove_action('wp_version_check', 'wp_version_check');
        remove_action('admin_init', '_maybe_update_core');
        remove_action('admin_init', '_maybe_update_plugins');
        remove_action('admin_init', '_maybe_update_themes');
        remove_action('load-plugins.php', 'wp_update_plugins');
        remove_action('load-themes.php', 'wp_update_themes');
        remove_action('load-update-core.php', 'wp_update_plugins');
        remove_action('load-update-core.php', 'wp_update_themes');
        remove_action('wp_update_plugins', 'wp_update_plugins');
        remove_action('wp_update_themes', 'wp_update_themes');
        wp_clear_scheduled_hook('wp_version_check');
        wp_clear_scheduled_hook('wp_update_plugins');
        wp_clear_scheduled_hook('wp_update_themes');

        add_filter('pre_site_transient_update_core', '__return_null');
        add_filter('pre_site_transient_update_plugins', '__return_null');
        add_filter('pre_site_transient_update_themes', '__return_null');

        if (!current_user_can('update_core')) return;
        add_filter('wp_get_update_data', [__CLASS__, 'filter_update_data'], 10, 2);
    }

    public static function filter_update_data($data) {
        $data['counts'] = ['total' => 0, 'plugins' => 0, 'themes' => 0, 'wordpress' => 0, 'translations' => 0];
        $data['title']  = '';
        return $data;
    }

    public static function action_links($links) {
        $links[] = '<a href="' . admin_url('options-general.php?page=wp-panel-optimizer') . '">设置</a>';
        return $links;
    }

    public static function settings_page() {
        add_options_page('WP Panel Optimizer', 'WP Panel Optimizer', 'manage_options', 'wp-panel-optimizer', [__CLASS__, 'render_settings']);
    }

    public static function render_settings() {
        $cfg = self::load_config();
        $panelUrl = self::get_panel_url();
        $apiKey = self::get_api_key();
        $currentDomain = wp_parse_url(home_url(), PHP_URL_HOST);
        $missing = !$panelUrl || !$apiKey;

        // 从面板同步最新状态
        $panelState = self::fetch_panel_state();
        if ($panelState) {
            update_option(self::OPTION_FCACHE_ENABLED, !empty($panelState['fastcgi_cache_enabled']) ? '1' : '0');
            update_option(self::OPTION_FCACHE_TTL, intval($panelState['fastcgi_cache_ttl'] ?? 300));
            update_option(self::OPTION_NO_UPDATES, !empty($panelState['disable_wp_updates']) ? '1' : '0');
            update_option(self::OPTION_NO_FILE_EDIT, !empty($panelState['disable_file_editing']) ? '1' : '0');
        }

        $notice = '';
        if (isset($_POST['wpp_save'])) {
            check_admin_referer('wpp_optimizer_settings');
            $fcacheEnabled  = !empty($_POST['fcache_enabled'])  ? true : false;
            $fcacheTTL      = isset($_POST['fcache_ttl']) ? intval($_POST['fcache_ttl']) : 300;
            $noUpdates      = !empty($_POST['no_updates'])      ? true : false;
            $noFileEdit     = !empty($_POST['no_file_edit'])    ? true : false;

            if ($fcacheTTL < 10)  $fcacheTTL = 300;
            if ($fcacheTTL > 86400) $fcacheTTL = 86400;

            update_option(self::OPTION_FCACHE_ENABLED, $fcacheEnabled ? '1' : '0');
            update_option(self::OPTION_FCACHE_TTL, $fcacheTTL);
            update_option(self::OPTION_NO_UPDATES, $noUpdates ? '1' : '0');
            update_option(self::OPTION_NO_FILE_EDIT, $noFileEdit ? '1' : '0');

            self::push_optimizer_settings($fcacheEnabled, $fcacheTTL, $noUpdates, $noFileEdit);
            $notice = '<div class="notice notice-success"><p>设置已保存，已同步到面板。</p></div>';
        }

        $fcacheEnabled  = get_option(self::OPTION_FCACHE_ENABLED, '0') === '1';
        $fcacheTTL      = get_option(self::OPTION_FCACHE_TTL, '300');
        $noUpdates      = get_option(self::OPTION_NO_UPDATES, '0') === '1';
        $noFileEdit     = get_option(self::OPTION_NO_FILE_EDIT, '0') === '1';
        $log            = get_option(self::OPTION_LOG, []);
        ?>
        <div class="wrap">
            <?php $pluginVersion = WP_Panel_Optimizer::VERSION; ?>
            <h1>WP Panel Optimizer</h1>
            <p>由 <a href="https://github.com/naibabiji/wp-panel" target="_blank">WP Panel</a> 面板统一管理。当前站点：<code><?php echo esc_html($currentDomain); ?></code></p>
            <p>插件版本：<code><?php echo esc_html($pluginVersion); ?></code>
                <button type="button" id="wpp-check-update-btn" class="button">检查更新</button>
                <span id="wpp-update-result"></span>
            </p>
            <?php echo wp_kses_post($notice); ?>
            <?php if ($missing): ?>
                <div class="notice notice-error"><p><strong>配置文件缺失</strong> — 请在 WP Panel 面板中进入该网站详情页，点击 WordPress 优化卡片的「安装配套插件」按钮完成初始化。</p></div>
            <?php endif; ?>
            <div id="wpp-verify-msg"></div>
            <hr>
            <form id="wpp-form" method="post">
                <?php wp_nonce_field('wpp_optimizer_settings'); ?>
                <table class="form-table">
                    <tr>
                        <th>面板地址</th>
                        <td><code><?php echo esc_html($panelUrl ?: '未配置'); ?></code></td>
                    </tr>
                    <tr>
                        <th>API Key</th>
                        <td><code><?php echo esc_html($apiKey ? substr($apiKey, 0, 8) . '...' : '未配置'); ?></code></td>
                    </tr>
                    <tr>
                        <th><label for="wpp-fcache-enabled">FastCGI 缓存</label></th>
                        <td>
                            <label><input id="wpp-fcache-enabled" name="fcache_enabled" type="checkbox" value="1" <?php checked($fcacheEnabled); ?>> 开启</label>
                            <p class="description">Nginx 将 PHP 页面缓存为静态 HTML，大幅提升访问速度。</p>
                        </td>
                    </tr>
                    <tr>
                        <th><label for="wpp-fcache-ttl">缓存有效期（秒）</label></th>
                        <td>
                            <input id="wpp-fcache-ttl" name="fcache_ttl" type="number" class="regular-text" value="<?php echo esc_attr($fcacheTTL); ?>" min="10" max="86400">
                            <p class="description">建议 300-3600 秒（5分钟到1小时）。</p>
                        </td>
                    </tr>
                    <tr>
                        <th><label for="wpp-no-updates">禁止检测更新</label></th>
                        <td>
                            <label><input id="wpp-no-updates" name="no_updates" type="checkbox" value="1" <?php checked($noUpdates); ?>> 完全屏蔽 WordPress 核心、插件和主题的更新检测和提示</label>
                            <p class="description">启用后完全屏蔽更新检测，仪表盘无红点无通知，后台「检查更新」也不生效。如需更新，先关闭此开关。</p>
                        </td>
                    </tr>
                    <tr>
                        <th><label for="wpp-no-file-edit">禁止文件编辑</label></th>
                        <td>
                            <label><input id="wpp-no-file-edit" name="no_file_edit" type="checkbox" value="1" <?php checked($noFileEdit); ?>> 禁止在 WordPress 后台编辑主题和插件文件</label>
                            <p class="description">面板将写入 <code>DISALLOW_FILE_EDIT</code> 常量到 wp-config.php。</p>
                        </td>
                    </tr>
                </table>
                <p>
                    <button type="submit" name="wpp_save" class="button button-primary">保存设置</button>
                    <button type="button" id="wpp-verify-btn" class="button">验证连接</button>
                </p>
            </form>

            <?php if (!empty($log)): ?>
            <hr>
            <h2>最近清除记录</h2>
            <table class="wp-list-table widefat fixed striped" style="max-width:600px">
                <thead><tr><th>时间</th><th>方式</th><th>结果</th></tr></thead>
                <tbody>
                    <?php foreach ($log as $entry): ?>
                    <tr>
                        <td><?php echo esc_html($entry['time']); ?></td>
                        <td><?php echo $entry['type'] === 'manual' ? '手动清除' : '自动清除（发布文章）'; ?></td>
                        <td><?php echo !empty($entry['success']) ? '<span style="color:green">成功</span>' : '<span style="color:red">失败</span>'; ?></td>
                    </tr>
                    <?php endforeach; ?>
                </tbody>
            </table>
            <?php endif; ?>

            <script>
            document.getElementById('wpp-verify-btn').addEventListener('click', function() {
                var btn = this, msg = document.getElementById('wpp-verify-msg');
                btn.disabled = true;
                btn.textContent = '验证中...';
                fetch('<?php echo esc_url(admin_url('admin-ajax.php')); ?>?action=wpp_optimizer_verify&_wpnonce=<?php echo esc_attr(wp_create_nonce('wpp_optimizer_settings')); ?>')
                    .then(r => r.json())
                    .then(data => {
                        if (data.success) {
                            msg.innerHTML = '<div class="notice notice-success"><p>✓ 连接成功 — 面板 API 响应正常</p></div>';
                        } else {
                            msg.innerHTML = '<div class="notice notice-error"><p>✗ 连接失败：' + (data.data?.message || '未知错误') + '</p></div>';
                        }
                    })
                    .catch(e => {
                        msg.innerHTML = '<div class="notice notice-error"><p>✗ 网络错误：无法连接到面板 (' + e.message + ')</p></div>';
                    })
                    .finally(() => { btn.disabled = false; btn.textContent = '验证连接'; });
            });

            document.getElementById('wpp-check-update-btn').addEventListener('click', function() {
                var btn = this, result = document.getElementById('wpp-update-result');
                btn.disabled = true;
                btn.textContent = '检查中...';
                result.innerHTML = '';
                fetch('<?php echo esc_url(admin_url('admin-ajax.php')); ?>?action=wpp_optimizer_check_update')
                    .then(r => r.json())
                    .then(data => {
                        if (data.success) {
                            var d = data.data;
                            if (d.has_update) {
                                result.innerHTML = ' <a href="' + d.release_url + '" target="_blank" style="color:#d63638;font-weight:bold">发现新版本 ' + d.latest + '（当前 ' + d.current + '）→ 在面板中更新</a>';
                            } else {
                                result.innerHTML = ' <span style="color:#00a32a">已是最新版本（' + d.current + '）</span>';
                            }
                        } else {
                            result.innerHTML = ' <span style="color:#d63638">检查失败：' + (data.data?.message || '未知错误') + '</span>';
                        }
                    })
                    .catch(e => {
                        result.innerHTML = ' <span style="color:#d63638">网络错误：' + e.message + '</span>';
                    })
                    .finally(() => { btn.disabled = false; btn.textContent = '检查更新'; });
            });
            </script>
        </div>
        <?php
    }

    public static function clear_notice() {
        if (!isset($_GET['wpp_cleared'])) return;
        if (!isset($_GET['_wpnonce']) || !wp_verify_nonce(sanitize_text_field(wp_unslash($_GET['_wpnonce'])), 'wpp_clear_notice')) return;
        if ($_GET['wpp_cleared'] === '1') {
            echo '<div class="notice notice-success is-dismissible"><p>Nginx 缓存已清除，旧页面将在几分钟内更新。</p></div>';
        } else {
            echo '<div class="notice notice-error is-dismissible"><p>清除缓存失败，请检查面板连接是否正常。</p></div>';
        }
    }

    public static function admin_bar_button($bar) {
        if (!self::get_panel_url()) return;
        $bar->add_node([
            'id'    => 'wpp-clear-cache',
            'title' => '清除 Nginx 缓存',
            'href'  => wp_nonce_url(admin_url('admin-post.php?action=wpp_cache_clear'), 'wpp_cache_clear'),
        ]);
    }

    public static function handle_clear() {
        check_admin_referer('wpp_cache_clear');
        $resp = self::do_clear();
        $success = !empty($resp['success']);
        self::log_clear('manual', $success);
        wp_safe_redirect(add_query_arg(['wpp_cleared' => $success ? '1' : '0', '_wpnonce' => wp_create_nonce('wpp_clear_notice')], wp_get_referer() ?: admin_url()));
        exit;
    }

    public static function auto_clear($post_id) {
        if (wp_is_post_revision($post_id) || wp_is_post_autosave($post_id)) return;
        $post = get_post($post_id);
        if (!$post || in_array($post->post_status, ['draft', 'auto-draft', 'inherit'])) return;
        if (!in_array($post->post_status, ['publish', 'trash', 'future', 'private'])) return;

        $resp = self::do_clear();
        $success = !empty($resp['success']);
        self::log_clear('auto', $success);
    }

    public static function auto_comment_clear($_) {
        $resp = self::do_clear();
        $success = !empty($resp['success']);
        self::log_clear('auto', $success);
    }

    private static function log_clear($type, $success) {
        $log = get_option(self::OPTION_LOG, []);
        array_unshift($log, [
            'time'    => current_time('Y-m-d H:i:s'),
            'type'    => $type,
            'success' => $success,
        ]);
        update_option(self::OPTION_LOG, array_slice($log, 0, 10));
    }

    // ============================================================
    // 面板 API 通信
    // ============================================================

    private static function fetch_panel_state() {
        $domain = wp_parse_url(home_url(), PHP_URL_HOST);
        $resp = self::api_request('GET', '/api/sites/find?domain=' . urlencode($domain));
        if (!$resp) return null;
        $data = json_decode($resp, true);
        return !empty($data['success']) ? ($data['data'] ?? null) : null;
    }

    private static function push_optimizer_settings($fcacheEnabled, $fcacheTTL, $noUpdates, $noFileEdit) {
        $domain = wp_parse_url(home_url(), PHP_URL_HOST);
        self::api_request('PUT', '/api/sites/optimizer-settings', [
            'domain'               => $domain,
            'enabled'              => $fcacheEnabled,
            'ttl'                  => $fcacheTTL,
            'disable_wp_updates'   => $noUpdates,
            'disable_file_editing' => $noFileEdit,
        ]);
    }

    private static function do_clear() {
        $domain = wp_parse_url(home_url(), PHP_URL_HOST);
        $resp = self::api_request('DELETE', '/api/sites/clear-cache', ['domain' => $domain]);
        $data = json_decode($resp, true);
        return ['success' => !empty($data['success']), 'message' => $data['message'] ?? ''];
    }

    public static function api_request_public($method, $path, $body = null) {
        return self::api_request($method, $path, $body);
    }

    public static function ajax_check_update() {
        $result = self::check_github_release();
        if (is_wp_error($result)) {
            wp_send_json(['success' => false, 'data' => ['message' => $result->get_error_message()]]);
            return;
        }
        $current  = self::VERSION;
        $latest   = ltrim($result['tag_name'], 'v');
        $hasUpdate = version_compare($latest, $current, '>');
        wp_send_json([
            'success'    => true,
            'data'       => [
                'current'     => $current,
                'latest'      => $latest,
                'has_update'  => $hasUpdate,
                'release_url' => $result['html_url'],
            ],
        ]);
    }

    private static function check_github_release() {
        $transient = get_transient('wpp_optimizer_release_v2');
        if ($transient !== false) return $transient;

        $resp = wp_remote_get('https://raw.githubusercontent.com/naibabiji/wp-panel/main/wp-panel-optimizer/wp-panel-optimizer.php', [
            'timeout'   => 10,
            'sslverify' => true,
            'headers'   => ['User-Agent' => 'WP-Panel-Optimizer'],
        ]);
        if (is_wp_error($resp)) return $resp;
        $code = wp_remote_retrieve_response_code($resp);
        if ($code !== 200) return new \WP_Error('github_error', "GitHub raw 返回 HTTP $code");

        $body = wp_remote_retrieve_body($resp);
        if (!preg_match('/Version:\s*([0-9]+\.[0-9]+\.[0-9]+(?:-[a-zA-Z0-9]+)?)/', $body, $m)) {
            return new \WP_Error('parse_error', '无法解析插件版本');
        }

        $result = [
            'tag_name' => 'v' . $m[1],
            'html_url' => 'https://github.com/naibabiji/wp-panel/releases',
        ];
        set_transient('wpp_optimizer_release_v2', $result, HOUR_IN_SECONDS);
        return $result;
    }

    private static function api_request($method, $path, $body = null) {
        $baseUrl = self::get_panel_url();
        $apiKey  = self::get_api_key();
        if (!$baseUrl || !$apiKey) return false;

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
        if (is_wp_error($response)) return false;
        return wp_remote_retrieve_body($response);
    }
}

add_action('init', ['WP_Panel_Optimizer', 'init']);

add_action('wp_ajax_wpp_optimizer_verify', function() {
    check_ajax_referer('wpp_optimizer_settings');
    $domain = wp_parse_url(home_url(), PHP_URL_HOST);
    $resp = WP_Panel_Optimizer::api_request_public('GET', '/api/sites/find?domain=' . urlencode($domain));
    if (!$resp) {
        wp_send_json(['success' => false, 'data' => ['message' => '无响应，请检查面板地址']]);
        return;
    }
    $data = json_decode($resp, true);
    if (!empty($data['success'])) {
        update_option(WP_Panel_Optimizer::OPTION_VERIFIED, '1');
        wp_send_json(['success' => true, 'data' => ['message' => '连接成功']]);
    } else {
        wp_send_json(['success' => false, 'data' => ['message' => $data['message'] ?? 'API 返回错误']]);
    }
});
