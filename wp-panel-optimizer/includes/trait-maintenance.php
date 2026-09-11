<?php
/** Restricted maintenance UI. No password is stored in WordPress. */
if (!defined('ABSPATH')) exit;

trait WPP_Optimizer_Maintenance_Trait {
    public static function maintenance_hooks() {
        add_action('admin_bar_menu', [__CLASS__, 'maintenance_bar'], 110);
        add_action('admin_enqueue_scripts', [__CLASS__, 'maintenance_assets']);
        add_action('admin_footer', [__CLASS__, 'maintenance_dialog']);
        add_action('wp_ajax_wpp_maintenance', [__CLASS__, 'maintenance_ajax']);
    }

    public static function maintenance_bar($bar) {
        if (!is_admin() || !current_user_can('manage_options') || is_multisite()) return;
        $bar->add_node(['id' => 'wpp-maintenance', 'title' => 'WP Panel: …', 'href' => '#wpp-maintenance-dialog']);
    }

    public static function maintenance_assets() {
        if (!current_user_can('manage_options') || is_multisite()) return;
        $base = plugin_dir_url(WPP_OPTIMIZER_PLUGIN_FILE);
        wp_enqueue_style('wpp-maintenance', $base . 'assets/maintenance.css', [], self::VERSION);
        wp_enqueue_script('wpp-maintenance', $base . 'assets/maintenance.js', [], self::VERSION, true);
        $zh = strpos(determine_locale(), 'zh') === 0;
        wp_localize_script('wpp-maintenance', 'WPPMaintenance', [
            'url' => admin_url('admin-ajax.php'), 'nonce' => wp_create_nonce('wpp_maintenance'),
            'text' => $zh ? [
                'locked'=>'已锁定', 'unlocked'=>'已解锁', 'unlocked_permanent'=>'未开启文件锁',
                'unlocking'=>'正在解锁', 'relocking'=>'正在重新锁定', 'relock_failed'=>'重新锁定失败，请联系管理员',
                'unknown'=>'状态未知', 'unlock'=>'临时解锁', 'relock'=>'立即重新锁定', 'close'=>'关闭',
                'password'=>'维护密码', 'extend'=>'增加', 'minute'=>'分钟', 'busy'=>'正在处理…',
                'warning'=>'更新未完成请提前加时，完成后立即锁定。到期或面板重启会回锁，可能打断更新。',
                'restart'=>'面板重启，维护窗口已提前结束。继续维护请重新输入密码申请解锁。',
                'disabled'=>'请联系面板所有者开启临时维护。', 'password_required'=>'请输入维护密码；加时跨 30 分钟区间时须重新验证。',
                'verification_failed'=>'验证失败', 'operation_unavailable'=>'操作暂不可用，请刷新状态或联系管理员。',
                'lock_mode_required'=>'请面板所有者先重新应用标准或严格文件锁，再开启临时维护。',
                'state_unknown'=>'状态未知，请刷新状态或联系管理员。', 'invalid_request'=>'请求无效',
            ] : [
                'locked'=>'Locked', 'unlocked'=>'Unlocked', 'unlocked_permanent'=>'File lock disabled',
                'unlocking'=>'Unlocking', 'relocking'=>'Relocking', 'relock_failed'=>'Relock failed — contact administrator',
                'unknown'=>'State unknown', 'unlock'=>'Temporary unlock', 'relock'=>'Relock now', 'close'=>'Close',
                'password'=>'Maintenance password', 'extend'=>'Add', 'minute'=>'minutes', 'busy'=>'Processing…',
                'warning'=>'Extend before expiry if the update is unfinished. Relocking at expiry or panel restart may interrupt updates.',
                'restart'=>'Panel restart ended the maintenance window early. Enter the password again to start a new window.',
                'disabled'=>'Ask the panel owner to enable maintenance.', 'password_required'=>'Enter the maintenance password; verification is required again across each 30-minute boundary.',
                'verification_failed'=>'Verification failed', 'operation_unavailable'=>'Operation unavailable. Refresh or contact the administrator.',
                'lock_mode_required'=>'Ask the panel owner to apply Standard or Strict file lock before enabling maintenance.',
                'state_unknown'=>'State unknown. Refresh or contact the administrator.', 'invalid_request'=>'Invalid request',
            ],
        ]);
    }

    public static function maintenance_dialog() {
        if (!current_user_can('manage_options') || is_multisite()) return;
        echo '<dialog id="wpp-maintenance-dialog" aria-labelledby="wpp-maintenance-title"><h2 id="wpp-maintenance-title">WP Panel</h2><p id="wpp-maintenance-state" role="status"></p><p id="wpp-maintenance-warning"></p><form id="wpp-maintenance-form"><label id="wpp-maintenance-password-label" for="wpp-maintenance-password"></label><input id="wpp-maintenance-password" type="password" autocomplete="off" maxlength="72"><div id="wpp-maintenance-actions"></div></form><p id="wpp-maintenance-message" role="alert"></p><button type="button" id="wpp-maintenance-close"></button></dialog>';
    }

    public static function maintenance_ajax() {
        if (!current_user_can('manage_options') || is_multisite()) wp_send_json_error(['message'=>'verification_failed'], 403);
        check_ajax_referer('wpp_maintenance', 'nonce');
        $op = isset($_POST['operation']) && is_string($_POST['operation']) ? sanitize_key(wp_unslash($_POST['operation'])) : '';
        if (!in_array($op, ['status','unlock','extend','relock'], true)) wp_send_json_error(['message'=>'invalid_request'], 400);
        $url = rtrim((string) self::get_panel_url(), '/');
        // This password-bearing path is loopback-only and never follows a redirect.
        if (!preg_match('~^https://(?:127\.0\.0\.1|\[::1\]):[0-9]+/[A-Za-z0-9_-]+$~D', $url)) wp_send_json_error(['message'=>'state_unknown'], 503);
        $body = [];
        if ($op !== 'status') {
            foreach (['window_id','request_id','password'] as $key) {
                if (isset($_POST[$key]) && !is_string($_POST[$key])) wp_send_json_error(['message'=>'invalid_request'], 400);
                $body[$key] = isset($_POST[$key]) ? wp_unslash($_POST[$key]) : '';
            }
            if (strlen($body['password']) > 72 || strlen($body['window_id']) > 36 || strlen($body['request_id']) > 36) wp_send_json_error(['message'=>'invalid_request'], 400);
            foreach (['minutes', 'revision'] as $key) {
                if (isset($_POST[$key]) && !is_scalar($_POST[$key])) wp_send_json_error(['message'=>'invalid_request'], 400);
            }
            $body['minutes'] = isset($_POST['minutes']) ? absint($_POST['minutes']) : 0;
            $body['revision'] = isset($_POST['revision']) ? absint($_POST['revision']) : 0;
            $body['actor'] = (string) get_current_user_id(); // Site-asserted, not panel-verified.
        }
        $args = ['method'=>$op === 'status' ? 'GET' : 'POST', 'timeout'=>20, 'redirection'=>0, 'sslverify'=>false,
            'headers'=>['X-WP-Panel-Key'=>self::get_api_key(), 'Content-Type'=>'application/json']];
        if ($op !== 'status') $args['body'] = wp_json_encode($body);
        $response = wp_remote_request($url . '/api/sites/maintenance' . ($op === 'status' ? '' : '/' . $op), $args);
        unset($body, $args);
        if (is_wp_error($response)) wp_send_json_error(['message'=>'state_unknown'], 503);
        $data = json_decode(wp_remote_retrieve_body($response), true);
        if (!is_array($data) || empty($data['success'])) {
            $allowed = ['password_required','verification_failed','operation_unavailable','state_unknown','invalid_request','lock_mode_required'];
            $code = isset($data['message']) && in_array($data['message'], $allowed, true) ? $data['message'] : 'state_unknown';
            wp_send_json_error(['message'=>$code], 409);
        }
        if (isset($data['data']['state'])) {
            // Refresh the pre-existing optimization guard after a maintenance action.
            self::update_file_lock_state_option(['file_lock_enabled'=>$data['data']['state'] !== 'unlocked' && $data['data']['state'] !== 'unlocked_permanent']);
            delete_transient(self::FILE_LOCK_STATE_TRANSIENT);
        }
        wp_send_json_success($data['data']);
    }
}
