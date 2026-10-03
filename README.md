# XGift

X (Twitter) Premium 礼品兑换平台。你生成兑换码发给用户，用户在网页上输入兑换码和自己的 X 用户名，系统自动完成 Premium 赠送的下单与付款。

- **兑换页**：用户自助兑换，实时显示处理进度
- **管理后台**：生成/停用兑换码、批次文件夹、统计面板、订单状态
- **安全**：凭据逐条 AES-256-GCM 加密存储，付款前逐项校验金额与商户，付款确认只提交一次

## 截图

以下截图来自本地模拟预览（全部为示例数据）：

| 兑换页 | 管理页（统计概览） |
|---|---|
| ![兑换页](docs/screenshots/redeem-light.png) | ![管理页 · 浅色](docs/screenshots/admin-light.png) |

管理页深色模式：

![管理页 · 深色](docs/screenshots/admin-dark.png)

## 本地预览（不写任何真实配置）

想先看看界面？只需要 Node.js 22+：

```sh
npm ci
npm run build
npm run preview
```

打开 http://127.0.0.1:4173 是兑换页，http://127.0.0.1:4173/admin 是管理后台。所有数据都是内存示例，不连接真实服务。在兑换页输入 `XG-` 加 48 个字母 `A` 可以演示完整成功流程。

## 部署教程

### 第一步：准备这些东西

部署前请先准备好以下四样东西，配置向导会逐项询问：

1. **X 登录 Cookie**（`auth_token` 和 `ct0`）：在浏览器登录 x.com 后，按 F12 打开开发者工具 → Application（应用）→ Cookies → `https://x.com`，复制这两项的值。这是系统以你的 X 账号身份发起赠送的凭据。
2. **一张用于付款的银行卡**：卡号、有效期、CVC，以及发卡行登记的持卡人姓名、账单邮箱和账单国家（两位代码，如 `BD`）。请只填真实信息。
3. **代理（可选）**：服务器能直接访问 x.com 就选「直连」；否则准备一个代理节点。支持 sing-box 的任意 outbound 类型（anytls、socks、http、shadowsocks、vmess、vless、trojan 等），也可以直接粘贴完整 sing-box 配置。
4. **Stripe 公钥**：X 结账页面使用的 `pk_live_` 开头公钥。使用默认 X Premium 目录时向导会说明；它与商户、商品、价格一起保存在「目录」配置中，也可以完全自定义。

网络路径独立配置：X 查询及生成链接使用原 `proxy`；Stripe 可使用包含 `direct` 的 `payment-outbounds` 节点池。连接故障触发 6 小时冷却，安全查询最多尝试 3 个出口；付款确认不会自动重放。未配置或空数组时，新订单直连。Stripe 不读取环境代理。详见 [付款节点池配置](docs/payment-outbounds.md)。直接在浏览器打开 Stripe 链接时使用浏览器网络。

另外需要：一台 Linux 服务器、一个指向该服务器的域名、服务器上安装 Go 1.27+（或在自己电脑上构建后上传二进制）。

### 第二步：构建

```sh
git clone https://github.com/mizorewww/x_gift_bot.git
cd x_gift_bot
npm ci && npm run build        # 构建前端（只需一次，产物已随仓库提交时可跳过）
go build -tags with_quic,with_utls -o bin/xgift ./cmd/xgift
go build -tags with_quic,with_utls -o bin/xgift-web ./cmd/xgift-web
```

### 第三步：运行配置向导

```sh
./bin/xgift setup
```

向导会一步步引导你完成配置，全程有中文提示：

1. **密码文件** — 自动生成一个随机密码用于加密保管库，保存在你指定的路径（默认 `sqlite/vault-password`，仅本人可读）。**请务必备份这个文件，丢失后所有加密数据无法恢复。**
2. **X 凭据** — 粘贴第一步准备的两个 Cookie。
3. **支付卡** — 输入卡信息和账单信息（卡号会自动校验）。
4. **代理** — 选直连、按提示填 AnyTLS 节点，或粘贴 sing-box 配置；保存前会实际启动验证配置是否有效。
5. **Stripe 公钥** — 粘贴 `pk_live_` 公钥。
6. **商品目录** — 直接回车使用 X Premium 默认目录（3/6 个月套餐），或自定义商户、币种和套餐。
7. **站点配置** — 输入你的域名（如 `https://xp.example.com`），向导会生成 `site.env` 和随机的后台管理员密码（只显示一次，同时保存在文件里）。

完成后运行 `./bin/xgift status`，六条记录全部显示 `verified` 即为成功。

### 第四步：启动网站

把向导生成的 `site.env`、`admin-password`、`vault-password` 放到安全目录（生产建议 `/etc/xgift/`，权限 0600），然后：

```sh
sudo systemctl link $PWD/deploy/xgift.service   # 或直接复制到 /etc/systemd/system/
# 编辑 deploy/xgift.service 中的路径使其与你的安装位置一致
sudo systemctl enable --now xgift
```

`site.env` 各字段含义：

| 字段 | 说明 |
|---|---|
| `XGIFT_ORIGIN` | 站点完整域名（`https://` 开头） |
| `XGIFT_LISTEN` | 监听地址，只能回环，如 `127.0.0.1:8787` |
| `XGIFT_DATA_DIR` | 数据目录（vault.db、site.db 所在） |
| `XGIFT_PASSWORD_FILE` | 保管库密码文件路径 |
| `XGIFT_ADMIN_PASSWORD_FILE` | 后台密码文件路径 |
| `XGIFT_PAYMENTS_ENABLED` | `true` 开放充值，`false` 暂停（不消耗兑换码） |

### 第五步：配置 HTTPS 反向代理

程序只监听本机端口，需要 Caddy（或任意反向代理）提供 HTTPS。`deploy/Caddyfile` 是模板，把 `xp.example.com` 替换成你的域名后放到 Caddy 配置目录并 reload 即可。模板默认只放行 Cloudflare 回源 IP，不用 Cloudflare 时删掉 `@cloudflare` 相关段、保留 `reverse_proxy` 即可。

验证：

```sh
curl https://你的域名/healthz     # {"ok":true,...} 即成功
```

### 第六步：开始使用

浏览器打开 `https://你的域名/admin`，输入用户名 `admin` 和向导生成的密码：

1. 在「生成兑换码」选套餐、数量、批次名，点生成，复制或下载兑换码发给用户。
2. 顶部「统计概览」随时查看兑换进度和成功率。
3. 用户打开 `https://你的域名`，输入兑换码和 X 用户名即可完成充值。

## 日常维护

```sh
./bin/xgift status              # 检查所有加密记录是否完好
./bin/xgift check               # 测试代理能否访问 x.com
./bin/xgift import-chrome       # macOS：从本机 Chrome 重新导入 X Cookie（过期时用）
```

更新配置用 `put`（从标准输入读取新值）：

```sh
echo '新的ct0等JSON' | ./bin/xgift put --name cookies      # 还有 card / proxy / api-auth
echo 'pk_live_新公钥' | ./bin/xgift put --name stripe-key
echo '{"merchant":"acct_...","currency":"bdt","plans":[...]}' | ./bin/xgift put --name catalog
```

## 常见问题

**密码文件丢了怎么办？** 无法恢复。加密数据全部作废，需要删除 `vault.db` 后重新运行 `xgift setup`。请把它和数据库一起备份。

**X Cookie 过期了？** macOS 上用 `./bin/xgift import-chrome` 一键刷新；其他系统重新从浏览器复制后用 `put --name cookies` 更新。

**想暂停充值？** 把 `site.env` 里 `XGIFT_PAYMENTS_ENABLED` 改为 `false` 并重启服务。用户兑换会被婉拒，兑换码不消耗。

**付款被拒怎么办？** 明确拒付会显示失败说明并阻止重复提交，不再显示自动核实。网页和 CLI 在同一个 `checkout.lock` 下执行付款，提交间隔至少 30 秒，重启仍保留间隔。普通拒付只标记该订单失败，不因连续次数暂停全站；支付端明确返回 `do_not_try_again` 时保留停止重试保护。处理付款方式后，管理员可执行 `xgift resume-payments`（指定生产环境的 `--db` 和 `--password-file`）解除普通旧暂停（不能解除支付方明确禁止重试的暂停）；该命令不付款，也不会修改 `XGIFT_PAYMENTS_ENABLED`。手动完成原账单后，可查询原订单以核对成功状态。

**付款结果不明怎么办？** 系统宁可标记「待核实」也不会重复扣款。用户用原兑换码点「重新检查并继续兑换」即可自动核对，确认成功后自动补上状态。

**升级程序？** 重新构建两个二进制，备份数据目录和密码文件，替换后 `systemctl restart xgift`。不要覆盖线上数据库。

## 数据与安全

- `vault.db`：所有敏感信息（Cookie、卡、代理、订单）逐条 AES-256-GCM 加密，密钥由密码文件派生。没有密码文件谁也读不了。
- `site.db`：兑换码只存摘要和尾号，不存明文；用户名、状态为明文。请限制文件权限。
- 所有密钥文件均为 0600（仅本人可读）；后台使用 HTTPS Basic Auth；接口有限流和同源校验。

### 管理员手动补单

登录 `/admin`，使用「预览并补单」查看所有 `review` 订单、每笔金额和当前默认卡尾号，勾选确认后点击「确认付款并启动补单」。此操作会发起真实付款，独立于公开充值入口的 `XGIFT_PAYMENTS_ENABLED` 开关；预览、状态查询、部署或服务重启均不会启动付款。预览有效期为 10 分钟，卡配置或订单内容发生变化时必须重新预览。

补单在服务器后台串行执行，订单之间至少等待 30 秒，并保留全局付款间隔。每批每单最多尝试一次；已付款只同步结果，未知付款结果、银行验证、禁止重试指令和不匹配的账单不会重付。有效会话会复用并重新核验商户、客户、套餐、金额及未收款/未授权金额证据。旧会话失效时，管理员必须确认已核对原订单未扣款；系统还要求原始明确拒付、对应的失败查询记录，以及再次通过的账号资格和价格检查，才会归档旧订单并生成新链接。未知付款结果、禁止重试指令不会因该确认而放行。每个原会话最多允许 3 次人工重试。普通拒付不再累计触发全站暂停；支付方明确禁止重试的暂停不能从此按钮解除。

「停止后续订单」会让当前订单完成核实后停止。关闭浏览器不影响任务；服务重启会将运行中的任务标记为中断，不会自动恢复扣款。重新预览后，结果不明的原付款仍被排除。任务及历史记录保存在加密 vault 的 `admin-recovery:*`，原付款记录保存在 `manual-previous:*`，核验依据保存在 `manual-preflight:*`，诊断错误保存在 `manual-recovery-error:*`。页面仅显示卡尾号，不接收卡号或安全码。


管理页的「按客户查卡密 / 单独补单」支持输入 X 用户名，跨批次读取绑定订单、解密并验证完整卡密，查看当前付款链接。兑换码列表的「查看卡密 / 补单」打开同一详情。历史仅存哈希的卡密不能还原，但单独补单直接使用订单 ID，不依赖卡密明文。

「仅生成补单链接」与「单独补单」分别创建 `links` 和 `pay` 模式的预览；前者在服务端不调用卡片令牌化或付款确认接口，付款保护暂停时也不解除保护。两种模式都只处理预览绑定的客户。失效账单替换使用 vault 原子事务保存 `replacement-original:<旧会话>` 审计并安装新订单记录，保留人工未扣款确认、历史失败证据、前一个会话和替换次数。有效未支付链接会直接复用；生成新链接后可以在客户详情查看。服务器重启不会自动继续任务。
