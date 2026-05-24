=== WP Panel Optimizer ===
Contributors: naibabiji
Requires at least: 5.0
Tested up to: 7.0
Requires PHP: 8.1
Stable tag: 1.0.0
License: GPL-2.0+
License URI: https://www.gnu.org/licenses/gpl-2.0.html

与 WP Panel 面板配合使用，在 WordPress 后台管理 FastCGI 缓存、禁止检测更新、禁止文件编辑等优化项，与面板设置双向同步。

== Description ==

WP Panel Optimizer 是 [WP Panel](https://github.com/naibabiji/wp-panel) 的配套插件，通过面板 API 与服务器端面板实时同步优化设置。

作者：[naibabiji](https://blog.naibabiji.com) | 插件地址：[GitHub](https://github.com/naibabiji/wp-panel)

= 功能 =

* **FastCGI 缓存管理**：在 WordPress 后台开启/关闭 Nginx FastCGI 全站缓存，设置缓存有效期
* **禁止检测更新**：完全屏蔽 WordPress 更新检测（仪表盘无红点无通知，检查更新按钮也不生效）。如需更新，先关闭此开关再检查
* **禁止文件编辑**：写入 DISALLOW_FILE_EDIT 常量到 wp-config.php
* **管理栏快捷清除**：在 WordPress 管理栏一键清除 Nginx 缓存
* **自动清除缓存**：发布/更新/删除文章时自动清除缓存
* **与面板双向同步**：修改设置后自动推送到面板，也自动拉取面板最新状态

= 要求 =

* 已安装 WP Panel v1.0.0-beta2+
* 插件由面板自动安装（网站详情页 → WordPress 优化 → 安装配套插件），无需手动上传

== Installation ==

1. 在 WP Panel 面板中进入网站详情页
2. 在「WordPress 优化」卡片中勾选需要启用的优化项
3. 点击「安装配套插件」按钮，面板自动部署插件到网站 wp-content/plugins/
4. 在 WordPress 后台激活插件，或面板自动激活

插件安装后会在 wp-content/plugins/ 下自动生成 wp-panel-config.json 配置文件（含面板地址和 API Key），无需手动填写凭证。

== Changelog ==

= 1.0.0 =
* 初始版本
* FastCGI 缓存管理
* 禁止检测更新 / 禁止文件编辑
* 管理栏清除缓存按钮
* 发布/更新文章自动清除缓存
* 与面板 API 双向同步
