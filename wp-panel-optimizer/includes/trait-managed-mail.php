<?php
/** Managed wp_mail transport and site-local full-content log. */
if (!defined('ABSPATH')) exit;
require_once ABSPATH . WPINC . '/PHPMailer/PHPMailer.php';
require_once ABSPATH . WPINC . '/PHPMailer/SMTP.php';
require_once ABSPATH . WPINC . '/PHPMailer/Exception.php';
require_once ABSPATH . WPINC . '/class-wp-phpmailer.php';

trait WPP_Optimizer_Managed_Mail_Trait {
    const OPTION_MANAGED_MAIL_ENABLED = 'wpp_optimizer_managed_mail_enabled';
    const OPTION_MANAGED_MAIL_LOGGING = 'wpp_optimizer_managed_mail_logging';
    const OPTION_MANAGED_MAIL_RETENTION_DAYS = 'wpp_optimizer_managed_mail_retention_days';
    const OPTION_MANAGED_MAIL_RETENTION_COUNT = 'wpp_optimizer_managed_mail_retention_count';
    const MANAGED_MAIL_CAPABILITY = 'wpp_view_managed_mail_body';

    public static function managed_mail_bootstrap() {
        self::managed_mail_maybe_install();
        if (is_multisite()) return;
        $conflict = self::managed_mail_conflict_plugin();
        $enabled = get_option(self::OPTION_MANAGED_MAIL_ENABLED, '0') === '1';
        if ($enabled || ($conflict === '' && is_admin())) self::managed_mail_report_conflict($conflict);
        if (!$enabled) return;
        if ($conflict !== '') {
            return;
        }
        global $phpmailer;
        $phpmailer = new WPP_Managed_Mailer(true);
        $phpmailer::$validator = static function ($email) { return (bool) is_email($email); };
    }

    public static function managed_mail_init() {
        add_action('admin_menu', [__CLASS__, 'managed_mail_menu']);
        add_action('admin_post_wpp_managed_mail_save', [__CLASS__, 'managed_mail_save']);
        add_action('admin_post_wpp_managed_mail_test', [__CLASS__, 'managed_mail_test']);
        self::managed_mail_maybe_install();
    }

    public static function managed_mail_activate() {
        self::managed_mail_install_table();
        $role = get_role('administrator');
        if ($role) $role->add_cap(self::MANAGED_MAIL_CAPABILITY);
        add_option(self::OPTION_MANAGED_MAIL_ENABLED, '0');
        add_option(self::OPTION_MANAGED_MAIL_LOGGING, '1');
        add_option(self::OPTION_MANAGED_MAIL_RETENTION_DAYS, '7');
        add_option(self::OPTION_MANAGED_MAIL_RETENTION_COUNT, '250');
    }

    private static function managed_mail_table() {
        global $wpdb;
        return $wpdb->prefix . 'wpp_managed_mail_log';
    }

    private static function managed_mail_install_table() {
        global $wpdb;
        require_once ABSPATH . 'wp-admin/includes/upgrade.php';
        $table = self::managed_mail_table();
        $charset = $wpdb->get_charset_collate();
        dbDelta("CREATE TABLE {$table} (
            id bigint(20) unsigned NOT NULL AUTO_INCREMENT,
            request_id varchar(128) NOT NULL,
            status varchar(32) NOT NULL,
            subject text NOT NULL,
            from_name text NULL,
            reply_to longtext NULL,
            recipients longtext NOT NULL,
            headers longtext NOT NULL,
            body longtext NOT NULL,
            error_code varchar(100) NOT NULL DEFAULT '',
            message_id varchar(300) NOT NULL DEFAULT '',
            duration_ms bigint(20) unsigned NOT NULL DEFAULT 0,
            message_size bigint(20) unsigned NOT NULL DEFAULT 0,
            attachments longtext NOT NULL,
            created_at datetime NOT NULL,
            viewed_at datetime NULL,
            viewed_by bigint(20) unsigned NOT NULL DEFAULT 0,
            PRIMARY KEY  (id),
            UNIQUE KEY request_id (request_id),
            KEY created_at (created_at)
        ) {$charset};");
        $hasFromName = $wpdb->get_var("SHOW COLUMNS FROM {$table} LIKE 'from_name'") === 'from_name';
        $hasReplyTo = $wpdb->get_var("SHOW COLUMNS FROM {$table} LIKE 'reply_to'") === 'reply_to';
        if ($hasFromName && $hasReplyTo) update_option('wpp_optimizer_managed_mail_db_version', '2');
    }

    private static function managed_mail_maybe_install() {
        static $checked = false;
        if ($checked) return;
        $checked = true;
        global $wpdb;
        $table = self::managed_mail_table();
        $tableExists = $wpdb->get_var($wpdb->prepare('SHOW TABLES LIKE %s', $wpdb->esc_like($table))) === $table;
        if (get_option('wpp_optimizer_managed_mail_db_version') !== '2' || !$tableExists) {
            self::managed_mail_install_table();
            $role = get_role('administrator');
            if ($role) $role->add_cap(self::MANAGED_MAIL_CAPABILITY);
            add_option(self::OPTION_MANAGED_MAIL_ENABLED, '0');
            add_option(self::OPTION_MANAGED_MAIL_LOGGING, '1');
            add_option(self::OPTION_MANAGED_MAIL_RETENTION_DAYS, '7');
            add_option(self::OPTION_MANAGED_MAIL_RETENTION_COUNT, '250');
        }
    }

    private static function managed_mail_conflict_plugin() {
        $known = [
            'post-smtp/postman-smtp.php', 'wp-mail-smtp/wp_mail_smtp.php',
            'easy-wp-smtp/easy-wp-smtp.php', 'fluent-smtp/fluent-smtp.php',
            'smtp-mailer/main.php', 'gmail-smtp/main.php',
        ];
        $active = (array) get_option('active_plugins', []);
        foreach ($known as $plugin) if (in_array($plugin, $active, true)) return $plugin;
        return '';
    }

    private static function managed_mail_report_conflict($plugin, $force = false) {
        $marker = $plugin === '' ? '__none__' : $plugin;
        $last = get_transient('wpp_managed_mail_conflict_report');
        if (!$force && $last === $marker) return true;
        if (!$force && $last === '__error__:' . $marker) return false;
        $response = self::api_request('POST', '/api/sites/managed-mail/conflict', ['plugin' => $plugin], $force ? 10 : 3);
        if (is_wp_error($response)) {
            set_transient('wpp_managed_mail_conflict_report', '__error__:' . $marker, 5 * MINUTE_IN_SECONDS);
            return false;
        }
        set_transient('wpp_managed_mail_conflict_report', $marker, HOUR_IN_SECONDS);
        return true;
    }

    public static function managed_mail_status() {
        static $status = null;
        static $loaded = false;
        if ($loaded) return $status;
        $loaded = true;
        $response = self::api_request('GET', '/api/sites/managed-mail/status');
        if (is_wp_error($response)) {
            $status = $response;
            return $status;
        }
        $decoded = json_decode($response, true);
        if (empty($decoded['success']) || !isset($decoded['data']) || !is_array($decoded['data'])) {
            $status = new WP_Error('panel_response', 'managed_mail_unavailable');
            return $status;
        }
        $status = $decoded['data'];
        return $status;
    }

    public static function managed_mail_is_authorized() {
        $status = self::managed_mail_status();
        return !is_wp_error($status) && !empty($status['authorized']);
    }

    public static function managed_mail_should_show() {
        $status = self::managed_mail_status();
        return (!is_wp_error($status) && !empty($status['authorized']))
            || (is_wp_error($status) && get_option(self::OPTION_MANAGED_MAIL_ENABLED, '0') === '1');
    }

    public static function managed_mail_dispatch($mailer) {
        $status = self::managed_mail_status();
        if (is_wp_error($status)) throw new \PHPMailer\PHPMailer\Exception('panel_unavailable:' . $status->get_error_message());
        if (empty($status['active'])) {
            // No panel authorization, no site confirmation, or an explicit
            // third-party conflict means managed transport never began.
            if (empty($status['authorized']) || empty($status['site_confirmed']) || !empty($status['conflict_plugin'])) return null;
            // Once both parties opted in, a disabled site, unavailable SMTP,
            // or a policy pause must fail closed instead of escaping quotas
            // and logs through PHP mail().
            $reason = sanitize_key($status['paused_reason'] ?? 'managed_mail_unavailable');
            if ($reason === '') $reason = empty($status['smtp_ready']) ? 'smtp_not_ready' : 'managed_mail_paused';
            $payload = self::managed_mail_log_payload($mailer);
            self::managed_mail_log($payload, [], 'paused', $reason, '', 0, 0);
            throw new \PHPMailer\PHPMailer\Exception('managed_mail_paused:' . $reason);
        }
        $attachments = [];
        $attachmentLog = [];
        $attachmentBytes = 0;
        if (count((array) $mailer->getAttachments()) > 5) throw new \PHPMailer\PHPMailer\Exception('managed_mail_too_many_attachments');
        foreach ((array) $mailer->getAttachments() as $item) {
            $path = isset($item[0]) ? $item[0] : '';
            $name = sanitize_file_name(isset($item[2]) && $item[2] !== '' ? $item[2] : basename((string) $path));
            $isString = !empty($item[5]);
            if ($isString) {
                $content = (string) $path;
            } else {
                if (!is_string($path) || strpos($path, '://') !== false || !is_file($path) || !is_readable($path)) {
                    throw new \PHPMailer\PHPMailer\Exception('managed_mail_attachment_unreadable');
                }
                $size = filesize($path);
                if ($size === false || $size > 10 * MB_IN_BYTES || $attachmentBytes + $size > 10 * MB_IN_BYTES) {
                    throw new \PHPMailer\PHPMailer\Exception('managed_mail_attachment_too_large');
                }
                $content = file_get_contents($path);
                if ($content === false) throw new \PHPMailer\PHPMailer\Exception('managed_mail_attachment_unreadable');
            }
            $attachmentBytes += strlen($content);
            if ($attachmentBytes > 10 * MB_IN_BYTES) throw new \PHPMailer\PHPMailer\Exception('managed_mail_attachment_too_large');
            $attachments[] = [
                'name' => $name, 'content_type' => sanitize_text_field($item[4] ?? 'application/octet-stream'),
                'content_base64' => base64_encode($content), 'inline' => (($item[6] ?? '') === 'inline'),
                'content_id' => sanitize_text_field($item[7] ?? ''),
            ];
            $attachmentLog[] = ['name' => $name, 'type' => sanitize_text_field($item[4] ?? ''), 'size' => strlen($content), 'inline' => (($item[6] ?? '') === 'inline')];
        }
        $headers = [];
        foreach ((array) $mailer->getCustomHeaders() as $header) {
            if (isset($header[0], $header[1])) $headers[(string) $header[0]] = (string) $header[1];
        }
        $fromName = self::managed_mail_from_name($mailer, $status);
        $replyTo = self::managed_mail_addresses($mailer->getReplyToAddresses());
        if (!$replyTo && !empty($status['reply_to'])) $replyTo[] = (string) $status['reply_to'];
        $payload = [
            'request_id' => wp_generate_uuid4(), 'from_name' => $fromName,
            'to' => self::managed_mail_addresses($mailer->getToAddresses()),
            'cc' => self::managed_mail_addresses($mailer->getCcAddresses()),
            'bcc' => self::managed_mail_addresses($mailer->getBccAddresses()),
            'reply_to' => $replyTo,
            'subject' => (string) $mailer->Subject, 'text_body' => (string) $mailer->AltBody,
            // preSend() promotes HTML + AltBody to multipart/alternative, so
            // ContentType alone no longer identifies the original HTML body.
            'html_body' => ((string) $mailer->AltBody !== '' || stripos((string) $mailer->ContentType, 'text/html') !== false) ? (string) $mailer->Body : '',
            // Preserve the API object shape even when no custom headers exist;
            // PHP otherwise serializes an empty array as [] instead of {}.
            'headers' => (object) $headers, 'attachments' => $attachments,
        ];
        if ($payload['html_body'] === '') $payload['text_body'] = (string) $mailer->Body;
        $started = microtime(true);
        $response = self::api_request('POST', '/api/sites/managed-mail/send', $payload, 20);
        if (is_wp_error($response)) {
            $errorMessage = sanitize_key($response->get_error_message());
            $statusMap = [
                'managed_mail_quota_exceeded' => 'quota_exceeded',
                'managed_mail_paused' => 'paused',
                'smtp_plugin_conflict' => 'conflict',
                'panel_authorization_required' => 'unauthorized',
                'site_confirmation_required' => 'unconfirmed',
                'invalid_request' => 'rejected',
            ];
            $logStatus = $statusMap[$errorMessage] ?? 'panel_unavailable';
            self::managed_mail_log($payload, $attachmentLog, $logStatus, $errorMessage ?: $response->get_error_code(), '', intval((microtime(true) - $started) * 1000), 0);
            throw new \PHPMailer\PHPMailer\Exception($response->get_error_message());
        }
        $decoded = json_decode($response, true);
        $result = isset($decoded['data']) && is_array($decoded['data']) ? $decoded['data'] : [];
        $sendStatus = sanitize_key($result['status'] ?? 'temporary_failure');
        self::managed_mail_log($payload, $attachmentLog, $sendStatus, sanitize_key($result['error_code'] ?? ''), sanitize_text_field($result['message_id'] ?? ''), intval($result['duration_ms'] ?? 0), intval($result['message_size'] ?? 0));
        if ($sendStatus !== 'accepted') throw new \PHPMailer\PHPMailer\Exception($sendStatus . ':' . sanitize_key($result['error_code'] ?? ''));
        return true;
    }

    private static function managed_mail_log_payload($mailer) {
        $html = ((string) $mailer->AltBody !== '' || stripos((string) $mailer->ContentType, 'text/html') !== false) ? (string) $mailer->Body : '';
        $status = self::managed_mail_status();
        $replyTo = self::managed_mail_addresses($mailer->getReplyToAddresses());
        if (!$replyTo && !is_wp_error($status) && !empty($status['reply_to'])) $replyTo[] = (string) $status['reply_to'];
        return [
            'request_id' => wp_generate_uuid4(), 'from_name' => self::managed_mail_from_name($mailer, $status),
            'to' => self::managed_mail_addresses($mailer->getToAddresses()),
            'cc' => self::managed_mail_addresses($mailer->getCcAddresses()),
            'bcc' => self::managed_mail_addresses($mailer->getBccAddresses()),
            'reply_to' => $replyTo,
            'subject' => (string) $mailer->Subject,
            'text_body' => $html === '' ? (string) $mailer->Body : (string) $mailer->AltBody,
            'html_body' => $html, 'headers' => (object) [], 'attachments' => [],
        ];
    }

    private static function managed_mail_from_name($mailer, $status) {
        $fromName = trim((string) $mailer->FromName);
        if (!is_wp_error($status) && !empty($status['lock_from_name'])) {
            return (string) ($status['from_name'] ?? '');
        }
        if (!is_wp_error($status) && ($fromName === '' || $fromName === 'WordPress') && !empty($status['from_name'])) {
            return (string) $status['from_name'];
        }
        return $fromName;
    }

    private static function managed_mail_addresses($addresses) {
        $result = [];
        foreach ((array) $addresses as $address) {
            if (empty($address[0])) continue;
            $result[] = !empty($address[1]) ? $address[1] . ' <' . $address[0] . '>' : $address[0];
        }
        return $result;
    }

    private static function managed_mail_log($payload, $attachments, $status, $error, $messageId, $duration, $size) {
        if (get_option(self::OPTION_MANAGED_MAIL_LOGGING, '1') !== '1') return;
        global $wpdb;
        $log = [
            'request_id' => $payload['request_id'], 'status' => $status, 'subject' => $payload['subject'],
            'recipients' => wp_json_encode(['to' => $payload['to'], 'cc' => $payload['cc'], 'bcc' => $payload['bcc']]),
            'headers' => wp_json_encode($payload['headers']),
            'body' => wp_json_encode(['text' => $payload['text_body'], 'html' => $payload['html_body']]),
            'error_code' => $error, 'message_id' => $messageId, 'duration_ms' => max(0, $duration),
            'message_size' => max(0, $size), 'attachments' => wp_json_encode($attachments),
            'created_at' => current_time('mysql', true),
        ];
        if (get_option('wpp_optimizer_managed_mail_db_version') === '2') {
            $log['from_name'] = $payload['from_name'];
            $log['reply_to'] = wp_json_encode($payload['reply_to']);
        }
        $wpdb->replace(self::managed_mail_table(), $log);
        self::managed_mail_cleanup();
    }

    private static function managed_mail_cleanup() {
        global $wpdb;
        $table = self::managed_mail_table();
        $days = max(1, min(30, intval(get_option(self::OPTION_MANAGED_MAIL_RETENTION_DAYS, 7))));
        $count = max(25, min(1000, intval(get_option(self::OPTION_MANAGED_MAIL_RETENTION_COUNT, 250))));
        $wpdb->query($wpdb->prepare("DELETE FROM {$table} WHERE created_at < DATE_SUB(UTC_TIMESTAMP(), INTERVAL %d DAY)", $days));
        $wpdb->query($wpdb->prepare("DELETE FROM {$table} WHERE id NOT IN (SELECT id FROM (SELECT id FROM {$table} ORDER BY id DESC LIMIT %d) kept)", $count));
    }

    public static function managed_mail_menu() {
        if (!self::managed_mail_should_show()) return;
        add_options_page(__('Managed Email', 'wp-panel-optimizer'), __('Managed Email', 'wp-panel-optimizer'), self::MANAGED_MAIL_CAPABILITY, 'wp-panel-managed-mail', [__CLASS__, 'managed_mail_page']);
    }

    public static function managed_mail_save() {
        if (!current_user_can('manage_options')) wp_die('Forbidden', 403);
        check_admin_referer('wpp_managed_mail_save');
        $status = self::managed_mail_status();
        $enabled = !empty($_POST['enabled']);
        $wasEnabled = get_option(self::OPTION_MANAGED_MAIL_ENABLED, '0') === '1';
        if ($enabled && (is_wp_error($status) || empty($status['authorized']))) {
            $statusCode = is_wp_error($status) ? 'managed_mail_unavailable' : 'panel_authorization_required';
            wp_safe_redirect(add_query_arg('wpp_mail_error', $statusCode, wp_get_referer())); exit;
        }
        if (!is_wp_error($status) && !empty($status['authorized']) && ($enabled || !$wasEnabled)) {
            $fromName = !empty($status['lock_from_name'])
                ? (string) ($status['from_name'] ?? '')
                : sanitize_text_field(wp_unslash($_POST['from_name'] ?? ''));
            $replyToInput = trim((string) wp_unslash($_POST['reply_to'] ?? ''));
            if ($replyToInput !== '' && !is_email($replyToInput)) {
                wp_safe_redirect(add_query_arg('wpp_mail_error', 'invalid_request', wp_get_referer())); exit;
            }
            $settingsResponse = self::api_request('POST', '/api/sites/managed-mail/settings', [
                'from_name' => $fromName,
                'reply_to' => sanitize_email($replyToInput),
            ]);
            if (is_wp_error($settingsResponse)) {
                $settingsCode = sanitize_key($settingsResponse->get_error_message());
                if (!in_array($settingsCode, ['panel_authorization_required', 'from_name_locked', 'invalid_request'], true)) $settingsCode = 'settings_failed';
                wp_safe_redirect(add_query_arg('wpp_mail_error', $settingsCode, wp_get_referer())); exit;
            }
        }
        if ($enabled) {
            $conflict = self::managed_mail_conflict_plugin();
            if ($conflict !== '') {
                self::managed_mail_report_conflict($conflict, true);
                wp_safe_redirect(add_query_arg('wpp_mail_error', 'conflict', wp_get_referer())); exit;
            }
            if (!self::managed_mail_report_conflict('', true)) {
                wp_safe_redirect(add_query_arg('wpp_mail_error', 'conflict_status_unavailable', wp_get_referer())); exit;
            }
            $response = self::api_request('POST', '/api/sites/managed-mail/confirm', ['enabled' => true]);
            if (is_wp_error($response)) {
                $errorCode = sanitize_key($response->get_error_message());
                $knownCodes = ['panel_authorization_required', 'smtp_plugin_conflict', 'managed_mail_unavailable'];
                if (!in_array($errorCode, $knownCodes, true)) $errorCode = 'request_failed';
                wp_safe_redirect(add_query_arg('wpp_mail_error', $errorCode, wp_get_referer())); exit;
            }
            update_option(self::OPTION_MANAGED_MAIL_ENABLED, '1');
        } else {
            self::api_request('POST', '/api/sites/managed-mail/confirm', ['enabled' => false]);
            update_option(self::OPTION_MANAGED_MAIL_ENABLED, '0');
        }
        update_option(self::OPTION_MANAGED_MAIL_LOGGING, !empty($_POST['logging']) ? '1' : '0');
        update_option(self::OPTION_MANAGED_MAIL_RETENTION_DAYS, max(1, min(30, intval($_POST['days'] ?? 7))));
        update_option(self::OPTION_MANAGED_MAIL_RETENTION_COUNT, max(25, min(1000, intval($_POST['count'] ?? 250))));
        wp_safe_redirect(add_query_arg('updated', '1', wp_get_referer())); exit;
    }

    public static function managed_mail_test() {
        if (!current_user_can('manage_options')) wp_die('Forbidden', 403);
        check_admin_referer('wpp_managed_mail_test');
        $recipient = sanitize_email(wp_unslash($_POST['recipient'] ?? ''));
        $status = self::managed_mail_status();
        if (!is_email($recipient)) {
            $result = 'invalid_recipient';
        } elseif (is_wp_error($status) || empty($status['active'])
            || get_option(self::OPTION_MANAGED_MAIL_ENABLED, '0') !== '1'
            || self::managed_mail_conflict_plugin() !== ''
            || empty($GLOBALS['phpmailer'])
            || !($GLOBALS['phpmailer'] instanceof WPP_Managed_Mailer)) {
            $result = 'inactive';
        } else {
            $failure = 'send_failed';
            $capture = static function ($error) use (&$failure) {
                $message = is_wp_error($error) ? $error->get_error_message() : '';
                foreach (['managed_mail_quota_exceeded', 'managed_mail_paused', 'smtp_plugin_conflict', 'panel_unavailable'] as $code) {
                    if (strpos($message, $code) !== false) {
                        $failure = $code;
                        break;
                    }
                }
            };
            global $wp_filter;
            $isolatedHooks = [];
            foreach (['wp_mail', 'pre_wp_mail', 'phpmailer_init'] as $hookName) {
                $isolatedHooks[$hookName] = $wp_filter[$hookName] ?? null;
                $wp_filter[$hookName] = new WP_Hook();
            }
            add_action('wp_mail_failed', $capture);
            try {
                $sent = wp_mail(
                    $recipient,
                    __('WP Panel managed email test', 'wp-panel-optimizer'),
                    __('This test email was sent through WP Panel managed SMTP.', 'wp-panel-optimizer')
                );
            } finally {
                remove_action('wp_mail_failed', $capture);
                foreach ($isolatedHooks as $hookName => $savedHook) {
                    if ($savedHook === null) {
                        unset($wp_filter[$hookName]);
                    } else {
                        $wp_filter[$hookName] = $savedHook;
                    }
                }
            }
            $result = $sent ? 'sent' : $failure;
        }
        wp_safe_redirect(add_query_arg('wpp_mail_test', $result, wp_get_referer())); exit;
    }

    public static function managed_mail_page() {
        if (!current_user_can(self::MANAGED_MAIL_CAPABILITY)) return;
        global $wpdb;
        $status = self::managed_mail_status();
        $table = self::managed_mail_table();
        $storedCount = intval($wpdb->get_var("SELECT COUNT(*) FROM {$table}"));
        $rows = (array) $wpdb->get_results("SELECT id,status,subject,recipients,error_code,message_id,duration_ms,message_size,attachments,created_at,viewed_at,viewed_by FROM {$table} ORDER BY id DESC LIMIT 100", ARRAY_A);
        $currentDomain = (string) wp_parse_url(home_url(), PHP_URL_HOST);
        $statusTone = 'risk';
        $statusLabel = __('Unavailable', 'wp-panel-optimizer');
        $statusMessage = __('WP Panel status is unavailable.', 'wp-panel-optimizer');
        if (is_wp_error($status)) {
        } elseif (!empty($status['conflict_plugin'])) {
            $statusTone = 'warn';
            $statusLabel = __('Paused', 'wp-panel-optimizer');
            $statusMessage = __('Managed email is paused because another SMTP plugin is active. Remove the conflict and confirm managed email again.', 'wp-panel-optimizer');
        } elseif (empty($status['authorized'])) {
            $statusTone = 'warn';
            $statusLabel = __('Waiting for authorization', 'wp-panel-optimizer');
            $statusMessage = __('The server administrator has not authorized managed email for this site.', 'wp-panel-optimizer');
        } elseif (empty($status['site_confirmed'])) {
            $statusTone = 'info';
            $statusLabel = __('Ready to confirm', 'wp-panel-optimizer');
            $statusMessage = __('Server authorization is ready. Confirm below to use WP Panel managed SMTP.', 'wp-panel-optimizer');
        } elseif (empty($status['active'])) {
            $statusTone = 'risk';
            $statusLabel = __('Unavailable', 'wp-panel-optimizer');
            $statusMessage = __('Managed email is temporarily unavailable or paused. Messages will fail instead of falling back to PHP mail.', 'wp-panel-optimizer');
        } else {
            $statusTone = 'ok';
            $statusLabel = __('Active', 'wp-panel-optimizer');
            $statusMessage = __('WP Panel managed SMTP is active.', 'wp-panel-optimizer');
        }
        $errorMessage = '';
        if (isset($_GET['wpp_mail_error'])) {
            $errorCode = sanitize_key(wp_unslash($_GET['wpp_mail_error']));
            $errorMessages = [
                'conflict' => __('Another SMTP plugin is still active. Remove it before enabling managed email.', 'wp-panel-optimizer'),
                'conflict_status_unavailable' => __('Could not refresh the SMTP plugin conflict status. Please try again.', 'wp-panel-optimizer'),
                'panel_authorization_required' => __('The server administrator has not authorized managed email for this site.', 'wp-panel-optimizer'),
                'smtp_plugin_conflict' => __('Another SMTP plugin is still active. Remove it before enabling managed email.', 'wp-panel-optimizer'),
                'managed_mail_unavailable' => __('WP Panel SMTP is not ready. Ask the server administrator to check the SMTP settings.', 'wp-panel-optimizer'),
                'from_name_locked' => __('The server administrator has locked the default sender name.', 'wp-panel-optimizer'),
                'invalid_request' => __('The sender settings are invalid. Check the sender name and Reply-To address.', 'wp-panel-optimizer'),
                'settings_failed' => __('Could not save the sender settings to WP Panel. Please try again.', 'wp-panel-optimizer'),
                'request_failed' => __('Could not confirm managed email with WP Panel. Please try again.', 'wp-panel-optimizer'),
            ];
            $errorMessage = $errorMessages[$errorCode] ?? $errorMessages['request_failed'];
        }
        $testMessage = '';
        $testTone = 'info';
        if (isset($_GET['wpp_mail_test'])) {
            $testCode = sanitize_key(wp_unslash($_GET['wpp_mail_test']));
            $testMessages = [
                'sent' => __('The test email was accepted by the SMTP service.', 'wp-panel-optimizer'),
                'invalid_recipient' => __('Enter a valid test recipient email address.', 'wp-panel-optimizer'),
                'inactive' => __('Managed email must be active before sending a test email.', 'wp-panel-optimizer'),
                'managed_mail_quota_exceeded' => __('The test email was blocked by the managed email quota.', 'wp-panel-optimizer'),
                'managed_mail_paused' => __('Managed email is paused, so the test email was not sent.', 'wp-panel-optimizer'),
                'smtp_plugin_conflict' => __('Another SMTP plugin is active, so the test email was not sent.', 'wp-panel-optimizer'),
                'panel_unavailable' => __('WP Panel could not be reached, so the test email was not sent.', 'wp-panel-optimizer'),
                'send_failed' => __('The test email could not be sent. Check the status and recent email log below.', 'wp-panel-optimizer'),
            ];
            $testMessage = $testMessages[$testCode] ?? $testMessages['send_failed'];
            $testTone = $testCode === 'sent' ? 'success' : 'error';
        }
        ?>
        <div class="wrap wpp-settings wpp-managed-mail">
            <header class="wpp-masthead">
                <div class="wpp-masthead__brand">
                    <span class="wpp-masthead__mark" aria-hidden="true"><img src="<?php echo esc_url(plugin_dir_url(WPP_OPTIMIZER_PLUGIN_FILE) . 'assets/wp-panel-logo.png'); ?>" alt=""></span>
                    <div>
                        <p class="wpp-masthead__eyebrow">WP PANEL · MANAGED WORDPRESS</p>
                        <h1>WP Panel Optimizer</h1>
                        <p class="wpp-masthead__lead"><?php echo esc_html__('Managed SMTP delivery and site-local email records.', 'wp-panel-optimizer'); ?></p>
                    </div>
                </div>
                <div class="wpp-masthead__meta">
                    <div class="wpp-masthead__item">
                        <span class="wpp-masthead__label"><?php echo esc_html__('Managed site', 'wp-panel-optimizer'); ?></span>
                        <span class="wpp-masthead__value"><?php echo esc_html($currentDomain); ?></span>
                    </div>
                    <div class="wpp-masthead__item">
                        <span class="wpp-masthead__label"><?php echo esc_html__('Component version', 'wp-panel-optimizer'); ?></span>
                        <span class="wpp-masthead__value"><?php echo esc_html(self::VERSION); ?></span>
                    </div>
                </div>
            </header>

            <div class="wpp-readout" role="group" aria-label="<?php echo esc_attr__('Managed email status', 'wp-panel-optimizer'); ?>">
                <div class="wpp-readout__item">
                    <span class="wpp-readout__label"><?php echo esc_html__('Managed Email', 'wp-panel-optimizer'); ?></span>
                    <span class="wpp-readout__value is-<?php echo esc_attr($statusTone); ?>"><span class="wpp-flag wpp-flag--<?php echo esc_attr($statusTone); ?>" aria-hidden="true"></span><?php echo esc_html($statusLabel); ?></span>
                </div>
                <div class="wpp-readout__item">
                    <span class="wpp-readout__label"><?php echo esc_html__('Email logging', 'wp-panel-optimizer'); ?></span>
                    <span class="wpp-readout__value <?php echo get_option(self::OPTION_MANAGED_MAIL_LOGGING, '1') === '1' ? 'is-ok' : 'is-idle'; ?>"><span class="wpp-flag wpp-flag--<?php echo get_option(self::OPTION_MANAGED_MAIL_LOGGING, '1') === '1' ? 'ok' : 'idle'; ?>" aria-hidden="true"></span><?php echo get_option(self::OPTION_MANAGED_MAIL_LOGGING, '1') === '1' ? esc_html__('Enabled', 'wp-panel-optimizer') : esc_html__('Disabled', 'wp-panel-optimizer'); ?></span>
                </div>
                <div class="wpp-readout__item">
                    <span class="wpp-readout__label"><?php echo esc_html__('Stored records', 'wp-panel-optimizer'); ?></span>
                    <span class="wpp-readout__value"><?php echo esc_html(number_format_i18n($storedCount)); ?></span>
                </div>
            </div>

            <nav class="wpp-tabs" aria-label="<?php echo esc_attr__('WP Panel Optimizer settings', 'wp-panel-optimizer'); ?>">
                <a href="<?php echo esc_url(admin_url('options-general.php?page=wp-panel-optimizer&wpp_tab=cache')); ?>" class="nav-tab"><span class="dashicons dashicons-performance" aria-hidden="true"></span><?php echo esc_html__('Cache & Performance', 'wp-panel-optimizer'); ?></a>
                <a href="<?php echo esc_url(admin_url('options-general.php?page=wp-panel-optimizer&wpp_tab=image')); ?>" class="nav-tab"><span class="dashicons dashicons-format-image" aria-hidden="true"></span><?php echo esc_html__('Image Optimization', 'wp-panel-optimizer'); ?></a>
                <a href="<?php echo esc_url(admin_url('options-general.php?page=wp-panel-optimizer&wpp_tab=security')); ?>" class="nav-tab"><span class="dashicons dashicons-shield" aria-hidden="true"></span><?php echo esc_html__('Security & Maintenance', 'wp-panel-optimizer'); ?></a>
                <a href="<?php echo esc_url(admin_url('options-general.php?page=wp-panel-managed-mail')); ?>" class="nav-tab nav-tab-active" aria-current="page"><span class="dashicons dashicons-email-alt" aria-hidden="true"></span><?php echo esc_html__('Managed Email', 'wp-panel-optimizer'); ?></a>
                <a href="<?php echo esc_url(admin_url('options-general.php?page=wp-panel-optimizer&wpp_tab=about')); ?>" class="nav-tab"><span class="dashicons dashicons-admin-links" aria-hidden="true"></span><?php echo esc_html__('About & Panel Sync', 'wp-panel-optimizer'); ?></a>
            </nav>

            <div class="wpp-tab-panel">
                <div class="wpp-page-notice wpp-page-notice--<?php echo esc_attr($statusTone === 'ok' ? 'success' : ($statusTone === 'info' ? 'info' : ($statusTone === 'warn' ? 'warning' : 'error'))); ?>"><p><?php echo esc_html($statusMessage); ?></p></div>
                <?php if ($errorMessage !== ''): ?><div class="wpp-page-notice wpp-page-notice--error"><p><?php echo esc_html($errorMessage); ?></p></div><?php endif; ?>
                <?php if ($testMessage !== ''): ?><div class="wpp-page-notice wpp-page-notice--<?php echo esc_attr($testTone); ?>"><p><?php echo esc_html($testMessage); ?></p></div><?php endif; ?>
                <section class="wpp-section">
                    <header class="wpp-section__head">
                        <h2 class="wpp-section__title"><span class="dashicons dashicons-admin-settings" aria-hidden="true"></span><?php echo esc_html__('Managed email settings', 'wp-panel-optimizer'); ?></h2>
                        <p class="wpp-section__desc"><?php echo esc_html__('Choose whether this site uses the SMTP service authorized by WP Panel and how long local troubleshooting records are kept.', 'wp-panel-optimizer'); ?></p>
                    </header>
                    <div class="wpp-section__body">
                        <form method="post" action="<?php echo esc_url(admin_url('admin-post.php')); ?>">
                            <input type="hidden" name="action" value="wpp_managed_mail_save">
                            <?php wp_nonce_field('wpp_managed_mail_save'); ?>
                            <div class="wpp-field">
                                <div class="wpp-field__head"><label class="wpp-field__title" for="wpp-managed-mail-enabled"><?php echo esc_html__('Managed SMTP', 'wp-panel-optimizer'); ?></label><span class="wpp-field__summary"><?php echo esc_html__('Requires authorization in WP Panel and confirmation by this site administrator.', 'wp-panel-optimizer'); ?></span></div>
                                <div class="wpp-field__body"><label class="wpp-switch"><input id="wpp-managed-mail-enabled" type="checkbox" name="enabled" value="1" <?php checked(get_option(self::OPTION_MANAGED_MAIL_ENABLED, '0'), '1'); ?>><span class="wpp-switch__track"><span class="wpp-switch__thumb"></span></span><span class="wpp-switch__text"><?php echo esc_html__('Use WP Panel managed SMTP', 'wp-panel-optimizer'); ?></span></label></div>
                            </div>
                            <div class="wpp-field">
                                <div class="wpp-field__head"><label class="wpp-field__title" for="wpp-managed-mail-from-name"><?php echo esc_html__('Default sender name', 'wp-panel-optimizer'); ?></label><span class="wpp-field__summary"><?php echo esc_html__('Used when a WordPress email does not provide its own sender name.', 'wp-panel-optimizer'); ?></span></div>
                                <div class="wpp-field__body"><input id="wpp-managed-mail-from-name" class="wpp-input" type="text" name="from_name" maxlength="100" value="<?php echo esc_attr(is_wp_error($status) ? '' : ($status['from_name'] ?? '')); ?>" <?php disabled(!is_wp_error($status) && !empty($status['lock_from_name'])); ?>><?php if (!is_wp_error($status) && !empty($status['lock_from_name'])): ?><p class="description"><?php echo esc_html__('Locked by the server administrator.', 'wp-panel-optimizer'); ?></p><?php endif; ?></div>
                            </div>
                            <div class="wpp-field">
                                <div class="wpp-field__head"><label class="wpp-field__title" for="wpp-managed-mail-reply-to"><?php echo esc_html__('Default Reply-To', 'wp-panel-optimizer'); ?></label><span class="wpp-field__summary"><?php echo esc_html__('Used when an individual email does not provide a Reply-To address.', 'wp-panel-optimizer'); ?></span></div>
                                <div class="wpp-field__body"><input id="wpp-managed-mail-reply-to" class="wpp-input" type="email" name="reply_to" value="<?php echo esc_attr(is_wp_error($status) ? '' : ($status['reply_to'] ?? '')); ?>"></div>
                            </div>
                            <div class="wpp-field">
                                <div class="wpp-field__head"><label class="wpp-field__title" for="wpp-managed-mail-logging"><?php echo esc_html__('Email logging', 'wp-panel-optimizer'); ?></label><span class="wpp-field__summary"><?php echo esc_html__('Stores complete email content in this WordPress database for troubleshooting.', 'wp-panel-optimizer'); ?></span></div>
                                <div class="wpp-field__body"><label class="wpp-switch"><input id="wpp-managed-mail-logging" type="checkbox" name="logging" value="1" <?php checked(get_option(self::OPTION_MANAGED_MAIL_LOGGING, '1'), '1'); ?>><span class="wpp-switch__track"><span class="wpp-switch__thumb"></span></span><span class="wpp-switch__text"><?php echo esc_html__('Keep complete email logs', 'wp-panel-optimizer'); ?></span></label></div>
                            </div>
                            <div class="wpp-field">
                                <div class="wpp-field__head"><span class="wpp-field__title"><?php echo esc_html__('Log retention', 'wp-panel-optimizer'); ?></span><span class="wpp-field__summary"><?php echo esc_html__('Older records are removed automatically when either limit is exceeded.', 'wp-panel-optimizer'); ?></span></div>
                                <div class="wpp-field__body wpp-mail-retention"><label><?php echo esc_html__('Retention days', 'wp-panel-optimizer'); ?><input type="number" name="days" min="1" max="30" value="<?php echo esc_attr(get_option(self::OPTION_MANAGED_MAIL_RETENTION_DAYS, 7)); ?>"></label><label><?php echo esc_html__('Maximum entries', 'wp-panel-optimizer'); ?><input type="number" name="count" min="25" max="1000" value="<?php echo esc_attr(get_option(self::OPTION_MANAGED_MAIL_RETENTION_COUNT, 250)); ?>"></label></div>
                            </div>
                            <div class="wpp-mail-actions"><?php submit_button(__('Save Changes', 'wp-panel-optimizer'), 'primary', 'submit', false); ?></div>
                        </form>
                    </div>
                </section>

                <section class="wpp-section">
                    <header class="wpp-section__head">
                        <h2 class="wpp-section__title"><span class="dashicons dashicons-email-alt" aria-hidden="true"></span><?php echo esc_html__('Send a test email', 'wp-panel-optimizer'); ?></h2>
                        <p class="wpp-section__desc"><?php echo esc_html__('The test uses the real managed SMTP path, counts toward the site quota and appears in the email log.', 'wp-panel-optimizer'); ?></p>
                    </header>
                    <div class="wpp-section__body">
                        <form method="post" action="<?php echo esc_url(admin_url('admin-post.php')); ?>">
                            <input type="hidden" name="action" value="wpp_managed_mail_test">
                            <?php wp_nonce_field('wpp_managed_mail_test'); ?>
                            <div class="wpp-field">
                                <div class="wpp-field__head"><label class="wpp-field__title" for="wpp-managed-mail-test-recipient"><?php echo esc_html__('Test recipient', 'wp-panel-optimizer'); ?></label><span class="wpp-field__summary"><?php echo esc_html__('Defaults to the WordPress administration email address.', 'wp-panel-optimizer'); ?></span></div>
                                <div class="wpp-field__body"><input id="wpp-managed-mail-test-recipient" class="wpp-input" type="email" name="recipient" required value="<?php echo esc_attr(get_option('admin_email')); ?>"></div>
                            </div>
                            <?php $testButtonAttributes = is_wp_error($status) || empty($status['active']) ? ['disabled' => 'disabled'] : []; ?>
                            <div class="wpp-mail-actions"><?php submit_button(__('Send test email', 'wp-panel-optimizer'), 'secondary', 'submit', false, $testButtonAttributes); ?></div>
                        </form>
                    </div>
                </section>

                <div class="wpp-hint"><span class="dashicons dashicons-privacy" aria-hidden="true"></span><div><strong><?php echo esc_html__('Privacy warning:', 'wp-panel-optimizer'); ?></strong> <?php echo esc_html__('Email logs may contain password reset links, verification codes and personal information.', 'wp-panel-optimizer'); ?></div></div>
        <?php
        if (isset($_GET['view'])) {
            $id = absint($_GET['view']);
            check_admin_referer('wpp_managed_mail_view_' . $id);
            $detailColumns = get_option('wpp_optimizer_managed_mail_db_version') === '2'
                ? 'from_name,reply_to'
                : 'NULL AS from_name,NULL AS reply_to';
            $message = $wpdb->get_row($wpdb->prepare("SELECT id,status,subject,{$detailColumns},recipients,body,headers,attachments,error_code,message_id,duration_ms,message_size,created_at FROM {$table} WHERE id=%d", $id), ARRAY_A);
            if ($message) {
                $wpdb->update($table, ['viewed_at' => current_time('mysql', true), 'viewed_by' => get_current_user_id()], ['id' => $id]);
                $body = json_decode($message['body'], true);
                $text = is_array($body) ? (string) ($body['text'] ?? '') : '';
                $html = is_array($body) ? (string) ($body['html'] ?? '') : '';
                $recipients = json_decode($message['recipients'], true);
                $recipients = is_array($recipients) ? $recipients : [];
                $replyTo = json_decode((string) ($message['reply_to'] ?? ''), true);
                $replyTo = is_array($replyTo) ? $replyTo : [];
                $headers = json_decode($message['headers'], true);
                $headers = is_array($headers) ? $headers : [];
                $attachments = json_decode($message['attachments'], true);
                $attachments = is_array($attachments) ? $attachments : [];
                $createdAt = get_date_from_gmt($message['created_at'], get_option('date_format') . ' ' . get_option('time_format'));
                $fromNameRecorded = $message['from_name'] !== null;
                $fromName = trim((string) ($message['from_name'] ?? ''));
                $logsUrl = admin_url('options-general.php?page=wp-panel-managed-mail');
                echo '<dialog id="wpp-mail-preview-dialog" class="wpp-mail-preview" open data-message-id="' . esc_attr($id) . '" aria-labelledby="wpp-mail-preview-title" aria-describedby="wpp-mail-preview-warning"><header class="wpp-mail-preview__head"><div><h2 id="wpp-mail-preview-title" class="wpp-mail-preview__title"><span class="dashicons dashicons-visibility" aria-hidden="true"></span>' . esc_html($message['subject']) . '</h2><p id="wpp-mail-preview-warning" class="wpp-mail-preview__warning">' . esc_html__('This content may contain active password-reset links, verification codes, or personal information.', 'wp-panel-optimizer') . '</p></div><a href="' . esc_url($logsUrl) . '" class="wpp-mail-preview__close" data-wpp-mail-preview-close aria-label="' . esc_attr__('Close', 'wp-panel-optimizer') . '"><span class="dashicons dashicons-no-alt" aria-hidden="true"></span></a></header><div class="wpp-mail-preview__body">';
                echo '<dl class="wpp-mail-details">';
                self::managed_mail_detail_row(__('Logged at', 'wp-panel-optimizer'), $createdAt);
                self::managed_mail_detail_row(__('Status', 'wp-panel-optimizer'), self::managed_mail_detail_status((string) $message['status']), 'status');
                self::managed_mail_detail_row(__('Sender', 'wp-panel-optimizer'), $fromNameRecorded ? ($fromName !== '' ? $fromName : __('No display name', 'wp-panel-optimizer')) : __('Not recorded', 'wp-panel-optimizer'));
                self::managed_mail_detail_row(__('To', 'wp-panel-optimizer'), self::managed_mail_detail_addresses($recipients['to'] ?? []));
                if (!empty($recipients['cc'])) self::managed_mail_detail_row(__('CC', 'wp-panel-optimizer'), self::managed_mail_detail_addresses($recipients['cc']));
                if (!empty($recipients['bcc'])) self::managed_mail_detail_row(__('BCC', 'wp-panel-optimizer'), self::managed_mail_detail_addresses($recipients['bcc']));
                if ($replyTo) self::managed_mail_detail_row(__('Reply-To', 'wp-panel-optimizer'), self::managed_mail_detail_addresses($replyTo));
                if ($message['message_id'] !== '') self::managed_mail_detail_row(__('Message-ID', 'wp-panel-optimizer'), (string) $message['message_id'], 'code');
                if ($message['error_code'] !== '') self::managed_mail_detail_row(__('Error code', 'wp-panel-optimizer'), (string) $message['error_code'], 'code');
                $durationLabel = __('Duration', 'wp-panel-optimizer');
                /* translators: %d: SMTP delivery duration in milliseconds. */
                $durationValue = sprintf(__('%d ms', 'wp-panel-optimizer'), intval($message['duration_ms']));
                self::managed_mail_detail_row($durationLabel, $durationValue);
                self::managed_mail_detail_row(__('Message size', 'wp-panel-optimizer'), size_format(intval($message['message_size']), 1));
                self::managed_mail_detail_row(__('Attachments', 'wp-panel-optimizer'), self::managed_mail_detail_attachments($attachments));
                if ($headers) self::managed_mail_detail_row(__('Custom headers', 'wp-panel-optimizer'), self::managed_mail_detail_headers($headers), 'code');
                echo '</dl><p class="wpp-mail-details__sender-note">' . esc_html__('The sender email address is controlled by WP Panel and is not included in the site status API or local log.', 'wp-panel-optimizer') . '</p>';
                if ($html !== '') {
                    $preview = self::managed_mail_safe_html_preview($html);
                    echo '<h3>' . esc_html__('Isolated HTML preview', 'wp-panel-optimizer') . '</h3><iframe sandbox="" referrerpolicy="no-referrer" class="wpp-mail-preview__frame" srcdoc="' . esc_attr($preview) . '"></iframe>';
                }
                echo '<h3>' . esc_html__('Complete escaped source', 'wp-panel-optimizer') . '</h3><pre class="wpp-mail-preview__source">' . esc_html($html !== '' ? $html : $text) . '</pre>';
                echo '</div><footer class="wpp-mail-preview__foot"><a href="' . esc_url($logsUrl) . '" class="button button-primary" data-wpp-mail-preview-close>' . esc_html__('Close', 'wp-panel-optimizer') . '</a></footer></dialog>';
            }
        }
        echo '<section class="wpp-section"><header class="wpp-section__head"><h2 class="wpp-section__title"><span class="dashicons dashicons-list-view" aria-hidden="true"></span>' . esc_html__('Recent email logs', 'wp-panel-optimizer') . '</h2><p class="wpp-section__desc">' . esc_html__('The most recent 100 delivery records stored by this site.', 'wp-panel-optimizer') . '</p></header><div class="wpp-section__body"><div class="wpp-log wpp-mail-log"><table><thead><tr><th>' . esc_html__('Time', 'wp-panel-optimizer') . '</th><th>' . esc_html__('Status', 'wp-panel-optimizer') . '</th><th>' . esc_html__('Subject', 'wp-panel-optimizer') . '</th><th>' . esc_html__('Recipients', 'wp-panel-optimizer') . '</th><th>' . esc_html__('Body', 'wp-panel-optimizer') . '</th></tr></thead><tbody>';
        foreach ($rows as $row) {
            $viewUrl = wp_nonce_url(admin_url('options-general.php?page=wp-panel-managed-mail&view=' . absint($row['id'])), 'wpp_managed_mail_view_' . absint($row['id']));
            $rowCreatedAt = get_date_from_gmt($row['created_at'], get_option('date_format') . ' ' . get_option('time_format'));
            echo '<tr><td>' . esc_html($rowCreatedAt) . '</td><td>' . esc_html(self::managed_mail_detail_status((string) $row['status'])) . '</td><td>' . esc_html($row['subject']) . '</td><td><code>' . esc_html($row['recipients']) . '</code></td><td><a href="' . esc_url($viewUrl) . '" data-wpp-mail-preview-id="' . esc_attr($row['id']) . '">' . esc_html__('View', 'wp-panel-optimizer') . '</a></td></tr>';
        }
        if (!$rows) echo '<tr><td colspan="5">' . esc_html__('No email logs yet.', 'wp-panel-optimizer') . '</td></tr>';
        echo '</tbody></table></div></div></section></div></div>';
    }

    private static function managed_mail_safe_html_preview($html) {
        $allowed = [
            'p' => [], 'br' => [], 'div' => [], 'span' => [], 'strong' => [], 'b' => [], 'em' => [], 'i' => [],
            'u' => [], 's' => [], 'blockquote' => [], 'pre' => [], 'code' => [], 'hr' => [],
            'h1' => [], 'h2' => [], 'h3' => [], 'h4' => [], 'h5' => [], 'h6' => [],
            'ul' => [], 'ol' => [], 'li' => [], 'table' => [], 'thead' => [], 'tbody' => [], 'tfoot' => [],
            'tr' => [], 'th' => ['colspan' => true, 'rowspan' => true], 'td' => ['colspan' => true, 'rowspan' => true],
            'img' => ['alt' => true, 'width' => true, 'height' => true],
        ];
        return wp_kses((string) $html, $allowed);
    }

    private static function managed_mail_detail_row($label, $value, $type = '') {
        $class = $type !== '' ? ' wpp-mail-details__value--' . sanitize_html_class($type) : '';
        echo '<div class="wpp-mail-details__row"><dt>' . esc_html($label) . '</dt><dd class="wpp-mail-details__value' . esc_attr($class) . '">' . esc_html($value !== '' ? $value : '—') . '</dd></div>';
    }

    private static function managed_mail_detail_addresses($addresses) {
        $addresses = array_values(array_filter(array_map('strval', (array) $addresses)));
        return implode(', ', $addresses);
    }

    private static function managed_mail_detail_attachments($attachments) {
        if (!$attachments) return __('None', 'wp-panel-optimizer');
        $items = [];
        foreach ($attachments as $attachment) {
            if (!is_array($attachment)) continue;
            $name = sanitize_file_name((string) ($attachment['name'] ?? ''));
            if ($name === '') $name = __('Unnamed attachment', 'wp-panel-optimizer');
            $size = size_format(max(0, intval($attachment['size'] ?? 0)), 1);
            $items[] = sprintf('%s (%s)', $name, $size);
        }
        return $items ? implode(', ', $items) : __('None', 'wp-panel-optimizer');
    }

    private static function managed_mail_detail_headers($headers) {
        $items = [];
        foreach ($headers as $name => $value) {
            $name = preg_replace('/[\x00-\x1F\x7F]+/', ' ', (string) $name);
            $value = str_replace(["\r\n", "\r"], "\n", (string) $value);
            $value = preg_replace('/[\x00-\x08\x0B\x0C\x0E-\x1F\x7F]/', '', $value);
            $items[] = trim($name) . ': ' . $value;
        }
        return implode("\n", $items);
    }

    private static function managed_mail_detail_status($status) {
        $labels = [
            'accepted' => __('Accepted by SMTP', 'wp-panel-optimizer'),
            'rejected' => __('Rejected', 'wp-panel-optimizer'),
            'temporary_failure' => __('Temporary failure', 'wp-panel-optimizer'),
            'unknown' => __('Delivery status unknown', 'wp-panel-optimizer'),
            'quota_exceeded' => __('Quota exceeded', 'wp-panel-optimizer'),
            'paused' => __('Paused', 'wp-panel-optimizer'),
            'conflict' => __('SMTP plugin conflict', 'wp-panel-optimizer'),
            'unauthorized' => __('Unauthorized', 'wp-panel-optimizer'),
            'unconfirmed' => __('Site confirmation required', 'wp-panel-optimizer'),
            'panel_unavailable' => __('WP Panel unavailable', 'wp-panel-optimizer'),
        ];
        return $labels[$status] ?? $status;
    }
}

final class WPP_Managed_Mailer extends WP_PHPMailer {
    public function postSend() {
        $result = WP_Panel_Optimizer::managed_mail_dispatch($this);
        return $result === null ? parent::postSend() : true;
    }
}
