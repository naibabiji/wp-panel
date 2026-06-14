<?php
/**
 * Plugin Name: WP Panel Optimizer
 * Plugin URI:  https://github.com/naibabiji/wp-panel
 * Description: 与 WP Panel 面板配合，管理 FastCGI 缓存、预加载、调试模式、文章修订、内存限制等优化项。发布/更新文章自动清除缓存。
 * Version:     1.1.3
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
    delete_option('wpp_optimizer_preload_enabled');
    delete_option('wpp_optimizer_preload_limit');
    delete_option('wpp_optimizer_preload_queue');
    delete_option('wpp_optimizer_preload_status');
    wp_clear_scheduled_hook('wpp_optimizer_preload_batch');
}

class WP_Panel_Optimizer {

    const VERSION = '1.1.3';

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
    const OPTION_PRELOAD_ENABLED = 'wpp_optimizer_preload_enabled';
    const OPTION_PRELOAD_LIMIT   = 'wpp_optimizer_preload_limit';
    const OPTION_PRELOAD_QUEUE   = 'wpp_optimizer_preload_queue';
    const OPTION_PRELOAD_STATUS  = 'wpp_optimizer_preload_status';
    const PRELOAD_HOOK           = 'wpp_optimizer_preload_batch';

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

    public static function init() {
        add_action('admin_bar_menu', [__CLASS__, 'admin_bar_button'], 100);
        add_action('admin_menu', [__CLASS__, 'settings_page']);
        add_action('admin_post_wpp_cache_clear', [__CLASS__, 'handle_clear']);
        add_action('admin_post_wpp_cache_preload', [__CLASS__, 'handle_preload']);
        add_action('admin_post_wpp_cache_preload_stop', [__CLASS__, 'handle_preload_stop']);
        add_action('save_post', [__CLASS__, 'auto_clear'], 99, 1);
        add_action('deleted_post', [__CLASS__, 'auto_clear'], 99, 1);
        add_action('wp_update_comment_count', [__CLASS__, 'auto_comment_clear']);
        add_filter('plugin_action_links_' . plugin_basename(__FILE__), [__CLASS__, 'action_links']);
        add_action('admin_notices', [__CLASS__, 'clear_notice']);
        add_action('wp_ajax_wpp_optimizer_check_update', [__CLASS__, 'ajax_check_update']);
        add_action(self::PRELOAD_HOOK, [__CLASS__, 'process_preload_batch']);

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

        $isPost = isset($_POST['wpp_save']);
        $notice = '';

        // 面板同步：GET 时从面板拉取最新状态，POST 时不拉（避免用旧值覆盖表单新值）
        if (!$isPost) {
            $panelState = self::fetch_panel_state();
            if ($panelState) {
                update_option(self::OPTION_FCACHE_ENABLED, !empty($panelState['fastcgi_cache_enabled']) ? '1' : '0');
                update_option(self::OPTION_FCACHE_TTL, intval($panelState['fastcgi_cache_ttl'] ?? 300));
                update_option(self::OPTION_NO_UPDATES, !empty($panelState['disable_wp_updates']) ? '1' : '0');
                update_option(self::OPTION_NO_FILE_EDIT, !empty($panelState['disable_file_editing']) ? '1' : '0');
                update_option(self::OPTION_XMLRPC_ENABLED, !empty($panelState['xmlrpc_enabled']) ? '1' : '0');
                update_option(self::OPTION_WP_DEBUG, !empty($panelState['wp_debug_enabled']) ? '1' : '0');
                update_option(self::OPTION_POST_REVISIONS, $panelState['wp_post_revisions'] ?? -1);
                update_option(self::OPTION_MEMORY_LIMIT, $panelState['wp_memory_limit'] ?? '');
            }
        }

        if ($isPost) {
            check_admin_referer('wpp_optimizer_settings');
            $fcacheEnabled  = !empty($_POST['fcache_enabled'])  ? true : false;
            $fcacheTTL      = isset($_POST['fcache_ttl']) ? intval($_POST['fcache_ttl']) : 300;
            $noUpdates      = !empty($_POST['no_updates'])      ? true : false;
            $noFileEdit     = !empty($_POST['no_file_edit'])    ? true : false;
            $wpDebug        = !empty($_POST['wp_debug'])        ? true : false;
            $postRevisions  = (isset($_POST['post_revisions']) && $_POST['post_revisions'] !== '') ? intval($_POST['post_revisions']) : -1;
            $memoryLimit    = isset($_POST['memory_limit']) ? sanitize_text_field($_POST['memory_limit']) : '';
            $preloadEnabled = !empty($_POST['preload_enabled']) ? true : false;
            $preloadLimit   = isset($_POST['preload_limit']) ? intval(wp_unslash($_POST['preload_limit'])) : 100;

            if ($fcacheTTL < 10)  $fcacheTTL = 300;
            if ($fcacheTTL > 86400) $fcacheTTL = 86400;
            $preloadLimit = self::normalize_preload_limit($preloadLimit);

            update_option(self::OPTION_FCACHE_ENABLED, $fcacheEnabled ? '1' : '0');
            update_option(self::OPTION_FCACHE_TTL, $fcacheTTL);
            update_option(self::OPTION_NO_UPDATES, $noUpdates ? '1' : '0');
            update_option(self::OPTION_NO_FILE_EDIT, $noFileEdit ? '1' : '0');
            update_option(self::OPTION_WP_DEBUG, $wpDebug ? '1' : '0');
            update_option(self::OPTION_POST_REVISIONS, $postRevisions);
            update_option(self::OPTION_MEMORY_LIMIT, $memoryLimit);
            update_option(self::OPTION_PRELOAD_ENABLED, $preloadEnabled ? '1' : '0');
            update_option(self::OPTION_PRELOAD_LIMIT, $preloadLimit);

            $pushed = self::push_optimizer_settings($fcacheEnabled, $fcacheTTL, $noUpdates, $noFileEdit, $wpDebug, $postRevisions, $memoryLimit);
            if ($pushed === true) {
                $notice = '<div class="notice notice-success"><p>设置已保存，已同步到面板。</p></div>';
            } else {
                $errMsg = is_wp_error($pushed) ? $pushed->get_error_message() : '未知错误';
                $notice = '<div class="notice notice-warning is-dismissible"><p><strong>注意：</strong>设置已保存在本地，但同步到面板失败。错误信息：<code>' . esc_html($errMsg) . '</code></p><p>下次进入本页面时将从面板拉取状态，可能覆盖本次修改。请检查插件设置中的「验证连接」是否正常。</p></div>';
            }
        }

        $fcacheEnabled  = get_option(self::OPTION_FCACHE_ENABLED, '0') === '1';
        $fcacheTTL      = get_option(self::OPTION_FCACHE_TTL, '300');
        $noUpdates      = get_option(self::OPTION_NO_UPDATES, '0') === '1';
        $noFileEdit     = get_option(self::OPTION_NO_FILE_EDIT, '0') === '1';
        $wpDebug        = get_option(self::OPTION_WP_DEBUG, '0') === '1';
        $postRevisions  = intval(get_option(self::OPTION_POST_REVISIONS, '-1'));
        $memoryLimit    = get_option(self::OPTION_MEMORY_LIMIT, '');
        $log            = get_option(self::OPTION_LOG, []);
        $preloadEnabled = get_option(self::OPTION_PRELOAD_ENABLED, '0') === '1';
        $preloadLimit   = self::normalize_preload_limit(get_option(self::OPTION_PRELOAD_LIMIT, 100));
        $preloadStatus  = self::get_preload_status();
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
                        <th><label for="wpp-preload-enabled">缓存预加载</label></th>
                        <td>
                            <label><input id="wpp-preload-enabled" name="preload_enabled" type="checkbox" value="1" <?php checked($preloadEnabled); ?>> 清除缓存后自动预加载</label>
                            <p class="description">插件会以未登录访客身份访问本站公开页面，让 Nginx 自然生成 FastCGI 缓存文件。默认低速批处理，避免压垮小服务器。</p>
                        </td>
                    </tr>
                    <tr>
                        <th><label for="wpp-preload-limit">单次最多预加载 URL</label></th>
                        <td>
                            <input id="wpp-preload-limit" name="preload_limit" type="number" class="small-text" value="<?php echo esc_attr($preloadLimit); ?>" min="10" max="500">
                            <p class="description">范围 10-500。首页优先，其次为最近更新的公开文章、页面和公开分类归档。</p>
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
                    <tr>
                        <th><label for="wpp-wp-debug">启用调试模式</label></th>
                        <td>
                            <label><input id="wpp-wp-debug" name="wp_debug" type="checkbox" value="1" <?php checked($wpDebug); ?>> 开启 <code>WP_DEBUG</code></label>
                            <p class="description">开启后 PHP 错误和警告将写入 <code>wp-content/debug.log</code>，并开启 <code>WP_DEBUG_LOG</code>、关闭 <code>WP_DEBUG_DISPLAY</code>（错误不显示在页面，仅记录日志）。<br>用于排查网站白屏、500 错误等问题，正常使用时请关闭以免日志文件持续增长。</p>
                        </td>
                    </tr>
                    <tr>
                        <th><label for="wpp-post-revisions">文章修订版本数</label></th>
                        <td>
                            <input id="wpp-post-revisions" name="post_revisions" type="number" class="small-text" value="<?php echo esc_attr($postRevisions >= 0 ? $postRevisions : ''); ?>" min="-1" placeholder="默认">
                            <p class="description">留空 = WordPress 默认（无限制），<strong>0 = 完全不保留修订</strong>，设置为 3~5 可有效减少数据库占用。<br>每保存一次文章就会生成一个修订版本，长期不清理会占用大量数据库空间。</p>
                        </td>
                    </tr>
                    <tr>
                        <th><label for="wpp-memory-limit">PHP 内存限制</label></th>
                        <td>
                            <input id="wpp-memory-limit" name="memory_limit" type="text" class="regular-text" value="<?php echo esc_attr($memoryLimit); ?>" placeholder="默认 40M">
                            <p class="description">设置 <code>WP_MEMORY_LIMIT</code>，如 <code>128M</code>、<code>256M</code>。留空使用 WordPress 默认值（40M）。<br>遇到"Allowed memory size exhausted"错误、后台白屏时可适当调高。注意不要超过服务器总内存。</p>
                        </td>
                    </tr>
                    <?php $xmlrpcEnabled = get_option('wpp_optimizer_xmlrpc_enabled', '0') === '1'; ?>
                    <tr>
                        <th>XML-RPC 接口</th>
                        <td>
                            <span style="font-weight:bold;color:<?php echo $xmlrpcEnabled ? '#00a32a' : '#d63638'; ?>"><?php echo $xmlrpcEnabled ? '已开启' : '已关闭'; ?></span>
                            <p class="description">
                                XML-RPC 是 WordPress 远程通信接口。关闭后 Nginx 直接返回 403，请求不到 PHP-FPM，可彻底防御 xmlrpc.php 暴力攻击。<br>
                                影响：<strong>无法使用 Jetpack、WordPress 手机 App、pingback/trackback、第三方通过 XML-RPC 发布文章</strong>。绝大多数站点不需要此功能。<br>
                                如需开启或关闭，请在 WP Panel 面板中打开网站详情页 → WordPress 优化 →「允许 XML-RPC 接口」开关。<br>
                            </p>
                        </td>
                    </tr>
                </table>
                <p>
                    <button type="submit" name="wpp_save" class="button button-primary">保存设置</button>
                    <button type="button" id="wpp-verify-btn" class="button">验证连接</button>
                </p>
            </form>

            <hr>
            <h2>缓存预加载</h2>
            <p>当前状态：<strong><?php echo esc_html($preloadStatus['running'] ? '运行中' : '空闲'); ?></strong>
                <?php if (!empty($preloadStatus['last_message'])): ?>
                    <span class="description"><?php echo esc_html($preloadStatus['last_message']); ?></span>
                <?php endif; ?>
            </p>
            <p class="description">
                队列：<?php echo intval($preloadStatus['queued']); ?>，
                成功：<?php echo intval($preloadStatus['done']); ?>，
                失败：<?php echo intval($preloadStatus['failed']); ?>
                <?php if (!empty($preloadStatus['started_at'])): ?>
                    ，开始：<?php echo esc_html($preloadStatus['started_at']); ?>
                <?php endif; ?>
                <?php if (!empty($preloadStatus['finished_at'])): ?>
                    ，结束：<?php echo esc_html($preloadStatus['finished_at']); ?>
                <?php endif; ?>
            </p>
            <form method="post" action="<?php echo esc_url(admin_url('admin-post.php')); ?>" style="display:inline-block;margin-right:8px;">
                <?php wp_nonce_field('wpp_cache_preload'); ?>
                <input type="hidden" name="action" value="wpp_cache_preload">
                <button type="submit" class="button" <?php disabled(!$fcacheEnabled); ?>>立即预加载</button>
            </form>
            <form method="post" action="<?php echo esc_url(admin_url('admin-post.php')); ?>" style="display:inline-block;">
                <?php wp_nonce_field('wpp_cache_preload_stop'); ?>
                <input type="hidden" name="action" value="wpp_cache_preload_stop">
                <button type="submit" class="button" <?php disabled(!$preloadStatus['running']); ?>>停止预加载</button>
            </form>
            <?php if (!$fcacheEnabled): ?>
                <p class="description">请先开启 FastCGI 缓存，再执行预加载。</p>
            <?php endif; ?>

            <?php if (!empty($log)): ?>
            <hr>
            <h2>最近清除记录</h2>
            <table class="wp-list-table widefat fixed striped" style="max-width:600px">
                <thead><tr><th>时间</th><th>方式</th><th>结果</th></tr></thead>
                <tbody>
                    <?php foreach ($log as $entry): ?>
                    <tr>
                        <td><?php echo esc_html($entry['time']); ?></td>
                        <td><?php
                            $labels = ['manual' => '手动清除', 'auto' => '自动清除（发布文章）', 'comment' => '自动清除（评论变更）'];
                            echo esc_html($labels[$entry['type']] ?? '自动清除');
                        ?></td>
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
            wp_schedule_single_event(time() + 10, self::PRELOAD_HOOK);
        }
        return count($queue);
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

        $batch = array_splice($queue, 0, 5);
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
            wp_schedule_single_event(time() + 20, self::PRELOAD_HOOK);
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

    // ============================================================
    // 面板 API 通信
    // ============================================================

    private static function fetch_panel_state() {
        $domain = wp_parse_url(home_url(), PHP_URL_HOST);
        $resp = self::api_request('GET', '/api/sites/find?domain=' . urlencode($domain));
        if (!$resp || is_wp_error($resp)) return null;
        $data = json_decode($resp, true);
        return !empty($data['success']) ? ($data['data'] ?? null) : null;
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
