### 新增功能
- **通用 PHP 网站类型支持**：创建网站时可选择 WordPress 或通用 PHP 网站两种类型
- PHP 站点共享同一套 Nginx + PHP-FPM + MariaDB 环境，无需额外安装组件
- PHP 站点创建后为空目录，用户通过文件管理器自行上传代码
- PHP 站点采用独立 Nginx 模板，FastCGI 缓存跳过规则自动适配
- 网站列表和详情页自动根据类型显示/隐藏相关功能（重装、缓存插件等）

### 重要提示
**该版本新增了数据库 site_type 列，未做数据库自动迁移。升级后需全新安装（重装面板），请勿直接覆盖旧版本二进制文件。**

### 安装
```bash
apt-get update && apt-get install -y wget ca-certificates && wget -qO- https://raw.githubusercontent.com/naibabiji/wp-panel/main/install.sh | bash
```
