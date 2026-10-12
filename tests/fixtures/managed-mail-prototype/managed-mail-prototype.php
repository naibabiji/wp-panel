<?php
/**
 * Plugin Name: WP Panel Managed Mail Compatibility Prototype
 * Description: Disposable test-only transport used to verify WordPress wp_mail semantics.
 * Version: 0.0.1
 */

if ( ! defined( 'ABSPATH' ) ) {
	exit;
}

require_once ABSPATH . WPINC . '/PHPMailer/PHPMailer.php';
require_once ABSPATH . WPINC . '/PHPMailer/SMTP.php';
require_once ABSPATH . WPINC . '/PHPMailer/Exception.php';
require_once ABSPATH . WPINC . '/class-wp-phpmailer.php';

use PHPMailer\PHPMailer\Exception as MailerException;

final class WPP_Managed_Mail_Prototype_Mailer extends WP_PHPMailer {
	public static $dispatches = array();
	public static $fail_next  = false;

	public function postSend() {
		self::$dispatches[] = array(
			'from'        => $this->From,
			'from_name'   => $this->FromName,
			'to'          => $this->getToAddresses(),
			'cc'          => $this->getCcAddresses(),
			'bcc'         => $this->getBccAddresses(),
			'reply_to'    => $this->getReplyToAddresses(),
			'subject'     => $this->Subject,
			'body'        => $this->Body,
			'alt_body'    => $this->AltBody,
			'content_type'=> $this->ContentType,
			'charset'     => $this->CharSet,
			'headers'     => $this->getCustomHeaders(),
			'attachments' => $this->getAttachments(),
			'mime'        => $this->getSentMIMEMessage(),
		);

		if ( self::$fail_next ) {
			self::$fail_next = false;
			throw new MailerException( 'prototype transport rejected the message', 550 );
		}

		return true;
	}
}

add_action(
	'plugins_loaded',
	static function () {
		global $phpmailer;
		$phpmailer = new WPP_Managed_Mail_Prototype_Mailer( true );
		$phpmailer::$validator = static function ( $email ) {
			return (bool) is_email( $email );
		};
	},
	PHP_INT_MAX
);

function wpp_managed_mail_prototype_assert( $condition, $message ) {
	if ( ! $condition ) {
		throw new RuntimeException( $message );
	}
}

function wpp_managed_mail_prototype_has_address( $addresses, $email, $name = null ) {
	foreach ( $addresses as $address ) {
		if ( isset( $address[0] ) && $address[0] === $email && ( null === $name || ( $address[1] ?? '' ) === $name ) ) {
			return true;
		}
	}
	return false;
}

function wpp_managed_mail_prototype_run() {
	global $phpmailer;

	wpp_managed_mail_prototype_assert( $phpmailer instanceof WPP_Managed_Mail_Prototype_Mailer, 'prototype mailer was not installed before wp_mail' );

	WPP_Managed_Mail_Prototype_Mailer::$dispatches = array();
	$events = array(
		'init'      => 0,
		'succeeded' => 0,
		'failed'    => 0,
	);

	$from_filter = static function () {
		return 'managed@example.test';
	};
	$name_filter = static function () {
		return 'Filtered Site Name';
	};
	$type_filter = static function () {
		return 'text/html';
	};
	$charset_filter = static function () {
		return 'UTF-8';
	};
	$init_action = static function ( $mailer ) use ( &$events ) {
		++$events['init'];
		$mailer->AltBody = 'Plain alternative';
	};
	$success_action = static function ( $mail_data ) use ( &$events ) {
		++$events['succeeded'];
		wpp_managed_mail_prototype_assert( isset( $mail_data['embeds'] ), 'WordPress 7 mail success data must include embeds' );
	};
	$failed_action = static function () use ( &$events ) {
		++$events['failed'];
	};

	add_filter( 'wp_mail_from', $from_filter );
	add_filter( 'wp_mail_from_name', $name_filter );
	add_filter( 'wp_mail_content_type', $type_filter );
	add_filter( 'wp_mail_charset', $charset_filter );
	add_action( 'phpmailer_init', $init_action );
	add_action( 'wp_mail_succeeded', $success_action );
	add_action( 'wp_mail_failed', $failed_action );

	$attachment = tempnam( sys_get_temp_dir(), 'wpp-mail-attachment-' );
	$embed      = tempnam( sys_get_temp_dir(), 'wpp-mail-inline-' );
	wpp_managed_mail_prototype_assert( false !== $attachment && false !== $embed, 'failed to create prototype attachment files' );
	file_put_contents( $attachment, 'attachment' );
	file_put_contents( $embed, 'inline-image' );

	try {
		$result = wp_mail(
			array( 'Recipient <recipient@example.test>' ),
			'Compatibility ✓',
			'<p>HTML body</p><img src="cid:logo">',
			array(
				'Cc: Copy <copy@example.test>',
				'Bcc: Hidden <hidden@example.test>',
				'Reply-To: Customer <customer@example.test>',
				'X-WPP-Test: preserved',
			),
			array( 'report.txt' => $attachment ),
			array( 'logo' => $embed )
		);

		wpp_managed_mail_prototype_assert( true === $result, 'successful fake transport must return true' );
		wpp_managed_mail_prototype_assert( 1 === count( WPP_Managed_Mail_Prototype_Mailer::$dispatches ), 'message must be dispatched exactly once' );
		wpp_managed_mail_prototype_assert( 1 === $events['init'], 'phpmailer_init must fire once' );
		wpp_managed_mail_prototype_assert( 1 === $events['succeeded'], 'wp_mail_succeeded must fire once' );
		wpp_managed_mail_prototype_assert( 0 === $events['failed'], 'wp_mail_failed must not fire for success' );

		$message = WPP_Managed_Mail_Prototype_Mailer::$dispatches[0];
		wpp_managed_mail_prototype_assert( 'managed@example.test' === $message['from'], 'wp_mail_from result was not preserved' );
		wpp_managed_mail_prototype_assert( 'Filtered Site Name' === $message['from_name'], 'wp_mail_from_name result was not preserved' );
		wpp_managed_mail_prototype_assert( 0 === strpos( $message['content_type'], 'multipart/' ), 'PHPMailer did not promote HTML with attachments to multipart MIME' );
		wpp_managed_mail_prototype_assert( false !== strpos( $message['mime'], 'Content-Type: text/html' ), 'HTML MIME part was not preserved' );
		wpp_managed_mail_prototype_assert( 'UTF-8' === $message['charset'], 'mail charset was not preserved' );
		wpp_managed_mail_prototype_assert( 'Plain alternative' === $message['alt_body'], 'phpmailer_init AltBody was not preserved' );
		wpp_managed_mail_prototype_assert( wpp_managed_mail_prototype_has_address( $message['to'], 'recipient@example.test', 'Recipient' ), 'To address/name missing' );
		wpp_managed_mail_prototype_assert( wpp_managed_mail_prototype_has_address( $message['cc'], 'copy@example.test', 'Copy' ), 'CC address/name missing' );
		wpp_managed_mail_prototype_assert( wpp_managed_mail_prototype_has_address( $message['bcc'], 'hidden@example.test', 'Hidden' ), 'BCC address/name missing' );
		wpp_managed_mail_prototype_assert( wpp_managed_mail_prototype_has_address( $message['reply_to'], 'customer@example.test', 'Customer' ), 'per-message Reply-To missing' );
		wpp_managed_mail_prototype_assert( 2 === count( $message['attachments'] ), 'attachment and inline image must both survive preSend' );
		wpp_managed_mail_prototype_assert( false !== strpos( $message['mime'], 'X-WPP-Test: preserved' ), 'custom header missing from final MIME' );
		wpp_managed_mail_prototype_assert( false !== strpos( $message['mime'], 'Content-ID: <logo>' ), 'inline image CID missing from final MIME' );

		$before_short_circuit = count( WPP_Managed_Mail_Prototype_Mailer::$dispatches );
		$short_circuit = static function ( $return, $atts ) {
			return 'Short circuit' === $atts['subject'] ? true : $return;
		};
		add_filter( 'pre_wp_mail', $short_circuit, 10, 2 );
		$short_result = wp_mail( 'nobody@example.test', 'Short circuit', 'not sent' );
		remove_filter( 'pre_wp_mail', $short_circuit, 10 );
		wpp_managed_mail_prototype_assert( true === $short_result, 'pre_wp_mail return value was not respected' );
		wpp_managed_mail_prototype_assert( $before_short_circuit === count( WPP_Managed_Mail_Prototype_Mailer::$dispatches ), 'pre_wp_mail message was dispatched again' );
		wpp_managed_mail_prototype_assert( 1 === $events['succeeded'], 'short-circuited mail must not emit a second success action' );

		$password_user = get_user_by( 'login', 'wpp-mail-prototype-user' );
		if ( ! $password_user ) {
			$password_user_id = wp_create_user( 'wpp-mail-prototype-user', wp_generate_password( 32 ), 'password-recipient@example.test' );
			wpp_managed_mail_prototype_assert( ! is_wp_error( $password_user_id ), 'failed to create disposable password-reset user' );
		}
		$password_result = retrieve_password( 'wpp-mail-prototype-user' );
		wpp_managed_mail_prototype_assert( true === $password_result, 'WordPress password reset did not complete through fake transport' );
		wpp_managed_mail_prototype_assert( 2 === count( WPP_Managed_Mail_Prototype_Mailer::$dispatches ), 'password reset must dispatch exactly once' );
		$password_message = WPP_Managed_Mail_Prototype_Mailer::$dispatches[1];
		wpp_managed_mail_prototype_assert( false !== strpos( $password_message['body'], 'action=rp' ), 'password reset body lost its reset action URL' );
		wpp_managed_mail_prototype_assert( 2 === $events['succeeded'], 'password reset must emit one success action' );

		WPP_Managed_Mail_Prototype_Mailer::$fail_next = true;
		$failed_result = wp_mail( 'failure@example.test', 'Expected failure', 'failure body' );
		wpp_managed_mail_prototype_assert( false === $failed_result, 'failed fake transport must return false' );
		wpp_managed_mail_prototype_assert( 3 === count( WPP_Managed_Mail_Prototype_Mailer::$dispatches ), 'failure must still dispatch exactly once' );
		wpp_managed_mail_prototype_assert( 1 === $events['failed'], 'wp_mail_failed must fire exactly once' );
		wpp_managed_mail_prototype_assert( 2 === $events['succeeded'], 'failed mail must not emit success' );

		$context_callback = static function () {
			return wp_mail( 'cron@example.test', 'Scheduled callback', 'cron body' );
		};
		add_action( 'wpp_managed_mail_prototype_scheduled', $context_callback );
		do_action( 'wpp_managed_mail_prototype_scheduled' );
		wpp_managed_mail_prototype_assert( 4 === count( WPP_Managed_Mail_Prototype_Mailer::$dispatches ), 'hook/cron-style callback did not use the managed transport' );

		return array(
			'wordpress'  => get_bloginfo( 'version' ),
			'php'        => PHP_VERSION,
			'dispatches' => count( WPP_Managed_Mail_Prototype_Mailer::$dispatches ),
			'events'     => $events,
			'checks'     => array(
				'filters_and_final_message',
				'to_cc_bcc_reply_to',
				'attachment_and_embed',
				'pre_wp_mail_short_circuit',
				'wordpress_password_reset',
				'success_and_failure_actions',
				'hook_context_without_admin_session',
			),
		);
	} finally {
		@unlink( $attachment );
		@unlink( $embed );
		remove_filter( 'wp_mail_from', $from_filter );
		remove_filter( 'wp_mail_from_name', $name_filter );
		remove_filter( 'wp_mail_content_type', $type_filter );
		remove_filter( 'wp_mail_charset', $charset_filter );
		remove_action( 'phpmailer_init', $init_action );
		remove_action( 'wp_mail_succeeded', $success_action );
		remove_action( 'wp_mail_failed', $failed_action );
	}
}

add_action(
	'template_redirect',
	static function () {
		if ( ! isset( $_GET['wpp-managed-mail-prototype'] ) ) {
			return;
		}

		header( 'Content-Type: application/json; charset=utf-8' );
		try {
			echo wp_json_encode(
				array(
					'success' => true,
					'data'    => wpp_managed_mail_prototype_run(),
				),
				JSON_PRETTY_PRINT | JSON_UNESCAPED_SLASHES
			);
		} catch ( Throwable $error ) {
			status_header( 500 );
			echo wp_json_encode(
				array(
					'success' => false,
					'message' => $error->getMessage(),
				),
				JSON_PRETTY_PRINT | JSON_UNESCAPED_SLASHES
			);
		}
		exit;
	}
);
