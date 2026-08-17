<?php
// 历史图库批量优化 filesize 回写 runner。
//
// 面板 Go 侧扫描 wp-content/uploads/ 并调用 jpegoptim/optipng 原地无损重编码后，
// 文件字节数变了但文件名没变；_wp_attachment_metadata 里缓存的 filesize 字段
// 不会自动更新。这个脚本接收一份"相对路径 -> 新字节数"的清单，在 WordPress
// 运行环境里用 update_post_meta()/wp_update_attachment_metadata() 完成回写——
// 序列化格式和对象缓存失效都交给 WordPress 自己处理，这个脚本不直接碰数据库。
//
// argv: token, site_root, manifest_file, result_file
$token = $argv[1] ?? '';
$site_root = $argv[2] ?? '';
$manifest_file = $argv[3] ?? '';
$result_file = $argv[4] ?? '';

$sent = false;
$send = function ($ok, $updated = 0, $total = 0, $code = '') use (&$sent, $token, $result_file) {
    if ($sent) return;
    $sent = true;
    $body = json_encode(['token' => $token, 'ok' => $ok, 'updated' => $updated, 'total' => $total, 'error_code' => $code], JSON_UNESCAPED_SLASHES);
    $f = @fopen($result_file, 'c+b');
    if (!$f) return;
    @flock($f, LOCK_EX);
    @ftruncate($f, 0);
    @fwrite($f, $body);
    @fflush($f);
    if (function_exists('fsync')) @fsync($f);
    @flock($f, LOCK_UN);
    @fclose($f);
};
register_shutdown_function(function () use (&$sent, $send) {
    if (!$sent) $send(false, 0, 0, 'fatal_error');
});

if (
    PHP_SAPI !== 'cli'
    || !preg_match('/^[0-9a-f]{32}$/D', $token)
    || !is_dir($site_root) || realpath($site_root) !== rtrim($site_root, '/')
    || !is_file($manifest_file) || realpath($manifest_file) !== $manifest_file
    || !is_file($result_file) || realpath($result_file) !== $result_file
) {
    $send(false, 0, 0, 'invalid_input');
    exit(2);
}

$manifest_raw = @file_get_contents($manifest_file);
$manifest = $manifest_raw !== false ? json_decode($manifest_raw, true) : null;
if (!is_array($manifest) || empty($manifest)) {
    $send(false, 0, 0, 'invalid_manifest');
    exit(2);
}

chdir($site_root);
ob_start();
if (!defined('WP_USE_THEMES')) define('WP_USE_THEMES', false);
require $site_root . '/wp-load.php';
if (ob_get_length() > 0) {
    $send(false, 0, 0, 'bootstrap_output');
    exit(1);
}

// relative_path => new_size，按目录分组，一个目录一次 postmeta 查询，
// 避免逐个附件反查（WordPress 没有"按文件路径找 attachment"的核心 API）。
$byDir = [];
foreach ($manifest as $relativePath => $newSize) {
    $relativePath = ltrim((string) $relativePath, '/');
    $newSize = (int) $newSize;
    if ($relativePath === '' || $newSize <= 0) continue;
    $dir = dirname($relativePath);
    $base = basename($relativePath);
    $byDir[$dir][$base] = $newSize;
}

global $wpdb;
$updated = 0;
$total = 0;
foreach ($byDir as $dir => $basenames) {
    $total += count($basenames);
    $like = $wpdb->esc_like($dir . '/') . '%';
    $rows = $wpdb->get_results(
        $wpdb->prepare(
            "SELECT post_id, meta_value FROM {$wpdb->postmeta} WHERE meta_key = '_wp_attached_file' AND meta_value LIKE %s",
            $like
        )
    );
    if (empty($rows)) continue;

    $matchedInDir = [];
    foreach ($rows as $row) {
        $attachmentId = (int) $row->post_id;
        $attachedBase = basename((string) $row->meta_value);
        $metaChanged = false;
        $backupChanged = false;

        $meta = get_post_meta($attachmentId, '_wp_attachment_metadata', true);
        if (is_array($meta)) {
            if (isset($basenames[$attachedBase])) {
                $meta['filesize'] = $basenames[$attachedBase];
                $metaChanged = true;
                $matchedInDir[$attachedBase] = true;
            }
            if (!empty($meta['sizes']) && is_array($meta['sizes'])) {
                foreach ($meta['sizes'] as $sizeKey => $sizeInfo) {
                    if (!is_array($sizeInfo) || empty($sizeInfo['file'])) continue;
                    $sizeBase = basename((string) $sizeInfo['file']);
                    if (isset($basenames[$sizeBase])) {
                        $meta['sizes'][$sizeKey]['filesize'] = $basenames[$sizeBase];
                        $metaChanged = true;
                        $matchedInDir[$sizeBase] = true;
                    }
                }
            }
        }

        $backup = get_post_meta($attachmentId, '_wp_attachment_backup_sizes', true);
        if (is_array($backup)) {
            foreach ($backup as $backupKey => $backupInfo) {
                if (!is_array($backupInfo) || empty($backupInfo['file'])) continue;
                $backupBase = basename((string) $backupInfo['file']);
                if (isset($basenames[$backupBase])) {
                    $backup[$backupKey]['filesize'] = $basenames[$backupBase];
                    $backupChanged = true;
                    $matchedInDir[$backupBase] = true;
                }
            }
        }

        if ($metaChanged) {
            update_post_meta($attachmentId, '_wp_attachment_metadata', $meta);
        }
        if ($backupChanged) {
            update_post_meta($attachmentId, '_wp_attachment_backup_sizes', $backup);
        }
    }
    // 按文件（basename）计数，跟 $total 的统计口径一致，而不是按附件计数。
    $updated += count($matchedInDir);
}

$send(true, $updated, $total, '');
