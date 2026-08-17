<?php
declare(strict_types=1);

// This launcher forges an Adminer login using credentials the panel already
// validated (see executor/adminer.go: ReadWebsiteDatabasePassword /
// validateAdminerCredentials), so the admin never has to type a database
// password for WordPress sites. It works by reverse-engineering Adminer's
// internal, undocumented login/session mechanics rather than calling a
// stable public API — see the WARNING in executor/adminer.go above the
// go:embed for the embedded Adminer build before touching this file or
// upgrading that build.

$server = getenv('WP_PANEL_ADMINER_SERVER');
$username = getenv('WP_PANEL_ADMINER_USER');
$password = getenv('WP_PANEL_ADMINER_PASSWORD');
$database = getenv('WP_PANEL_ADMINER_DATABASE');

// Adminer only sets up its own session (name "adminer_sid", secure cookie
// params) when none is active yet, so start that same session ourselves on
// every request. Otherwise the login-priming request below and Adminer's
// later requests (which read the stored password back out of $_SESSION)
// end up with two disjoint sessions and the auto-login silently fails.
//
// The session name is suffixed with the website ID so that concurrently
// running instances (one per website, all reverse-proxied under the same
// panel origin) never share a cookie: without this, enabling Adminer for
// site B overwrites the browser's cookie for site A's session, logging site
// A's tab out.
session_name('adminer_sid_' . getenv('WP_PANEL_ADMINER_SITE_ID'));
session_set_cookie_params([
    'lifetime' => 0,
    'path' => '/',
    'domain' => '',
    'secure' => getenv('WP_PANEL_ADMINER_SECURE_COOKIE') === '1',
    'httponly' => true,
    'samesite' => 'Lax',
]);
session_start();

if (!isset($_GET['username'])) {
    // Adminer 6 also requires a CSRF token on the login POST (verify_token()
    // checks $_POST['token'] against $_SESSION['token']). We control both
    // sides of that check, so mint a token that will satisfy it before
    // simulating the login form submission.
    if (empty($_SESSION['token'])) {
        $_SESSION['token'] = random_int(1, 1000000);
    }
    $_POST['token'] = $_SESSION['token'] . ':0';
    $_POST['auth'] = [
        'driver' => 'server',
        'server' => $server,
        'username' => $username,
        'password' => $password,
        'db' => $database,
    ];
}

function adminer_object() {
    return new class extends \Adminer\Adminer {
        public function credentials() {
            return [
                getenv('WP_PANEL_ADMINER_SERVER'),
                getenv('WP_PANEL_ADMINER_USER'),
                getenv('WP_PANEL_ADMINER_PASSWORD'),
            ];
        }

        public function database() {
            return getenv('WP_PANEL_ADMINER_DATABASE');
        }
    };
}

require __DIR__ . '/adminer.php';
