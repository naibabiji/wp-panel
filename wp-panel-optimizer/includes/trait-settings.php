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
		$cfg = self::load_config();
		if (!empty($cfg['disable_application_passwords'])) {
			add_filter('wp_is_application_passwords_available', '__return_false', PHP_INT_MAX);
		}
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
        add_action('admin_enqueue_scripts', [__CLASS__, 'settings_assets']);
        add_action('admin_post_wpp_cache_clear', [__CLASS__, 'handle_clear']);
        add_action('admin_post_wpp_cache_preload', [__CLASS__, 'handle_preload']);
        add_action('admin_post_wpp_cache_preload_stop', [__CLASS__, 'handle_preload_stop']);
        add_action('save_post', [__CLASS__, 'auto_clear'], 99, 1);
        add_action('deleted_post', [__CLASS__, 'auto_clear'], 99, 1);
        add_action('wp_update_comment_count', [__CLASS__, 'auto_comment_clear']);
        add_filter('plugin_action_links_' . plugin_basename(WPP_OPTIMIZER_PLUGIN_FILE), [__CLASS__, 'action_links']);
        add_action('admin_notices', [__CLASS__, 'clear_notice']);
        add_action('admin_notices', [__CLASS__, 'file_lock_notice']);
        add_action('admin_notices', [__CLASS__, 'companion_update_notice']);
        add_action('wp_ajax_wpp_optimizer_update_companion', [__CLASS__, 'ajax_update_companion']);
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
        $links[] = '<a href="' . esc_url(admin_url('options-general.php?page=wp-panel-optimizer')) . '">' . esc_html__('Settings', 'wp-panel-optimizer') . '</a>';
        return $links;
    }

    public static function settings_page() {
        add_options_page('WP Panel Optimizer', 'WP Panel Optimizer', 'manage_options', 'wp-panel-optimizer', [__CLASS__, 'render_settings']);
    }

    public static function settings_assets($hook) {
        if ($hook !== 'settings_page_wp-panel-optimizer') {
            return;
        }
        wp_enqueue_style(
            'wpp-optimizer-settings',
            plugin_dir_url(WPP_OPTIMIZER_PLUGIN_FILE) . 'assets/settings.css',
            ['dashicons'],
            self::VERSION
        );
    }

    private static function companion_update_info($refresh = false) {
        $key = 'wpp_optimizer_companion_update';
        if (!$refresh) {
            $cached = get_transient($key);
            if (is_array($cached)) return $cached;
        }
        $state = self::fetch_panel_state();
        $latest = is_array($state) ? sanitize_text_field($state['companion_latest_version'] ?? '') : '';
        $info = ['latest' => $latest, 'available' => $latest !== '' && version_compare(self::VERSION, $latest, '<')];
        set_transient($key, $info, 5 * MINUTE_IN_SECONDS);
        return $info;
    }

    public static function companion_update_notice() {
        if (!current_user_can('manage_options')) return;
        $info = self::companion_update_info();
        if (empty($info['available'])) return;
        $url = admin_url('options-general.php?page=wp-panel-optimizer#wpp-component-update');
        echo '<div class="notice notice-info"><p><strong>' . esc_html__('A new WP Panel Optimizer version is available.', 'wp-panel-optimizer') . '</strong> ';
        echo esc_html(sprintf(__('Installed: %1$s; available from WP Panel: %2$s.', 'wp-panel-optimizer'), self::VERSION, $info['latest'])) . ' ';
        echo '<a href="' . esc_url($url) . '">' . esc_html__('Open WP Panel Optimizer to update', 'wp-panel-optimizer') . '</a></p></div>';
    }

    public static function ajax_update_companion() {
        check_ajax_referer('wpp_optimizer_settings');
        if (!current_user_can('manage_options')) {
            wp_send_json_error(['message' => __('Insufficient permissions', 'wp-panel-optimizer')], 403);
        }
        $domain = wp_parse_url(home_url(), PHP_URL_HOST);
        $resp = self::api_request_public('POST', '/api/sites/companion/update', ['domain' => $domain]);
        if (is_wp_error($resp)) wp_send_json_error(['message' => $resp->get_error_message()]);
        $data = json_decode($resp, true);
        if (empty($data['success'])) wp_send_json_error(['message' => $data['message'] ?? __('Update failed', 'wp-panel-optimizer')]);
        delete_transient('wpp_optimizer_companion_update');
        wp_send_json_success(['version' => sanitize_text_field($data['data']['version'] ?? '')]);
    }

    public static function render_settings() {
        $showMaintenance = current_user_can('manage_options') && !is_multisite();
        $cfg = self::load_config();
        $panelUrl = self::get_panel_url();
        $apiKey = self::get_api_key();
        $currentDomain = wp_parse_url(home_url(), PHP_URL_HOST);
        $missing = !$panelUrl || !$apiKey;

        $isPost = isset($_POST['wpp_save']);
        $notice = '';

        // 面板同步：GET 时从面板拉取最新状态，POST 时不拉（避免用旧值覆盖表单新值）
        $companionLatestVersion = '';
        if (!$isPost) {
            $panelState = self::fetch_panel_state();
            if ($panelState) {
                $companionLatestVersion = sanitize_text_field($panelState['companion_latest_version'] ?? '');
                update_option(self::OPTION_FCACHE_ENABLED, !empty($panelState['fastcgi_cache_enabled']) ? '1' : '0');
                update_option(self::OPTION_FCACHE_TTL, intval($panelState['fastcgi_cache_ttl'] ?? 300));
                update_option(self::OPTION_NO_UPDATES, !empty($panelState['disable_wp_updates']) ? '1' : '0');
                update_option(self::OPTION_NO_FILE_EDIT, !empty($panelState['disable_file_editing']) ? '1' : '0');
                update_option(self::OPTION_XMLRPC_ENABLED, !empty($panelState['xmlrpc_enabled']) ? '1' : '0');
                update_option(self::OPTION_DISABLE_APPLICATION_PASSWORDS, !empty($panelState['disable_application_passwords']) ? '1' : '0');
                update_option(self::OPTION_WP_DEBUG, !empty($panelState['wp_debug_enabled']) ? '1' : '0');
                update_option(self::OPTION_POST_REVISIONS, $panelState['wp_post_revisions'] ?? -1);
                update_option(self::OPTION_MEMORY_LIMIT, $panelState['wp_memory_limit'] ?? '');
                update_option(self::OPTION_ANOMALY_MONITOR_STATUS, sanitize_key($panelState['anomaly_monitor_status'] ?? 'disabled'));
                update_option(self::OPTION_PASSWORD_RESET_MODE, sanitize_key($panelState['password_reset_mode'] ?? 'allow'));
                self::update_file_lock_state_option($panelState);
            }
        }

        if ($isPost) {
            check_admin_referer('wpp_optimizer_settings');
            $fileLockSafeOnly = self::sync_file_lock_state(true);
            $fcacheEnabled  = !empty($_POST['fcache_enabled'])  ? true : false;
            $fcacheTTL      = isset($_POST['fcache_ttl']) ? intval($_POST['fcache_ttl']) : 300;
            $noUpdates      = $fileLockSafeOnly ? get_option(self::OPTION_NO_UPDATES, '0') === '1' : !empty($_POST['no_updates']);
            $noFileEdit     = $fileLockSafeOnly ? get_option(self::OPTION_NO_FILE_EDIT, '0') === '1' : !empty($_POST['no_file_edit']);
            $wpDebug        = $fileLockSafeOnly ? get_option(self::OPTION_WP_DEBUG, '0') === '1' : !empty($_POST['wp_debug']);
            $postRevisions  = $fileLockSafeOnly ? intval(get_option(self::OPTION_POST_REVISIONS, '-1')) : ((isset($_POST['post_revisions']) && $_POST['post_revisions'] !== '') ? intval($_POST['post_revisions']) : -1);
            $memoryLimit    = $fileLockSafeOnly ? get_option(self::OPTION_MEMORY_LIMIT, '') : (isset($_POST['memory_limit']) ? sanitize_text_field($_POST['memory_limit']) : '');
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
            if (!$fileLockSafeOnly) {
                update_option(self::OPTION_NO_UPDATES, $noUpdates ? '1' : '0');
                if ($noUpdates) {
                    self::clear_update_schedules();
                }
                update_option(self::OPTION_NO_FILE_EDIT, $noFileEdit ? '1' : '0');
                update_option(self::OPTION_WP_DEBUG, $wpDebug ? '1' : '0');
                update_option(self::OPTION_POST_REVISIONS, $postRevisions);
                update_option(self::OPTION_MEMORY_LIMIT, $memoryLimit);
            }
            update_option(self::OPTION_PRELOAD_ENABLED, $preloadEnabled ? '1' : '0');
            update_option(self::OPTION_PRELOAD_LIMIT, $preloadLimit);

            $pushed = self::push_optimizer_settings($fcacheEnabled, $fcacheTTL, $noUpdates, $noFileEdit, $wpDebug, $postRevisions, $memoryLimit, $fileLockSafeOnly);
            if ($pushed === true) {
                $noticeText = $fileLockSafeOnly
                    ? __('Available settings were saved. Settings that modify wp-config.php remain unchanged while file protection is enabled.', 'wp-panel-optimizer')
                    : __('Settings saved and synced to the panel.', 'wp-panel-optimizer');
                $notice = '<div class="notice notice-success"><p>' . esc_html($noticeText) . '</p></div>';
            } else {
                $errMsg = is_wp_error($pushed) ? $pushed->get_error_message() : __('Unknown error', 'wp-panel-optimizer');
                $notice = '<div class="notice notice-warning is-dismissible"><p><strong>' . esc_html__('Note:', 'wp-panel-optimizer') . '</strong> ' . esc_html__('Settings were saved locally but failed to sync to the panel. Error message:', 'wp-panel-optimizer') . ' <code>' . esc_html($errMsg) . '</code></p><p>' . esc_html__('The next time you open this page, state will be pulled from the panel and may overwrite these changes. Please check whether "Verify panel connection" in the plugin settings works.', 'wp-panel-optimizer') . '</p></div>';
            }
            if ($switchedToWebp) {
                $notice .= '<div class="notice notice-info"><p><strong>' . esc_html__('WebP mode is enabled.', 'wp-panel-optimizer') . '</strong> ' . esc_html__('Newly uploaded JPG/PNG images are converted automatically to smaller WebP files; the originals are no longer kept. The vast majority of sites can switch without any impact; if some email notifications, share cards, or older plugins turn out to need the original format later, the affected WebP images can be converted back to JPG/PNG at any time.', 'wp-panel-optimizer') . '</p></div>';
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
        $applicationPasswordsDisabled = !empty($cfg['disable_application_passwords']);
        $anomalyMonitorStatus = get_option(self::OPTION_ANOMALY_MONITOR_STATUS, 'disabled');
        if (!in_array($anomalyMonitorStatus, ['disabled', 'pending', 'active', 'error'], true)) {
            $anomalyMonitorStatus = 'disabled';
        }
        $passwordResetMode = get_option(self::OPTION_PASSWORD_RESET_MODE, 'allow');
        if (!in_array($passwordResetMode, ['allow', 'admin', 'all'], true)) {
            $passwordResetMode = 'allow';
        }
        $anomalyLabels = [
            'disabled' => __('Not enabled', 'wp-panel-optimizer'),
            'pending'  => __('Waiting for first check', 'wp-panel-optimizer'),
            'active'   => __('Monitoring active', 'wp-panel-optimizer'),
            'error'    => __('Check failed', 'wp-panel-optimizer'),
        ];
        $passwordResetLabels = [
            'allow' => __('All users may reset passwords', 'wp-panel-optimizer'),
            'admin' => __('Administrators cannot reset passwords', 'wp-panel-optimizer'),
            'all'   => __('Password reset disabled for all users', 'wp-panel-optimizer'),
        ];
        $phpMemoryLimit = (string) ini_get('memory_limit');
        if ($phpMemoryLimit === '' || $phpMemoryLimit === '-1') {
            $phpMemoryLimit = __('Unlimited', 'wp-panel-optimizer');
        }
        if ($companionLatestVersion === '') {
            $updateInfo = self::companion_update_info();
            $companionLatestVersion = $updateInfo['latest'];
        }
        $companionUpdateAvailable = $companionLatestVersion !== '' && version_compare(self::VERSION, $companionLatestVersion, '<');
        ?>
        <div class="wrap wpp-settings">
            <?php
            $pluginVersion = WP_Panel_Optimizer::VERSION;
            $imageModeLabel = !$imageEnvReady
                ? __('Environment not ready', 'wp-panel-optimizer')
                : ($imageMode === self::IMAGE_MODE_WEBP ? __('WebP conversion', 'wp-panel-optimizer') : ($imageMode === self::IMAGE_MODE_OPTIMIZE ? __('Compression', 'wp-panel-optimizer') : __('Off', 'wp-panel-optimizer')));

            // 预加载状态派生：仅用于展示，不改变任何业务逻辑。
            $preloadRunning = !empty($preloadStatus['running']);
            $preloadQueued  = intval($preloadStatus['queued']);
            $preloadDone    = intval($preloadStatus['done']);
            $preloadFailed  = intval($preloadStatus['failed']);
            if ($preloadRunning) {
                $preloadSummary = sprintf(__('Preload is running; %d URLs remain in the queue.', 'wp-panel-optimizer'), $preloadQueued);
            } elseif ($preloadQueued > 0) {
                $preloadSummary = sprintf(__('Preload is queued; %d URLs are waiting to be processed.', 'wp-panel-optimizer'), $preloadQueued);
            } else {
                $preloadSummary = __('Preload is idle and no URLs are pending.', 'wp-panel-optimizer');
            }
            $stateTone  = $preloadRunning ? 'info' : 'idle';
            $queueTone  = $preloadQueued > 0 ? 'info' : 'idle';
            $doneTone   = $preloadDone > 0 ? 'ok' : 'idle';
            $failedTone = $preloadFailed > 0 ? 'risk' : 'idle';
            ?>
            <header class="wpp-masthead">
                <div class="wpp-masthead__brand">
                    <span class="wpp-masthead__mark" aria-hidden="true"><img src="<?php echo esc_url(plugin_dir_url(WPP_OPTIMIZER_PLUGIN_FILE) . 'assets/wp-panel-logo.png'); ?>" alt=""></span>
                    <div>
                        <p class="wpp-masthead__eyebrow">WP PANEL · MANAGED WORDPRESS</p>
                        <h1>WP Panel Optimizer</h1>
                        <p class="wpp-masthead__lead"><?php echo esc_html__('Caching, image, and security policies managed centrally by the server panel.', 'wp-panel-optimizer'); ?></p>
                    </div>
                </div>
                <div class="wpp-masthead__meta">
                    <div class="wpp-masthead__item">
                        <span class="wpp-masthead__label"><?php echo esc_html__('Managed site', 'wp-panel-optimizer'); ?></span>
                        <span class="wpp-masthead__value"><?php echo esc_html($currentDomain); ?></span>
                    </div>
                    <div class="wpp-masthead__item">
                        <span class="wpp-masthead__label"><?php echo esc_html__('Component version', 'wp-panel-optimizer'); ?></span>
                        <span class="wpp-masthead__value"><?php echo esc_html($pluginVersion); ?></span>
                    </div>
                    <?php if ($showMaintenance): ?>
                        <button type="button" class="button wpp-masthead__action" data-wpp-maintenance-open>
                            <span class="dashicons dashicons-lock" aria-hidden="true"></span>
                            <?php echo esc_html__('File protection / maintenance', 'wp-panel-optimizer'); ?>
                        </button>
                    <?php endif; ?>
                </div>
            </header>

            <div class="wpp-readout" role="group" aria-label="<?php echo esc_attr__('Component status overview', 'wp-panel-optimizer'); ?>">
                <div class="wpp-readout__item">
                    <span class="wpp-readout__label"><?php echo esc_html__('Panel connection', 'wp-panel-optimizer'); ?></span>
                    <span class="wpp-readout__value <?php echo $missing ? 'is-risk' : 'is-ok'; ?>">
                        <span class="wpp-flag wpp-flag--<?php echo $missing ? 'risk' : 'ok'; ?>" aria-hidden="true"></span>
                        <?php echo $missing ? esc_html__('Not configured', 'wp-panel-optimizer') : esc_html__('Connected', 'wp-panel-optimizer'); ?>
                    </span>
                </div>
                <div class="wpp-readout__item">
                    <span class="wpp-readout__label"><?php echo esc_html__('File protection', 'wp-panel-optimizer'); ?></span>
                    <span class="wpp-readout__value <?php echo $fileLockEnabled ? 'is-ok' : 'is-idle'; ?>">
                        <span class="wpp-flag wpp-flag--<?php echo $fileLockEnabled ? 'ok' : 'idle'; ?>" aria-hidden="true"></span>
                        <?php echo $fileLockEnabled ? esc_html__('Enabled', 'wp-panel-optimizer') : esc_html__('Not enabled', 'wp-panel-optimizer'); ?>
                    </span>
                </div>
                <div class="wpp-readout__item">
                    <span class="wpp-readout__label"><?php echo esc_html__('FastCGI cache', 'wp-panel-optimizer'); ?></span>
                    <span class="wpp-readout__value <?php echo $fcacheEnabled ? 'is-ok' : 'is-idle'; ?>">
                        <span class="wpp-flag wpp-flag--<?php echo $fcacheEnabled ? 'ok' : 'idle'; ?>" aria-hidden="true"></span>
                        <?php echo $fcacheEnabled ? esc_html__('Enabled', 'wp-panel-optimizer') : esc_html__('Disabled', 'wp-panel-optimizer'); ?>
                    </span>
                </div>
                <div class="wpp-readout__item">
                    <span class="wpp-readout__label"><?php echo esc_html__('Image handling', 'wp-panel-optimizer'); ?></span>
                    <span class="wpp-readout__value <?php echo $imageEnvReady && $imageMode !== self::IMAGE_MODE_OFF ? 'is-ok' : 'is-idle'; ?>">
                        <span class="wpp-flag wpp-flag--<?php echo $imageEnvReady && $imageMode !== self::IMAGE_MODE_OFF ? 'ok' : 'idle'; ?>" aria-hidden="true"></span>
                        <?php echo esc_html($imageModeLabel); ?>
                    </span>
                </div>
                <div class="wpp-readout__item">
                    <span class="wpp-readout__label"><?php echo esc_html__('Component updates', 'wp-panel-optimizer'); ?></span>
                    <span class="wpp-readout__value <?php echo $companionUpdateAvailable ? 'is-warn' : 'is-ok'; ?>">
                        <span class="wpp-flag wpp-flag--<?php echo $companionUpdateAvailable ? 'warn' : 'ok'; ?>" aria-hidden="true"></span>
                        <?php echo $companionUpdateAvailable ? esc_html(sprintf(__('Version %s available', 'wp-panel-optimizer'), $companionLatestVersion)) : esc_html__('Up to date', 'wp-panel-optimizer'); ?>
                    </span>
                </div>
            </div>

            <?php if ($companionUpdateAvailable): ?>
                <div class="wpp-component-update" id="wpp-component-update">
                    <div>
                        <strong><?php echo esc_html__('An update is available for this managed plugin.', 'wp-panel-optimizer'); ?></strong>
                        <p><?php echo esc_html(sprintf(__('WP Panel can update this plugin from version %1$s to %2$s. The update will not change whether the plugin is active.', 'wp-panel-optimizer'), self::VERSION, $companionLatestVersion)); ?></p>
                    </div>
                    <button type="button" class="button button-primary" id="wpp-component-update-btn"><?php echo esc_html__('Update now', 'wp-panel-optimizer'); ?></button>
                    <div id="wpp-component-update-msg" aria-live="polite"></div>
                </div>
            <?php endif; ?>

            <?php echo wp_kses_post($notice); ?>
            <?php if ($missing): ?>
                <div class="notice notice-error"><p><strong><?php echo esc_html__('Configuration file missing', 'wp-panel-optimizer'); ?></strong> — <?php echo esc_html__('Open the site details page for this website in the WP Panel and click the "Install companion plugin" button on the WordPress optimization card to complete initialization.', 'wp-panel-optimizer'); ?></p></div>
            <?php endif; ?>
            <nav class="wpp-tabs" id="wpp-tabs" aria-label="<?php echo esc_attr__('WP Panel Optimizer settings', 'wp-panel-optimizer'); ?>">
                <a href="#" class="nav-tab nav-tab-active" data-tab="cache"><span class="dashicons dashicons-performance" aria-hidden="true"></span><?php echo esc_html__('Cache & Performance', 'wp-panel-optimizer'); ?></a>
                <a href="#" class="nav-tab" data-tab="image"><span class="dashicons dashicons-format-image" aria-hidden="true"></span><?php echo esc_html__('Image Optimization', 'wp-panel-optimizer'); ?></a>
                <a href="#" class="nav-tab" data-tab="security"><span class="dashicons dashicons-shield" aria-hidden="true"></span><?php echo esc_html__('Security & Maintenance', 'wp-panel-optimizer'); ?></a>
                <a href="#" class="nav-tab" data-tab="about"><span class="dashicons dashicons-admin-links" aria-hidden="true"></span><?php echo esc_html__('About & Panel Sync', 'wp-panel-optimizer'); ?></a>
            </nav>

            <form id="wpp-form" method="post">
                <?php wp_nonce_field('wpp_optimizer_settings'); ?>

                <div class="wpp-tab-panel" data-tab-panel="cache">
                    <section class="wpp-section wpp-section--featured">
                        <header class="wpp-section__head">
                            <div class="wpp-section__titlerow">
                                <h2 class="wpp-section__title"><span class="dashicons dashicons-performance" aria-hidden="true"></span><?php echo esc_html__('FastCGI cache', 'wp-panel-optimizer'); ?></h2>
                                <span class="wpp-section__badge"><?php echo esc_html__('Core cache switch', 'wp-panel-optimizer'); ?></span>
                            </div>
                            <p class="wpp-section__desc"><?php echo esc_html__('Helps visitors open pages faster and reduces repeated server work for the same content.', 'wp-panel-optimizer'); ?></p>
                        </header>
                        <div class="wpp-section__body">
                            <div class="wpp-field">
                                <div class="wpp-field__head">
                                    <label class="wpp-field__title" for="wpp-fcache-enabled"><?php echo esc_html__('FastCGI cache', 'wp-panel-optimizer'); ?></label>
                                    <span class="wpp-field__summary"><?php echo esc_html__('Ideal for blogs and business sites that mostly serve public content.', 'wp-panel-optimizer'); ?></span>
                                </div>
                                <div class="wpp-field__body">
                                    <label class="wpp-switch">
                                        <input id="wpp-fcache-enabled" name="fcache_enabled" type="checkbox" value="1" <?php checked($fcacheEnabled); ?>>
                                        <span class="wpp-switch__track"><span class="wpp-switch__thumb"></span></span>
                                        <span class="wpp-switch__text"><?php echo esc_html__('Enable FastCGI cache', 'wp-panel-optimizer'); ?></span>
                                    </label>
                                    <div class="wpp-hint">
                                        <span class="dashicons dashicons-info" aria-hidden="true"></span>
                                        <div><?php echo esc_html__('The plugin clears the site cache automatically when content or comments change.', 'wp-panel-optimizer'); ?></div>
                                    </div>
                                    <details class="wpp-more">
                                        <summary><?php echo esc_html__('Details', 'wp-panel-optimizer'); ?></summary>
                                        <div class="wpp-more__body">
                                            <p><?php echo esc_html__('Logged-in users never get cached pages. Common dynamic requests such as shopping carts are also excluded from the cache by server rules.', 'wp-panel-optimizer'); ?></p>
                                            <p><?php echo esc_html__('If the front end does not update immediately after saving content, use "Clear Nginx cache" below. If a CDN is also in use, its cache must be cleared at the CDN provider as well.', 'wp-panel-optimizer'); ?></p>
                                        </div>
                                    </details>
                                </div>
                            </div>
                        </div>
                    </section>

                    <section class="wpp-section">
                        <header class="wpp-section__head">
                            <h2 class="wpp-section__title"><span class="dashicons dashicons-clock" aria-hidden="true"></span><?php echo esc_html__('Cache lifetime', 'wp-panel-optimizer'); ?></h2>
                            <p class="wpp-section__desc"><?php echo esc_html__('How long a cached copy is kept before it is regenerated automatically.', 'wp-panel-optimizer'); ?></p>
                        </header>
                        <div class="wpp-section__body">
                            <div class="wpp-field">
                                <div class="wpp-field__head">
                                    <label class="wpp-field__title" for="wpp-fcache-ttl"><?php echo esc_html__('Cache lifetime', 'wp-panel-optimizer'); ?></label>
                                    <span class="wpp-field__summary"><?php echo esc_html__('Regenerated automatically when it expires; no manual cleanup is required.', 'wp-panel-optimizer'); ?></span>
                                </div>
                                <div class="wpp-field__body">
                                    <input id="wpp-fcache-ttl" name="fcache_ttl" type="number" class="wpp-input wpp-input--num" value="<?php echo esc_attr($fcacheTTL); ?>" min="10" max="86400">
                                    <p class="wpp-field__note"><?php echo sprintf(esc_html__('%1$s300%2$s seconds equal 5 minutes. For most sites, %1$s300–3600%2$s seconds works well.', 'wp-panel-optimizer'), '<span class="wpp-key">', '</span>'); ?></p>
                                    <p class="wpp-field__note"><?php echo esc_html__('Longer lifetimes reduce server load; the cache can still be cleared automatically or manually after content updates.', 'wp-panel-optimizer'); ?></p>
                                </div>
                            </div>
                        </div>
                    </section>

                    <section class="wpp-section">
                        <header class="wpp-section__head">
                            <h2 class="wpp-section__title"><span class="dashicons dashicons-update" aria-hidden="true"></span><?php echo esc_html__('Cache preload', 'wp-panel-optimizer'); ?></h2>
                            <p class="wpp-section__desc"><?php echo esc_html__('After the cache is cleared, the plugin visits commonly used pages so they are already cached for the next visitor.', 'wp-panel-optimizer'); ?></p>
                        </header>
                        <div class="wpp-section__body">
                            <div class="wpp-field">
                                <div class="wpp-field__head">
                                    <label class="wpp-field__title" for="wpp-preload-enabled"><?php echo esc_html__('Preload automatically after cache clear', 'wp-panel-optimizer'); ?></label>
                                    <span class="wpp-field__summary"><?php echo esc_html__('Processed slowly in the background, never hitting many pages at once.', 'wp-panel-optimizer'); ?></span>
                                </div>
                                <div class="wpp-field__body">
                                    <label class="wpp-switch">
                                        <input id="wpp-preload-enabled" name="preload_enabled" type="checkbox" value="1" <?php checked($preloadEnabled); ?>>
                                        <span class="wpp-switch__track"><span class="wpp-switch__thumb"></span></span>
                                        <span class="wpp-switch__text"><?php echo esc_html__('Enable automatic preload', 'wp-panel-optimizer'); ?></span>
                                    </label>
                                    <p class="wpp-hint" id="wpp-preload-requires-cache" <?php echo $fcacheEnabled ? 'style="display:none"' : ''; ?>>
                                        <span class="dashicons dashicons-info" aria-hidden="true"></span>
                                        <?php echo sprintf(esc_html__('Preloading only takes effect after the %1$sFastCGI cache%2$s above is enabled.', 'wp-panel-optimizer'), '<strong>', '</strong>'); ?>
                                    </p>
                                    <ul class="wpp-points">
                                        <li><span class="wpp-points__label"><?php echo esc_html__('What it does', 'wp-panel-optimizer'); ?></span><?php echo esc_html__('Visits public pages automatically to build their cache.', 'wp-panel-optimizer'); ?></li>
                                        <li><span class="wpp-points__label"><?php echo esc_html__('Scope', 'wp-panel-optimizer'); ?></span><?php echo esc_html__('Prioritizes the home page and recently updated public content; it does not crawl the whole site.', 'wp-panel-optimizer'); ?></li>
                                        <li><span class="wpp-points__label"><?php echo esc_html__('Other pages', 'wp-panel-optimizer'); ?></span><?php echo esc_html__('These pages are cached the first time someone visits them.', 'wp-panel-optimizer'); ?></li>
                                    </ul>
                                </div>
                            </div>
                            <div class="wpp-field">
                                <div class="wpp-field__head">
                                    <label class="wpp-field__title" for="wpp-preload-limit"><?php echo esc_html__('Max URLs per preload run', 'wp-panel-optimizer'); ?></label>
                                    <span class="wpp-field__summary"><?php echo esc_html__('The maximum number of URLs processed in a single preload run.', 'wp-panel-optimizer'); ?></span>
                                </div>
                                <div class="wpp-field__body">
                                    <input id="wpp-preload-limit" name="preload_limit" type="number" class="wpp-input wpp-input--num" value="<?php echo esc_attr($preloadLimit); ?>" min="10" max="500">
                                    <p class="wpp-field__note"><?php echo sprintf(esc_html__('Range %1$s10–500%2$s; %1$s100%2$s is a common choice. The home page comes first, followed by recently updated public posts, pages, and term archives.', 'wp-panel-optimizer'), '<span class="wpp-key">', '</span>'); ?></p>
                                </div>
                            </div>
                        </div>
                    </section>
                </div>

                <div class="wpp-tab-panel" data-tab-panel="image" style="display:none">
                    <?php if (!$imageEnvReady): ?>
                        <div class="wpp-section">
                            <div class="wpp-section__body">
                                <div class="notice notice-error" style="margin-top:16px"><p><strong><?php echo esc_html__('Image processing is unavailable: the server is missing the exif extension.', 'wp-panel-optimizer'); ?></strong> <?php echo sprintf(esc_html__('This feature relies on the PHP %1$sexif%2$s extension to correct photo orientation. Once the panel has installed it, refresh this page to start using it.', 'wp-panel-optimizer'), '<code>', '</code>'); ?></p></div>
                            </div>
                        </div>
                    <?php endif; ?>
                    <section class="wpp-section">
                        <header class="wpp-section__head">
                            <h2 class="wpp-section__title"><span class="dashicons dashicons-upload" aria-hidden="true"></span><?php echo esc_html__('Image handling for new uploads', 'wp-panel-optimizer'); ?></h2>
                            <p class="wpp-section__desc"><?php echo esc_html__('Chooses how newly uploaded images are processed; published content is not affected.', 'wp-panel-optimizer'); ?></p>
                        </header>
                        <div class="wpp-section__body">
                            <div class="wpp-field">
                                <div class="wpp-field__head">
                                    <span class="wpp-field__title"><?php echo esc_html__('Processing mode', 'wp-panel-optimizer'); ?></span>
                                    <span class="wpp-field__summary"><?php echo esc_html__('Compress or convert on upload to reduce file size.', 'wp-panel-optimizer'); ?></span>
                                </div>
                                <div class="wpp-field__body">
                                    <div class="wpp-choice">
                                        <label class="wpp-choice__item">
                                            <input id="wpp-image-mode-off" type="radio" name="image_mode" value="off" <?php checked($imageMode, self::IMAGE_MODE_OFF); ?> <?php disabled(!$imageEnvReady); ?>>
                                            <span class="wpp-choice__text">
                                                <span class="wpp-choice__label"><?php echo esc_html__('Off', 'wp-panel-optimizer'); ?></span>
                                                <span class="wpp-choice__desc"><?php echo esc_html__('Keep the default WordPress behavior: no conversion or compression on upload.', 'wp-panel-optimizer'); ?></span>
                                            </span>
                                        </label>
                                        <label class="wpp-choice__item">
                                            <input id="wpp-image-mode-optimize" type="radio" name="image_mode" value="optimize" <?php checked($imageMode, self::IMAGE_MODE_OPTIMIZE); ?> <?php disabled(!$imageEnvReady); ?>>
                                            <span class="wpp-choice__text">
                                                <span class="wpp-choice__label"><?php echo esc_html__('Compression', 'wp-panel-optimizer'); ?></span>
                                                <span class="wpp-choice__desc"><?php echo esc_html__('JPEG files are compressed at the selected quality, while PNG files are optimized losslessly. File formats and URLs remain unchanged.', 'wp-panel-optimizer'); ?></span>
                                            </span>
                                        </label>
                                        <label class="wpp-choice__item">
                                            <input id="wpp-image-mode-webp" type="radio" name="image_mode" value="webp" <?php checked($imageMode, self::IMAGE_MODE_WEBP); ?> <?php disabled(!$imageEnvReady); ?>>
                                            <span class="wpp-choice__text">
                                                <span class="wpp-choice__label"><?php echo esc_html__('WebP conversion', 'wp-panel-optimizer'); ?></span>
                                                <span class="wpp-choice__desc"><?php echo esc_html__('Convert to smaller WebP files and remove the original. Current versions of Chrome, Edge, Firefox, Safari, and other major browsers all display WebP.', 'wp-panel-optimizer'); ?></span>
                                            </span>
                                        </label>
                                    </div>
                                    <div class="wpp-input-group">
                                        <span class="wpp-input-group__item">
                                            <label for="wpp-image-jpeg-quality"><?php echo esc_html__('JPEG quality', 'wp-panel-optimizer'); ?></label>
                                            <input id="wpp-image-jpeg-quality" type="number" name="image_jpeg_quality" class="wpp-input wpp-input--short" value="<?php echo esc_attr($imageJpegQuality); ?>" min="1" max="100" <?php disabled(!$imageEnvReady); ?>>
                                        </span>
                                        <span class="wpp-input-group__item">
                                            <label for="wpp-image-webp-quality"><?php echo esc_html__('WebP quality', 'wp-panel-optimizer'); ?></label>
                                            <input id="wpp-image-webp-quality" type="number" name="image_webp_quality" class="wpp-input wpp-input--short" value="<?php echo esc_attr($imageWebpQuality); ?>" min="1" max="100" <?php disabled(!$imageEnvReady); ?>>
                                        </span>
                                    </div>
                                    <?php if ($imageMode === self::IMAGE_MODE_WEBP): ?>
                                        <p class="wpp-field__note"><?php echo sprintf(esc_html__('Modern browsers display WebP reliably. WebP mode does %1$snot keep the original%2$s, so test older plugins, email templates, social sharing, and external services that may require JPEG or PNG files.', 'wp-panel-optimizer'), '<strong>', '</strong>'); ?></p>
                                    <?php endif; ?>
                                    <?php if ($imageSkippedCount > 0): ?>
                                        <p class="wpp-field__note"><?php echo esc_html(sprintf(__('%d images could not be converted (unsupported file format, or the converted file would have been larger, etc.); the originals were kept automatically and uploads are unaffected.', 'wp-panel-optimizer'), $imageSkippedCount)); ?></p>
                                    <?php endif; ?>
                                </div>
                            </div>
                        </div>
                    </section>

                    <section class="wpp-section">
                        <header class="wpp-section__head">
                            <h2 class="wpp-section__title"><span class="dashicons dashicons-images-alt2" aria-hidden="true"></span><?php echo esc_html__('Optimize existing Media Library images', 'wp-panel-optimizer'); ?></h2>
                            <p class="wpp-section__desc"><?php echo esc_html__('Re-encode existing media library images in place to save space.', 'wp-panel-optimizer'); ?></p>
                        </header>
                        <div class="wpp-section__body">
                            <ul class="wpp-points" style="margin-top:16px">
                                <li><span class="wpp-points__label"><?php echo esc_html__('Scope', 'wp-panel-optimizer'); ?></span><?php echo esc_html__('Only existing JPEG and PNG images in the Media Library are processed.', 'wp-panel-optimizer'); ?></li>
                                <li><span class="wpp-points__label"><?php echo esc_html__('Method', 'wp-panel-optimizer'); ?></span><?php echo esc_html__('Lossless in-place re-encoding: file names stay the same, no WebP copies are created, and image references in published content are untouched.', 'wp-panel-optimizer'); ?></li>
                                <li><span class="wpp-points__label"><?php echo esc_html__('Execution', 'wp-panel-optimizer'); ?></span><?php echo esc_html__('The task runs on WP Panel in the background; you can close this page once it starts.', 'wp-panel-optimizer'); ?></li>
                            </ul>
                            <div class="wpp-stats">
                                <div class="wpp-stats__item">
                                    <span class="wpp-stats__label"><?php echo esc_html__('Job status', 'wp-panel-optimizer'); ?></span>
                                    <span class="wpp-stats__value"><span id="wpp-image-batch-status" class="wpp-task-status"><?php echo esc_html__('Idle', 'wp-panel-optimizer'); ?></span></span>
                                </div>
                                <div class="wpp-stats__item">
                                    <span class="wpp-stats__label"><?php echo esc_html__('Total saved', 'wp-panel-optimizer'); ?></span>
                                    <span class="wpp-stats__value" id="wpp-image-batch-lifetime-value">—</span>
                                </div>
                            </div>
                            <div class="wpp-meter" id="wpp-image-batch-meter" style="display:none"><div class="wpp-meter__fill" id="wpp-image-batch-fill"></div></div>
                            <p class="wpp-field__note" id="wpp-image-batch-progress" style="display:none"></p>
                            <p class="wpp-field__note" id="wpp-image-batch-lifetime" style="display:none"></p>
                            <div class="wpp-ops">
                                <button type="button" id="wpp-image-batch-start" class="button button-primary"><?php echo esc_html__('Start batch optimization', 'wp-panel-optimizer'); ?></button>
                                <button type="button" id="wpp-image-batch-stop" class="button" style="display:none"><?php echo esc_html__('Stop', 'wp-panel-optimizer'); ?></button>
                                <p class="wpp-ops__note"><?php echo esc_html__('Speed is controlled by the panel; large libraries can take a while.', 'wp-panel-optimizer'); ?></p>
                            </div>
                        </div>
                    </section>
                </div>

                <div class="wpp-tab-panel" data-tab-panel="security" style="display:none">
                    <?php if ($fileLockEnabled): ?>
                        <p class="wpp-statusline is-active"><span class="dashicons dashicons-lock" aria-hidden="true"></span><?php echo esc_html__('File protection is enabled. Settings that modify wp-config.php are read-only; other settings and actions remain available.', 'wp-panel-optimizer'); ?></p>
                    <?php endif; ?>
                    <section class="wpp-section">
                        <header class="wpp-section__head">
                            <h2 class="wpp-section__title"><span class="dashicons dashicons-lock" aria-hidden="true"></span><?php echo esc_html__('WordPress hardening', 'wp-panel-optimizer'); ?></h2>
                            <p class="wpp-section__desc"><?php echo esc_html__('Reduce unnecessary WordPress access points while keeping security settings synchronized with WP Panel.', 'wp-panel-optimizer'); ?></p>
                        </header>
                        <div class="wpp-section__body">
                            <div class="wpp-field">
                                <div class="wpp-field__head">
                                    <label class="wpp-field__title" for="wpp-no-updates"><?php echo esc_html__('Disable update checks', 'wp-panel-optimizer'); ?></label>
                                    <span class="wpp-field__summary"><?php echo esc_html__('Hides update notices in the WordPress dashboard. Designed for business sites that receive infrequent maintenance.', 'wp-panel-optimizer'); ?></span>
                                </div>
                                <div class="wpp-field__body">
                                    <label class="wpp-switch">
                                        <input id="wpp-no-updates" name="no_updates" type="checkbox" value="1" <?php checked($noUpdates); ?> <?php disabled($fileLockEnabled); ?>>
                                        <span class="wpp-switch__track"><span class="wpp-switch__thumb"></span></span>
                                        <span class="wpp-switch__text"><?php echo esc_html__('Block update checks for core, plugins, and themes', 'wp-panel-optimizer'); ?></span>
                                    </label>
                                    <ul class="wpp-points">
                                        <li><span class="wpp-points__label"><?php echo esc_html__('When enabled', 'wp-panel-optimizer'); ?></span><?php echo esc_html__('WordPress no longer checks for or displays update notices for core, plugins, and themes.', 'wp-panel-optimizer'); ?></li>
                                        <li class="is-warn"><span class="wpp-points__label"><?php echo esc_html__('Security risk', 'wp-panel-optimizer'); ?></span><?php echo esc_html__('Skipping updates for a long time leaves known vulnerabilities unpatched. Do not treat "no notices" as "no updates needed".', 'wp-panel-optimizer'); ?></li>
                                        <li><span class="wpp-points__label"><?php echo esc_html__('Recommendation', 'wp-panel-optimizer'); ?></span><?php echo esc_html__('Turn this setting off periodically to check for updates, or manage updates from "WP Overview" in WP Panel. Re-enable the site file lock when maintenance is complete.', 'wp-panel-optimizer'); ?></li>
                                    </ul>
                                </div>
                            </div>
                            <div class="wpp-field">
                                <div class="wpp-field__head">
                                    <label class="wpp-field__title" for="wpp-no-file-edit"><?php echo esc_html__('Disable file editing', 'wp-panel-optimizer'); ?></label>
                                    <span class="wpp-field__summary"><?php echo esc_html__('Removes the dashboard code editors, reducing the chance of injected code.', 'wp-panel-optimizer'); ?></span>
                                </div>
                                <div class="wpp-field__body">
                                    <label class="wpp-switch">
                                        <input id="wpp-no-file-edit" name="no_file_edit" type="checkbox" value="1" <?php checked($noFileEdit); ?> <?php disabled($fileLockEnabled); ?>>
                                        <span class="wpp-switch__track"><span class="wpp-switch__thumb"></span></span>
                                        <span class="wpp-switch__text"><?php echo esc_html__('Prevent editing theme and plugin files in the dashboard', 'wp-panel-optimizer'); ?></span>
                                    </label>
                                    <p class="wpp-field__note"><?php echo esc_html__('Recommended. It only disables the built-in dashboard code editors; post editing and media uploads are not affected.', 'wp-panel-optimizer'); ?></p>
                                </div>
                            </div>
                            <div class="wpp-field">
                                <div class="wpp-field__head">
                                    <label class="wpp-field__title" for="wpp-wp-debug"><?php echo esc_html__('Enable debug mode', 'wp-panel-optimizer'); ?></label>
                                    <span class="wpp-field__summary"><?php echo esc_html__('Logs PHP errors to a file for troubleshooting.', 'wp-panel-optimizer'); ?></span>
                                </div>
                                <div class="wpp-field__body">
                                    <label class="wpp-switch">
                                        <input id="wpp-wp-debug" name="wp_debug" type="checkbox" value="1" <?php checked($wpDebug); ?> <?php disabled($fileLockEnabled); ?>>
                                        <span class="wpp-switch__track"><span class="wpp-switch__thumb"></span></span>
                                        <span class="wpp-switch__text"><?php echo esc_html__('Enable the debug log', 'wp-panel-optimizer'); ?></span>
                                    </label>
                                    <ul class="wpp-points">
                                        <li><span class="wpp-points__label"><?php echo esc_html__('Use', 'wp-panel-optimizer'); ?></span><?php echo esc_html__('Diagnose white screens, 500 errors, or plugin faults.', 'wp-panel-optimizer'); ?></li>
                                        <li class="is-warn"><span class="wpp-points__label"><?php echo esc_html__('Note', 'wp-panel-optimizer'); ?></span><?php echo esc_html__('Enable only temporarily while troubleshooting; turn it off afterwards.', 'wp-panel-optimizer'); ?></li>
                                    </ul>
                                    <details class="wpp-more">
                                        <summary><?php echo esc_html__('More details', 'wp-panel-optimizer'); ?></summary>
                                        <div class="wpp-more__body">
                                            <p><?php echo sprintf(esc_html__('Errors are written to %1$s and are not shown to visitors by default. To display errors on the page temporarily, configure it on the site details page in WP Panel.', 'wp-panel-optimizer'), '<code>wp-content/debug.log</code>'); ?></p>
                                        </div>
                                    </details>
                                </div>
                            </div>
                        </div>
                    </section>

                    <section class="wpp-section">
                        <header class="wpp-section__head">
                            <h2 class="wpp-section__title"><span class="dashicons dashicons-database" aria-hidden="true"></span><?php echo esc_html__('Resources & database', 'wp-panel-optimizer'); ?></h2>
                        </header>
                        <div class="wpp-section__body">
                            <div class="wpp-field">
                                <div class="wpp-field__head">
                                    <label class="wpp-field__title" for="wpp-post-revisions"><?php echo esc_html__('Post revisions', 'wp-panel-optimizer'); ?></label>
                                    <span class="wpp-field__summary"><?php echo esc_html__('Limit how many historical revisions each post keeps.', 'wp-panel-optimizer'); ?></span>
                                </div>
                                <div class="wpp-field__body">
                                    <input id="wpp-post-revisions" name="post_revisions" type="number" class="wpp-input wpp-input--num" value="<?php echo esc_attr($postRevisions >= 0 ? $postRevisions : ''); ?>" min="-1" placeholder="<?php echo esc_attr__('Default', 'wp-panel-optimizer'); ?>" <?php disabled($fileLockEnabled); ?>>
                                    <ul class="wpp-points">
                                        <li><span class="wpp-points__label"><?php echo esc_html__('Purpose', 'wp-panel-optimizer'); ?></span><?php echo esc_html__('Keeps past versions of a post so accidental edits can be restored.', 'wp-panel-optimizer'); ?></li>
                                        <li><span class="wpp-points__label"><?php echo esc_html__('Recommendation', 'wp-panel-optimizer'); ?></span><?php echo sprintf(esc_html__('A value of %1$s3–5%2$s works for most sites; leave empty for no limit.', 'wp-panel-optimizer'), '<strong>', '</strong>'); ?></li>
                                        <li class="is-warn"><span class="wpp-points__label"><?php echo esc_html__('Entering 0', 'wp-panel-optimizer'); ?></span><?php echo esc_html__('Disables revisions entirely; accidental edits can no longer be restored from revision history.', 'wp-panel-optimizer'); ?></li>
                                    </ul>
                                </div>
                            </div>
                            <div class="wpp-field">
                                <div class="wpp-field__head">
                                    <label class="wpp-field__title" for="wpp-memory-limit"><?php echo esc_html__('WordPress memory limit', 'wp-panel-optimizer'); ?></label>
                                    <span class="wpp-field__summary"><?php echo esc_html__('Adjust only when memory exhaustion errors appear.', 'wp-panel-optimizer'); ?></span>
                                </div>
                                <div class="wpp-field__body">
                                    <input id="wpp-memory-limit" name="memory_limit" type="text" class="wpp-input wpp-input--num" value="<?php echo esc_attr($memoryLimit); ?>" placeholder="<?php echo esc_attr__('Default 40M', 'wp-panel-optimizer'); ?>" <?php disabled($fileLockEnabled); ?>>
                                    <p class="wpp-field__note"><?php echo sprintf(esc_html__('Current PHP memory limit on this server: %1$s. If you are unsure, leave this blank. If more memory is needed, try %2$s or %3$s.', 'wp-panel-optimizer'), '<strong>' . esc_html($phpMemoryLimit) . '</strong>', '<strong>128M</strong>', '<strong>256M</strong>'); ?></p>
                                    <details class="wpp-more">
                                        <summary><?php echo esc_html__('More details', 'wp-panel-optimizer'); ?></summary>
                                        <div class="wpp-more__body">
                                            <p><?php echo esc_html__('Increase this if you see "Allowed memory size exhausted" errors or memory-related white screens in the dashboard.', 'wp-panel-optimizer'); ?></p>
                                            <p><?php echo esc_html__('The value here cannot exceed the PHP memory limit shown above, which comes from the effective configuration in "Software Management" in WP Panel.', 'wp-panel-optimizer'); ?></p>
                                        </div>
                                    </details>
                                </div>
                            </div>
                        </div>
                    </section>

                    <section class="wpp-section">
                        <header class="wpp-section__head">
                            <h2 class="wpp-section__title"><span class="dashicons dashicons-admin-network" aria-hidden="true"></span><?php echo esc_html__('Security features managed by WP Panel', 'wp-panel-optimizer'); ?></h2>
                            <p class="wpp-section__desc"><?php echo esc_html__('This plugin displays these settings as read-only. They can be changed only in WP Panel, so WordPress administrators cannot weaken server security policies.', 'wp-panel-optimizer'); ?></p>
                        </header>
                        <div class="wpp-section__body">
                            <div class="wpp-field">
                                <div class="wpp-field__head">
                                    <span class="wpp-field__title"><?php echo esc_html__('File protection & temporary maintenance', 'wp-panel-optimizer'); ?></span>
                                    <span class="wpp-field__summary"><?php echo esc_html__('Helps prevent accidental or malicious changes to plugins, themes, and other site code.', 'wp-panel-optimizer'); ?></span>
                                </div>
                                <div class="wpp-field__body">
                                    <div class="wpp-locked">
                                        <span class="wpp-locked__value <?php echo $fileLockEnabled ? 'is-ok' : 'is-warn'; ?>"><?php echo $fileLockEnabled ? esc_html__('File protection enabled', 'wp-panel-optimizer') : esc_html__('File protection not enabled', 'wp-panel-optimizer'); ?></span>
                                        <span class="wpp-locked__hint"><?php echo esc_html__('Configured in WP Panel → Site details → File protection & temporary maintenance', 'wp-panel-optimizer'); ?></span>
                                    </div>
                                    <ul class="wpp-points">
                                        <li><span class="wpp-points__label"><?php echo esc_html__('When enabled', 'wp-panel-optimizer'); ?></span><?php echo esc_html__('Writing posts, editing pages, and uploading images are unaffected; installing, updating, or modifying program files is restricted.', 'wp-panel-optimizer'); ?></li>
                                        <li><span class="wpp-points__label"><?php echo esc_html__('During maintenance', 'wp-panel-optimizer'); ?></span><?php echo esc_html__('Use the maintenance password configured in the panel to unlock temporarily, then relock the site as soon as maintenance is complete.', 'wp-panel-optimizer'); ?></li>
                                    </ul>
                                    <?php if ($showMaintenance): ?>
                                        <button type="button" class="button wpp-managed-action" data-wpp-maintenance-open><span class="dashicons dashicons-lock" aria-hidden="true"></span><?php echo esc_html__('View temporary maintenance', 'wp-panel-optimizer'); ?></button>
                                    <?php endif; ?>
                                </div>
                            </div>
                            <div class="wpp-field">
                                <div class="wpp-field__head">
                                    <span class="wpp-field__title"><?php echo esc_html__('XML-RPC interface', 'wp-panel-optimizer'); ?></span>
                                    <span class="wpp-field__summary"><?php echo esc_html__('A legacy remote connection API; most ordinary sites do not need it.', 'wp-panel-optimizer'); ?></span>
                                </div>
                                <div class="wpp-field__body">
                                    <div class="wpp-locked">
                                        <span class="wpp-locked__value <?php echo $xmlrpcEnabled ? 'is-warn' : 'is-ok'; ?>"><?php echo $xmlrpcEnabled ? esc_html__('Enabled', 'wp-panel-optimizer') : esc_html__('Disabled', 'wp-panel-optimizer'); ?></span>
                                        <span class="wpp-locked__hint"><?php echo esc_html__('Changed in WP Panel → Site details → WordPress optimization', 'wp-panel-optimizer'); ?></span>
                                    </div>
                                    <ul class="wpp-points">
                                        <li><span class="wpp-points__label"><?php echo esc_html__('Recommendation', 'wp-panel-optimizer'); ?></span><?php echo esc_html__('Keep it off unless you use Jetpack, an older mobile app, or remote publishing tools.', 'wp-panel-optimizer'); ?></li>
                                        <li class="is-warn"><span class="wpp-points__label"><?php echo esc_html__('When to enable', 'wp-panel-optimizer'); ?></span><?php echo esc_html__('Enable only when a tool you rely on clearly cannot connect.', 'wp-panel-optimizer'); ?></li>
                                    </ul>
                                </div>
                            </div>
                            <div class="wpp-field">
                                <div class="wpp-field__head">
                                    <span class="wpp-field__title"><?php echo esc_html__('WordPress application passwords', 'wp-panel-optimizer'); ?></span>
                                    <span class="wpp-field__summary"><?php echo esc_html__('Let mobile apps, auto-publishing tools, and third-party services connect to your site.', 'wp-panel-optimizer'); ?></span>
                                </div>
                                <div class="wpp-field__body">
                                    <div class="wpp-locked">
                                        <span class="wpp-locked__value <?php echo $applicationPasswordsDisabled ? 'is-ok' : 'is-idle'; ?>"><?php echo $applicationPasswordsDisabled ? esc_html__('Disabled', 'wp-panel-optimizer') : esc_html__('Allowed', 'wp-panel-optimizer'); ?></span>
                                        <span class="wpp-locked__hint"><?php echo esc_html__('Changed in WP Panel → Site details → WordPress optimization', 'wp-panel-optimizer'); ?></span>
                                    </div>
                                    <ul class="wpp-points">
                                        <li><span class="wpp-points__label"><?php echo esc_html__('Recommendation', 'wp-panel-optimizer'); ?></span><?php echo esc_html__('Keep disabled on ordinary sites; enable when an external tool needs to connect.', 'wp-panel-optimizer'); ?></li>
                                        <li><span class="wpp-points__label"><?php echo esc_html__('Unaffected', 'wp-panel-optimizer'); ?></span><?php echo esc_html__('Disabling does not affect dashboard login, writing posts, or uploading images.', 'wp-panel-optimizer'); ?></li>
                                        <li><span class="wpp-points__label"><?php echo esc_html__('Existing passwords', 'wp-panel-optimizer'); ?></span><?php echo esc_html__('Disabling does not delete existing credentials; they keep working once re-enabled.', 'wp-panel-optimizer'); ?></li>
                                    </ul>
                                </div>
                            </div>
                            <div class="wpp-field">
                                <div class="wpp-field__head">
                                    <span class="wpp-field__title"><?php echo esc_html__('WordPress anomaly monitoring', 'wp-panel-optimizer'); ?></span>
                                    <span class="wpp-field__summary"><?php echo esc_html__('Periodically checks for changes to administrators, content, security settings, and unusual persisted database objects.', 'wp-panel-optimizer'); ?></span>
                                </div>
                                <div class="wpp-field__body">
                                    <div class="wpp-locked">
                                        <span class="wpp-locked__value <?php echo $anomalyMonitorStatus === 'active' ? 'is-ok' : ($anomalyMonitorStatus === 'error' ? 'is-warn' : 'is-idle'); ?>"><?php echo esc_html($anomalyLabels[$anomalyMonitorStatus]); ?></span>
                                        <span class="wpp-locked__hint"><?php echo esc_html__('Configured and reviewed in WP Panel → Site details → WordPress anomaly monitoring', 'wp-panel-optimizer'); ?></span>
                                    </div>
                                    <p class="wpp-field__note"><?php echo esc_html__('Anomalies are recorded and notified by the panel; files, users, or content are never deleted automatically.', 'wp-panel-optimizer'); ?></p>
                                </div>
                            </div>
                            <div class="wpp-field">
                                <div class="wpp-field__head">
                                    <span class="wpp-field__title"><?php echo esc_html__('Password reset policy', 'wp-panel-optimizer'); ?></span>
                                    <span class="wpp-field__summary"><?php echo esc_html__('Controls which users may reset their login password via "Lost your password?".', 'wp-panel-optimizer'); ?></span>
                                </div>
                                <div class="wpp-field__body">
                                    <div class="wpp-locked">
                                        <span class="wpp-locked__value <?php echo $passwordResetMode === 'allow' ? 'is-idle' : 'is-ok'; ?>"><?php echo esc_html($passwordResetLabels[$passwordResetMode]); ?></span>
                                        <span class="wpp-locked__hint"><?php echo esc_html__('Changed in WP Panel → Site details → Password reset protection', 'wp-panel-optimizer'); ?></span>
                                    </div>
                                    <p class="wpp-field__note"><?php echo esc_html__('Restricting resets reduces the risk of malicious admin password resets; a forgotten password then requires another administrator or the panel owner to recover.', 'wp-panel-optimizer'); ?></p>
                                </div>
                            </div>
                        </div>
                    </section>
                </div>

                <div class="wpp-tab-panel" data-tab-panel="about" style="display:none">
                    <section class="wpp-section">
                        <header class="wpp-section__head">
                            <h2 class="wpp-section__title"><span class="dashicons dashicons-admin-links" aria-hidden="true"></span><?php echo esc_html__('About & panel sync', 'wp-panel-optimizer'); ?></h2>
                        </header>
                        <div class="wpp-section__body">
                            <p class="wpp-about-lead"><?php echo esc_html__('WP Panel Optimizer is the companion plugin installed automatically by WP Panel. It connects caching, image handling, and common WordPress settings to the panel for centralized management.', 'wp-panel-optimizer'); ?></p>
                            <div class="wpp-grid">
                                <div class="wpp-panel-card">
                                    <div class="wpp-panel-card__head">
                                        <span class="dashicons dashicons-admin-site-alt3" aria-hidden="true"></span>
                                        <h3><?php echo esc_html__('Current connection', 'wp-panel-optimizer'); ?></h3>
                                    </div>
                                    <div class="wpp-panel-card__body">
                                        <dl class="wpp-dl">
                                            <div class="wpp-dl__row">
                                                <dt><?php echo esc_html__('Site', 'wp-panel-optimizer'); ?></dt>
                                                <dd><?php echo esc_html($currentDomain); ?></dd>
                                            </div>
                                            <div class="wpp-dl__row">
                                                <dt><?php echo esc_html__('Plugin version', 'wp-panel-optimizer'); ?></dt>
                                                <dd><?php echo esc_html($pluginVersion); ?></dd>
                                            </div>
                                            <div class="wpp-dl__row">
                                                <dt>API Key</dt>
                                                <dd><?php echo esc_html($apiKey ? substr($apiKey, 0, 8) . '...' : __('Not set', 'wp-panel-optimizer')); ?></dd>
                                            </div>
                                            <div class="wpp-dl__row">
                                                <dt><?php echo esc_html__('Connection status', 'wp-panel-optimizer'); ?></dt>
                                                <dd class="<?php echo $missing ? 'is-risk' : 'is-ok'; ?>"><?php echo $missing ? esc_html__('Awaiting configuration', 'wp-panel-optimizer') : esc_html__('Configured', 'wp-panel-optimizer'); ?></dd>
                                            </div>
                                        </dl>
                                        <button type="button" id="wpp-verify-btn" class="button button-primary"><?php echo esc_html__('Verify panel connection', 'wp-panel-optimizer'); ?></button>
                                        <div id="wpp-verify-msg" aria-live="polite"></div>
                                    </div>
                                </div>
                                <div class="wpp-panel-card">
                                    <div class="wpp-panel-card__head">
                                        <span class="dashicons dashicons-lock" aria-hidden="true"></span>
                                        <h3><?php echo esc_html__('Credentials & updates', 'wp-panel-optimizer'); ?></h3>
                                    </div>
                                    <div class="wpp-panel-card__body">
                                        <p><?php echo esc_html__('The API key is the dedicated credential this plugin uses to connect to WP Panel; it is generated and stored by the panel automatically. Only the first 8 characters are shown here; the full key cannot be viewed or changed.', 'wp-panel-optimizer'); ?></p>
                                        <p><?php echo esc_html__('The plugin is installed and updated automatically by WP Panel; there is no need to download it separately from the WordPress dashboard.', 'wp-panel-optimizer'); ?></p>
                                        <p><?php echo esc_html__('"Verify panel connection" checks that the plugin can read and save settings properly; it is not a security scan of the site.', 'wp-panel-optimizer'); ?></p>
                                    </div>
                                </div>
                                <div class="wpp-panel-card">
                                    <div class="wpp-panel-card__head">
                                        <span class="dashicons dashicons-sos" aria-hidden="true"></span>
                                        <h3><?php echo esc_html__('Help & support', 'wp-panel-optimizer'); ?></h3>
                                    </div>
                                    <div class="wpp-panel-card__body">
                                        <p><?php echo esc_html__('Read the WP Panel documentation or report reproducible issues and error messages.', 'wp-panel-optimizer'); ?></p>
                                        <div class="wpp-links">
                                            <a class="button" href="https://wp-panel.org/" target="_blank" rel="noopener noreferrer"><span class="dashicons dashicons-external" aria-hidden="true"></span><?php echo esc_html__('Visit the WP Panel website', 'wp-panel-optimizer'); ?></a>
                                            <a class="button" href="https://github.com/naibabiji/wp-panel/issues" target="_blank" rel="noopener noreferrer"><span class="dashicons dashicons-editor-help" aria-hidden="true"></span><?php echo esc_html__('Report a bug', 'wp-panel-optimizer'); ?></a>
                                        </div>
                                    </div>
                                </div>
                            </div>
                        </div>
                    </section>
                </div>

                <div class="wpp-actions">
                    <button type="submit" name="wpp_save" class="button button-primary"><?php echo esc_html__('Save settings', 'wp-panel-optimizer'); ?></button>
                    <?php if ($fileLockEnabled): ?>
                        <p class="wpp-actions__hint"><?php echo esc_html__('Available settings can still be saved. Read-only settings remain unchanged until file protection is temporarily unlocked.', 'wp-panel-optimizer'); ?></p>
                    <?php else: ?>
                        <p class="wpp-actions__hint"><?php echo esc_html__('Settings sync to WP Panel after saving; cache-related changes may take a few seconds to take effect.', 'wp-panel-optimizer'); ?></p>
                    <?php endif; ?>
                </div>
            </form>

            <!-- 这几个操作各自独立提交到 admin-post.php，不能嵌套在 wpp-form 里面
                 （HTML 不允许 form 嵌套 form，嵌套会导致浏览器解析时提前把外层
                 form 截断，图片优化等后面标签页的字段和保存按钮都会跟着掉出表单）。
                 用同一个 data-tab-panel="cache" 让标签页切换 JS 一并控制显隐。 -->
            <div class="wpp-tab-panel wpp-cache-status" data-tab-panel="cache">
                <section class="wpp-section">
                    <header class="wpp-section__head">
                        <h2 class="wpp-section__title"><span class="dashicons dashicons-dashboard" aria-hidden="true"></span><?php echo esc_html__('Cache actions & preload status', 'wp-panel-optimizer'); ?></h2>
                        <p class="wpp-section__desc"><?php echo esc_html__('Cache maintenance actions are separate from saving settings and take effect immediately.', 'wp-panel-optimizer'); ?></p>
                    </header>
                    <div class="wpp-section__body">
                        <p class="wpp-statusline <?php echo $preloadRunning ? 'is-active' : ''; ?>">
                            <span class="dashicons <?php echo $preloadRunning ? 'dashicons-update' : 'dashicons-yes-alt'; ?>" aria-hidden="true"></span>
                            <?php echo esc_html($preloadSummary); ?>
                        </p>
                        <div class="wpp-stats">
                            <div class="wpp-stats__item is-<?php echo esc_attr($stateTone); ?>">
                                <span class="wpp-stats__label"><?php echo esc_html__('Current status', 'wp-panel-optimizer'); ?></span>
                                <span class="wpp-stats__value"><?php echo esc_html($preloadRunning ? __('Running', 'wp-panel-optimizer') : __('Idle', 'wp-panel-optimizer')); ?></span>
                            </div>
                            <div class="wpp-stats__item is-<?php echo esc_attr($queueTone); ?>">
                                <span class="wpp-stats__label"><?php echo esc_html__('Queued', 'wp-panel-optimizer'); ?></span>
                                <span class="wpp-stats__value"><?php echo $preloadQueued; ?></span>
                            </div>
                            <div class="wpp-stats__item is-<?php echo esc_attr($doneTone); ?>">
                                <span class="wpp-stats__label"><?php echo esc_html__('Succeeded', 'wp-panel-optimizer'); ?></span>
                                <span class="wpp-stats__value"><?php echo $preloadDone; ?></span>
                            </div>
                            <div class="wpp-stats__item is-<?php echo esc_attr($failedTone); ?>">
                                <span class="wpp-stats__label"><?php echo esc_html__('Failed', 'wp-panel-optimizer'); ?></span>
                                <span class="wpp-stats__value"><?php echo $preloadFailed; ?></span>
                            </div>
                        </div>
                        <?php if (!empty($preloadStatus['last_message'])): ?>
                            <p class="wpp-field__note"><?php echo esc_html(sprintf(__('Last message: %s', 'wp-panel-optimizer'), $preloadStatus['last_message'])); ?></p>
                        <?php endif; ?>
                        <p class="wpp-field__note">
                            <?php if (!empty($preloadStatus['started_at'])): ?>
                                <?php echo esc_html__('Started:', 'wp-panel-optimizer'); ?> <?php echo esc_html($preloadStatus['started_at']); ?>　
                            <?php endif; ?>
                            <?php if (!empty($preloadStatus['last_run_at'])): ?>
                                <?php echo esc_html__('Last run:', 'wp-panel-optimizer'); ?> <?php echo esc_html($preloadStatus['last_run_at']); ?>　
                            <?php endif; ?>
                            <?php if (!empty($preloadStatus['finished_at'])): ?>
                                <?php echo esc_html__('Finished:', 'wp-panel-optimizer'); ?> <?php echo esc_html($preloadStatus['finished_at']); ?>
                            <?php endif; ?>
                        </p>

                        <div class="wpp-cache-actions">
                            <div class="wpp-cache-action">
                                <form method="post" action="<?php echo esc_url(admin_url('admin-post.php')); ?>">
                                    <?php wp_nonce_field('wpp_cache_clear'); ?>
                                    <input type="hidden" name="action" value="wpp_cache_clear">
                                    <button type="submit" class="button button-primary" <?php disabled($missing); ?>><?php echo esc_html__('Clear Nginx cache', 'wp-panel-optimizer'); ?></button>
                                </form>
                                <p><?php echo esc_html__('Use when content does not refresh promptly; if a CDN is in use, its cache must be cleared too.', 'wp-panel-optimizer'); ?></p>
                            </div>
                            <div class="wpp-cache-action">
                                <form method="post" action="<?php echo esc_url(admin_url('admin-post.php')); ?>">
                                    <?php wp_nonce_field('wpp_cache_preload'); ?>
                                    <input type="hidden" name="action" value="wpp_cache_preload">
                                    <button type="submit" class="button" <?php disabled(!$fcacheEnabled); ?>><?php echo esc_html__('Preload now', 'wp-panel-optimizer'); ?></button>
                                </form>
                                <p class="wpp-ops__reason <?php echo $fcacheEnabled ? '' : 'is-warn'; ?>" id="wpp-preload-reason"><?php echo esc_html($fcacheEnabled ? __('After saving settings, published pages will be preloaded according to the rules.', 'wp-panel-optimizer') : __('Preload unavailable: enable the FastCGI cache first and save.', 'wp-panel-optimizer')); ?></p>
                            </div>
                            <div class="wpp-cache-action">
                                <form method="post" action="<?php echo esc_url(admin_url('admin-post.php')); ?>">
                                    <?php wp_nonce_field('wpp_cache_preload_stop'); ?>
                                    <input type="hidden" name="action" value="wpp_cache_preload_stop">
                                    <button type="submit" class="button" <?php disabled(!$preloadStatus['running']); ?>><?php echo esc_html__('Stop preload', 'wp-panel-optimizer'); ?></button>
                                </form>
                                <p><?php echo esc_html__('You can stop a running task at any time. Pages that were already cached will remain cached.', 'wp-panel-optimizer'); ?></p>
                            </div>
                        </div>

                        <?php if (!empty($log)): ?>
                            <h3 class="wpp-subhead"><?php echo esc_html__('Recent cache clears', 'wp-panel-optimizer'); ?></h3>
                            <div class="wpp-log">
                                <table>
                                    <thead><tr><th><?php echo esc_html__('Time', 'wp-panel-optimizer'); ?></th><th><?php echo esc_html__('Trigger', 'wp-panel-optimizer'); ?></th><th><?php echo esc_html__('Result', 'wp-panel-optimizer'); ?></th></tr></thead>
                                    <tbody>
                                        <?php foreach ($log as $entry): ?>
                                        <tr>
                                            <td><?php echo esc_html($entry['time']); ?></td>
                                            <td><?php
                                                $labels = [
                                                    'manual'  => __('Manual clear', 'wp-panel-optimizer'),
                                                    'auto'    => __('Automatic (post published)', 'wp-panel-optimizer'),
                                                    'comment' => __('Automatic (comment activity)', 'wp-panel-optimizer'),
                                                ];
                                                echo esc_html($labels[$entry['type']] ?? __('Automatic', 'wp-panel-optimizer'));
                                            ?></td>
                                            <td><?php echo !empty($entry['success']) ? '<span class="is-ok">' . esc_html(__('Success', 'wp-panel-optimizer')) . '</span>' : '<span class="is-risk">' . esc_html(__('Failed', 'wp-panel-optimizer')) . '</span>'; ?></td>
                                        </tr>
                                        <?php endforeach; ?>
                                    </tbody>
                                </table>
                            </div>
                        <?php endif; ?>
                    </div>
                </section>
            </div>

            <script>
            // 由 PHP 传入的已翻译字符串（等效 wp_localize_script：本脚本内联在页面中，
            // 直接用 wp_json_encode 输出，避免在 JS 里硬编码用户可见文字）。
            var WPPSettingsL10n = <?php echo wp_json_encode([
                'preloadReasonOk'      => __('After saving settings, published pages will be preloaded according to the rules.', 'wp-panel-optimizer'),
                'preloadReasonBlocked' => __('Preload unavailable: enable the FastCGI cache first and save.', 'wp-panel-optimizer'),
                'verifying'            => __('Verifying…', 'wp-panel-optimizer'),
                'verifyOk'             => __('Connection verified. The WP Panel API authenticated the request and responded successfully.', 'wp-panel-optimizer'),
                'verifyFail'           => __('Connection failed: %s', 'wp-panel-optimizer'),
                'networkError'         => __('Network error: could not reach the panel (%s)', 'wp-panel-optimizer'),
                'unknownError'         => __('Unknown error', 'wp-panel-optimizer'),
                'updatingComponent'    => __('Updating…', 'wp-panel-optimizer'),
                'updateComplete'       => __('Update completed. Reloading…', 'wp-panel-optimizer'),
                'updateFailed'         => __('Update failed: %s', 'wp-panel-optimizer'),
                'statusQueued'         => __('Queued', 'wp-panel-optimizer'),
                'statusRunning'        => __('Running', 'wp-panel-optimizer'),
                'statusSucceeded'      => __('Completed', 'wp-panel-optimizer'),
                'statusFailed'         => __('Failed', 'wp-panel-optimizer'),
                'statusStopped'        => __('Stopped', 'wp-panel-optimizer'),
                'statusNone'           => __('Idle', 'wp-panel-optimizer'),
                'lifetimeSaved'        => __('%s MB saved in total (all batch jobs)', 'wp-panel-optimizer'),
                'progressBase'         => __('Progress: %s / %s', 'wp-panel-optimizer'),
                'progressFailed'       => __(' (%s failed)', 'wp-panel-optimizer'),
                'progressSkipped'      => __('; %s files were already processed earlier and were skipped this run', 'wp-panel-optimizer'),
                'progressSaved'        => __('; %s MB saved this run', 'wp-panel-optimizer'),
                'startFailed'          => __('Start failed: %s', 'wp-panel-optimizer'),
            ]); ?>;
            function wppFmt(str) {
                var args = Array.prototype.slice.call(arguments, 1);
                return String(str).replace(/%([sd%])/g, function(m, t) {
                    if (t === '%') return '%';
                    return args.length ? String(args.shift()) : m;
                });
            }
            // WordPress 的通知关闭按钮只隐藏元素，不会移除地址栏参数。清理本插件的
            // 一次性通知参数，避免用户刷新页面后再次看到已经关闭的提示。
            document.addEventListener('click', function(event) {
                var dismiss = event.target.closest('.notice-dismiss');
                if (!dismiss || !dismiss.closest('.wpp-settings')) return;
                var url = new URL(window.location.href);
                var changed = false;
                ['wpp_cleared', 'wpp_preload', 'count', '_wpnonce'].forEach(function(key) {
                    if (url.searchParams.has(key)) {
                        url.searchParams.delete(key);
                        changed = true;
                    }
                });
                if (changed) {
                    window.history.replaceState({}, '', url.pathname + url.search + url.hash);
                }
            });

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

            // 预加载依赖缓存：原因前置到开关旁，并直接绑定在按钮下方，
            // 不让用户自己去推断"为什么按钮是灰的"。
            (function() {
                var fcache = document.getElementById('wpp-fcache-enabled');
                var hint = document.getElementById('wpp-preload-requires-cache');
                var reason = document.getElementById('wpp-preload-reason');
                if (!fcache) return;
                function sync() {
                    var on = fcache.checked;
                    if (hint) hint.style.display = on ? 'none' : '';
                    if (reason) {
                        reason.textContent = on
                            ? WPPSettingsL10n.preloadReasonOk
                            : WPPSettingsL10n.preloadReasonBlocked;
                        reason.classList.toggle('is-warn', !on);
                    }
                }
                fcache.addEventListener('change', sync);
                sync();
            })();

            function wppNotice(el, type, textContent) {
                var div = document.createElement('div');
                div.className = 'notice notice-' + type;
                var p = document.createElement('p');
                p.textContent = textContent;
                div.appendChild(p);
                el.replaceChildren(div);
            }

            document.getElementById('wpp-verify-btn').addEventListener('click', function() {
                var btn = this, msg = document.getElementById('wpp-verify-msg');
                var label = btn.textContent;
                btn.disabled = true;
                btn.textContent = WPPSettingsL10n.verifying;
                fetch('<?php echo esc_url(admin_url('admin-ajax.php')); ?>?action=wpp_optimizer_verify&_wpnonce=<?php echo esc_attr(wp_create_nonce('wpp_optimizer_settings')); ?>')
                    .then(r => r.json())
                    .then(data => {
                        if (data.success) {
                            wppNotice(msg, 'success', WPPSettingsL10n.verifyOk);
                        } else {
                            wppNotice(msg, 'error', wppFmt(WPPSettingsL10n.verifyFail, data.data?.message || WPPSettingsL10n.unknownError));
                        }
                    })
                    .catch(e => {
                        wppNotice(msg, 'error', wppFmt(WPPSettingsL10n.networkError, e.message));
                    })
                    .finally(() => { btn.disabled = false; btn.textContent = label; });
            });

            var componentUpdateBtn = document.getElementById('wpp-component-update-btn');
            if (componentUpdateBtn) componentUpdateBtn.addEventListener('click', function() {
                var btn = this, msg = document.getElementById('wpp-component-update-msg');
                var label = btn.textContent;
                btn.disabled = true;
                btn.textContent = WPPSettingsL10n.updatingComponent;
                fetch('<?php echo esc_url(admin_url('admin-ajax.php')); ?>?action=wpp_optimizer_update_companion&_wpnonce=<?php echo esc_attr(wp_create_nonce('wpp_optimizer_settings')); ?>', { method: 'POST', credentials: 'same-origin' })
                    .then(r => r.json())
                    .then(data => {
                        if (!data.success) throw new Error(data.data?.message || WPPSettingsL10n.unknownError);
                        wppNotice(msg, 'success', WPPSettingsL10n.updateComplete);
                        window.setTimeout(() => window.location.reload(), 800);
                    })
                    .catch(e => {
                        wppNotice(msg, 'error', wppFmt(WPPSettingsL10n.updateFailed, e.message));
                        btn.disabled = false;
                        btn.textContent = label;
                    });
            });

            (function() {
                var startBtn = document.getElementById('wpp-image-batch-start');
                var stopBtn = document.getElementById('wpp-image-batch-stop');
                var statusEl = document.getElementById('wpp-image-batch-status');
                var progressEl = document.getElementById('wpp-image-batch-progress');
                var lifetimeEl = document.getElementById('wpp-image-batch-lifetime');
                var lifetimeValue = document.getElementById('wpp-image-batch-lifetime-value');
                var meterEl = document.getElementById('wpp-image-batch-meter');
                var fillEl = document.getElementById('wpp-image-batch-fill');
                var nonce = '<?php echo esc_attr(wp_create_nonce('wpp_optimizer_settings')); ?>';
                var ajaxUrl = '<?php echo esc_url(admin_url('admin-ajax.php')); ?>';
                var pollTimer = null;

                function call(action, extra) {
                    var params = new URLSearchParams(Object.assign({ action: action, _wpnonce: nonce }, extra || {}));
                    return fetch(ajaxUrl + '?' + params.toString(), { method: 'POST' }).then(r => r.json());
                }

                var statusLabels = {
                    queued: WPPSettingsL10n.statusQueued,
                    running: WPPSettingsL10n.statusRunning,
                    succeeded: WPPSettingsL10n.statusSucceeded,
                    failed: WPPSettingsL10n.statusFailed,
                    stopped: WPPSettingsL10n.statusStopped,
                    none: WPPSettingsL10n.statusNone
                };

                // LifetimeBytesSaved 是这个站点历史所有批量任务的累计节省量，跟当前
                // 有没有任务在跑无关——单次任务的节省量只统计"这次任务实际处理过的
                // 文件"，跳过的文件不计入，跳过越多，单次数字看起来就越小，容易被
                // 误以为"节省数据消失了"，所以单独展示一个不随任务重置的累计值。
                function renderLifetime(job) {
                    var lifetime = (job && job.LifetimeBytesSaved) || 0;
                    if (lifetime > 0) {
                        lifetimeEl.style.display = '';
                        lifetimeEl.textContent = wppFmt(WPPSettingsL10n.lifetimeSaved, (lifetime / 1024 / 1024).toFixed(2));
                        lifetimeValue.textContent = (lifetime / 1024 / 1024).toFixed(2) + ' MB';
                    } else {
                        lifetimeEl.style.display = 'none';
                        lifetimeValue.textContent = '—';
                    }
                }

                function renderMeter(job) {
                    var total = (job && job.TotalFiles) || 0;
                    var processed = (job && job.ProcessedFiles) || 0;
                    if (!total || total <= 0) {
                        meterEl.style.display = 'none';
                        fillEl.style.width = '0%';
                        return;
                    }
                    meterEl.style.display = '';
                    fillEl.style.width = Math.min(100, Math.max(0, (processed / total) * 100)).toFixed(1) + '%';
                }

                function render(job) {
                    renderLifetime(job);
                    renderMeter(job);
                    if (!job || !job.Status || job.Status === 'none') {
                        statusEl.textContent = WPPSettingsL10n.statusNone;
                        progressEl.style.display = 'none';
                        meterEl.style.display = 'none';
                        startBtn.style.display = '';
                        stopBtn.style.display = 'none';
                        return;
                    }
                    var running = job.Status === 'queued' || job.Status === 'running';
                    statusEl.textContent = statusLabels[job.Status] || job.Status;
                    startBtn.style.display = running ? 'none' : '';
                    stopBtn.style.display = running ? '' : 'none';
                    progressEl.style.display = '';
                    var text = wppFmt(WPPSettingsL10n.progressBase, job.ProcessedFiles || 0, job.TotalFiles || 0);
                    if (job.FailedFiles > 0) text += wppFmt(WPPSettingsL10n.progressFailed, job.FailedFiles);
                    if (job.SkippedFiles > 0) text += wppFmt(WPPSettingsL10n.progressSkipped, job.SkippedFiles);
                    var saved = (job.BytesBefore || 0) - (job.BytesAfter || 0);
                    if (saved > 0) text += wppFmt(WPPSettingsL10n.progressSaved, (saved / 1024 / 1024).toFixed(2));
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
                            statusEl.textContent = wppFmt(WPPSettingsL10n.startFailed, data.data?.message || WPPSettingsL10n.unknownError);
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
        echo '<div class="notice notice-warning"><p><strong>' . esc_html__('WP Panel file lock is enabled.', 'wp-panel-optimizer') . '</strong> ' . esc_html__('Publishing posts, editing pages, and uploading media are unaffected. Protected plugin, theme, code, and site configuration files cannot be changed directly. If an installation, update, or setup task needs to write to these files, use File protection / maintenance in the upper-right corner to unlock the site temporarily with the maintenance password. If temporary maintenance is unavailable, ask the server administrator to handle the task in WP Panel. WP Panel Optimizer is managed by the panel and can still receive its own updates.', 'wp-panel-optimizer') . '</p></div>';
    }

}
