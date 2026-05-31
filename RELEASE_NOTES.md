## v1.2.1

- 修复：远程备份 SSH 主机密钥验证过严 (`StrictHostKeyChecking=yes`) 导致首次同步或 known_hosts 丢失后同步失败，改为与测试连接一致的 `accept-new`
