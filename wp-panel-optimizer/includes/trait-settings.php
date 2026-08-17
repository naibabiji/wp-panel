<?php
/**
 * WP Panel Optimizer — 设置页与开关模块
 *
 * 插件设置页渲染、禁止检测更新/禁止文件编辑等开关的挂载逻辑、
 * 文件锁定后台提示、插件列表页的设置链接。
 */

if (!defined('ABSPATH')) exit;

trait WPP_Optimizer_Settings_Trait {

    public static function bootstrap() {
        if (get_option(self::OPTION_NO_UPDATES, '0') !== '1') {
            return;
        }

        // Run before core's init callback so update checks are not re-scheduled
        // and immediately cleared on every request.
        remove_action('init', 'wp_schedule_update_checks');
        self::suppress_updates();
        self::clear_update_schedules();
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
        add_filter('plugin_action_links_' . plugin_basename(WPP_OPTIMIZER_PLUGIN_FILE), [__CLASS__, 'action_links']);
        add_action('admin_notices', [__CLASS__, 'clear_notice']);
        add_action('admin_notices', [__CLASS__, 'file_lock_notice']);
        add_action('wp_ajax_wpp_optimizer_check_update', [__CLASS__, 'ajax_check_update']);
        add_action(self::PRELOAD_HOOK, [__CLASS__, 'process_preload_batch']);
        self::maybe_process_preload_tick();
        self::image_optimizer_init();

    }

    public static function suppress_updates() {
        remove_action('admin_notices', 'update_nag', 3);
        remove_action('network_admin_notices', 'update_nag', 3);
        remove_action('wp_version_check', 'wp_version_check');
        remove_action('admin_init', '_maybe_update_core');
        remove_action('admin_init', '_maybe_update_plugins');
        remove_action('admin_init', '_maybe_update_themes');
        remove_action('load-plugins.php', 'wp_update_plugins');
        remove_action('load-update.php', 'wp_update_plugins');
        remove_action('load-themes.php', 'wp_update_themes');
        remove_action('load-update-core.php', 'wp_update_plugins');
        remove_action('load-update-core.php', 'wp_update_themes');
        remove_action('wp_update_plugins', 'wp_update_plugins');
        remove_action('wp_update_themes', 'wp_update_themes');
        add_filter('pre_site_transient_update_core', [__CLASS__, 'suppress_update_transient']);
        add_filter('pre_site_transient_update_plugins', [__CLASS__, 'suppress_update_transient']);
        add_filter('pre_site_transient_update_themes', [__CLASS__, 'suppress_update_transient']);

        add_filter('wp_get_update_data', [__CLASS__, 'filter_update_data'], 10, 2);
    }

    public static function suppress_update_transient() {
        return null;
    }

    public static function clear_update_schedules() {
        foreach (array('wp_version_check', 'wp_update_plugins', 'wp_update_themes') as $hook) {
            if (wp_next_scheduled($hook) !== false) {
                wp_clear_scheduled_hook($hook);
            }
        }
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
                self::update_file_lock_state_option($panelState);
            }
        }

        if ($isPost) {
            check_admin_referer('wpp_optimizer_settings');
            if (self::sync_file_lock_state(true)) {
                $notice = '<div class="notice notice-warning"><p><strong>WP Panel 文件锁定已开启。</strong>当前设置未保存。如需修改会写入 wp-config.php 的优化项，请先到 WP Panel 网站详情页解除文件锁定。</p></div>';
            } else {
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

            $imageModeBefore = self::image_optimizer_mode();
            $imageMode = isset($_POST['image_mode']) ? sanitize_key(wp_unslash($_POST['image_mode'])) : self::IMAGE_MODE_OFF;
            if (!self::image_optimizer_env_ready() || !in_array($imageMode, [self::IMAGE_MODE_OFF, self::IMAGE_MODE_OPTIMIZE, self::IMAGE_MODE_WEBP], true)) {
                $imageMode = self::IMAGE_MODE_OFF;
            }
            $imageJpegQuality = self::clamp_image_quality($_POST['image_jpeg_quality'] ?? 85);
            $imageWebpQuality = self::clamp_image_quality($_POST['image_webp_quality'] ?? 82);
            update_option(self::OPTION_IMAGE_MODE, $imageMode);
            update_option(self::OPTION_IMAGE_JPEG_QUALITY, $imageJpegQuality);
            update_option(self::OPTION_IMAGE_WEBP_QUALITY, $imageWebpQuality);
            $switchedToWebp = ($imageMode === self::IMAGE_MODE_WEBP && $imageModeBefore !== self::IMAGE_MODE_WEBP);

            update_option(self::OPTION_FCACHE_ENABLED, $fcacheEnabled ? '1' : '0');
            update_option(self::OPTION_FCACHE_TTL, $fcacheTTL);
            update_option(self::OPTION_NO_UPDATES, $noUpdates ? '1' : '0');
            if ($noUpdates) {
                self::clear_update_schedules();
            }
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
            if ($switchedToWebp) {
                $notice .= '<div class="notice notice-warning"><p><strong>WebP 模式已开启。</strong>新上传的 JPG/PNG 图片会统一转换为 WebP 格式并删除原图。请确认：① 如需保留原始文件，请自行在本地备份；② 网站没有依赖 JPG/PNG 格式的邮件通知（部分 Outlook 客户端不支持在邮件中显示 WebP 图片）、社交平台分享卡片（部分微信/QQ 等抓取器对 WebP 支持不佳）、或需要原图格式的第三方插件。这些场景仍需要 JPG/PNG 时可以从 WebP 反向转换生成，但不是自动的。</p></div>';
            }
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
        $fileLockEnabled = get_option(self::OPTION_FILE_LOCK_ENABLED, '0') === '1';
        $imageEnvReady    = self::image_optimizer_env_ready();
        $imageMode        = self::image_optimizer_mode();
        $imageJpegQuality = self::clamp_image_quality(get_option(self::OPTION_IMAGE_JPEG_QUALITY, 85));
        $imageWebpQuality = self::clamp_image_quality(get_option(self::OPTION_IMAGE_WEBP_QUALITY, 82));
        $imageSkippedCount = intval(get_option(self::OPTION_IMAGE_SKIPPED_COUNT, 0));
        $xmlrpcEnabled = get_option('wpp_optimizer_xmlrpc_enabled', '0') === '1';
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
            <?php if ($fileLockEnabled): ?>
                <div class="notice notice-warning"><p><strong>WP Panel 文件锁定已开启。</strong>发文章、编辑页面和上传图片不受影响；其他运行目录的写入范围由当前文件锁规则决定，安装、更新、删除插件或主题，以及修改代码和站点配置会被阻止。如需维护插件主题或首次配置安全/缓存插件，请先到 WP Panel 网站详情页解除文件锁定。</p></div>
            <?php endif; ?>

            <h2 class="nav-tab-wrapper" id="wpp-tabs">
                <a href="#" class="nav-tab nav-tab-active" data-tab="cache">缓存与性能</a>
                <a href="#" class="nav-tab" data-tab="image">图片优化</a>
                <a href="#" class="nav-tab" data-tab="security">安全与维护</a>
                <a href="#" class="nav-tab" data-tab="about">关于与面板同步</a>
            </h2>

            <form id="wpp-form" method="post">
                <?php wp_nonce_field('wpp_optimizer_settings'); ?>

                <div class="wpp-tab-panel" data-tab-panel="cache">
                    <table class="form-table">
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
                                <p class="description"><strong>说明：</strong>预加载只提前处理首页和最近更新的公开内容（最多为下方设置的 URL 数量），不是全站爬虫。未进入预加载队列的页面仍会在真实访客首次访问后由 Nginx 正常生成缓存。</p>
                            </td>
                        </tr>
                        <tr>
                            <th><label for="wpp-preload-limit">单次最多预加载 URL</label></th>
                            <td>
                                <input id="wpp-preload-limit" name="preload_limit" type="number" class="small-text" value="<?php echo esc_attr($preloadLimit); ?>" min="10" max="500">
                                <p class="description">范围 10-500。首页优先，其次为最近更新的公开文章、页面和公开分类归档。</p>
                            </td>
                        </tr>
                    </table>

                    <h3>缓存预加载状态</h3>
                    <form method="post" action="<?php echo esc_url(admin_url('admin-post.php')); ?>" style="margin:0 0 12px;">
                        <?php wp_nonce_field('wpp_cache_clear'); ?>
                        <input type="hidden" name="action" value="wpp_cache_clear">
                        <button type="submit" class="button button-primary" <?php disabled($missing); ?>>清除 Nginx 缓存</button>
                        <span class="description">适合手机后台或管理栏不方便操作时手动清理缓存。</span>
                    </form>
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
                        <?php if (!empty($preloadStatus['last_run_at'])): ?>
                            ，上次执行：<?php echo esc_html($preloadStatus['last_run_at']); ?>
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
                    <h3>最近清除记录</h3>
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
                </div>

                <div class="wpp-tab-panel" data-tab-panel="image" style="display:none">
                    <table class="form-table">
                        <tr>
                            <th><label for="wpp-image-mode-off">新上传图片处理</label></th>
                            <td>
                                <?php if (!$imageEnvReady): ?>
                                    <p class="description" style="color:#d63638"><strong>服务器缺少 exif 扩展，暂不支持图片处理。</strong>该功能依赖 PHP <code>exif</code> 扩展做拍照方向修正，面板补装完成后刷新本页即可使用。</p>
                                <?php endif; ?>
                                <label style="display:block;margin-bottom:6px;"><input id="wpp-image-mode-off" type="radio" name="image_mode" value="off" <?php checked($imageMode, self::IMAGE_MODE_OFF); ?> <?php disabled(!$imageEnvReady); ?>> 关闭</label>
                                <label style="display:block;margin-bottom:6px;"><input id="wpp-image-mode-optimize" type="radio" name="image_mode" value="optimize" <?php checked($imageMode, self::IMAGE_MODE_OPTIMIZE); ?> <?php disabled(!$imageEnvReady); ?>> 优化模式 — JPEG 按质量重新编码（有损但视觉无差异，非无损），PNG 无损压缩</label>
                                <label style="display:block;"><input id="wpp-image-mode-webp" type="radio" name="image_mode" value="webp" <?php checked($imageMode, self::IMAGE_MODE_WEBP); ?> <?php disabled(!$imageEnvReady); ?>> WebP 模式 — 统一转换为 WebP 并删除原图</label>
                                <p class="description" style="margin-top:10px;">
                                    JPEG 质量：<input type="number" name="image_jpeg_quality" class="small-text" value="<?php echo esc_attr($imageJpegQuality); ?>" min="1" max="100" <?php disabled(!$imageEnvReady); ?>>
                                    　WebP 质量：<input type="number" name="image_webp_quality" class="small-text" value="<?php echo esc_attr($imageWebpQuality); ?>" min="1" max="100" <?php disabled(!$imageEnvReady); ?>>
                                </p>
                                <?php if ($imageMode === self::IMAGE_MODE_WEBP): ?>
                                    <p class="description">WebP 模式会删除原图，不提供保留原图的选项。如需保留原始文件，请自行在本地备份；网站依赖 JPG/PNG 格式的邮件通知、社交平台分享卡片或第三方插件时请先确认不受影响。</p>
                                <?php endif; ?>
                                <?php if ($imageSkippedCount > 0): ?>
                                    <p class="description">有 <?php echo esc_html($imageSkippedCount); ?> 张图片未能转换成功（文件格式不受支持、转换后体积反而更大等原因），已自动保留原图，不影响正常上传。</p>
                                <?php endif; ?>
                            </td>
                        </tr>
                    </table>

                    <hr>
                    <h3>历史图库批量优化</h3>
                    <p class="description">对媒体库里已有的历史 JPEG/PNG 做原地无损重编码，不改文件名、不生成 WebP 副本、不影响已发布内容里的图片引用。处理由面板降权执行，这里只是发起和查看进度，跟 WP Panel 网站详情页的「图片优化」卡片是同一个任务。</p>
                    <p style="margin-top:10px;">
                        <button type="button" id="wpp-image-batch-start" class="button button-primary" <?php disabled($fileLockEnabled); ?>>开始批量优化</button>
                        <button type="button" id="wpp-image-batch-stop" class="button" style="display:none">停止</button>
                        <span id="wpp-image-batch-status" class="description"></span>
                    </p>
                    <p id="wpp-image-batch-progress" class="description" style="display:none"></p>
                </div>

                <div class="wpp-tab-panel" data-tab-panel="security" style="display:none">
                    <table class="form-table">
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
                            <th><label for="wpp-memory-limit">WordPress 内存限制</label></th>
                            <td>
                                <input id="wpp-memory-limit" name="memory_limit" type="text" class="regular-text" value="<?php echo esc_attr($memoryLimit); ?>" placeholder="默认 40M">
                                <p class="description">设置 WordPress 的 <code>WP_MEMORY_LIMIT</code>，如 <code>128M</code>、<code>256M</code>。留空使用 WordPress 默认值（40M）。<br>这是 WordPress 应用层内存限制，不是 PHP-FPM 的 <code>memory_limit</code> 硬上限；实际值不应超过面板「软件管理」中的 PHP 内存限制。遇到"Allowed memory size exhausted"错误、后台白屏时可适当调高。</p>
                            </td>
                        </tr>
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
                </div>

                <div class="wpp-tab-panel" data-tab-panel="about" style="display:none">
                    <table class="form-table">
                        <tr>
                            <th>API Key</th>
                            <td><code><?php echo esc_html($apiKey ? substr($apiKey, 0, 8) . '...' : '未配置'); ?></code></td>
                        </tr>
                    </table>
                    <p>
                        <button type="button" id="wpp-verify-btn" class="button">验证连接</button>
                    </p>
                    <div id="wpp-verify-msg"></div>
                </div>

                <p>
                    <button type="submit" name="wpp_save" class="button button-primary" <?php disabled($fileLockEnabled); ?>>保存设置</button>
                </p>
            </form>

            <script>
            (function() {
                var tabs = document.querySelectorAll('#wpp-tabs .nav-tab');
                var panels = document.querySelectorAll('.wpp-tab-panel');
                function activate(name) {
                    tabs.forEach(function(t) { t.classList.toggle('nav-tab-active', t.dataset.tab === name); });
                    panels.forEach(function(p) { p.style.display = (p.dataset.tabPanel === name) ? '' : 'none'; });
                    try { sessionStorage.setItem('wpp_active_tab', name); } catch (e) {}
                }
                tabs.forEach(function(t) {
                    t.addEventListener('click', function(e) { e.preventDefault(); activate(t.dataset.tab); });
                });
                var saved = null;
                try { saved = sessionStorage.getItem('wpp_active_tab'); } catch (e) {}
                if (saved && document.querySelector('.wpp-tab-panel[data-tab-panel="' + saved + '"]')) {
                    activate(saved);
                }
            })();

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

            (function() {
                var startBtn = document.getElementById('wpp-image-batch-start');
                var stopBtn = document.getElementById('wpp-image-batch-stop');
                var statusEl = document.getElementById('wpp-image-batch-status');
                var progressEl = document.getElementById('wpp-image-batch-progress');
                var nonce = '<?php echo esc_attr(wp_create_nonce('wpp_optimizer_settings')); ?>';
                var ajaxUrl = '<?php echo esc_url(admin_url('admin-ajax.php')); ?>';
                var pollTimer = null;

                function call(action, extra) {
                    var params = new URLSearchParams(Object.assign({ action: action, _wpnonce: nonce }, extra || {}));
                    return fetch(ajaxUrl + '?' + params.toString(), { method: 'POST' }).then(r => r.json());
                }

                var statusLabels = { queued: '排队中', running: '运行中', succeeded: '已完成', failed: '失败', stopped: '已停止', none: '' };

                function render(job) {
                    if (!job || !job.Status || job.Status === 'none') {
                        statusEl.textContent = '';
                        progressEl.style.display = 'none';
                        startBtn.style.display = '';
                        stopBtn.style.display = 'none';
                        return;
                    }
                    var running = job.Status === 'queued' || job.Status === 'running';
                    statusEl.textContent = statusLabels[job.Status] || job.Status;
                    startBtn.style.display = running ? 'none' : '';
                    stopBtn.style.display = running ? '' : 'none';
                    progressEl.style.display = '';
                    var text = '进度：' + (job.ProcessedFiles || 0) + ' / ' + (job.TotalFiles || 0);
                    if (job.FailedFiles > 0) text += '（失败 ' + job.FailedFiles + '）';
                    var saved = (job.BytesBefore || 0) - (job.BytesAfter || 0);
                    if (saved > 0) text += '，已节省 ' + (saved / 1024 / 1024).toFixed(2) + ' MB';
                    progressEl.textContent = text;
                    if (running) {
                        clearTimeout(pollTimer);
                        pollTimer = setTimeout(poll, 3000);
                    }
                }

                function poll() {
                    call('wpp_optimizer_image_batch_status').then(function(data) {
                        if (data.success) render(data.data);
                    });
                }

                startBtn.addEventListener('click', function() {
                    startBtn.disabled = true;
                    call('wpp_optimizer_image_batch_start').then(function(data) {
                        startBtn.disabled = false;
                        if (data.success) {
                            render(data.data && data.data.Status ? data.data : { Status: 'queued' });
                            poll();
                        } else {
                            statusEl.textContent = '启动失败：' + (data.data?.message || '未知错误');
                        }
                    });
                });

                stopBtn.addEventListener('click', function() {
                    stopBtn.disabled = true;
                    call('wpp_optimizer_image_batch_stop').then(function() {
                        stopBtn.disabled = false;
                        poll();
                    });
                });

                poll();
            })();
            </script>
        </div>
        <?php
    }

    public static function file_lock_notice() {
        if (!current_user_can('manage_options')) {
            return;
        }
        $screen = function_exists('get_current_screen') ? get_current_screen() : null;
        if ($screen && $screen->id === 'settings_page_wp-panel-optimizer') {
            return;
        }
        if (!self::sync_file_lock_state()) {
            return;
        }
        echo '<div class="notice notice-warning"><p><strong>WP Panel 文件锁定已开启。</strong>发文章、编辑页面和上传图片不受影响；其他运行目录的写入范围由当前文件锁规则决定，安装、更新、删除插件或主题，以及修改代码和站点配置会被阻止。如需维护插件主题或首次配置安全/缓存插件，请先到 WP Panel 网站详情页解除文件锁定。</p></div>';
    }

}
