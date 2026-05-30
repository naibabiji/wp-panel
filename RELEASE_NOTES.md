## v1.2.0-rc6

### 功能改进
- **文件管理器解压支持 tar 格式**：解压功能新增支持 `.tar` / `.tar.gz` / `.tgz` / `.tar.bz2` / `.tbz2` 格式，不再仅限 `.zip`。
- **WordPress 优化新增调试模式、文章修订、内存限制**：面板网站详情页和配套插件（v1.1.0）同步新增三项 wp-config.php 管理功能——WP_DEBUG 调试模式开关（含 WP_DEBUG_LOG / WP_DEBUG_DISPLAY）、WP_POST_REVISIONS 文章修订版本数、WP_MEMORY_LIMIT PHP 内存限制。插件设置页含中文用途说明，面板与插件双向同步。
- **数据库信息按钮改名**：网站详情页数据库卡片中的"填写数据库信息"按钮改名为"同步数据库信息"，语义更明确。
- **WP_MEMORY_LIMIT 格式校验**：对内存限制输入增加正则校验，仅允许合法格式（纯数字或数字+K/M/G后缀），防止非法字符注入 wp-config.php。
