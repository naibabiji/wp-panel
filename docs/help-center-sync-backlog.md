# Help Center 同步待办清单（跨项目契约）

本文件是 WP Panel（Go 后端）与 wp-panel-theme（官网主题）之间帮助中心内容的**唯一同步接口**。

主题项目位置：`/home/luanc/Projects/wp-themes/wp-panel-theme`。Help Center 内容是真实 WordPress 页面（存数据库，`/help/` 及其子页面），无主题文件副本、无自动同步层。

## 规则

- **状态两态**：`待同步`（主题 `/help/` 内容尚未更新）/ `已同步`（已更新到局域网测试站，用户已或将手动复制到线上）。
- **对齐主键**：`主题 slug` 必须等于主题项目 `/help/{slug}/` 子页面的 `post_name`（如 `getting-started`、`websites`）。新建主题时两边用同一 slug。
- **维护方**：
  - Go 项目 AI：用户可见功能变化（入口、流程、默认值、前置条件、限制、风险、恢复步骤、界面文案）时，在此追加/更新条目，状态=`待同步`，并同步提醒用户。
  - 主题项目 AI：更新完测试站 help 页面后，回写状态=`已同步` + 完成日期。主题项目仅授权写本文件，Go 项目其余文件仍只读。
- 已同步的条目保留在表中（不删除），作为历史留痕，避免重复处理。

## 同步清单

| 日期 | 主题 slug | 变更摘要 | 用户可见变化点 | 来源 | 状态 |
|---|---|---|---|---|---|
| 2026-09-12 | wordpress | 配套插件发现并请求面板更新 | 插件 1.1.22 会显示面板提供的新版本并允许管理员在插件设置页点击“立即更新”；更新文件来自 WP Panel 内嵌副本，保持插件启停状态。AI 开发访问、临时维护和文件锁不阻止已有配套插件更新；直接修改该托管目录会被覆盖。已删除插件不会自动装回，首次安装仍从面板完成 | `docs/features/wordpress-management.md`、ADR-0033 | 已同步（2026-09-13） |
| 2026-09-12 | wordpress | 文件保护期间的配套插件设置范围 | 插件 1.1.23 按实际写入范围开放功能：缓存、预加载、图片上传策略和历史图片优化可继续使用；仅需改写 `wp-config.php` 的更新检测、文件编辑、调试、修订数和 WordPress 内存上限为只读，临时解锁后可修改 | `docs/features/wordpress-management.md` | 已同步（2026-09-13） |
| 2026-09-13 | wordpress | 临时维护密码的 30 分钟复用边界 | 重新锁定和当前 30 分钟验证期限内的加时无需重复输入密码；跨越期限时由面板服务端强制再次验证。插件按边界显示密码框，并在隐藏、关闭或提交后清空；不额外判断键盘、粘贴或密码管理器输入来源 | `docs/features/wordpress-maintenance.md` | 已同步（2026-09-13） |
| 2026-09-13 | wordpress | 合并连续首页内容变化通知 | 同一静态首页首次内容变化立即通知；只要相邻两次变化间隔不足 6 小时，就在面板累计次数并保留 info 历史，不重复发送邮件/Webhook；连续 6 小时无变化后的下一次变化重新通知。首页指向改变或原首页删除、回收、取消发布仍即时提醒；文案改为“WordPress 内容变化”，不增加人工确认或长期无人维护判断 | `docs/features/wordpress-anomaly-monitoring.md`、ADR-0034 | 已同步（2026-09-13） |
| 2026-09-12 | wordpress | 配套插件设置页视觉、托管状态与多语言改版 | 插件使用统一的安全品牌界面，支持英文与简体中文；英文文案已按美国英语和 WordPress 常用术语复审。“安全与维护”以只读方式展示由面板管理的文件保护、临时维护、XML-RPC、应用程序密码、异常监控和密码找回策略，插件内不提供这些策略的开关；插件设置页用顶部状态代替重复的文件锁长提示，WordPress 其他后台页面仍引导管理员优先使用维护密码短时解锁，临时维护不可用时再联系服务器管理员，并明确配套插件自身仍可接收面板更新；文件保护导致“保存设置”或“开始批量优化”不可用时，按钮旁会显示具体原因与临时解锁指引；“关于与面板同步”补充同步机制、API Key 仅显示前 8 位、官网及 GitHub Issues 反馈入口；缓存、预加载和图片处理的原有操作语义不变 | `docs/features/wordpress-management.md`、`docs/features/image-optimization.md` | 已同步（2026-09-13） |
| 2026-09-12 | wordpress | 禁用 WordPress 应用程序密码 | 网站详情 → WordPress 优化新增开关；新建网站默认禁用，存量升级保持允许；禁用后手机 App、自动发布和第三方 REST 集成无法使用应用程序密码，后台编辑不受影响；不会删除已有凭据，重新允许前应复核并撤销未知凭据 | `docs/features/website-runtime-and-cdn.md`、ADR-0032 | 已同步（2026-09-13） |
| 2026-09-12 | security | 面板持久封禁补齐 IPv6 | 扫描防御、面板登录防护和管理员手动封禁现在通过独立 IPv4/IPv6 Nftables 集合持久拦截；规则说明与人工核验命令需同时列出 `table ip`、`table ip6`；任一地址族读取失败时页面保留已确认结果并提示列表可能不完整 | `docs/features/security-protection.md` | 已同步（2026-09-13） |
| 2026-09-12 | security | 文件锁组件变化告警降噪 | 仅对已开启且健康应用文件锁的 WordPress 站点监控代码和组件变化；锁定期间发现插件、主题、MU 插件或 Drop-in 增删改时在既有文件安全告警中显示组件摘要；正确维护窗口或面板受控任务首次成功回锁后只写操作摘要并接受新基线，不发送安全通知；回锁失败、重启恢复或状态未知时不静默接受变化；未锁站点不承担组件/代码监控，初始基线不代表网站已经安全；第一版不比较插件启停和主题切换 | `docs/features/security-protection.md`、`docs/features/wordpress-maintenance.md`、ADR-0031 | 已同步（2026-09-13） |
| 2026-09-12 | operations | 安全取证与运维日志默认保留调整 | 新建网站原始日志默认 14 天（存量站点设置不变）；WordPress 安全事件 90 天；更新事件日志 7 天但更新备份仍为 24 小时；操作日志最近 1000 条，Cron 日志最近 1000 行；单次日志分析仍最多 7 天/512MB，两年告警去重标记不变 | `docs/features/website-runtime-and-cdn.md`、`docs/features/ai-and-log-analysis.md`、`docs/features/wordpress-management.md`、`docs/features/operations-and-settings.md` | 已同步（2026-09-13） |
| 2026-09-12 | security | 安全设置保存失败回滚一致性 | 同一次保存涉及 SQL 注入防护、Fail2ban、限速或日志白名单时统一提交并串行应用；服务器配置应用失败会恢复修改前设置，恢复时一个子系统失败也会继续尝试其余配置，不再留下静默的部分新值；重复 WordPress 搜索参数不享受纯搜索豁免 | `docs/features/security-protection.md` | 已同步（2026-09-13） |
| 2026-09-12 | wordpress | WordPress 安全界面反馈修正 | `WP_DEBUG_DISPLAY=TRUE` 可被正确识别；维护解锁/回锁过渡状态显示“处理中”而非“文件锁应用失败”；异常监控接口失败只显示卡片内提示，不再重复弹出全局错误 | `docs/features/wordpress-management.md`、`docs/features/wordpress-maintenance.md`、`docs/features/wordpress-anomaly-monitoring.md` | 已同步（2026-09-13） |
| 2026-09-12 | wordpress | 异常监控增加数据库持久化对象 | 配套插件要求升至 1.1.18；每小时或立即检查当前 WordPress 数据库的 Trigger、Event、Procedure、Function；首次存量、新增或修改会告警，删除留信息历史；页面只显示数量和待检查状态，不展示完整 SQL；最多 100 个对象，查询失败或可见对象定义体不可读时保留旧基线；受限数据库用户可能静默看不到无权对象，导入站点或手工限权后需确认权限；仅提醒、不自动删除 | `docs/features/wordpress-anomaly-monitoring.md`、ADR-0030 | 已同步（2026-09-13） |
| 2026-09-12 | wordpress | 异常监控增加应用程序密码 | 配套插件要求升至 1.1.17；首次检查发现存量管理员应用程序密码会提醒，后续新增、首次使用及使用 IP 变化会告警；账号降权不宣称凭据已撤销，新增/重新提权管理员的凭据按账号聚合复核；全部指纹变化触发一条可能为站点密钥轮换、也可能含凭据变化的聚合告警；升级后首次采样前显示“待检查”，容量超限和格式异常分别提示；页面不显示密码、哈希或原始 UUID；仅提醒，不自动撤销 | `docs/features/wordpress-anomaly-monitoring.md`、ADR-0029 | 已同步（2026-09-13） |
| 2026-09-11 | wordpress | 异常监控扩展内容与关键设置 | 配套插件要求升至 1.1.16；发布量覆盖文章和页面；新增已发布内容删除/取消发布、首页变化、24 小时集中修改，以及站点地址、开放注册和默认角色变化提醒；告警页显示对应中文/英文类型；不监控插件来源与变化，不含 SQL 注入，不自动处置 | `docs/features/wordpress-anomaly-monitoring.md`、ADR-0026 | 已同步（2026-09-13） |
| 2026-09-11 | wordpress | 维护回锁安全终态加强 | 更新、迁移或 AI 等遗留状态不再阻止到期/重启回锁；回锁必须验证真实文件权限，失败站点保持写操作冻结并持续重试告警，其他站点继续运行；检测到非字面量、重复或冲突的 `DISALLOW_FILE_MODS` 定义时不允许开启维护窗口，需先由管理员核对 `wp-config.php` | `docs/features/wordpress-maintenance.md`、ADR-0024 | 已同步（2026-09-13） |
| 2026-09-11 | wordpress | 维护密码查看复制 | 至少4字符/最多72字节，可生成16位随机密码；网站详情使用同一密码框查看、修改、生成和复制，保存失败保留输入；旧哈希需重新设置；maintenance.key 缺失或损坏时旧副本不可读，管理员可直接设置新密码，其他站点旧副本按需重设；不提供恢复或强制修改流程，不改变窗口和冻结。文件锁开启确认文案已调整为完整陈述后再询问确认 | ADR-0023、ADR-0025、`docs/features/wordpress-maintenance.md` | 已同步（2026-09-13） |
| 2026-09-12 | security | WordPress SQL 注入请求防护 | 安全设置新增请求拦截与自动封禁开关、独立阈值和窗口；高置信度 URL 请求在 PHP 前返回 403，弱信号只留证；可信来源重复触发后由独立 SQL jail 临时封禁，兼容代理模式不自动封禁。Cloudflare 官方 IP 段及真实 IP Header 由系统全局严格处理，无需在网站详情页选择；其他 CDN、自定义可信段或兼容模式才按网站配置。安全防御展示证据、封禁结果和手动处置；不代表已确认漏洞或入侵成功 | `docs/features/security-protection.md`、`docs/features/operations-and-settings.md`、ADR-0027 | 已同步（2026-09-13） |
| 2026-09-11 | wordpress | 移除重复插件入口提示 | 异常监控不再展示“查看配套插件状态与安装入口”链接；插件操作仍在同卡片内，其他控件与依赖不变。Help 后续统一补 | `docs/features/wordpress-anomaly-monitoring.md` | 已同步（2026-09-13） |
| 2026-09-11 | wordpress | 配套插件状态与恢复入口 | 优化卡片始终显示插件操作区，支持刷新；区分未安装、未启用、已启用和未知，停用需在 WordPress 后台手动启用，首次安装需先解锁；启动会更新仍安装的已启用或停用插件，但不会自动启用或把已删除插件装回；管理员/文章异常监控依赖已启用插件 | `docs/features/wordpress-management.md`、ADR-0020 | 已同步（2026-09-13） |
| 2026-09-12 | operations | 告警页范围修订 | 旧 SQL 探测提醒开关已停止使用，不新增 SQL 面板、邮件或 Webhook 通知；伪装爬虫告警及其阈值、窗口保持不变。SQL 证据和处置改由安全防御承载 | `docs/features/operations-and-settings.md`、ADR-0027 | 已同步（2026-09-13） |
| 2026-09-11 | wordpress | 异常监控卡片顺序 | 异常监控位于 WordPress 优化卡片原有内容最下方，中间使用与网站运行与配置相同的分割线；后续帮助截图按此位置更新 | `docs/features/wordpress-anomaly-monitoring.md` | 已同步（2026-09-13） |
| 2026-09-11 | wordpress | 轻量异常监控 | 网站详情 → WordPress 优化 → 异常监控；需启用插件 1.1.15+，默认关闭，默认 24 小时超过 5 篇、每小时或立即检查；首采建基线、管理员变化只提醒一次，失败保留旧结果；发布量回落后再触发；重新开启从新基线开始；不自动处置。页面待验收，用户要求统一补 Help | `docs/features/wordpress-anomaly-monitoring.md`、ADR-0022 | 已同步（2026-09-13） |
| 2026-09-12 | security | 文件锁代码完整性监控 | 文件锁健康时建立站外代码基线；监控核心、插件、主题、MU 插件和关键根文件的增删改，运行目录继续检查 PHP；初始基线不等于安全扫描，维护窗口成功回锁后接受授权变化；只告警留证，不自动删除或恢复；关闭文件锁后，锁定期完整性变化保留为历史且不再计入当前风险 | `docs/features/security-protection.md`、ADR-0028 | 已同步（2026-09-13） |
| 2026-09-11 | wordpress | 维护密码冻结提示 | 普通验证失败与十分钟暂停验证分别提示；冻结中正确密码也不验证，重复请求不延长冻结；立即重新锁定无需维护密码。用户要求 Help 后续统一补 | `docs/features/wordpress-maintenance.md`、ADR-0021 | 已同步（2026-09-13） |
| 2026-09-11 | files-databases | 跨站移动源站回锁保护 | 复制期间源站到期或回锁，后续源项目不再删除，提示“移动未完全完成”并保留两端副本；核对副本后，在允许写入时人工完成清理 | `docs/features/files-and-databases.md` | 已同步（2026-09-13） |
| 2026-09-11 | wordpress | 临时维护复审修复与存量插件交付 | 已锁定站点可直接更新内嵌配套插件，仍保持只读；首次安装/配置重建须解锁；legacy 须先应用标准或严格模式；相同失败请求重放不重复计数 | `docs/features/wordpress-maintenance.md`、ADR-0020 | 已同步（2026-09-13） |
| 2026-09-11 | wordpress | Admin Bar 与临时维护窗口 | 网站详情默认关闭的维护密码设置；后台解锁与 1/3/5 分钟加时、跨 30 分钟验证；到期/重启强制回锁可能打断更新；5 次密码失败冻结 10 分钟；失败至少 60 秒重试；不含轻量监控 | `docs/features/wordpress-maintenance.md`、ADR-0019 | 已同步（2026-09-13） |
| 2026-08-18 | wordpress | 新增图片优化功能（内容并入 wordpress 页面的「图片优化」小节） | 插件设置页「图片优化」标签；WebP 模式删除原图、exif 扩展依赖等风险提示 | `docs/features/image-optimization.md` | 已同步（2026-09-03） |
| 2026-08-23 | site-migration | 新增网站搬家功能（同版本 WP Panel 间迁移） | 网站管理 → 网站搬家；配对/迁移/维护窗口/完成删除流程；远程备份重配提醒 | `docs/features/site-migration.md` | 已同步（2026-09-03） |
| 2026-09-03 | websites | 修复 AI 开发连接包的跨平台本地权限恢复 | Linux、macOS、WSL 标准入口改为 `bash .wp-panel-ai/connect.sh`；脚本自动收紧私钥为 `0600`，并说明不保留 ZIP mode 的解压工具兼容方式 | `docs/features/website-runtime-and-cdn.md` | 已同步（2026-09-03） |
| 2026-09-10 | websites | 网站暂停联动站点自动任务 | 暂停后自动备份、WP Cron 和站点用户命令暂缓；已运行任务完成，启用后自然恢复；SSL、安全维护和库存刷新继续 | `docs/features/website-runtime-and-cdn.md` | 已同步（2026-09-13） |
| 2026-09-10 | backups | 暂停网站的备份运行语义 | 自动数据库/文件备份及远程后台维护暂缓，策略不关闭；手动维护保留；暂停或迁移冻结期间不触发备份失败误报 | `docs/features/backup-and-restore.md` | 已同步（2026-09-13） |
| 2026-09-10 | operations | 计划任务增加站点运行门禁 | 计划任务页显示“随网站暂停”或“迁移期间暂缓”；暂停网站手动执行需二次确认，正常跳过不覆盖最后执行结果 | `docs/features/operations-and-settings.md` | 已同步（2026-09-13） |
| 2026-09-11 | wordpress | 修复并完善 WordPress 调试模式 | 开启后真正启用 WP_DEBUG 并写入 debug.log；浏览器错误显示为独立高风险选项，默认关闭；配置文件写入失败时不再显示保存成功 | `docs/features/wordpress-management.md` | 已同步（2026-09-13） |
| 2026-09-11 | websites | 修复高流量网站开启/关闭 AI 开发访问失败及交互反馈 | 强制开启会处理立即重生的站点 PHP 进程；确认后显示明确的全区处理中状态；关闭时若站点 PHP worker 短暂占用账户，会仅针对本站点温和终止 worker 并有限重试，完成或失败后自动展示结果 | `docs/features/website-runtime-and-cdn.md` | 已同步（2026-09-13） |
