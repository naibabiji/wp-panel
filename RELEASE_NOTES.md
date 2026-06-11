# 更新说明

## v1.2.11

### 安全
- WordPress 站点新增可疑访问分析：记录高风险异常文件路径到 `wp-security.log`，并在「安全防御」页面提供本地统计、风险等级和可复制报告，管理员可自行结合 AI 或 IP 查询工具判断
- 新增 WordPress 安全日志路径白名单，仅在服务器面板后台配置，用于排除站点所有权验证文件等正常路径；通用 PHP 站点不加载该规则
- WordPress Nginx 模板对不存在的 PHP 文件先返回 404，避免扫描请求进入 PHP-FPM 产生 `Primary script unknown` 和资源占用
- Nginx 日志规则保存时增加 `nginx -t` 预检和失败回滚，避免错误白名单配置影响现有站点

### 优化
- 遥测心跳改为 UTC 00:00 统一上报，解决不同时区面板统计窗口不一致的问题
- 活跃统计从 48 小时窗口改为精确 24 小时（UTC 当日），数值更准确
- 新装面板首次启动立即上报，后续更新或重启不再重复立即上报
- Nginx 模板新增 FastCGI 缓冲指令（buffer_size 128k、buffers 8 128k、busy_buffers_size 256k），解决大响应头被截断的问题
- WP Panel Optimizer 升级到 1.1.2，修复启用 open_basedir 时 www/裸域配置探测可能写入 PHP Warning 的问题
- WordPress 可疑访问分析增加 30 秒内存缓存，降低多站点或慢 I/O 场景下频繁刷新安全防御页的开销
