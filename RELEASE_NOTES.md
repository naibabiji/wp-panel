### Bug 修复
- 修复清空数据库功能误报失败（CombinedOutput 与 Start 冲突，数据库实际已清空）
- 登录表单增加 name 属性，支持浏览器密码自动填充
- 修复版本更新检测：pre-release 后缀（如 -beta3）被忽略导致检测不到新版本
- 新增自定义 favicon 和 logo 支持（static/logo.png）

### 新增功能
- **远程备份**：支持 SSH + rsync 将备份同步到远程服务器，可选密码或密钥认证，可选是否保留本地备份
