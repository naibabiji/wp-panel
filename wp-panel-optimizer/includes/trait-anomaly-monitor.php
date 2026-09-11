<?php
/** Read-only sampling, called only by the panel's site-user CLI runner. */
if (!defined('ABSPATH')) exit;

trait WPP_Optimizer_Anomaly_Monitor_Trait {
    public static function collect_anomaly_sample($since, $until, $known_ids) {
        if (PHP_SAPI !== 'cli' || !defined('WP_PANEL_INVENTORY_RUNNER') || !WP_PANEL_INVENTORY_RUNNER) {
            throw new RuntimeException('runner_required');
        }
        if (is_multisite()) return ['error'=>'multisite_unsupported'];
        if (!is_int($since) || !is_int($until) || $since < 0 || $until < $since || count($known_ids) > 100) {
            return ['error'=>'sample_invalid'];
        }
        global $wpdb;
        $users = get_users(['role'=>'administrator', 'number'=>101, 'orderby'=>'ID', 'order'=>'ASC']);
        if ($wpdb->last_error !== '' || count($users) > 100) return ['error'=>'sample_failed'];
        $admins = [];
        $current = [];
        foreach ($users as $user) {
            $roles = array_values($user->roles);
            sort($roles, SORT_STRING);
            $admins[] = ['id'=>(int)$user->ID, 'login'=>$user->user_login, 'roles'=>$roles,
                'email_hash'=>hash_hmac('sha256', strtolower(trim($user->user_email)), wp_salt('auth'))];
            $current[(int)$user->ID] = true;
        }
        // Only former administrators are looked up: no full subscriber inventory.
        $removed = [];
        foreach ($known_ids as $id) {
            if (!is_int($id) || $id < 1) return ['error'=>'sample_invalid'];
            if (isset($current[$id])) continue;
            $user = get_userdata($id);
            if ($wpdb->last_error !== '') return ['error'=>'sample_failed'];
            $removed[] = ['id'=>$id, 'deleted'=>$user === false];
        }
        $count = $wpdb->get_var($wpdb->prepare(
            "SELECT COUNT(*) FROM {$wpdb->posts} WHERE post_type = 'post' AND post_status = 'publish' AND post_date_gmt > %s AND post_date_gmt <= %s",
            gmdate('Y-m-d H:i:s', max($since, $until - DAY_IN_SECONDS)), gmdate('Y-m-d H:i:s', $until)
        ));
        if ($wpdb->last_error !== '' || $count === null) return ['error'=>'sample_failed'];
        return ['admins'=>$admins, 'removed'=>$removed, 'post_count'=>(int)$count];
    }
}
