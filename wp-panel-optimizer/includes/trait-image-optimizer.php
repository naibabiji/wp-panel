<?php
/**
 * WP Panel Optimizer — 新上传图片处理模块
 *
 * 在 wp_handle_upload_prefilter / wp_handle_sideload_prefilter（WordPress 上传链路
 * 里最早的介入点）把新上传的 JPEG/PNG 按用户选择的模式重新编码或转换为 WebP。
 * 处理发生在附件记录创建之前，WordPress 后续的 guid/_wp_attached_file/
 * _wp_attachment_metadata 全部原生基于转换后的文件产出，不需要事后改写任何字段。
 */

if (!defined('ABSPATH')) exit;

trait WPP_Optimizer_Image_Trait {

    const OPTION_IMAGE_MODE          = 'wpp_optimizer_image_mode';
    const OPTION_IMAGE_JPEG_QUALITY  = 'wpp_optimizer_image_jpeg_quality';
    const OPTION_IMAGE_WEBP_QUALITY  = 'wpp_optimizer_image_webp_quality';
    const OPTION_IMAGE_SKIPPED_COUNT = 'wpp_optimizer_image_skipped_count';

    const IMAGE_MODE_OFF      = 'off';
    const IMAGE_MODE_OPTIMIZE = 'optimize';
    const IMAGE_MODE_WEBP     = 'webp';

    // 前置校验上限：大小、像素乘积。像素上限没有 WordPress 核心常量可参照，
    // 按经验值（约等于 memory_limit 字节数 / 4）给一个固定值，避免解压炸弹式
    // 畸形文件在通过校验后才被 GD 完整解码。
    const IMAGE_MAX_UPLOAD_BYTES = 26214400;  // 25MB
    const IMAGE_MAX_PIXELS       = 100000000; // 1 亿像素（如 10000x10000）

    /**
     * 服务器是否具备新上传图片处理的运行环境。exif 扩展缺失是默认状态而不是
     * 边缘情况（install.sh 目前不装 php8.3-exif），WordPress 自己的
     * maybe_exif_rotate() 同样以 is_callable('exif_read_data') 为前提；
     * 没有这个前提就不能保证方向修正，因此整体禁用，不做"跳过修正继续转换"。
     */
    public static function image_optimizer_env_ready() {
        return is_callable('exif_read_data')
            && function_exists('imagecreatefromjpeg')
            && function_exists('imagecreatefrompng')
            && function_exists('imagejpeg')
            && function_exists('imagepng')
            && function_exists('imagerotate')
            && function_exists('imageflip');
    }

    public static function image_optimizer_mode() {
        if (!self::image_optimizer_env_ready()) {
            return self::IMAGE_MODE_OFF;
        }
        $mode = get_option(self::OPTION_IMAGE_MODE, self::IMAGE_MODE_OFF);
        if (!in_array($mode, [self::IMAGE_MODE_OFF, self::IMAGE_MODE_OPTIMIZE, self::IMAGE_MODE_WEBP], true)) {
            return self::IMAGE_MODE_OFF;
        }
        return $mode;
    }

    public static function image_optimizer_init() {
        // 注册在非常大的 priority，让安全类插件（病毒扫描、大小限制）等其他挂在
        // 同一钩子上的逻辑先对原始文件做完校验，不合规的文件不会先被我们拿去做
        // 重量级的 GD 解码。
        add_filter('wp_handle_upload_prefilter', [__CLASS__, 'convert_uploaded_image'], PHP_INT_MAX);
        add_filter('wp_handle_sideload_prefilter', [__CLASS__, 'convert_uploaded_image'], PHP_INT_MAX);

        // 历史图库批量优化的实际扫描/执行都在面板侧（需要降权 runuser 调用系统
        // 二进制，插件的 PHP-FPM 环境做不到），插件这边只是把设置页的按钮转发成
        // 对面板 /api/sites/image-optimizer/* 的请求——跟 fetch_panel_state() 等
        // 现有面板通信一样走 X-WP-Panel-Key 认证，不需要插件自己维护任务状态。
        add_action('wp_ajax_wpp_optimizer_image_batch_start', [__CLASS__, 'ajax_image_batch_start']);
        add_action('wp_ajax_wpp_optimizer_image_batch_status', [__CLASS__, 'ajax_image_batch_status']);
        add_action('wp_ajax_wpp_optimizer_image_batch_stop', [__CLASS__, 'ajax_image_batch_stop']);
    }

    public static function ajax_image_batch_start() {
        check_ajax_referer('wpp_optimizer_settings');
        if (!current_user_can('manage_options')) {
            wp_send_json(['success' => false, 'data' => ['message' => '权限不足']]);
            return;
        }
        $domain = wp_parse_url(home_url(), PHP_URL_HOST);
        self::relay_image_batch_response(
            self::api_request_public('POST', '/api/sites/image-optimizer/start', ['domain' => $domain])
        );
    }

    public static function ajax_image_batch_status() {
        check_ajax_referer('wpp_optimizer_settings');
        if (!current_user_can('manage_options')) {
            wp_send_json(['success' => false, 'data' => ['message' => '权限不足']]);
            return;
        }
        $domain = wp_parse_url(home_url(), PHP_URL_HOST);
        self::relay_image_batch_response(
            self::api_request_public('GET', '/api/sites/image-optimizer/status?domain=' . urlencode($domain))
        );
    }

    public static function ajax_image_batch_stop() {
        check_ajax_referer('wpp_optimizer_settings');
        if (!current_user_can('manage_options')) {
            wp_send_json(['success' => false, 'data' => ['message' => '权限不足']]);
            return;
        }
        $domain = wp_parse_url(home_url(), PHP_URL_HOST);
        self::relay_image_batch_response(
            self::api_request_public('POST', '/api/sites/image-optimizer/stop', ['domain' => $domain])
        );
    }

    private static function relay_image_batch_response($resp) {
        if (is_wp_error($resp)) {
            wp_send_json(['success' => false, 'data' => ['message' => $resp->get_error_message()]]);
            return;
        }
        $data = json_decode($resp, true);
        if (!is_array($data) || empty($data['success'])) {
            wp_send_json(['success' => false, 'data' => ['message' => ($data['message'] ?? '面板返回错误')]]);
            return;
        }
        wp_send_json(['success' => true, 'data' => $data['data'] ?? null]);
    }

    /**
     * wp_handle_upload_prefilter / wp_handle_sideload_prefilter 回调。
     * 任何一步不满足就原样放行 $file，绝不能让上传本身因为这里的处理失败而失败。
     */
    public static function convert_uploaded_image($file) {
        $mode = self::image_optimizer_mode();
        if ($mode === self::IMAGE_MODE_OFF) {
            return $file;
        }

        // 第 0 步：上传本身已失败（或其他插件已经在 error 里写了拒绝原因，可能
        // 是非数字字符串），零成本直接跳过；不能用 intval() 判断，字符串错误消息
        // intval() 后是 0，会被误判为"没有错误"。
        if (!empty($file['error'])) {
            return $file;
        }
        if (empty($file['tmp_name']) || empty($file['name']) || !is_readable($file['tmp_name'])) {
            return $file;
        }

        // 第 1 步：大小上限。wp_handle_sideload_prefilter 路径下 $file 没有 size
        // 键（REST 原始二进制上传），必须用 filesize() 兜底，不能直接读 $file['size']。
        $size = (isset($file['size']) && $file['size'] > 0) ? intval($file['size']) : @filesize($file['tmp_name']);
        if (!$size || $size > self::IMAGE_MAX_UPLOAD_BYTES) {
            return $file;
        }

        // 第 2 步：真实类型嗅探（只读文件头，不做完整解码），只放行 JPEG/PNG。
        $imageInfo = @getimagesize($file['tmp_name']);
        if (!$imageInfo || empty($imageInfo['mime'])) {
            return $file;
        }
        $sourceMime = $imageInfo['mime'];
        if ($sourceMime === 'image/webp') {
            // 幂等：本来就是 webp，不需要处理。
            return $file;
        }
        if (!in_array($sourceMime, ['image/jpeg', 'image/png'], true)) {
            return $file;
        }

        // 第 3 步：像素乘积上限，拒绝"解压炸弹"式畸形文件；真正的完整解码
        // （imagecreatefromjpeg/imagecreatefrompng）是唯一的重量级步骤，必须在
        // 前面全部通过之后才执行。
        $width  = isset($imageInfo[0]) ? intval($imageInfo[0]) : 0;
        $height = isset($imageInfo[1]) ? intval($imageInfo[1]) : 0;
        if ($width <= 0 || $height <= 0 || ($width * $height) > self::IMAGE_MAX_PIXELS) {
            return $file;
        }

        $image = ($sourceMime === 'image/jpeg')
            ? @imagecreatefromjpeg($file['tmp_name'])
            : @imagecreatefrompng($file['tmp_name']);
        if (!$image) {
            // GD 不支持的变体（例如 CMYK JPEG）会在这里返回 false。
            self::bump_image_skipped_count();
            return $file;
        }

        if ($sourceMime === 'image/jpeg') {
            self::apply_exif_orientation($image, $file['tmp_name']);
        }

        $targetMime = ($mode === self::IMAGE_MODE_WEBP) ? 'image/webp' : $sourceMime;
        $encoded = self::encode_image($image, $targetMime);
        imagedestroy($image);

        if ($encoded === false || $encoded === '') {
            self::bump_image_skipped_count();
            return $file;
        }

        // 体积比较必须在覆盖写回 tmp_name 之前完成——如果实现成"先覆盖、再比较、
        // 发现更大再后悔"，这时候原文件已经被转换结果覆盖掉了，兜底名存实亡。
        if (strlen($encoded) >= $size) {
            self::bump_image_skipped_count();
            return $file;
        }

        // 原地覆盖写回 tmp_name 这同一个路径，不能新建临时文件后把 tmp_name 指
        // 过去——wp_handle_upload 这个 action 路径下，prefilter 之后 WordPress
        // 会用 is_uploaded_file($file['tmp_name']) 校验这个路径确实是本次请求里
        // PHP 通过 HTTP POST 收到的上传文件，指向新建文件会导致校验失败、上传被拒绝。
        //
        // file_put_contents 默认按 'w' 模式打开即截断原文件，磁盘满等情况下可能
        // 写到一半失败，留下损坏的临时文件；先把原始字节读进内存（前面已校验
        // 大小不超过 IMAGE_MAX_UPLOAD_BYTES），写失败时尽力恢复，不留半写文件。
        $originalBytes = @file_get_contents($file['tmp_name']);
        if ($originalBytes === false) {
            self::bump_image_skipped_count();
            return $file;
        }
        if (@file_put_contents($file['tmp_name'], $encoded) === false) {
            @file_put_contents($file['tmp_name'], $originalBytes);
            self::bump_image_skipped_count();
            return $file;
        }

        if ($targetMime !== $sourceMime) {
            $newName = preg_replace('/\.(jpe?g|png)$/i', '.webp', $file['name']);
            if (!$newName || $newName === $file['name']) {
                $newName = $file['name'] . '.webp';
            }
            $file['name'] = $newName;
        }
        $file['type'] = $targetMime;

        return $file;
    }

    /**
     * 按 EXIF orientation 把像素转正，完整覆盖 2-8 全部 8 种取值，直接照抄
     * WP_Image_Editor::maybe_exif_rotate() 的 switch 分支（case 2/3/4 用翻转，
     * case 5/6/7/8 用旋转，5/7 是"先转 90 度再翻转"的组合），不自行推导。
     */
    private static function apply_exif_orientation(&$image, $path) {
        $exif = @exif_read_data($path);
        if (!$exif || empty($exif['Orientation'])) {
            return;
        }

        $orientation = intval($exif['Orientation']);
        switch ($orientation) {
            case 2:
                imageflip($image, IMG_FLIP_HORIZONTAL);
                break;
            case 3:
                imageflip($image, IMG_FLIP_BOTH);
                break;
            case 4:
                imageflip($image, IMG_FLIP_VERTICAL);
                break;
            case 5:
                $rotated = imagerotate($image, 90, 0);
                if ($rotated !== false) {
                    $image = $rotated;
                    imageflip($image, IMG_FLIP_VERTICAL);
                }
                break;
            case 6:
                $rotated = imagerotate($image, 270, 0);
                if ($rotated !== false) {
                    $image = $rotated;
                }
                break;
            case 7:
                $rotated = imagerotate($image, 90, 0);
                if ($rotated !== false) {
                    $image = $rotated;
                    imageflip($image, IMG_FLIP_HORIZONTAL);
                }
                break;
            case 8:
                $rotated = imagerotate($image, 90, 0);
                if ($rotated !== false) {
                    $image = $rotated;
                }
                break;
        }
    }

    /**
     * 把 GD 图像资源编码为目标格式，返回文件内容字符串（不直接写文件，方便调用方
     * 在覆盖 tmp_name 之前先做体积比较）。失败返回 false。
     */
    private static function encode_image($image, $targetMime) {
        ob_start();
        $ok = false;
        if ($targetMime === 'image/webp') {
            $quality = self::clamp_image_quality(get_option(self::OPTION_IMAGE_WEBP_QUALITY, 82));
            $ok = function_exists('imagewebp') && imagewebp($image, null, $quality);
        } elseif ($targetMime === 'image/jpeg') {
            $quality = self::clamp_image_quality(get_option(self::OPTION_IMAGE_JPEG_QUALITY, 85));
            $ok = imagejpeg($image, null, $quality);
        } elseif ($targetMime === 'image/png') {
            imagesavealpha($image, true);
            $ok = imagepng($image, null, 9);
        }
        $data = ob_get_clean();
        return $ok ? $data : false;
    }

    private static function clamp_image_quality($value) {
        $value = intval($value);
        if ($value < 1) return 1;
        if ($value > 100) return 100;
        return $value;
    }

    /**
     * 静默放行场景（前置校验拒绝、GD 解码/编码失败、负优化）的轻量计数，避免
     * 完全没有埋点——用户在设置页能看到"有 N 张图片没有转换成功"。
     */
    private static function bump_image_skipped_count() {
        $count = intval(get_option(self::OPTION_IMAGE_SKIPPED_COUNT, 0));
        update_option(self::OPTION_IMAGE_SKIPPED_COUNT, $count + 1, false);
    }
}
