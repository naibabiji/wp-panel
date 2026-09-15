# Help Center 同步待办清单（跨项目契约）

本文件是 WP Panel（Go 后端）与 wp-panel-theme（官网主题）之间帮助中心内容的**唯一同步接口**。

主题项目位置：`/home/luanc/Projects/wp-themes/wp-panel-theme`。Help Center 内容是真实 WordPress 页面（存数据库，`/help/` 及其子页面），无主题文件副本、无自动同步层。

## 规则

- **状态两态**：`待同步`（主题 `/help/` 内容尚未更新）/ `已同步`（已更新到局域网测试站，用户已或将手动复制到线上）。
- **对齐主键**：`主题 slug` 必须等于主题项目 `/help/{slug}/` 子页面的 `post_name`（如 `getting-started`、`websites`）。新建主题时两边用同一 slug。
- **维护方**：
  - Go 项目 AI：用户可见功能变化（入口、流程、默认值、前置条件、限制、风险、恢复步骤、界面文案）时，在此追加/更新条目，状态=`待同步`，并提醒用户；不主动修改 LAN 或官网 Help 页面。
  - 主题项目 AI：更新完测试站 help 页面后，回写状态=`已同步` + 完成日期。主题项目仅授权写本文件，Go 项目其余文件仍只读。
- **授权边界**：待同步条目只是记录，不是 Help 内容修改授权。“同步必要文档”“更新文档”“发布新版本”均不触发 WordPress 运维；必须等用户另行明确要求同步 Help Center 或指定 Help 页面。
- 已同步的条目保留在表中（不删除），作为历史留痕，避免重复处理。

## 同步清单

| 日期 | 主题 slug | 变更摘要 | 用户可见变化点 | 来源 | 状态 |
|---|---|---|---|---|---|
| 2026-09-15 | websites | AI 开发连接包首次连接与交接说明完善 | 新连接包自带并固定服务器 SSH 身份，新电脑无需预先保存指纹；身份不匹配会拒绝连接。服务器交接文档是最新网站、能力和边界准则，面板更新重启后会自动刷新，旧连接包与其冲突时以服务器文档为准；并列出本站日志及只读 PHP-FPM/Nginx 托管配置位置，避免用 CLI PHP 或全局配置误判站点限制。交接包不规定用户 AI 的 Git、计划、授权或开发方式 | `docs/ai-development-access-design.md`、`docs/features/website-runtime-and-cdn.md` | 待同步 |
| 2026-09-15 | getting-started | 面板更新确认目标版本并核对回滚健康 | 更新后只有实际运行进程版本与目标版本一致才会显示成功；失败回滚会确认旧版本重新健康，重启或健康恢复失败会保留诊断计划并记录明确阶段 | `docs/features/getting-started-and-panel-update.md` | 待同步 |
| 2026-09-15 | websites | 建站主组失败时清理本次系统用户 | 创建网站若新系统用户建立后主组确认失败，会清理本次创建的账号并提示真实结果；系统用户创建本身失败时不会删除可能由外部管理的同名账号 | `docs/features/website-runtime-and-cdn.md` | 待同步 |
| 2026-09-15 | wordpress | 后台 WordPress 任务避免搬家写入并可重试图片元数据 | 定时组件库存会在网站搬家期间暂缓；更新数据库备份恢复在搬家或 AI 开发访问开启时拒绝；历史图片优化若 WordPress 文件大小元数据同步失败会显示失败，下次任务可重新尝试 | `docs/features/wordpress-management.md` | 待同步 |
| 2026-09-15 | websites | 运行监控保存失败不再误报成功 | 网站详情保存运行监控开关或间隔时会确认面板数据库已经写入；网站不存在或保存失败会明确报错。仪表盘时间范围仍为页面已有的 24 小时、7 天和 15 天 | `docs/features/website-runtime-and-cdn.md` | 待同步 |
| 2026-09-15 | websites | FastCGI 缓存操作等待真实生效 | 网站详情或配套插件保存 FastCGI 开关、TTL 及清缓存时，会等待 Nginx 实际应用成功后再提示成功；应用失败会恢复旧缓存设置或缓存标识并明确报错。WordPress 组合设置中其它已成功项目保持不变，缓存失败会单独说明 | `docs/features/website-runtime-and-cdn.md` | 待同步 |
| 2026-09-15 | backups | 文件备份失败不再推进增量链 | 数据库旧备份文件删除失败时面板保留记录等待重试；文件全量/增量备份遇到读取或归档校验错误会明确失败。增量截止时间只在本地记录及已启用的远程同步成功后推进，避免失败任务造成后续文件遗漏；仍需定期做真实恢复演练 | `docs/features/backup-and-restore.md` | 待同步 |
| 2026-09-15 | files-databases | 上传、远程导入和压缩包失败结果更可靠 | 上传和远程导入完成前会准备权限并复查文件保护/搬家状态，失败保留原文件；在线压缩失败不会覆盖已有归档；在线解压限制 20GB 展开量并预留 1GB 空间，中途失败会说明目标目录可能已有部分变化。远程 URL 仍由管理员自行确认来源与证书跳过风险 | `docs/features/files-and-databases.md` | 待同步 |
| 2026-09-15 | wordpress | WordPress 管理写操作补齐真实结果与安全发布 | 搬家或维护期间会拒绝修改管理员、站点 URL、wp-config 和密码找回策略；siteurl/home 不再只成功一半；wp-config 与密码策略文件通过语法检查后原子替换并拒绝异常符号链接；相同密码策略会核对并修复实际文件；配套插件用户组、属主或权限设置失败会明确提示，不再显示安装成功 | `docs/features/wordpress-management.md` | 待同步 |
| 2026-09-15 | operations | 设置、扩展、操作日志与服务操作显示真实结果 | 自动更新时间非法或配置读取失败时不会执行；扩展清单保存、删除和恢复默认失败会保留真实数据并提示；操作日志区分运行中、等待、跳过和信息；服务启停只有达到 systemd 目标状态且暂停意图保存成功才显示成功 | `docs/features/operations-and-settings.md` | 待同步 |
| 2026-09-14 | security | 日志分析任务在面板重启后立即显示中断 | 日志分析无法跨面板重启续跑；面板重新启动后会立即把遗留任务显示为“面板重启，日志分析已中断”，用户可重新开始。正常运行的大日志任务不再因固定 30 分钟而被误判失败 | `docs/features/ai-and-log-analysis.md` | 待同步 |
| 2026-09-14 | operations | 面板重启后释放卡住的手动计划任务 | 手动“立即执行”期间面板重启或异常退出后，任务不会永久显示运行中；面板再次启动会允许重新执行并在 Cron 日志记录中断，不会自动重跑或覆盖上一次完成结果 | `docs/features/operations-and-settings.md` | 待同步 |
| 2026-09-14 | operations | 同一计划任务不再重叠执行 | 手动和自动执行共用同一运行锁；上一次尚未结束时，自动触发会跳过本次并写入执行日志，手动触发会提示任务正在执行，避免重复备份或重复运行命令 | `docs/features/operations-and-settings.md` | 待同步 |
| 2026-09-14 | operations | 一键系统更新改为可恢复进度的后台任务 | 设置页继续安装 Debian 13 全部常规软件包更新；关闭或刷新页面不会中断，可重新显示当前阶段并阻止重复更新。完成后检查软件包、Nginx 配置和核心服务；检查失败会提示联系项目支持，不会自动降级软件包 | `docs/features/operations-and-settings.md`、ADR-0043 | 待同步 |
| 2026-09-14 | wordpress | 明确插件批量更新共享数据库备份的恢复范围 | 批次第一项回滚会恢复插件文件和数据库；后续项回滚只恢复插件文件，数据库变化可能保留。更新备份页标记批量共享数据库备份，手动恢复前提示数据库会退回批次起点、其它成功插件文件不会同步回退，建议仅在技术支持指导下使用 | `docs/features/wordpress-management.md`、ADR-0042 | 待同步 |
| 2026-09-14 | getting-started | repair 失败时同步恢复面板数据库 | repair 已经保存的面板数据库快照现在会参与失败回滚；新程序启动后若修复未完成，会先停服并恢复同一时间点的旧程序和数据库。数据库快照无法恢复时面板保持停止并明确提示联系支持，不再冒险启动旧程序读取新结构 | `docs/features/getting-started-and-panel-update.md`、ADR-0041 | 待同步 |
| 2026-09-14 | backups | 面板数据库恢复增加停机替换、健康检查与自动回退 | 恢复面板数据库时会短暂重启面板；恢复前自动保存当前面板数据库，恢复文件来自更高版本时拒绝。新数据库无法正常启动时自动回退，设置页重新连接后显示成功、已回退或需要人工处理；网站文件和服务器实际配置不会随面板数据库回退 | `docs/features/backup-and-restore.md`、ADR-0040 | 待同步 |
| 2026-09-14 | websites | 删除网站失败时保留“删除中”状态 | 删除真实资源前先保存“删除中”；若面板状态保存失败则不会开始删除，最终记录清理失败或进程中断时不会继续把残缺网站显示成正常网站，排除故障后可再次点击删除完成收尾。备份文件保留规则不变 | `docs/features/website-runtime-and-cdn.md` | 待同步 |
| 2026-09-14 | backups | 数据库恢复明示失败后果 | 从已有备份恢复或上传数据库文件时，确认框会说明恢复先清空当前数据库，失败可能留下空库或不完整数据，并建议先备份；面板不强制备份、不自动回滚 | `docs/features/backup-and-restore.md` | 待同步 |
| 2026-09-14 | security | 面板无法确认封禁状态时临时拒绝访问 | 面板入口查询封禁数据库失败时返回 503“暂时无法确认访问权限”，不会继续认证或错误显示成 IP 已封禁；数据库恢复后自动恢复访问。托管网站及 `wppanel-login` 管理员例外不变 | `docs/features/security-protection.md`、ADR-0039 | 待同步 |
| 2026-09-14 | getting-started | 修改管理员凭据后所有设备需重新登录 | Web 管理员用户名或密码确实修改成功后，当前浏览器和其他浏览器的既有登录会话全部失效；未变化、保存失败以及单独修改 BasicAuth 或其它设置不触发 | `docs/features/getting-started-and-panel-update.md`、ADR-0038 | 待同步 |
| 2026-09-14 | getting-started | 登录接口补齐 CSRF 校验 | 正常从登录页登录不变；直接调用登录 API 时必须先取得登录页生成的 CSRF Cookie，并在请求头携带相同 token，缺失或不匹配会返回 403 | `docs/features/getting-started-and-panel-update.md`、ADR-0037 | 待同步 |
| 2026-09-14 | websites | 网站列表增加整行悬停高亮 | 鼠标经过网站列表中的某个网站时整行高亮，便于网站较多时沿行查看监控、SSL、备份和操作等列；表格布局与功能不变 | `docs/features/website-runtime-and-cdn.md` | 待同步 |
| 2026-09-14 | websites | 暂停网站跳过 SSL 自动续签并保护手动证书 | 暂停期间不再尝试自动续签；重新启用时若证书已过期或 30 天内到期会提示管理员处理，但不会偷偷申请。只有面板自动申请的证书会自动续签，手动上传的证书不会被 Let's Encrypt 覆盖；续签失败会显示整批失败摘要 | `docs/features/website-runtime-and-cdn.md`、ADR-0036 | 待同步 |
| 2026-09-14 | websites | 删除 SSL 失败时保留证书并显示真实状态 | 删除 SSL 与同站维护或网站搬家冲突时会拒绝；数据库或 HTTP 切换失败时不删除证书并恢复面板状态，最终只剩旧证书文件清理失败时会明确提示“SSL 已关闭但文件未清理” | `docs/features/website-runtime-and-cdn.md` | 待同步 |
| 2026-09-14 | files-databases | 数据库密码修改失败时显示真实恢复结果 | 修改密码与同站维护操作冲突时会拒绝；WordPress 数据库密码修改失败会恢复原 `wp-config.php`，如果恢复本身失败会明确提示立即检查网站数据库配置，不再错误显示或记录已经回滚 | `docs/features/files-and-databases.md` | 待同步 |
| 2026-09-14 | websites | 网站别名修改失败时保持面板与 Nginx 一致 | 只修改别名时，数据库保存失败不会修改 Nginx；Nginx 应用失败会恢复原别名，排队期间别名已变化时拒绝覆盖，恢复失败会提示人工检查。主域名修改不在本次调整范围 | `docs/features/website-runtime-and-cdn.md` | 待同步 |
| 2026-09-14 | websites | 主域名修改改为完整预检查和可验证切换 | 修改前检查新域名相关目录和配置冲突；切换会同步搬迁网站、日志、证书、本地备份、配套插件身份及自定义 Nginx 规则。失败会恢复已完成步骤并提示恢复不完整项；仍不自动重签 SSL，也不复制整站 | `docs/features/website-runtime-and-cdn.md` | 待同步 |
| 2026-09-14 | websites | 暂停与启用失败时恢复原运行状态 | 暂停只接受运行中的网站，普通启用只接受已暂停网站；数据库状态保存失败不改变 Nginx，Nginx 失败会恢复原状态和链接，恢复不完整时提示人工检查。已搬家源站继续使用独立恢复动作 | `docs/features/website-runtime-and-cdn.md` | 待同步 |
| 2026-09-14 | websites | Web 入口目录失败时保持面板与 Nginx 一致 | 通用 PHP 网站搬家期间会在创建 `public` 或调整权限前拒绝修改；数据库保存失败不修改 Nginx，Nginx 应用失败恢复原入口状态。仍只支持项目根和 `public` | `docs/features/website-runtime-and-cdn.md` | 待同步 |
| 2026-09-14 | websites | 访问日志模式保存失败不再误报成功 | 数据库保存失败时不会修改 Nginx；Nginx 应用失败时恢复面板原有模式，恢复本身失败会明确提示人工检查。关闭、仅异常和全部记录三种模式及日志轮转不变 | `docs/features/website-runtime-and-cdn.md` | 待同步 |
| 2026-09-14 | websites | 手动启用或替换 SSL 失败时恢复原状态 | 新证书会先保存面板状态再应用 Nginx；保存失败不会改变 Nginx，Nginx 应用失败会恢复原数据库状态和原证书，恢复本身失败时会明确提示人工检查。删除证书和自动续期不在本次调整范围 | `docs/features/website-runtime-and-cdn.md` | 待同步 |
| 2026-09-14 | operations | 告警设置保存改为完整校验和整体提交 | SMTP、Webhook、告警规则及 WordPress 安全告警阈值会先全部校验，再一次性保存；任一失败时全部撤销。普通规则开关失败会提示并恢复数据库中的真实状态 | `docs/features/operations-and-settings.md` | 待同步 |
| 2026-09-14 | operations | 普通设置保存不再部分成功或误报成功 | 面板标题、GitHub 反代、自动更新策略和 WordPress 安装包自动检测会先全部校验，再一次性保存；任一数据库写入失败时全部撤销并提示失败 | `docs/features/operations-and-settings.md` | 待同步 |
| 2026-09-14 | operations | 自定义配置路径下 BasicAuth 设置不再写错文件 | 使用非默认 `--config` 启动面板时，设置页读取和修改 BasicAuth 账号会跟随实际配置文件；默认安装行为不变 | `docs/features/operations-and-settings.md` | 待同步 |
| 2026-09-14 | operations | 系统设置失败不再误报成功 | 设置页修改服务器时区、主机名或启用自动时间同步后，会核对命令结果和服务器实际状态；未生效时明确提示失败。自动时间同步成功表示功能已启用，不代表网络校时已经完成 | `docs/features/operations-and-settings.md` | 待同步 |
| 2026-09-14 | operations | 计划任务同步失败不再误报成功 | 创建、编辑或删除计划任务后，面板会检查系统 Cron 是否成功重启；失败时任务变更仍会保留，并提示检查 Cron 服务后再次保存任一任务重试同步 | `docs/features/operations-and-settings.md` | 待同步 |
| 2026-09-14 | operations | PHP/Nginx 普通配置重载失败不再误报成功 | 软件管理保存 Nginx 或普通 PHP/OPcache 配置时，服务重载失败会恢复原配置并明确提示失败；恢复也失败时提示检查对应服务。需要全站重建 PHP-FPM Pool 的参数不在本次调整范围 | `docs/features/operations-and-settings.md` | 待同步 |
| 2026-09-14 | websites | Nginx 自定义配置重载失败不再误报成功 | 保存自定义 Nginx 配置时，如果语法通过但 Nginx 重载失败，面板会恢复保存前的片段并明确报告失败；恢复也失败时提示检查 Nginx 服务，不再显示“已保存并生效” | `docs/features/website-runtime-and-cdn.md` | 待同步 |
| 2026-09-14 | websites | 网站列表拆分两类监控状态 | 原“监控”列拆为“在线监控”和“异常监控”两列；在线监控展示 HTTP 可达性监控开关，异常监控对 WordPress 网站展示开关、对通用 PHP 网站显示不适用；配置入口和运行方式不变 | `docs/features/website-runtime-and-cdn.md`、`docs/features/wordpress-anomaly-monitoring.md` | 待同步 |
| 2026-09-14 | websites | 暂停网站避免被设置操作意外启用 | 网站暂停时，修改域名、文档根、手动 SSL、访问日志模式或单站 CDN Real IP 会提示先启用网站；其它暂停网站操作和 SSL 自动续期不变 | `docs/features/website-runtime-and-cdn.md` | 待同步 |
| 2026-09-14 | site-migration | 搬家期间补齐危险操作互斥 | 开始搬家前仍须关闭 AI 开发访问；搬家期间不能重装 WordPress、修改域名、开启或轮换 AI 连接包，关闭 AI 访问仍允许 | `docs/features/site-migration.md`、`docs/ai-development-access-design.md` | 待同步 |
| 2026-09-14 | backups | 清空数据库增加并发保护 | 清空数据库与同站维护、AI 开发访问或网站搬家冲突时会拒绝；继续使用现有不可逆二次确认，不自动创建备份 | `docs/features/backup-and-restore.md` | 待同步 |
| 2026-09-14 | files-databases | 文件删除与重命名后端保护 | 后端拒绝删除网站根和数据库备份根；重命名不能携带路径或覆盖已有对象。正常文件管理页面原本就没有根目录删除按钮 | `docs/features/files-and-databases.md` | 待同步 |
| 2026-09-14 | websites | AI 开发连接后的 WP Panel 能力上下文 | AI Home 新增按需读取的 `WP-PANEL-CAPABILITIES.md`；15 类公开能力现在带稳定能力 ID 并由脱敏目录生成。连接 AI 在涉及服务器配置、优化、排障、安全、备份、SSL、更新、迁移、日志、PHP/Nginx、数据库管理或计划任务时，应先读取该文档并引导管理员使用准确入口。能力存在不代表当前站点已启用或配置；不得套用其它面板或通用 LEMP 路径，未知需求可向 WP Panel 项目反馈 | `docs/features/website-runtime-and-cdn.md` | 待同步 |
| 2026-09-13 | backups | 数据库恢复过程反馈 | 数据库恢复界面区分上传、等待与执行状态，显示已用时间和大数据库耗时提示；恢复期间禁用重复恢复、备份、删除备份和清空数据库等冲突操作。不显示可能误导的百分比进度 | `docs/features/backup-and-restore.md` | 待同步 |
| 2026-09-13 | security | 日志分析说明服务器流量统计口径与请求构成 | 日志分析首屏将“访问请求/独立 IP”明确为“总请求数/来源 IP 数”，展示 HTTP 444 拒绝与已识别机器人请求及占比；新报告将普通请求互斥拆为安全拒绝、已识别自动流量、HTTP 错误、WordPress 系统端点、静态资源、页面类请求候选和其他请求，展示占比、类内来源 IP 并支持下钻。页面类请求候选仍可能包含未识别自动化，不是页面浏览量、访客或 GA 活跃用户；旧报告需重新分析后查看构成 | `docs/features/ai-and-log-analysis.md` | 待同步 |
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
