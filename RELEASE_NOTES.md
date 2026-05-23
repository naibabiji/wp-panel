### 数据库管理增强
- **上传恢复支持多种格式**：`.sql` / `.sql.gz` / `.gz` / `.zip` 四种格式均可直接上传恢复
- **新增清空数据库功能**：网站详情页数据库区域可一键清空所有表
- **修复通用 PHP 网站修改密码报错**：不再强制读写不存在的 wp-config.php

### SSL 安全增强
- **新增 Nginx 默认 SSL 拦截**：未在面板创建的域名访问 443 端口时，TLS 握手直接拒绝（ssl_reject_handshake），防止证书跨站泄露

### 文件管理器增强
- **上传进度条**：大文件上传实时显示百分比进度
- **上传后自动修正权限**：文件上传后自动设为 644，确保 Web 服务器可读取
- **新增权限修复按钮**：一键将网站目录内文件设为 644、目录 755，所有者设为系统用户:www-data

### 其他修复
- 登录表单增加 `name` 属性，支持浏览器密码自动填充

### 重要提示
**该版本新增了数据库 site_type 列，未做数据库自动迁移。升级后需全新安装（重装面板），请勿直接覆盖旧版本二进制文件。**

### 安装
```bash
apt-get update && apt-get install -y wget ca-certificates && wget -qO- https://raw.githubusercontent.com/naibabiji/wp-panel/main/install.sh | bash
```
