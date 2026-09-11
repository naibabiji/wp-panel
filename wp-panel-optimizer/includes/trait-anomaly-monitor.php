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
                'email_hash'=>hash_hmac('sha256', strtolower(trim($user->user_email)), wp_salt('auth')),
                'display_hash'=>hash_hmac('sha256', (string)$user->display_name, wp_salt('auth')),
                'credential_hash'=>hash_hmac('sha256', (string)$user->user_pass, wp_salt('auth'))];
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
            "SELECT COUNT(*) FROM {$wpdb->posts} WHERE post_type IN ('post','page') AND post_status = 'publish' AND post_date_gmt > %s AND post_date_gmt <= %s",
            gmdate('Y-m-d H:i:s', max($since, $until - DAY_IN_SECONDS)), gmdate('Y-m-d H:i:s', $until)
        ));
        if ($wpdb->last_error !== '' || $count === null) return ['error'=>'sample_failed'];
        $content = [];
        $last_id = 0;
        do {
            $rows = $wpdb->get_results($wpdb->prepare(
                "SELECT ID,post_type,post_status,post_title,post_content,post_excerpt,post_name FROM {$wpdb->posts} WHERE post_type IN ('post','page') AND post_status='publish' AND ID > %d ORDER BY ID ASC LIMIT 251",
                $last_id
            ), ARRAY_A);
            if ($wpdb->last_error !== '' || !is_array($rows)) return ['error'=>'sample_failed'];
            foreach ($rows as $row) {
                $encoded = wp_json_encode([
                    (int)$row['ID'], (string)$row['post_type'], (string)$row['post_status'],
                    (string)$row['post_title'], (string)$row['post_content'],
                    (string)$row['post_excerpt'], (string)$row['post_name'],
                ]);
                if ($encoded === false) return ['error'=>'sample_failed'];
                $content[] = [
                    'id'=>(int)$row['ID'],
                    'type'=>(string)$row['post_type'],
                    'fingerprint'=>hash_hmac('sha256', $encoded, wp_salt('auth')),
                ];
                $last_id = (int)$row['ID'];
                if (count($content) > 5000) return ['error'=>'sample_failed'];
            }
        } while (count($rows) === 251);
        return [
            'version'=>2,
            'admins'=>$admins,
            'removed'=>$removed,
            'post_count'=>(int)$count,
            'content'=>$content,
            'options'=>[
                'siteurl'=>(string)get_option('siteurl', ''),
                'home'=>(string)get_option('home', ''),
                'users_can_register'=>(bool)get_option('users_can_register', false),
                'default_role'=>(string)get_option('default_role', 'subscriber'),
                'front_page_id'=>(int)get_option('page_on_front', 0),
            ],
        ];
    }
}
