<?php
/** Graceful no-op for WordPress versions without the WP_PHPMailer transport class. */
if (!defined('ABSPATH')) exit;

trait WPP_Optimizer_Managed_Mail_Trait {
    const OPTION_MANAGED_MAIL_ENABLED = 'wpp_optimizer_managed_mail_enabled';
    const OPTION_MANAGED_MAIL_LOGGING = 'wpp_optimizer_managed_mail_logging';
    const OPTION_MANAGED_MAIL_RETENTION_DAYS = 'wpp_optimizer_managed_mail_retention_days';
    const OPTION_MANAGED_MAIL_RETENTION_COUNT = 'wpp_optimizer_managed_mail_retention_count';
    const MANAGED_MAIL_CAPABILITY = 'wpp_view_managed_mail_body';

    public static function managed_mail_bootstrap() {}
    public static function managed_mail_init() {}
    public static function managed_mail_activate() {
        add_option(self::OPTION_MANAGED_MAIL_ENABLED, '0');
    }
}
