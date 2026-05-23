### 新增功能
- **通用 PHP 网站类型支持**：创建网站时可选择 WordPress 或通用 PHP 网站两种类型
- PHP 站点共享同一套 Nginx + PHP-FPM + MariaDB 环境，无需额外安装组件
- PHP 站点创建后为空目录，用户通过文件管理器自行上传代码
- PHP 站点采用独立 Nginx 模板，FastCGI 缓存跳过规则自动适配
- 网站列表和详情页自动根据类型显示/隐藏相关功能（重装、缓存插件等）

### 数据库管理增强
- **上传恢复支持多种格式**：`.sql` / `.sql.gz` / `.gz` / `.zip` 四种格式均可直接上传恢复
- **新增清空数据库功能**：网站详情页数据库区域可一键清空所有表
- **修复通用 PHP 网站修改密码报错**：不再强制读写 wp-config.php

### SSL 安全增强
- **新增 Nginx 默认 SSL 拦截**：未在面板创建的域名访问 443 端口时，TLS 握手直接拒绝（ssl_reject_handshake），防止证书跨站泄露
- 配合 nginx -t 语法校验，错误配置不会导致 nginx 不可用

### 文件管理器增强
- **上传进度条**：大文件上传实时显示百分比进度
- **上传后自动修正权限**：文件上传后自动设为 644，确保 Web 服务器可读取
- **新增权限修复按钮**：一键将网站目录内所有文件设为 644、目录 755，所有者设为系统用户:www-data

### 其他修复
- 登录表单增加 `name` 属性，支持浏览器密码自动填充

### 重要提示
**该版本新增了数据库 site_type 列，未做数据库自动迁移。升级后需全新安装（重装面板），请勿直接覆盖旧版本二进制文件。**

### 安装
```bash
apt-get update && apt-get install -y wget ca-certificates && wget -qO- https://raw.githubusercontent.com/naibabiji/wp-panel/main/install.sh | bash
```
