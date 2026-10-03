# XGift

Go CLI + Premium 兑换站。网站：`https://xp.example.com`，后台：`/admin`。

**已部署；用户已成功完成一次 6 个月自动充值。** 账单国家按用户指定配置为 `BD`，姓名和邮箱已加密保存，不强制街道或邮编；已移除导致 Stripe 拒绝的 `save_payment_method=false` 参数。旧 checkout 已失效，其关联 PaymentIntent 自动取消，但公开查询缺少完整收款字段，归档检查因此拒绝解锁。`user-a` 的旧订单仍锁定，不能用该账号再次充值；可用其他符合赠送条件的账号测试。早期日志中的 0 是缺失字段的默认值，不能作为未收款证明。用户已自行完成另一账号的 6 个月充值，并反馈到账；随后通过 Stripe 专用结果接口确认成功。已修复成功付款被误判为待核实的问题，历史成功订单同步为成功 / 100%。本次修复未提交任何付款。

## 构建

需要 Go 1.27.1、CGO 与 C 编译器。修改前端时还需要 Node.js 22+ 与 npm：

```sh
npm ci
npm run build
go build -o bin/xgift ./cmd/xgift
go build -o bin/xgift-web ./cmd/xgift-web
go vet ./...
```

前端使用 React 19 + TypeScript + MUI 7 / Emotion，采用 M3 风格的蓝色主色、粉色辅助色与中性灰表面、8px 间距体系、响应式布局和 MUI 交互组件。浅色与深色主题默认跟随系统，可通过标题旁的外观菜单手动选择；仅外观偏好保存至 localStorage，兑换码不保存在浏览器持久存储。源码在 `frontend/src/`，`npm run build` 先执行严格类型检查，再将两个入口打包至 `internal/site/assets/app.js` 和 `admin.js`，并生成主题初始化脚本 `appearance.js` 及各脚本的 `.gz` 文件；构建产物随项目保存，请勿手工修改。静态页面继续嵌入 Go 二进制，生产运行无需 Node、外部 CDN 或外部字体。仅修改 Go 时可以直接使用已提交的前端产物构建。

所有主题颜色集中在 `frontend/src/colors.json`。页面背景浅色为 `#F5F5F5`、深色为 `#121212`；卡片分别为 `#FFFFFF`、`#1E1E1E`。HTML 模板位于 `frontend/pages/`，构建时从同一配色生成初始背景，并写入脚本内容哈希版本，避免旧脚本混用。外观菜单不锁定页面滚动条，关闭时使用 `preventScroll` 恢复焦点；桌面与手机视口各连续切换 12 次，卡片坐标和滚动位置保持稳定。

`npm run check` 执行 TypeScript 检查。按项目原有约定未新增测试文件；前端通过隔离的本地模拟预览进行浏览器验收，不调用生产付款接口。

```sh
npm run preview                 # http://127.0.0.1:4173 和 /admin
PREVIEW_PAUSED=true PREVIEW_PORT=4174 npm run preview
```

预览仅监听回环地址，所有兑换码、账号与接口响应都是内存示例，不连接 Go 服务、生产数据库、X 或 Stripe。修改前端后重新执行 `npm run build` 并刷新页面。输入 `XG-` 加 48 位 A 可演示成功流程；最后一位改为 B 演示待核实、C 演示资格拒绝、D 演示停用、F 演示持续处理中。其余输入先遵循与生产相同的格式校验。管理页可演示生成、单枚/全部复制、下载、清除、分页、停用和文件夹管理；重启预览会清空示例数据。预览不验证真实后台 Basic Auth 或第三方付款链路。

两个项目级技能已安装到 `.agents/skills/`：

- `frontend-design`：来自 [PracticalSwan/agent-skills](https://github.com/PracticalSwan/agent-skills/tree/main/frontend-design)。
- `mui`：来自 [softaworks/agent-toolkit](https://github.com/softaworks/agent-toolkit/tree/main/skills/mui)，包含 README、SKILL.md 和配套资源。

MUI 动态样式通过每响应随机 CSP nonce 传给 Emotion；仅样式属性允许内联，以支持进度与组件布局。脚本仍仅允许同源，后台页面、脚本与 API 保留 Basic Auth。兑换码在浏览器中仅保存在页面内存；后台复制时按需读取服务器密钥库中的加密内容；结果不确定时前端限制为查询，只有后端确认成功才显示 100%。

## Lighthouse 验收

先执行 `npm run build`，再在另一个终端启动 `npm run preview`。审计需要本机安装 Chrome：

```sh
npm run audit:ui                                  # 兑换页，连续 3 轮
npm run audit:ui -- --dark --runs 1                # 系统深色模式
npm run audit:ui -- --url http://127.0.0.1:4173/admin --runs 1
```

每一轮独立创建空的临时 Chrome profile，禁用扩展，不复用缓存或登录状态，结束后自动删除。保留 Lighthouse 默认移动设备、模拟网络与 CPU 限速，检查 Performance、Accessibility、Best Practices、SEO 四项；任一项低于 97 分时命令以非零状态退出。HTML、JSON 与汇总报告保存在忽略提交的 `.artifacts/lighthouse/`。

2026-10-02 使用 Lighthouse 13.5.0，本地移动端兑换页优化前为 **83 / 100 / 92 / 100**；修复后连续三轮均为 **99 / 100 / 100 / 100**。深色兑换页同为 **99 / 100 / 100 / 100**；后台浅色与深色均为 **98 / 100 / 100 / 100**。兑换页 FCP 约 1.65 秒，LCP 约 1.95 秒，CLS 为 0。主要修复是让本地预览和 Go 静态资源均支持 gzip，以及移除 MUI 主题切换时不带 nonce 的临时样式注入。脚本原始大小约 494 KiB，压缩后约 154 KiB；生产 Caddy 原有压缩继续保留。订单、API、后台响应仍禁止缓存，不为性能分数缓存敏感内容。这些是本地实验室结果，不代表线上实测 Core Web Vitals。

加入文件夹管理、单击复制和中性灰主题后，再用独立干净 profile 各复测一轮：兑换页浅色 **98 / 100 / 100 / 100**、深色 **99 / 100 / 100 / 100**；后台浅色与深色均为 **98 / 100 / 100 / 100**。兑换页 CLS 为 0，后台约 0.0024，四项均达到 97 分门槛。

## 首次配置（引导向导）

`xgift setup` 是交互式首次配置向导，逐行读取标准输入，终端下密码类输入不回显，管道输入也可完整驱动，便于脚本化和测试。保管库或密码文件已存在时拒绝运行，更新凭据仍使用 `put` / `billing` / `import-chrome`。向导依次询问并写入：

1. 密码文件路径（默认 `<数据目录>/vault-password`，权限 0600，内容为 32 字节随机密钥的 base64）。生成后写入 `<数据目录>/password-path` 并创建加密保管库。
2. X 凭据：`auth_token` 与 `ct0`（必填）；Authorization 请求头（留空使用 X 网页版默认 Bearer）；User-Agent（留空使用当前 macOS 版 Chrome 字符串）。macOS 之后可用 `import-chrome` 刷新 Cookie。
3. 支付卡：卡号（Luhn 校验）、有效期、CVC、持卡人姓名、账单邮箱、两位账单国家代码，以及可选的邮编、地址行、城市、州/省（留空则不写入该键）。只填写发卡行登记的真实信息。
4. 代理：直连、AnyTLS 引导填写或粘贴 sing-box outbound JSON（单个对象或含 `outbounds` 数组的完整配置，可含 `route` / `dns`）。保存前在本机临时端口实际启动内嵌 sing-box 验证配置有效，可选做连通性测试，失败仅警告并保留配置。
5. Stripe 公钥：`pk_live_` 开头的 X 结账商户公钥，原始字节保存，不是用户的 secret key。
6. 站点配置（可选）：站点 Origin、监听地址、支付开关。生成随机后台密码写入 `<数据目录>/admin-password`（0600，终端仅显示一次），并写出与 `deploy/site.env.example` 相同键的 `<数据目录>/site.env`（0600），其中 `XGIFT_DATA_DIR`、`XGIFT_PASSWORD_FILE`、`XGIFT_ADMIN_PASSWORD_FILE` 指向本次实际路径。生产部署按部署章节表格移至 `/etc/xgift/` 并保持仅属主可读。

向导共写入五条保管库记录：`cookies`（X 登录 Cookie）、`api-auth`（Authorization 与 User-Agent）、`card`（支付卡）、`proxy`（sing-box 配置）、`stripe-key`（Stripe 公钥）。脚本化或非交互环境仍可使用 `init`（标准输入读入整个 JSON 对象）和 `put` 逐条写入。

示例会话（值为虚构）：

```text
$ ./bin/xgift --db sqlite/vault.db setup
密码文件保存路径 [sqlite/vault-password]: ↵
auth_token: 3f7c…（不回显）
ct0: 9a2e…（不回显）
Authorization 请求头（留空使用 X 网页版默认 Bearer）: ↵
User-Agent（留空使用当前 macOS 版 Chrome）: ↵
卡号（仅数字，可含空格或连字符）: 4242 4242 4242 4242（不回显）
有效期月份（01-12）: 12
有效期年份（4 位）: 2030
CVC（3-4 位）: 123（不回显）
持卡人姓名: Test User
账单邮箱: test@example.com
账单国家（两位代码，如 BD）: BD
…（可选地址字段均可留空跳过）
请选择 [1]: 1
代理配置有效。
是否进行连通性测试（通过代理访问 https://x.com）？ [y/N]: ↵
Stripe publishable key（pk_live_...）: pk_live_EXAMPLE…（不回显）
是否生成站点配置文件？ [y/N]: n
配置完成。已写入记录：cookies、api-auth、card、proxy、stripe-key
```

## CLI

```sh
./bin/xgift setup                     # 交互式首次配置向导
./bin/xgift status
./bin/xgift import-chrome --profile Default
./bin/xgift check
./bin/xgift username --inspect        # 只读核对已保存订单的 Stripe 状态
./bin/xgift proxy --port 18791
./bin/xgift username                  # 默认 6 个月，只生成/读取结账链接
./bin/xgift username --months 3       # 3 个月，只生成/读取结账链接
```

`--pay` 是真实付款入口。网站已开放用户自行测试，使用有效兑换码点击充值会触发真实付款；网站开关只管网站，CLI 自身仍执行配置、订单状态和金额校验。

- 仅允许 3 个月恰好 300 BDT、6 个月恰好 600 BDT。校验 X 报价和 Stripe 最终总额、币种、商品、数量、一次性模式及商户身份，任何不符都停止。
- 内嵌 sing-box，只监听本机端口，不修改系统代理。代理配置从加密库读取，为完整 sing-box 配置对象，支持任意 sing-box outbound 类型：direct、socks、http、shadowsocks、vmess、vless、trojan、anytls、shadowtls、snell、ssh、tor、block、selector/urltest，并可携带 route 与 dns 配置。为安全起见，配置中的 `services`、`endpoints`、`experimental` 段会被忽略（防止打开非本机监听或修改主机网络）。hysteria/hysteria2/tuic 与 wireguard/tailscale 需要对应构建标签（如 `go build -tags with_quic`），默认构建未启用。最小配置示例：

```json
{"outbounds":[{"type":"direct","tag":"direct"}]}
```

```json
{"outbounds":[{"type":"anytls","tag":"proxy","server":"example.com","server_port":443,"password":"...","tls":{"enabled":true,"server_name":"example.com","insecure":false}}]}
```
- 固定 X API / Stripe 入口，无网页识别或 ChatGPT Chrome 扩展。
- 私有 API 可能变化；6 个月已有用户实付成功记录，3 个月尚未实付验证；银行验证或 Stripe/X 风控仍可能阻止自动完成。
- 按 X 固定用户 ID 保存订单。`submitting`、`unknown`、`requires_action` 阻止再次付款确认。建单阶段仅有明确临时错误恢复许可、剩余次数且无付款证据时允许有限恢复；取得有效 session 后固定复用，付款方式沿用原幂等键。
- 同一收件人已有记录时，不能用另一枚兑换码或另一套餐重复付款。当前没有自动追加时长、自动退款或人工“强制成功”入口。

## 兑换站

用户填写兑换码和用户名，系统先以只读请求核实 X 是否允许赠送。X 不允许时明确提示，兑换码不使用；服务暂停或查询失败也不消耗兑换码。付款会原子锁定兑换码和收件人，使用同一订单校验流程；只有 Stripe 确认成功后才显示完成。付款确认只发一次；结果不明时后台继续只读核实，不重新付款。重启后也只恢复结果查询。点击「查询兑换进度」会对待核实订单执行只读结果查询；若确认已成功，自动修复账本和兑换码状态，不再次扣款。

待核实订单允许用户使用原兑换码和绑定用户名，主动点击「重新检查并继续兑换」。尚无付款提交证据的原订单会重新核对 X 身份、资格、套餐以及 Stripe 金额、商户、商品和未付款状态，已有有效 session 时复用原链接继续付款。已提交、结果未知或需要银行验证的订单只查询原付款结果；查询接口、后台任务和页面轮询不会触发新付款。兑换码不能换绑其他账号；过期链接和缺少安全恢复条件的创建记录仍停止处理。首次建单前未通过资格检查的未使用码仍可换账号，已绑定的待核实码保留原绑定。

后台使用 HTTPS Basic Auth，用户名 `admin`，随机密码从权限为 `0600` 的文件读取。支持：

- 每批 1–500 枚兑换码，绑定 3 或 6 个月，可自定义批次名称。
- 新生成的兑换码以 AES-256-GCM 加密保存在现有 vault；`site.db` 保留摘要、尾号及可复制标记。列表不返回完整兑换码，点击条目或复制图标后，通过受认证与同源校验保护的接口按需读取，刷新或重启后仍可复制。旧版只保存摘要的历史码无法恢复，显示“历史码未保存”。生成结果仍支持复制全部或下载 TXT。
- 分页浏览全部兑换码，查看账号和状态，停用未使用的码。
- 批次名称就是文件夹名称：同名批次自动归组，点击批次文件夹筛选，支持重命名以及每次最多 100 枚移动到另一批次。生成区只填写批次名称，不再维护独立文件夹分类。
- 启动时执行一次性批次迁移，按已有 `batch` 名称归组；重命名和移动会同步批次名称，不改变兑换码或订单状态。重复启动不会重新覆盖分类。复制、停用、重命名和刷新使用带边框、提示及无障碍名称的图标按钮；停用仍需确认，操作按钮与勾选不会触发行复制。
- 同源 POST 校验、请求限流、禁止缓存与页面嵌入；用户接口不返回卡信息、Cookie 或 Stripe 付款链接。

浏览器关闭/刷新后，仍可从管理列表复制本版本生成的兑换码。若生成响应丢失，请先刷新核实批次并复制，不要立即重复生成。没有自动重复生成。

## 本地数据

`sqlite/vault.db` 的敏感记录采用 AES-256-GCM 加密，密钥通过 scrypt 派生；SQLite 结构和记录名称不是密文，不是 SQLCipher 全库加密。Cookie、卡、代理与订单记录分别加密。仅读取 Chrome 的 X `auth_token` / `ct0`。

`sqlite/password-path` 保存密码文件位置。`setup` 向导默认把密码文件写到持久路径 `<数据目录>/vault-password`（0600）；`init` 不带 `--password-file` 时的旧默认仍是 `/tmp/xgift-password-*`，`/tmp` 清理后不能恢复。无论哪种方式都请将密码文件安全备份。也可通过 `XGIFT_PASSWORD_FILE` 或 CLI `--password-file` 指定持久路径。目录权限 `0700`，秘密文件 `0600`。

`.private/`、`sqlite/`、数据库、日志、Go 二进制和环境秘密均不提交 Git（嵌入的前端构建产物除外）。服务器密钥与本地密钥独立。本地 `.private/export/admin-password` 保存本次生成的后台密码，勿提交或分享。

## 服务器部署

目标服务器 `example-server`。使用专用无登录用户 `xgift`、systemd 服务和 Caddy HTTPS：

| 路径 | 用途 |
|---|---|
| `/opt/xgift/bin/xgift-web` | 站点程序 |
| `/opt/xgift/bin/xgift` | CLI，维护用 |
| `/var/lib/xgift/vault.db` | 加密凭据及订单 |
| `/var/lib/xgift/site.db` | 兑换码哈希、用户名、状态；访问权限保护，不是加密库 |
| `/etc/xgift/vault-password` | 服务器独立密钥 |
| `/etc/xgift/admin-password` | 后台密码 |
| `/etc/xgift/site.env` | 服务配置，生产站点 `XGIFT_PAYMENTS_ENABLED=true`，模板默认关闭 |
| `/etc/caddy/conf.d/xp.example.com.caddy` | 本站反向代理 |

配置模板在 `deploy/`。程序仅允许绑定回环 IP；Caddy 覆盖 `X-Real-IP`。`xp.example.com` 的 A 记录已开启 Cloudflare 代理（橙云），源站保留 Caddy HTTPS。本站 HTTP/HTTPS 入口仅接受 TCP 来源属于 Cloudflare 官方 IP 段且带有 `CF-Connecting-IP` 的请求；其他来源统一返回 403，不转发到应用。来源匹配使用 `remote_ip`，不能通过伪造 `CF-Connecting-IP`、`X-Real-IP` 或 `X-Forwarded-For` 获得访问权限。IP 段来自 `https://www.cloudflare.com/ips-v4` 和 `https://www.cloudflare.com/ips-v6`，更新时须同步 `deploy/Caddyfile` 及线上配置。此限制仅作用于本站，其他同机站点不受影响；本机运维健康检查使用 `http://127.0.0.1:8787/healthz`。兑换、查询与后台响应维持 `Cache-Control: no-store`，不缓存订单或管理数据。

2026-10-02 19:54（UTC+8）已部署 Cloudflare 回源白名单，核对官方 IPv4 15 段、IPv6 7 段。配置备份位于 `/var/backups/xgift/cloudflare-only-20261002T115405Z`。完整 Caddy 配置校验通过后热加载；14 项低频访问检查通过，包括 CDN 首页/健康/静态资源、后台鉴权、HTTP/HTTPS 直连拒绝以及伪造转发头拒绝。其他站点配置摘要和付款服务 PID 保持不变，充值开关保持开启，未发起付款。验证记录：`.artifacts/security-audit/cloudflare-only-verification.json`。白名单是静态快照，Cloudflare 官方网段更新时需复核并同步。

```sh
ssh example-server 'sudo systemctl status xgift --no-pager'
ssh example-server 'sudo journalctl -u xgift --since "1 hour ago" --no-pager'
```

更新前先执行 `npm ci && npm run build` 生成最新前端产物，再在服务器构建两个二进制，停止 `xgift`，备份数据及密钥，再替换程序并启动。不要覆盖线上 `site.db` 或用旧的本地 vault 覆盖线上订单。备份必须包含 `site.db`（停服或用 SQLite backup API，不能忽略 WAL）、`vault.db` 及独立密钥；所有备份同样限制权限。

2026-10-02 18:56（UTC+8）已部署 React / MUI 蓝粉灰主题版本。服务器构建目录为 `/home/operator/xgift-releases/release-20261002-fSYHZH`，停服备份位于 `/var/backups/xgift/20261002T105631Z`（目录 `0700`，状态与密钥归档 `0600`），保留旧的两个二进制。已验证服务运行、公网健康检查、后台认证、静态资源哈希、gzip 和 CSP nonce。此次升级保留线上数据库、密钥和充值配置，未提交测试付款。

**本次验证边界**：用户已自行完成一笔 6 个月充值。修复通过构建、静态检查、独立代码复查、桌面/手机页面预览及该笔订单只读对账；开发核验不提交付款。付款状态不明会锁定为待核实，不自动重试。

## 账单信息与错误诊断

`billing` 子命令从标准输入接受并加密保存这些字段：`billing_name`、`email`、`billing_country`（两位大写国家代码）、`billing_address_line1`、`billing_address_line2`、`billing_city`、`billing_state`、`billing_postal_code`。只填写发卡行登记的真实信息，不从代理所在地推断。

确认请求发送前将确切参数、PM、幂等键和时间加密保存，错误原文同样加密保存；终端仅输出脱敏错误和请求编号。`--inspect` 不创建 checkout、不创建付款方式、不提交付款，即使收件人已经不能接收新赠送也能检查原订单。临时修复用的恢复入口未保留在生产 CLI 中，避免成为通用的重试通道。

`xgift username --retire-canceled` 是只读核实后的维护归档入口，不提交付款，也不能与 `--pay` 合用。仅当 checkout 已失效、绑定的实时 PaymentIntent 为 canceled、已收款/可扣款均显式为 0 且 latest_charge 显式为 null 时，才在同一事务保存原订单、快照、取消证据并删除活动记录。其他状态全部拒绝；兑换站不会自动执行归档。归档不会解除网站中已绑定收件人的待核实兑换码，需要人工核实，不能用此入口绕过网站重复充值保护。

## 成功判定与进度

付款后通过 Stripe 原 session 的 `/poll` 只读结果接口核实，而不是重新初始化已完成的结账会话。只有原 session、正式环境、付款模式、回调匹配，且外层和付款对象均明确 `succeeded` 才记成功。新订单按 session 加密保存完整的付款前校验响应、原确认参数和返回结果；旧记录使用原有已落库提交证据，不补造历史响应。

兑换页按后台实际阶段显示百分比：检查账号、核对套餐、创建订单、核验金额、准备付款、提交与核实结果。阶段百分比不是时间预测；100% 只表示已确认成功。进度条平滑过渡，提交后平滑滚动至进度区；尊重系统减少动态效果设置。断网或等待较久时保留原订单查询引导，不诱导再次充值。

## 建单失败诊断

X 建单请求在发送前建立加密审计，记录固定操作与接收人/商品参数；返回后补记 HTTP 状态、响应，以及网络或读取错误。网站另按兑换码保存加密失败原因和阶段。响应、Cookie 和错误原文不会进入公开日志；Cookie 也不写入建单审计。

建单失败和付款准备失败明确提示“尚未提交付款”；只有存在付款提交证据时才提示付款结果待核实。旧格式或没有明确临时恢复许可的 `creating` 记录继续阻止自动重建；付款确认始终不自动重试。2026-10-02 的 `user-b` 历史失败缺少原始响应，无法追溯具体上游原因；用户授权后已核对无付款提交证据，归档原记录，按 6 个月 / 600 BDT 补办成功，原兑换码已更新为成功。一次性补办工具已删除。

## 临时故障自动恢复

- X 的只读请求、Stripe 结账初始化、付款方式创建遇临时传输/读取故障、429 或 5xx 时最多尝试 3 次，退避等待可取消并尊重不超过 60 秒的 Retry-After。X 只读查询的 HTTP 403 也在同一预算内重试（默认等待 2 秒、4 秒），不改变凭据或代理；此例外不适用于 X 建单或 Stripe 请求。401、其他不可恢复 4xx、明确业务拒绝、金额/商品/身份校验失败及审计失败不自动重试。X 查询失败响应、阶段、HTTP 状态和请求标识加密保存，响应正文最多保留 32 KiB，不保存请求认证头。
- X 建单每次 POST 前持久化创建次数并清除恢复许可；只有已完成审计的临时失败才允许继续，最多 3 次，总次数不因重启或再次调用清零。响应丢失可能留下未使用的外部未付款 session，本站只选择并保存一个有效 session 进入付款。
- 已保存 session 复用原单；付款方式的重试使用同一表单与幂等键。Stripe 付款确认请求从不套重试包装，确认失败或超时后仅查询。
- 整个兑换任务最长 240 秒，重试时进度不回退。后台每 30 秒最多检查一条最近 24 小时的待核实记录，单次 8 秒超时，服从同一订单锁；浏览器关闭也继续只读核实，成功自动回写原兑换码。未提交付款的失败不会被后台查询任务自动转成新扣款。
- 持续故障、业务限制、银行验证和无法可信核实的付款结果仍可能需要人工处理。不能把这些情况伪装成功，也不能保证第三方永不报错。

2026-10-02 19:15（UTC+8）已部署文件夹管理、点击复制、中性灰背景和主题切换位置修复。发布目录为 `/home/operator/xgift-releases/release-20261002-folders-c8Thh2`，备份位于 `/var/backups/xgift/20261002T111551Z`，权限与上述备份一致。增量迁移后原有 24 枚兑换码全部归入未分类，状态数量保持为可使用 16、已停用 3、已完成 5，处理中及待核实均为 0。已验证迁移与后台统计、认证、资源一致性、gzip、CSP、公网健康及实际背景颜色；线上连续切换外观时卡片坐标不变。未提交测试付款。

2026-10-02 后续修正（19:30 UTC+8 已部署）：批次与文件夹合并为同一概念，并支持列表点击复制加密保存的新码。隔离 SQLite/vault 验证覆盖迁移重复执行、同名批次合并、改名/移动同步、重新打开 vault 后复制、历史码不可恢复、分页和权限检查。浏览器验证覆盖刷新后整行复制、键盘复制，以及停用/勾选不误复制。批次导航固定高度并支持横向滚动，避免异步加载导致内容跳动；后台 Lighthouse 深色连续两轮 98，CLS 约 0.0007，浅色 98，其余三项均为 100；SSH 恢复后已部署至 `/home/operator/xgift-releases/release-20261002-batches-WtRlXw`，切换前备份位于 `/var/backups/xgift/20261002T113011Z`。原有 24 枚兑换码已按原批次归入 5 个文件夹，状态保持可使用 16、已停用 3、已完成 5；批次名称与分类一致性、后台认证、静态资源一致性、gzip、CSP 及公网健康检查均通过，充值配置保持开启。未生成生产测试兑换码或提交付款。

2026-10-02 19:33（UTC+8）按用户提供的 20 枚兑换码白名单完成生产清理：逐一摘要匹配后保留这 20 条，删除其余 4 条测试兑换码及无内容批次。保留记录的批次、状态、接收账号和时间等元数据均未改变，当前可使用 16、已完成 4。完整兑换码补存至现有加密 vault，逐条通过后台复制接口核验 20/20 与输入一致；未将明文写入 Git 或操作日志，已删除本地临时输入。备份位于 `/var/backups/xgift/20261002T113306Z-retain20`，公网健康检查正常，充值保持开启。

2026-10-02 19:47（UTC+8）已部署 Stripe checkout 路径兼容修复：严格接受 `/f/pay/<同一正式环境 session>`，保留原 `/g/pay/`、`/c/pay/` 及域名、协议、身份限制。建单错误区分状态、session ID、URL 校验原因。新增 15 项链接兼容与安全边界回归用例，`go test ./...` 与 checkout 静态检查通过。发布目录 `/home/operator/xgift-releases/release-20261002-checkout-path`；部署备份 `/var/backups/xgift/20261002T114734Z-checkout-path`；二进制哈希、服务与内外网健康检查通过。

同次处理 `user-c` 的 6 个月订单：原 X 建单返回 HTTP 200 / Unpaid，但 `/f/pay/` 链接被旧校验拒绝。用户授权再次尝试付款后，核对加密审计中的账号、商品、回调和原 session，并以 Stripe 实时 guard 确认 open / unpaid、600 BDT、PaymentIntent 明确为 null，随后恢复同一 session 至 created。正常付款流程被 X 当前明确的 `premium_gifting_eligible=false` 拦住，未提交付款；复核原 Stripe session 仍未付款。网站保持 review 并更新资格提示，未强制成功或绕过资格检查。恢复证据和资格响应保存在加密 vault；一次性工具已删除。恢复前备份 `/var/backups/xgift/20261002T114518Z-user-c-recovery`。X 资格变化原因尚不明确，不能据此推断账号已经充值。

2026-10-02 19:50（UTC+8）按用户要求将 checkout 路径统一为 `/[A-Za-z]/pay/<同一正式环境 session>`，接受任意单个大小写英文字母，不再逐个维护路径白名单；保留 HTTPS、精确域名、无凭据、无查询参数及 session 一致性校验。回归验证覆盖全部 52 个字母及多字母、数字、符号、非 ASCII、错误操作等拒绝场景。测试、checkout 静态检查、线上二进制一致性与内外网健康检查通过。发布目录 `/home/operator/xgift-releases/release-20261002-checkout-letter`，备份 `/var/backups/xgift/20261002T115034Z-checkout-letter`。本次仅更新程序，未提交付款。

2026-10-02 20:03（UTC+8）已部署待核实订单主动恢复：同一码、同一绑定账号再次提交时，以订单锁和条件更新切换到 processing；未提交付款的原订单重新完整核验并复用有效 session，已提交订单仅只读对账。前端待核实状态提供「重新检查并继续兑换」及付款说明，查询仍不付款。新增恢复状态、身份/金额、提交证据、锁冲突、暂停、处理中和只读查询测试；本地与服务器竞态测试通过，Go 静态检查及前端构建通过，隔离浏览器验证首次失败→主动恢复→模拟成功和手机布局。生产服务、二进制及公网前端哈希核验通过。发布目录 `/home/operator/xgift-releases/release-20261002-resume-review`；备份 `/var/backups/xgift/20261002T120324Z-resume-review`。本次未提交真实付款，user-c 原码仍为 review，等待用户主动重新提交。

2026-10-02 22:38（UTC+8）复查 user-d 的 22:30 失败：建单前 PremiumGiftingQuery 返回 403，旧策略未重试；稍后只读查询恢复，用户授权重新提交后于 22:34:53 完成 6 个月付款，原码 succeeded / 100%。已部署只读 X 查询 403 的有限重试、加密失败诊断及明确恢复提示；不扩大 X 建单和 Stripe 确认重试。模拟测试覆盖 403→成功、持续 403 三次后停止、401/建单403不重试、长 Retry-After 停止、业务资格拒绝不重试和失败诊断留存；本地/服务器竞态测试、静态检查、服务与内外网健康检查通过，未提交测试付款。发布目录 `/home/operator/xgift-releases/release-20261002-query-recovery`，备份 `/var/backups/xgift/20261002T143845Z-query-recovery`。

2026-10-03 12:11（UTC+8）已部署管理页统计面板、轮询超时继续查询、首次配置向导与通用代理 outbound。新增 `GET /api/admin/stats`（各状态计数、兑换率/成功率、套餐分布、30 天活动序列、待审核阶段分布，只读事务，空表返回全零），后台首页新增可折叠统计板块（汇总卡片与纯 SVG 图表，无新增前端依赖，明暗主题与无障碍文本齐备）；后台分页显示总条数与总页数；兑换页 5 分钟轮询超时后可通过「继续查询」恢复，只查询不重复兑换。新增 `xgift setup` 交互式首次配置向导：引导写入 X 凭据、支付卡（Luhn 与有效期校验）、代理（direct / AnyTLS 引导 / 粘贴 sing-box JSON 并实际启动验证）与 Stripe 公钥，自动生成持久路径的 0600 保管库密码，可选生成 site.env 与随机后台密码；中途失败打印明确修复指引。`put` 新增 `api-auth` / `stripe-key` 修复入口，`status` 同步校验全部五条记录。代理由 anytls 单一注册改为 sing-box include 全量注册，支持任意核心 outbound 及 route/dns 段，并忽略 `services` / `endpoints` / `experimental` 段以保持仅本机监听；已验证 direct 配置可经本机 mixed 入口出网，粘贴含 clash_api 等配置不会开放外部监听。发布目录 `/home/operator/xgift-releases/release-20261003-setup-wizard`，备份 `/var/backups/xgift/20261003T041119Z-setup-wizard`。部署前以新二进制对线上保管库只读 `status` 预检五条记录全部通过；部署后二进制哈希、内外网健康、后台认证、统计接口（90 枚：可使用 60、已完成 29、待核实 1）、gzip 与 CSP nonce 核验通过，充值保持开启。未生成生产兑换码或提交付款。
