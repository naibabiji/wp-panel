<?php

declare(strict_types=1);

const WP_PANEL_INVENTORY_PROTOCOL = 'wp-panel-inventory';
const WP_PANEL_INVENTORY_RUNNER_VERSION = '1';
const WP_PANEL_INVENTORY_SCHEMA_VERSION = 1;
const WP_PANEL_INVENTORY_PLUGIN_LIMIT = 2000;
const WP_PANEL_INVENTORY_THEME_LIMIT = 1000;
const WP_PANEL_INVENTORY_UPDATE_LIMIT = 3000;
const WP_PANEL_INVENTORY_NAME_LIMIT = 512;
const WP_PANEL_INVENTORY_VERSION_LIMIT = 128;

$wpPanelInventoryState = array(
    'emitted' => false,
    'bootstrap_output_bytes' => 0,
    'protocol' => null,
);
$wpPanelInventoryEmergencyReserve = str_repeat('R', 256 * 1024);

function wp_panel_inventory_diagnostics(): array
{
    global $wpPanelInventoryState;

    $allowUrlInclude = filter_var(ini_get('allow_url_include'), FILTER_VALIDATE_BOOLEAN) ? '1' : '0';

    return array(
        'sapi' => PHP_SAPI,
        'effective_uid' => function_exists('posix_geteuid') ? (int) posix_geteuid() : -1,
        'effective_gid' => function_exists('posix_getegid') ? (int) posix_getegid() : -1,
        'open_basedir' => (string) ini_get('open_basedir'),
        'disable_functions' => (string) ini_get('disable_functions'),
        'allow_url_include' => $allowUrlInclude,
        'memory_limit' => (string) ini_get('memory_limit'),
        'bootstrap_output_bytes' => (int) $wpPanelInventoryState['bootstrap_output_bytes'],
    );
}

function wp_panel_inventory_emit(array $envelope): void
{
    global $wpPanelInventoryState;

    if ($wpPanelInventoryState['emitted']) {
        return;
    }
    $wpPanelInventoryState['emitted'] = true;
    $envelope['protocol'] = WP_PANEL_INVENTORY_PROTOCOL;
    $envelope['runner_version'] = WP_PANEL_INVENTORY_RUNNER_VERSION;
    $envelope['inventory_schema_version'] = WP_PANEL_INVENTORY_SCHEMA_VERSION;
    $envelope['diagnostics'] = wp_panel_inventory_diagnostics();
    $encoded = json_encode($envelope, JSON_UNESCAPED_SLASHES | JSON_UNESCAPED_UNICODE);
    if (!is_string($encoded)) {
        $encoded = '{"protocol":"wp-panel-inventory","runner_version":"1","inventory_schema_version":1,"ok":false,"error":{"code":"json_encode_failed"},"diagnostics":{"sapi":"cli","effective_uid":-1,"effective_gid":-1,"open_basedir":"","disable_functions":"","allow_url_include":"0","memory_limit":"256M","bootstrap_output_bytes":0}}';
    }
    $token = (string) getenv('WP_PANEL_RUNNER_TOKEN');
    $frame = 'WP_PANEL_INVENTORY_BEGIN ' . $token . "\n" . $encoded . "\n" . 'WP_PANEL_INVENTORY_END ' . $token . "\n";
    $protocol = $wpPanelInventoryState['protocol'];
    if (is_resource($protocol)) {
        @fwrite($protocol, $frame);
        @fflush($protocol);
    }
}

function wp_panel_inventory_fail(string $code): void
{
    wp_panel_inventory_emit(array('ok' => false, 'error' => array('code' => $code)));
}

function wp_panel_inventory_string(mixed $value, int $limit): string
{
    $value = is_scalar($value) ? (string) $value : '';
    if (strlen($value) > $limit || !preg_match('//u', $value)) {
        throw new LengthException('inventory value exceeds limit');
    }
    return $value;
}

function wp_panel_inventory_component_id(mixed $value): string
{
    $value = wp_panel_inventory_string($value, WP_PANEL_INVENTORY_NAME_LIMIT);
    if ($value === '' || str_contains($value, '\\') || str_starts_with($value, '/') || preg_match('~(^|/)\.\.(/|$)~', $value)) {
        throw new UnexpectedValueException('invalid component id');
    }
    return $value;
}

function wp_panel_inventory_object_value(mixed $object, string $key, mixed $default = ''): mixed
{
    if (is_object($object) && property_exists($object, $key)) {
        return $object->{$key};
    }
    if (is_array($object) && array_key_exists($key, $object)) {
        return $object[$key];
    }
    return $default;
}

function wp_panel_inventory_component_updates(string $transientName): array
{
    $transient = get_site_transient($transientName);
    $present = $transient !== false;
    $items = array();
    $responses = $present ? wp_panel_inventory_object_value($transient, 'response', array()) : array();
    if (is_array($responses)) {
        foreach ($responses as $id => $response) {
            $items[] = array(
                'id' => wp_panel_inventory_component_id($id),
                'version' => wp_panel_inventory_string(wp_panel_inventory_object_value($response, 'new_version'), WP_PANEL_INVENTORY_VERSION_LIMIT),
            );
        }
    }
    usort($items, static fn(array $a, array $b): int => strcmp($a['id'], $b['id']));

    return array(
        'transient_present' => $present,
        'last_checked' => $present ? (int) wp_panel_inventory_object_value($transient, 'last_checked', 0) : 0,
        'items' => $items,
    );
}

function wp_panel_inventory_core_updates(): array
{
    $transient = get_site_transient('update_core');
    $present = $transient !== false;
    $items = array();
    $updates = $present ? wp_panel_inventory_object_value($transient, 'updates', array()) : array();
    if (is_array($updates)) {
        foreach ($updates as $update) {
            $items[] = array(
                'version' => wp_panel_inventory_string(wp_panel_inventory_object_value($update, 'current'), WP_PANEL_INVENTORY_VERSION_LIMIT),
                'response' => wp_panel_inventory_string(wp_panel_inventory_object_value($update, 'response'), WP_PANEL_INVENTORY_VERSION_LIMIT),
                'locale' => wp_panel_inventory_string(wp_panel_inventory_object_value($update, 'locale'), WP_PANEL_INVENTORY_VERSION_LIMIT),
            );
        }
    }
    usort($items, static function (array $a, array $b): int {
        return strcmp($a['version'] . "\0" . $a['locale'] . "\0" . $a['response'], $b['version'] . "\0" . $b['locale'] . "\0" . $b['response']);
    });

    return array(
        'transient_present' => $present,
        'last_checked' => $present ? (int) wp_panel_inventory_object_value($transient, 'last_checked', 0) : 0,
        'version_checked' => $present ? wp_panel_inventory_string(wp_panel_inventory_object_value($transient, 'version_checked'), WP_PANEL_INVENTORY_VERSION_LIMIT) : '',
        'items' => $items,
    );
}

function wp_panel_inventory_maybe_force_update_check(): void
{
    if (getenv('WP_PANEL_FORCE_UPDATE_CHECK') !== '1') {
        return;
    }
    if (!function_exists('wp_update_plugins')) {
        $adminUpdate = ABSPATH . 'wp-admin/includes/update.php';
        if (is_file($adminUpdate)) {
            require_once $adminUpdate;
        }
    }
    foreach (array('update_core', 'update_plugins', 'update_themes') as $name) {
        delete_site_transient($name);
    }
    if (function_exists('wp_version_check')) {
        wp_version_check();
    }
    if (function_exists('wp_update_plugins')) {
        wp_update_plugins();
    }
    if (function_exists('wp_update_themes')) {
        wp_update_themes();
    }
}

function wp_panel_inventory_collect(): array
{
    global $wp_version;

    if (!function_exists('get_plugins')) {
        require_once ABSPATH . 'wp-admin/includes/plugin.php';
    }
    wp_panel_inventory_maybe_force_update_check();
    $pluginRows = array();
    $activePlugins = array_fill_keys((array) get_option('active_plugins', array()), true);
    $networkPlugins = is_multisite() ? (array) get_site_option('active_sitewide_plugins', array()) : array();
    foreach (get_plugins() as $file => $plugin) {
        $file = wp_panel_inventory_component_id($file);
        $pluginRows[] = array(
            'file' => $file,
            'name' => wp_panel_inventory_string($plugin['Name'] ?? '', WP_PANEL_INVENTORY_NAME_LIMIT),
            'version' => wp_panel_inventory_string($plugin['Version'] ?? '', WP_PANEL_INVENTORY_VERSION_LIMIT),
            'active' => isset($activePlugins[$file]) || isset($networkPlugins[$file]),
            'network_active' => isset($networkPlugins[$file]),
        );
    }
    if (count($pluginRows) > WP_PANEL_INVENTORY_PLUGIN_LIMIT) {
        throw new OverflowException('plugin limit');
    }
    usort($pluginRows, static fn(array $a, array $b): int => strcmp($a['file'], $b['file']));

    $themeRows = array();
    foreach (wp_get_themes() as $stylesheet => $theme) {
        $themeRows[] = array(
            'stylesheet' => wp_panel_inventory_component_id($stylesheet),
            'name' => wp_panel_inventory_string($theme->get('Name'), WP_PANEL_INVENTORY_NAME_LIMIT),
            'version' => wp_panel_inventory_string($theme->get('Version'), WP_PANEL_INVENTORY_VERSION_LIMIT),
        );
    }
    if (count($themeRows) > WP_PANEL_INVENTORY_THEME_LIMIT) {
        throw new OverflowException('theme limit');
    }
    usort($themeRows, static fn(array $a, array $b): int => strcmp($a['stylesheet'], $b['stylesheet']));

    $current = wp_get_theme();
    $currentTheme = $current->exists() ? array(
        'stylesheet' => wp_panel_inventory_component_id($current->get_stylesheet()),
        'name' => wp_panel_inventory_string($current->get('Name'), WP_PANEL_INVENTORY_NAME_LIMIT),
        'version' => wp_panel_inventory_string($current->get('Version'), WP_PANEL_INVENTORY_VERSION_LIMIT),
    ) : null;

    $coreUpdates = wp_panel_inventory_core_updates();
    $pluginUpdates = wp_panel_inventory_component_updates('update_plugins');
    $themeUpdates = wp_panel_inventory_component_updates('update_themes');
    if (count($coreUpdates['items']) + count($pluginUpdates['items']) + count($themeUpdates['items']) > WP_PANEL_INVENTORY_UPDATE_LIMIT) {
        throw new OverflowException('update limit');
    }

    return array(
        'wordpress' => array(
            'version' => wp_panel_inventory_string($wp_version ?? '', WP_PANEL_INVENTORY_VERSION_LIMIT),
            'locale' => wp_panel_inventory_string(get_locale(), WP_PANEL_INVENTORY_VERSION_LIMIT),
            'multisite' => is_multisite(),
        ),
        'plugins' => $pluginRows,
        'themes' => $themeRows,
        'current_theme' => $currentTheme,
        'updates' => array('core' => $coreUpdates, 'plugins' => $pluginUpdates, 'themes' => $themeUpdates),
    );
}

$token = (string) getenv('WP_PANEL_RUNNER_TOKEN');
$wpPanelInventoryState['protocol'] = @fopen('php://fd/3', 'wb');
if (PHP_SAPI !== 'cli' || !preg_match('/^[0-9a-f]{32}$/', $token) || !is_resource($wpPanelInventoryState['protocol'])) {
    wp_panel_inventory_fail('invalid_sapi');
    exit(2);
}

ob_start(static function (string $buffer) use (&$wpPanelInventoryState): string {
    $wpPanelInventoryState['bootstrap_output_bytes'] += strlen($buffer);
    return '';
}, 1);

register_shutdown_function(static function () use (&$wpPanelInventoryState, &$wpPanelInventoryEmergencyReserve): void {
    $wpPanelInventoryEmergencyReserve = null;
    if ($wpPanelInventoryState['emitted']) {
        return;
    }
    $last = error_get_last();
    if (is_array($last)) {
        $message = (string) ($last['message'] ?? '');
        wp_panel_inventory_fail(str_contains($message, 'Allowed memory size') ? 'memory_limit_exhausted' : 'fatal_error');
        return;
    }
    wp_panel_inventory_fail('bootstrap_terminated');
});

$siteRoot = $argv[1] ?? '';
$realSiteRoot = is_string($siteRoot) ? realpath($siteRoot) : false;
if ($realSiteRoot === false || !is_dir($realSiteRoot)) {
    wp_panel_inventory_fail('invalid_site_root');
    exit(2);
}
$wpLoad = $realSiteRoot . DIRECTORY_SEPARATOR . 'wp-load.php';
$realWpLoad = realpath($wpLoad);
if ($realWpLoad === false || dirname($realWpLoad) !== $realSiteRoot || !is_file($realWpLoad)) {
    wp_panel_inventory_fail('invalid_wp_load');
    exit(2);
}

define('WP_PANEL_INVENTORY_RUNNER', true);
define('WP_USE_THEMES', false);
define('DISABLE_WP_CRON', true);

try {
    require $realWpLoad;
    $inventory = wp_panel_inventory_collect();
    wp_panel_inventory_emit(array('ok' => true, 'data' => $inventory));
} catch (OverflowException | LengthException | UnexpectedValueException $error) {
    wp_panel_inventory_fail('inventory_limit_exceeded');
    exit(3);
} catch (Throwable $error) {
    wp_panel_inventory_fail('bootstrap_throwable');
    exit(3);
}
