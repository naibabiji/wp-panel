<?php
/**
 * WP Panel Optimizer — 缓存与预加载模块
 *
 * FastCGI 缓存清除（手动/自动）、缓存预加载队列与批处理、
 * 管理栏清缓存按钮、缓存/预加载相关的后台通知。
 */

if (!defined('ABSPATH')) exit;

trait WPP_Optimizer_Cache_Trait {

    public static function clear_notice() {
        if (isset($_GET['wpp_cleared'])) {
            if (!isset($_GET['_wpnonce']) || !wp_verify_nonce(sanitize_text_field(wp_unslash($_GET['_wpnonce'])), 'wpp_clear_notice')) return;
            if ($_GET['wpp_cleared'] === '1') {
                echo '<div class="notice notice-success is-dismissible"><p>Nginx 缓存已清除，旧页面将在几分钟内更新。</p></div>';
            } else {
                echo '<div class="notice notice-error is-dismissible"><p>清除缓存失败，请检查面板连接是否正常。</p></div>';
            }
        }

        if (isset($_GET['wpp_preload'])) {
            if (!isset($_GET['_wpnonce']) || !wp_verify_nonce(sanitize_text_field(wp_unslash($_GET['_wpnonce'])), 'wpp_preload_notice')) return;
            $state = sanitize_key(wp_unslash($_GET['wpp_preload']));
            $count = isset($_GET['count']) ? intval($_GET['count']) : 0;
            if ($state === 'queued') {
                echo '<div class="notice notice-success is-dismissible"><p>缓存预加载已加入队列，共 ' . esc_html($count) . ' 个 URL。</p></div>';
            } elseif ($state === 'stopped') {
                echo '<div class="notice notice-warning is-dismissible"><p>缓存预加载已停止，当前队列已清空。</p></div>';
            } else {
                echo '<div class="notice notice-error is-dismissible"><p>缓存预加载启动失败，请确认 FastCGI 缓存已开启。</p></div>';
            }
        }
    }

    public static function admin_bar_button($bar) {
        if (!current_user_can('manage_options')) return;
        if (!self::get_panel_url()) return;
        $bar->add_node([
            'id'    => 'wpp-clear-cache',
            'title' => '清除 Nginx 缓存',
            'href'  => wp_nonce_url(admin_url('admin-post.php?action=wpp_cache_clear'), 'wpp_cache_clear'),
        ]);
    }

    public static function handle_clear() {
        if (!current_user_can('manage_options')) return;
        check_admin_referer('wpp_cache_clear');
        $resp = self::do_clear();
        $success = !empty($resp['success']);
        self::log_clear('manual', $success);
        if ($success) {
            self::maybe_queue_preload(self::build_full_preload_urls(), 'manual_clear');
        }
        wp_safe_redirect(add_query_arg(['wpp_cleared' => $success ? '1' : '0', '_wpnonce' => wp_create_nonce('wpp_clear_notice')], wp_get_referer() ?: admin_url()));
        exit;
    }

    public static function handle_preload() {
        if (!current_user_can('manage_options')) return;
        check_admin_referer('wpp_cache_preload');

        if (get_option(self::OPTION_FCACHE_ENABLED, '0') !== '1') {
            self::redirect_preload_notice('failed', 0);
        }

        $count = self::queue_preload(self::build_full_preload_urls(), 'manual');
        if ($count > 0) {
            self::process_preload_batch();
        }
        self::redirect_preload_notice($count > 0 ? 'queued' : 'failed', $count);
    }

    public static function handle_preload_stop() {
        if (!current_user_can('manage_options')) return;
        check_admin_referer('wpp_cache_preload_stop');

        wp_clear_scheduled_hook(self::PRELOAD_HOOK);
        delete_option(self::OPTION_PRELOAD_QUEUE);
        $status = self::get_preload_status();
        $status['running'] = false;
        $status['queued'] = 0;
        $status['finished_at'] = current_time('Y-m-d H:i:s');
        $status['last_message'] = '已手动停止';
        update_option(self::OPTION_PRELOAD_STATUS, $status, false);
        self::redirect_preload_notice('stopped', 0);
    }

    public static function auto_clear($post_id) {
        if (wp_is_post_revision($post_id) || wp_is_post_autosave($post_id)) return;
        $post = get_post($post_id);
        if (!$post || in_array($post->post_status, ['draft', 'auto-draft', 'inherit'])) return;
        if (!in_array($post->post_status, ['publish', 'trash', 'future', 'private'])) return;

        $pt = get_post_type_object($post->post_type);
        if (!$pt || !$pt->public) return;

        if (get_transient('wpp_auto_clearing')) return;
        set_transient('wpp_auto_clearing', 1, 5);

        $resp = self::do_clear();
        $success = !empty($resp['success']);
        self::log_clear('auto', $success);
        if ($success) {
            self::maybe_queue_preload(self::build_related_preload_urls($post_id), 'content_change');
        }
    }

    public static function auto_comment_clear($_) {
        if (get_transient('wpp_comment_clearing')) return;
        set_transient('wpp_comment_clearing', 1, 5);

        $resp = self::do_clear();
        $success = !empty($resp['success']);
        self::log_clear('comment', $success);
        if ($success) {
            self::maybe_queue_preload([home_url('/')], 'comment_change');
        }
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

    private static function redirect_preload_notice($state, $count) {
        wp_safe_redirect(add_query_arg([
            'wpp_preload' => $state,
            'count'       => max(0, intval($count)),
            '_wpnonce'    => wp_create_nonce('wpp_preload_notice'),
        ], wp_get_referer() ?: admin_url('options-general.php?page=wp-panel-optimizer')));
        exit;
    }

    private static function normalize_preload_limit($limit) {
        $limit = intval($limit);
        if ($limit < 10) {
            return 100;
        }
        if ($limit > 500) {
            return 500;
        }
        return $limit;
    }

    private static function get_preload_limit() {
        return self::normalize_preload_limit(get_option(self::OPTION_PRELOAD_LIMIT, 100));
    }

    private static function get_preload_status() {
        $status = get_option(self::OPTION_PRELOAD_STATUS, []);
        if (!is_array($status)) {
            $status = [];
        }
        return array_merge([
            'running'      => false,
            'queued'       => 0,
            'done'         => 0,
            'failed'       => 0,
            'reason'       => '',
            'started_at'   => '',
            'last_run_at'  => '',
            'finished_at'  => '',
            'last_message' => '',
        ], $status);
    }

    private static function maybe_queue_preload($urls, $reason) {
        if (get_option(self::OPTION_PRELOAD_ENABLED, '0') !== '1') {
            return 0;
        }
        if (get_option(self::OPTION_FCACHE_ENABLED, '0') !== '1') {
            return 0;
        }
        return self::queue_preload($urls, $reason);
    }

    private static function queue_preload($urls, $reason) {
        $urls = self::filter_preload_urls($urls, self::get_preload_limit());
        if (empty($urls)) {
            return 0;
        }

        $queue = get_option(self::OPTION_PRELOAD_QUEUE, []);
        if (!is_array($queue)) {
            $queue = [];
        }
        $queue = self::filter_preload_urls(array_merge($queue, $urls), self::get_preload_limit());

        $status = self::get_preload_status();
        if (empty($status['running'])) {
            $status['done'] = 0;
            $status['failed'] = 0;
            $status['started_at'] = current_time('Y-m-d H:i:s');
            $status['finished_at'] = '';
        }
        $status['running'] = true;
        $status['queued'] = count($queue);
        $status['reason'] = sanitize_key($reason);
        $status['last_message'] = '等待后台批量预加载';

        update_option(self::OPTION_PRELOAD_QUEUE, array_values($queue), false);
        update_option(self::OPTION_PRELOAD_STATUS, $status, false);

        if (!wp_next_scheduled(self::PRELOAD_HOOK)) {
            wp_schedule_single_event(time() + 60, self::PRELOAD_HOOK);
        }
        return count($queue);
    }

    public static function maybe_process_preload_tick() {
        $queue = get_option(self::OPTION_PRELOAD_QUEUE, []);
        $status = self::get_preload_status();
        if (empty($status['running']) || empty($queue) || !is_array($queue)) {
            return;
        }
        if (get_transient('wpp_optimizer_preload_tick')) {
            return;
        }
        set_transient('wpp_optimizer_preload_tick', 1, self::PRELOAD_TICK_THROTTLE);
        self::process_preload_batch();
    }

    public static function process_preload_batch() {
        if (get_transient('wpp_optimizer_preload_lock')) {
            return;
        }
        set_transient('wpp_optimizer_preload_lock', 1, 60);

        $queue = get_option(self::OPTION_PRELOAD_QUEUE, []);
        if (!is_array($queue)) {
            $queue = [];
        }
        $status = self::get_preload_status();

        if (empty($queue)) {
            $status['running'] = false;
            $status['queued'] = 0;
            $status['finished_at'] = current_time('Y-m-d H:i:s');
            $status['last_message'] = '预加载队列为空';
            update_option(self::OPTION_PRELOAD_STATUS, $status, false);
            delete_transient('wpp_optimizer_preload_lock');
            return;
        }

        $status['last_run_at'] = current_time('Y-m-d H:i:s');
        $batch = array_splice($queue, 0, self::PRELOAD_BATCH_SIZE);
        foreach ($batch as $url) {
            if (!self::is_preload_url_allowed($url)) {
                $status['failed']++;
                continue;
            }
            $resp = wp_remote_get($url, [
                'timeout'     => 8,
                'redirection' => 3,
                'reject_unsafe_urls' => true,
                'headers'     => [
                    'User-Agent' => 'WP Panel Optimizer Preload/' . self::VERSION,
                    'Accept'     => 'text/html,application/xhtml+xml',
                ],
                'cookies'     => [],
            ]);
            if (is_wp_error($resp)) {
                $status['failed']++;
                continue;
            }
            $code = intval(wp_remote_retrieve_response_code($resp));
            if ($code >= 200 && $code < 400) {
                $status['done']++;
            } else {
                $status['failed']++;
            }
        }

        $status['queued'] = count($queue);
        if (!empty($queue)) {
            $status['running'] = true;
            $status['last_message'] = '预加载进行中';
            update_option(self::OPTION_PRELOAD_QUEUE, array_values($queue), false);
            update_option(self::OPTION_PRELOAD_STATUS, $status, false);
            if (!wp_next_scheduled(self::PRELOAD_HOOK)) {
                wp_schedule_single_event(time() + 60, self::PRELOAD_HOOK);
            }
        } else {
            delete_option(self::OPTION_PRELOAD_QUEUE);
            $status['running'] = false;
            $status['finished_at'] = current_time('Y-m-d H:i:s');
            $status['last_message'] = '预加载完成';
            update_option(self::OPTION_PRELOAD_STATUS, $status, false);
        }

        delete_transient('wpp_optimizer_preload_lock');
    }

    private static function build_full_preload_urls() {
        $limit = self::get_preload_limit();
        $urls = [home_url('/')];

        $postTypes = get_post_types(['public' => true], 'names');
        unset($postTypes['attachment']);
        if (!empty($postTypes)) {
            $posts = get_posts([
                'post_type'      => array_values($postTypes),
                'post_status'    => 'publish',
                'posts_per_page' => $limit,
                'orderby'        => 'modified',
                'order'          => 'DESC',
                'no_found_rows'  => true,
                'fields'         => 'ids',
            ]);
            foreach ($posts as $postID) {
                $urls[] = get_permalink($postID);
                if (count($urls) >= $limit) {
                    break;
                }
            }
        }

        if (count($urls) < $limit) {
            $taxonomies = get_taxonomies(['public' => true], 'names');
            foreach ($taxonomies as $taxonomy) {
                $terms = get_terms([
                    'taxonomy'   => $taxonomy,
                    'hide_empty' => true,
                    'number'     => max(1, $limit - count($urls)),
                ]);
                if (is_wp_error($terms)) {
                    continue;
                }
                foreach ($terms as $term) {
                    $link = get_term_link($term);
                    if (!is_wp_error($link)) {
                        $urls[] = $link;
                    }
                    if (count($urls) >= $limit) {
                        break 2;
                    }
                }
            }
        }

        return self::filter_preload_urls($urls, $limit);
    }

    private static function build_related_preload_urls($postID) {
        $urls = [home_url('/')];
        $permalink = get_permalink($postID);
        if ($permalink) {
            $urls[] = $permalink;
        }

        $postType = get_post_type($postID);
        if ($postType && get_post_type_archive_link($postType)) {
            $urls[] = get_post_type_archive_link($postType);
        }

        if ($postType) {
            $taxonomies = get_object_taxonomies($postType, 'names');
            foreach ($taxonomies as $taxonomy) {
                $terms = wp_get_post_terms($postID, $taxonomy);
                if (is_wp_error($terms)) {
                    continue;
                }
                foreach ($terms as $term) {
                    $link = get_term_link($term);
                    if (!is_wp_error($link)) {
                        $urls[] = $link;
                    }
                }
            }
        }

        return self::filter_preload_urls($urls, 20);
    }

    private static function filter_preload_urls($urls, $limit) {
        $clean = [];
        $seen = [];
        foreach ((array) $urls as $url) {
            $url = esc_url_raw($url);
            if (!$url || !self::is_preload_url_allowed($url)) {
                continue;
            }
            $key = rtrim($url, '/');
            if (isset($seen[$key])) {
                continue;
            }
            $seen[$key] = true;
            $clean[] = $url;
            if (count($clean) >= $limit) {
                break;
            }
        }
        return $clean;
    }

    private static function is_preload_url_allowed($url) {
        $homeHost = strtolower((string) wp_parse_url(home_url('/'), PHP_URL_HOST));
        $host = strtolower((string) wp_parse_url($url, PHP_URL_HOST));
        $scheme = strtolower((string) wp_parse_url($url, PHP_URL_SCHEME));
        $path = (string) wp_parse_url($url, PHP_URL_PATH);
        $query = wp_parse_url($url, PHP_URL_QUERY);

        if (!$homeHost || !$host || $host !== $homeHost) {
            return false;
        }
        if ($scheme !== 'http' && $scheme !== 'https') {
            return false;
        }
        if ($query !== null && $query !== '') {
            return false;
        }

        $path = '/' . ltrim($path, '/');
        $excluded = [
            '#^/wp-admin(/|$)#i',
            '#^/wp-login\.php$#i',
            '#^/wp-json(/|$)#i',
            '#^/xmlrpc\.php$#i',
            '#^/wp-cron\.php$#i',
            '#/cart(/|$)#i',
            '#/checkout(/|$)#i',
            '#/my-account(/|$)#i',
            '#/feed(/|$)#i',
            '#/page/[0-9]+/?$#i',
        ];
        foreach ($excluded as $pattern) {
            if (preg_match($pattern, $path)) {
                return false;
            }
        }

        return true;
    }

}
