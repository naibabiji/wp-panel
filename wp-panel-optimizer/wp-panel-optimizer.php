<?php
/**
 * Plugin Name: WP Panel Optimizer
 * Plugin URI:  https://github.com/naibabiji/wp-panel
 * Description: 与 WP Panel 面板配合，管理 FastCGI 缓存、预加载、调试模式、文章修订、内存限制等优化项。发布/更新文章自动清除缓存。
 * Version:     1.1.11
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
    delete_option('wpp_optimizer_xmlrpc_enabled');
    delete_option('wpp_optimizer_wp_debug');
    delete_option('wpp_optimizer_post_revisions');
    delete_option('wpp_optimizer_memory_limit');
    delete_option('wpp_optimizer_file_lock_enabled');
    delete_transient('wpp_optimizer_file_lock_state');
    delete_option('wpp_optimizer_preload_enabled');
    delete_option('wpp_optimizer_preload_limit');
    delete_option('wpp_optimizer_preload_queue');
    delete_option('wpp_optimizer_preload_status');
    wp_clear_scheduled_hook('wpp_optimizer_preload_batch');
    delete_option('wpp_optimizer_image_mode');
    delete_option('wpp_optimizer_image_jpeg_quality');
    delete_option('wpp_optimizer_image_webp_quality');
    delete_option('wpp_optimizer_image_skipped_count');
}


require_once __DIR__ . '/includes/trait-config.php';
require_once __DIR__ . '/includes/trait-cache.php';
require_once __DIR__ . '/includes/trait-settings.php';
require_once __DIR__ . '/includes/trait-image-optimizer.php';

class WP_Panel_Optimizer {

    use WPP_Optimizer_Config_Trait;
    use WPP_Optimizer_Cache_Trait;
    use WPP_Optimizer_Settings_Trait;
    use WPP_Optimizer_Image_Trait;

    const VERSION = '1.1.11';

    const OPTION_FCACHE_ENABLED = 'wpp_optimizer_fcache_enabled';
    const OPTION_FCACHE_TTL     = 'wpp_optimizer_fcache_ttl';
    const OPTION_NO_UPDATES     = 'wpp_optimizer_no_updates';
    const OPTION_NO_FILE_EDIT   = 'wpp_optimizer_no_file_edit';
    const OPTION_VERIFIED       = 'wpp_optimizer_verified';
    const OPTION_LOG            = 'wpp_optimizer_log';
    const OPTION_XMLRPC_ENABLED = 'wpp_optimizer_xmlrpc_enabled';
    const OPTION_WP_DEBUG       = 'wpp_optimizer_wp_debug';
    const OPTION_POST_REVISIONS = 'wpp_optimizer_post_revisions';
    const OPTION_MEMORY_LIMIT   = 'wpp_optimizer_memory_limit';
    const OPTION_FILE_LOCK_ENABLED = 'wpp_optimizer_file_lock_enabled';
    const FILE_LOCK_STATE_TRANSIENT = 'wpp_optimizer_file_lock_state';
    const FILE_LOCK_STATE_TTL       = 300;
    const OPTION_PRELOAD_ENABLED = 'wpp_optimizer_preload_enabled';
    const OPTION_PRELOAD_LIMIT   = 'wpp_optimizer_preload_limit';
    const OPTION_PRELOAD_QUEUE   = 'wpp_optimizer_preload_queue';
    const OPTION_PRELOAD_STATUS  = 'wpp_optimizer_preload_status';
    const PRELOAD_HOOK           = 'wpp_optimizer_preload_batch';
    const PRELOAD_BATCH_SIZE     = 5;
    const PRELOAD_TICK_THROTTLE  = 50;
}

add_action('plugins_loaded', ['WP_Panel_Optimizer', 'bootstrap'], 1);
add_action('init', ['WP_Panel_Optimizer', 'init']);

add_action('wp_ajax_wpp_optimizer_verify', function() {
    check_ajax_referer('wpp_optimizer_settings');
    $domain = wp_parse_url(home_url(), PHP_URL_HOST);
    $resp = WP_Panel_Optimizer::api_request_public('GET', '/api/sites/find?domain=' . urlencode($domain));
    if (!$resp || is_wp_error($resp)) {
        $err = is_wp_error($resp) ? $resp->get_error_message() : '无响应，请检查面板地址';
        wp_send_json(['success' => false, 'data' => ['message' => $err]]);
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
