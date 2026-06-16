# 爬虫 UA 统一限速 + 搜索引擎 IP 验证方案

状态：待审核（已按讨论修订）

日期：2026-06-16

---

## 一、问题背景

### 1.1 当前遇到的问题

示例访问日志：

```log
2a03:2880:f800:19:: - - [16/Jun/2026:04:08:16 +0000] "GET /products/19745486 HTTP/2.0" 404 32602 "-" "meta-externalagent/1.1 (+https://developers.facebook.com/docs/sharing/webmasters/crawler)"
2a03:2880:f800:11:: - - [16/Jun/2026:04:08:19 +0000] "GET /products/16111119 HTTP/2.0" 404 32600 "-" "meta-externalagent/1.1 (+https://developers.facebook.com/docs/sharing/webmasters/crawler)"
```

特征：

- 请求来自大量不同 IPv6 地址。
- UA 统一伪装为 Facebook / Meta 爬虫，例如 `meta-externalagent`。
- 请求不存在的页面，触发 WordPress 输出完整 404 页面。
- 单 IP 请求数不高，但整体请求量持续不断。

核心问题不是单个 IP 暴力访问，而是“海量 IP + 伪装爬虫 UA + 持续打到 WordPress 动态页面”。

### 1.2 现有防御为什么不够

| 现有机制 | 检测维度 | 局限 |
|---|---|---|
| Nginx IP 限速 | 单个 IP | 海量 IP 分摊后，每个 IP 不一定超限 |
| Fail2ban 429/404 | 单个 IP | 同样依赖单 IP 多次违规 |
| Nginx 黑名单 | 单个 IP | 封一个 IP，攻击流量可换下一个 IP |
| 面板扫描防御 | 面板端口 | 保护面板自身，不保护托管网站 |

因此，本方案的目标不是替代现有防御，而是在 Nginx 层增加一层“按爬虫 UA 类别汇总限速”的保护。

---

## 二、目标和非目标

### 2.1 目标

- 限制伪装 Facebook / Meta 等爬虫的分布式扫描，避免服务器被 WordPress 404 动态渲染拖垮。
- 普通浏览器访问不进入新增 bot 限速桶。
- 真 Googlebot / Bingbot 尽量不受新增 bot 限速影响。
- 假冒 Googlebot / Bingbot 不能因为 UA 名字就绕过防护。
- 多站点之间互不拖累，一个站被爬不影响另一个站的 bot 限速额度。
- 不引入新技术栈，不依赖外部 WAF/CDN。
- 第一版 UA 匹配列表采用代码内置规则，不开放自定义 Nginx 正则配置。

### 2.2 非目标

- 不尝试彻底解决应用层 DDoS。
- 不识别所有合法/非法爬虫身份。
- 不做按路径、按业务 URL 的定制拦截。
- 不把 WordPress 插件层作为主要防线，因为那时请求已经进入 PHP/WordPress。
- 第一版不提供自定义 Bot UA 匹配 UI；后续如确有需要，作为高级功能单独评审。

---

## 三、推荐方案：统一 Bot 桶，按站点隔离

### 3.1 核心思路

保留现有 IP 限速，新增一个统一 bot 限速桶：

```text
请求进入
  -> 现有 IP 限速（按 IP，保护单 IP 滥用）
  -> 新增 Bot UA 限速（按站点 + bot 类别，保护多 IP 同类爬虫）
  -> 任一超限返回 429
  -> 429 仍可被现有 Fail2ban 捕获
```

新增 bot 限速不是为了阻止所有爬虫，而是把爬虫请求压到服务器能承受的范围内，避免影响正常访客。

### 3.2 为什么不再分 social / other 两档

本次需求的核心是“不要让爬虫把服务器爬挂”，不是精细区分 Facebook、Twitter、Ahrefs、Semrush 的业务价值。

因此第一版采用一个统一 bot 桶更合适：

- 配置更简单。
- 行为更容易理解。
- UI 只需要启用、每分钟请求数、突发缓冲。
- 所有非验证搜索引擎的 bot 共享同一个站点级额度。
- 攻击者即使在 `facebookexternalhit`、`meta-externalagent`、`crawler`、`spider` 等 UA 之间切换，仍落入同一个站点 bot 桶。

### 3.3 按站点隔离

限速 key 使用站点维度：

```text
$server_name:bot
```

效果：

- `site-a.com` 被假 Facebook 扫描时，只消耗 `site-a.com:bot`。
- `site-b.com` 的社交预览、SEO 爬虫访问不受 `site-a.com` 影响。

这比全服务器共用一个 `bot` 桶更安全，尤其适合 WP Panel 管理多站点的场景。

---

## 四、Google/Bing 处理策略

### 4.1 不能只按 UA 豁免

不应该写成：

```nginx
~*googlebot "";
~*bingbot "";
```

因为当前问题本身就是“伪装成某大厂爬虫”。如果只看 UA，攻击者伪装成 `Googlebot` 或 `Bingbot` 就能绕过新增 bot 限速。

### 4.2 推荐策略：官方 IP 验证后才豁免

规则：

| 场景 | 新增 bot 限速 |
|---|---|
| 真 Googlebot，来源 IP 在 Googlebot 官方 IP 段 | 豁免 |
| 假 Googlebot，来源 IP 不在 Googlebot 官方 IP 段 | 进入 bot 桶 |
| 真 Bingbot，来源 IP 在 Bingbot 官方 IP 段 | 豁免 |
| 假 Bingbot，来源 IP 不在 Bingbot 官方 IP 段 | 进入 bot 桶 |
| Facebook / Meta / Twitter / LinkedIn 等 | 进入 bot 桶 |
| 普通浏览器 | 不进入 bot 桶 |

注意：这里的“豁免”只指新增 bot UA 限速层。现有全局 IP 限速仍然存在，默认 `60r/m + burst 300`，通常不会影响正常搜索引擎抓取。

### 4.3 需要单独保存 Google/Bing IP 段

当前项目已能拉取 Cloudflare、Googlebot、Bingbot 官方 IP 段，但现有 `official_whitelist_ips` 是合并列表，主要用于 Fail2ban `ignoreip`。

新增 bot 限速不能直接复用合并列表，否则 Cloudflare 等非搜索引擎来源也可能被错误视为“验证搜索爬虫 IP”。

因此建议新增独立缓存：

| 设置键 | 用途 |
|---|---|
| `googlebot_ips` | Googlebot 官方 IP 段 |
| `bingbot_ips` | Bingbot 官方 IP 段 |

这两个 key 仍放在现有 `security_settings` key-value 表中，不需要数据库 schema 变更。

---

## 五、Nginx 配置设计

### 5.1 新增全局 Bot 限速配置文件

建议新增：

```text
/etc/nginx/conf.d/wppanel-botlimit.conf
```

示意内容：

```nginx
# WP Panel Generated - Bot UA rate limiting

# 官方搜索引擎 IP 验证，内容由面板根据 Googlebot/Bingbot 官方 IP 段生成
geo $wp_verified_search_bot_ip {
    default 0;
    # Googlebot / Bingbot official ranges:
    # 66.249.64.0/19 1;
    # 2001:4860:4801::/48 1;
}

map $http_user_agent $wp_search_bot_ua {
    ~*(googlebot|bingbot) 1;
    default 0;
}

map $http_user_agent $wp_bot_ua {
    ~*(googlebot|bingbot|facebookexternalhit|facebook|meta-externalagent|twitterbot|linkedinbot|slackbot|discordbot|telegrambot|semrushbot|ahrefsbot|mj12bot|dotbot|bot|crawler|spider|scraper|scan) 1;
    default 0;
}

# 只有“搜索引擎 UA + 官方搜索引擎 IP”才豁免。
# 其他 bot UA 统一进入站点级 bot 桶。
map "$wp_bot_ua:$wp_search_bot_ua:$wp_verified_search_bot_ip" $wp_bot_rate_key {
    "1:1:1" "";
    ~^1: "$server_name:bot";
    default "";
}

limit_req_zone $wp_bot_rate_key zone=wp_bot_limit:10m rate=30r/m;
```

说明：

- 普通浏览器：`$wp_bot_ua = 0`，key 为空，不触发新增 bot 限速。
- 真 Google/Bing：`1:1:1`，key 为空，不触发新增 bot 限速。
- 假 Google/Bing：`1:1:0`，key 为 `$server_name:bot`，触发限速。
- 假 Facebook/Meta：`1:0:0`，key 为 `$server_name:bot`，触发限速。
- 其他 bot/crawler/spider：同样进入 `$server_name:bot`。

### 5.2 新增全局 limit_req 状态码配置

`limit_req_status 429;` 不再写在每个站点 server block 中，也不由 IP 限速或 Bot 限速分别管理。

建议新增独立 http 级配置文件：

```text
/etc/nginx/conf.d/wppanel-limit-status.conf
```

内容：

```nginx
# WP Panel Generated - shared limit_req status
limit_req_status 429;
```

原因：

- Nginx `limit_req_status` 支持 `http` / `server` / `location` 上下文。
- 放在 http 级后，所有 `limit_req` 拒绝请求统一返回 429。
- IP 限速和 Bot 限速不再互相争夺或误删 `limit_req_status 429;`。
- 即使 IP 限速和 Bot 限速都关闭，保留该全局配置也基本无副作用。

需要注意：如果用户在自定义 Nginx 配置中手写了自己的 `limit_req`，该全局 429 也会影响它。但 WP Panel 的防御设计本身希望限速拒绝统一返回 429，并让 Fail2ban 可捕获，因此该行为可接受。

### 5.3 注入到站点 server block

每个站点 server block 中新增一行：

```nginx
limit_req zone=wp_bot_limit burst=20 nodelay;
```

站点模板中不再输出 `limit_req_status 429;`。现有模板里的 `limit_req_status 429;` 需要迁移到全局 `wppanel-limit-status.conf`。

实现时需要清理旧站点配置中的 `limit_req_status 429;`，避免同一含义在全局和站点内重复维护。

### 5.4 Bot UA 匹配规则

第一版使用内置 UA 匹配列表，不提供 UI 自定义规则。

内置列表覆盖：

- `googlebot`
- `bingbot`
- `facebookexternalhit`
- `facebook`
- `meta-externalagent`
- `twitterbot`
- `linkedinbot`
- `slackbot`
- `discordbot`
- `telegrambot`
- `semrushbot`
- `ahrefsbot`
- `mj12bot`
- `dotbot`
- `bot`
- `crawler`
- `spider`
- `scraper`
- `scan`

暂不开放自定义的原因：

- 自定义规则实际会生成 Nginx `map` 正则，错误配置可能导致 `nginx -t` 失败。
- 需要额外设计正则校验、长度限制、危险字符限制、回滚和 UI 提示。
- 当前目标是解决明确的假 Facebook/Meta 和常见 bot 扫描问题，内置规则足够覆盖第一版需求。

### 5.5 默认参数建议

| 设置键 | 默认值 | 范围 | 说明 |
|---|---:|---:|---|
| `bot_limit_enabled` | `false` | bool | 是否启用 Bot UA 统一限速，新装和老用户升级均默认关闭，由管理员手动开启 |
| `bot_limit_rpm` | `30` | 5-300 | 每站点 bot 总请求数/分钟 |
| `bot_limit_burst` | `20` | 5-300 | 每站点 bot 突发缓冲 |
| `googlebot_ips` | 空 | text | Googlebot 官方 IP 缓存 |
| `bingbot_ips` | 空 | text | Bingbot 官方 IP 缓存 |

默认策略确定为关闭：新装用户和老用户升级后都不会自动启用 Bot 限速。管理员需要在安全设置页面手动开启后才会写入站点 bot limit 行并改变线上访问行为。

---

## 六、模拟访问判断

| 访问情况 | 示例 | 结果 |
|---|---|---|
| 普通用户浏览器 | Chrome/Safari/Firefox | 不进入 bot 桶，只受现有 IP 限速 |
| 登录 WordPress 用户 | 带 `wordpress_logged_in` cookie | 现有 IP 限速豁免；不含 bot UA 时也不进 bot 桶 |
| 假 Facebook/Meta | `meta-externalagent` + 海量 IP | 全部进入同一站点 bot 桶，超过阈值返回 429 |
| 真 Facebook/Meta | `facebookexternalhit` | 进入 bot 桶，但默认 30/min + burst 20 应满足正常分享抓取 |
| 假 Googlebot | UA 是 Googlebot，IP 不在 Google 官方段 | 进入 bot 桶 |
| 真 Googlebot | UA 是 Googlebot，IP 在 Google 官方段 | 不进新增 bot 桶 |
| 假 Bingbot | UA 是 Bingbot，IP 不在 Bing 官方段 | 进入 bot 桶 |
| 真 Bingbot | UA 是 Bingbot，IP 在 Bing 官方段 | 不进新增 bot 桶 |
| 攻击者轮换多个 bot UA | Facebook/Twitter/Ahrefs/crawler 混用 | 仍进入同一个站点 bot 桶 |
| 攻击者伪装普通浏览器 UA | Chrome UA + 海量 IP | 不被 bot 桶限制，仍受现有 IP 限速；这是本方案明确不覆盖的应用层 DDoS 场景 |

---

## 七、与现有防御系统的关系

### 7.1 不替代现有 IP 限速

现有 IP 限速继续负责单 IP 高频访问；新增 bot 限速负责多 IP 同类爬虫。

两个机制叠加：

- 单 IP 刷站：现有 IP 限速有效。
- 多 IP 假 Facebook：新增 bot 限速有效。
- 普通浏览器：不进入新增 bot 限速。

### 7.2 Fail2ban 仍是辅助，不是主防线

429 仍会被现有 Fail2ban 规则捕获。但对于海量 IP 分摊的情况，单个 IP 未必达到 Fail2ban 阈值。

因此：

- Nginx bot 限速是主防线。
- Fail2ban 是辅助封禁和记录。

### 7.3 不影响面板自身扫描防御

面板端口仍由 `middleware/scan_defense.go`、随机入口、BasicAuth、Session、CSRF 保护。

新增 bot 限速只作用于托管网站的 Nginx server block，不应改变面板路由、中间件顺序或认证边界。

### 7.4 与 CDN Real IP 的关系

如果站点启用了 CDN Real IP，Nginx 的 real_ip 模块会在较早阶段改写客户端地址；配置正确时，`geo $wp_verified_search_bot_ip` 看到的 `$remote_addr` 应是真实客户端 IP，而不是 CDN 回源 IP。新增 bot 限速的 key 主要按 `$server_name:bot` 聚合，不依赖单 IP，因此 CDN 场景下仍能发挥作用。

官方搜索引擎 IP 验证使用 Nginx 实际识别的客户端 IP。只要该 IP 命中 Googlebot/Bingbot 官方 IP 段，就豁免新增 Bot 限速；不因 CDN 兼容模式额外进入 bot 桶。

需要注意：通用 CDN 兼容模式会信任请求头中的真实 IP，管理员应只在可信 CDN 场景下启用。若上游 Header 本身不可信，任何基于真实 IP 的判断都可能失真，这属于 CDN Real IP 配置风险，不由 Bot 限速单独解决。

---

## 八、实现改动范围

### 8.1 预计涉及文件

| 文件 | 改动说明 |
|---|---|
| `executor/rate_limit.go` | 新增 Bot 限速配置生成、设置读取、备份回滚；避免与现有 IP 限速互删 |
| `executor/fail2ban.go` | 拉取官方白名单时分别缓存 Googlebot/Bingbot IP 段 |
| `executor/template_engine.go` | WordPress 站点模板支持 bot limit 行；移除站点级 `limit_req_status 429;` |
| `executor/template_php.go` | PHP 站点模板支持 bot limit 行；移除站点级 `limit_req_status 429;` |
| `handlers/security.go` | 新增 bot 限速参数校验和保存后应用逻辑 |
| `database/migrations.go` | 新装 seed：bot 限速设置和 Google/Bing IP 缓存 key；`bot_limit_enabled` 默认为 `false` |
| `database/upgrades.go` | 老用户升级 seed，保证已安装服务器获得新设置；`bot_limit_enabled` 默认为 `false` |
| `templates/security.html` | 安全设置页面新增“爬虫限速”配置 |
| `install.sh` | 新装时确保全局 `wppanel-limit-status.conf` 存在；Bot 默认关闭时不引用不存在的 `wp_bot_limit` zone |
| `main.go` | 启动时在批量重建站点 Nginx 前确保 bot limit 全局 zone 已生成 |

### 8.2 不应改动

- 不改变现有 API 路径。
- 不引入新技术栈。
- 不改变面板随机入口、BasicAuth、Session、CSRF 顺序。
- 不删除历史升级条目。
- 不把路径级业务规则写死到通用防御中。

---

## 九、数据库与安装/升级影响

### 9.1 数据库

涉及数据库数据变更，但不涉及 schema 变更。

需要在 `security_settings` 中新增 key：

- `bot_limit_enabled`
- `bot_limit_rpm`
- `bot_limit_burst`
- `googlebot_ips`
- `bingbot_ips`

必须同时处理：

- 新装：`database/migrations.go`
- 老用户升级：`database/upgrades.go`
- 重复执行：使用 `INSERT OR IGNORE`

### 9.2 已安装服务器

会影响已安装服务器，因为升级后可能：

- 新增 `/etc/nginx/conf.d/wppanel-botlimit.conf`
- 更新每个站点 Nginx 配置
- reload Nginx
- 改变 bot 访问的 429 行为

`bot_limit_enabled` 默认值确定为 `false`。老用户升级后只会获得新增设置项，不会自动启用 Bot 限速，不会立即改变现有站点访问行为。管理员手动开启后，才会新增 `/etc/nginx/conf.d/wppanel-botlimit.conf`、更新站点 Nginx 配置并 reload Nginx。

升级后会生成或更新 `/etc/nginx/conf.d/wppanel-limit-status.conf`，使所有 Nginx `limit_req` 拒绝响应统一返回 429。该配置不启用新的限速规则，只改变限速拒绝时的状态码归属位置；现有站点模板中的站点级 `limit_req_status 429;` 会在重建配置时移除。

### 9.3 安装脚本

新装流程需要保证：

- 全局 `wppanel-limit-status.conf` 存在，使现有 IP 限速和未来 Bot 限速都统一返回 429。
- Bot 默认关闭时，不生成或不引用 `limit_req zone=wp_bot_limit`。
- Bot 开启时，Nginx 全局 bot zone 在站点配置引用前已经存在。
- 如果首次拉取 Google/Bing IP 失败，配置仍能生成，假 Google/Bing 默认进入 bot 桶。
- `nginx -t` 失败时必须回滚。

---

## 十、回滚安全

必须满足：

- 生成 bot limit 全局配置前备份旧文件。
- 修改站点 Nginx 配置前备份旧文件。
- `nginx -t` 失败时恢复所有文件。
- `nginx reload` 失败时恢复所有文件并尝试 reload 旧配置。
- 关闭 bot 限速时只移除 bot limit 行，不误删现有 IP 限速行。
- 关闭 IP 限速时不误删 bot limit 行。
- `limit_req_status 429;` 由全局 `wppanel-limit-status.conf` 管理，不由站点注入/剥离函数管理。
- 迁移过程中如果全局 429 配置写入失败，不能继续移除站点内旧的 `limit_req_status 429;`。

现有 `stripRateLimitFromSites()` 和 `injectRateLimitToSites()` 会删除 `limit_req_status 429`。实现时需要调整为：这些函数可以清理旧站点配置中遗留的站点级 429 行，但状态码是否存在由全局 `wppanel-limit-status.conf` 决定，避免旧逻辑误删唯一的状态码配置。

---

## 十一、验证方法

### 11.1 本地验证

必须运行：

```powershell
scripts\verify.ps1
```

需要增加或覆盖的测试：

- 新装数据库包含新增 setting。
- 老版本升级插入新增 setting。
- 重复运行升级不报错。
- Bot 限速开启/关闭时模板渲染正确。
- IP 限速开启/关闭与 Bot 限速开启/关闭的组合都正确。
- 必须覆盖四种组合：IP 开/Bot 开、IP 开/Bot 关、IP 关/Bot 开、IP 关/Bot 关。
- 关闭一个限速功能不会删除另一个限速功能配置。
- IP 关/Bot 开时仍通过全局 `wppanel-limit-status.conf` 返回 429。
- 站点模板不再输出 `limit_req_status 429;`。
- `wppanel-limit-status.conf` 生成失败时不会破坏旧站点配置。
- 假 Googlebot / 真 Googlebot 的 key 逻辑符合预期。
- 第一版不接受自定义 UA 正则输入，安全设置接口不会保存未知 UA 规则字段。

### 11.2 测试服务器验证

建议至少验证：

1. 普通浏览器访问 10 个页面，不应出现 429。
2. `meta-externalagent` 连续请求同一站点，超过 burst 后返回 429。
3. 同样的 bot UA 请求另一个站点，不应被第一个站点的额度影响。
4. 假 Googlebot（非官方 IP）应进入 bot 限速。
5. 关闭 bot 限速后，bot 429 消失，但现有 IP 限速仍正常。
6. 开启 bot 限速后，现有 IP 限速仍正常。
7. 关闭 IP 限速、仅开启 Bot 限速时，Bot 超限仍通过全局配置返回 429。
8. IP 限速和 Bot 限速都关闭时，站点配置不保留无用的 `limit_req` 行；全局 `limit_req_status 429;` 可保留且无副作用。
9. CDN Real IP 正确配置时，`geo` 验证使用真实客户端 IP；命中 Google/Bing 官方 IP 段时不进入 Bot 限速桶。
10. `nginx -t` 和 reload 成功。
11. Fail2ban 可继续捕获 429，但不把它作为分布式防御的唯一依据。

---

## 十二、仍未覆盖的场景

| 场景 | 说明 |
|---|---|
| 攻击者伪装普通浏览器 UA 并使用海量 IP | 本方案不覆盖，需要 CDN/WAF、路径级规则或应用层策略 |
| 低频长期爬取 | 只要低于服务器可承受阈值，本方案允许通过 |
| 需要按 URL 业务规则拦截 | 不在第一版实现，避免误伤 WooCommerce/产品目录 |
| 需要给不同爬虫不同额度 | 第一版不做 social/other 分层，后续可扩展 |
| 需要管理员自定义 UA 正则 | 第一版不做，避免错误正则导致 Nginx 配置失败 |

---

## 十三、结论

当前推荐方案是：

```text
统一 Bot UA 限速桶
+ 按站点隔离
+ Google/Bing 官方 IP 验证后才豁免
+ limit_req_status 429 迁移到全局 http 级配置
+ 保留现有 IP 限速和 Fail2ban
```

这比原始 social/other 双桶方案更简单、更符合当前目标，也避免了两个 zone 共用同一个 key 导致限速叠加的实现问题。

已确定规则：

- 新装和老用户升级后 `bot_limit_enabled` 均默认关闭。
- 管理员手动开启后才应用 Bot 限速并 reload Nginx。
- 默认参数为 `30r/m + burst 20`，用于管理员开启时的初始建议值。
- 第一版 UA 匹配列表硬编码，不提供自定义 UA 规则 UI。
- `limit_req_status 429;` 使用全局 `wppanel-limit-status.conf` 管理，站点模板不再重复输出。
