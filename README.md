# XGift

Go CLI + Premium 兑换站。网站：`https://xp.example.com`，后台：`/admin`。

**当前付款关闭。** 已验证 X 赠送资格查询、固定套餐报价和 Stripe 结账链接生成；此前一次付款确认返回 HTTP 400，原订单处于 `unknown`，不得自动重试。网站目前可生成、管理兑换码，并核实账号是否允许赠送；符合条件时提示充值尚未开放，兑换码不消耗。不能将当前部署当作已经可成功自动充值的服务。

## 构建

需要 Go 1.27.1、CGO 与 C 编译器：

```sh
go build -o bin/xgift ./cmd/xgift
go build -o bin/xgift-web ./cmd/xgift-web
go vet ./...
```

按要求未保留测试文件，不运行测试。静态页面嵌入 Go 二进制，无 Node 运行依赖、外部字体或浏览器自动化依赖。

## CLI

```sh
./bin/xgift status
./bin/xgift import-chrome --profile Default
./bin/xgift check
./bin/xgift proxy --port 18791
./bin/xgift username                  # 默认 6 个月，只生成/读取结账链接
./bin/xgift username --months 3       # 3 个月，只生成/读取结账链接
```

`--pay` 是真实付款入口，当前不要运行。网站的暂停配置只管网站，不会禁用手动执行 CLI 的 `--pay`。

- 仅允许 3 个月恰好 300 BDT、6 个月恰好 600 BDT。校验 X 报价和 Stripe 最终总额、币种、商品、数量、一次性模式及商户身份，任何不符都停止。
- 内嵌 sing-box AnyTLS，只监听本机端口，不修改系统代理。代理配置从加密库读取。
- 固定 X API / Stripe 入口，无网页识别或 ChatGPT Chrome 扩展。
- 私有 API 可能变化；目前 Stripe 确认请求仍未跑通，不能保证链接或支付长期可用。
- 按 X 固定用户 ID 保存订单。`creating`、`submitting`、`unknown`、`requires_action` 等不明/待处理状态阻止再次提交。不会为了重试重建订单或改变幂等键。
- 同一收件人已有记录时，不能用另一枚兑换码或另一套餐重复付款。当前没有自动追加时长、自动退款或人工“强制成功”入口。

## 兑换站

用户填写兑换码和用户名，系统先以只读请求核实 X 是否允许赠送。X 不允许时明确提示，兑换码不使用；服务暂停或查询失败也不消耗兑换码。付款开放后的实现会原子锁定兑换码和收件人，使用同一订单校验流程；只有 Stripe 确认成功后才显示完成。付款结果不明时锁定为待核实，不自动重试，进程重启同样不重试。

后台使用 HTTPS Basic Auth，用户名 `admin`，随机密码从权限为 `0600` 的文件读取。支持：

- 每批 1–500 枚兑换码，绑定 3 或 6 个月，可自定义批次名称。
- 兑换码明文只在生成响应中出现一次，可下载 TXT；数据库仅保存 SHA-256 摘要和末 8 位。
- 分页浏览全部兑换码，查看账号和状态，停用未使用的码。
- 同源 POST 校验、请求限流、禁止缓存与页面嵌入；用户接口不返回卡信息、Cookie 或 Stripe 付款链接。

浏览器关闭/刷新不会保存兑换码明文。若生成响应丢失，请先核实批次，必要时停用失去明文的码；没有自动重复生成。

## 本地数据

`sqlite/vault.db` 的敏感记录采用 AES-256-GCM 加密，密钥通过 scrypt 派生；SQLite 结构和记录名称不是密文，不是 SQLCipher 全库加密。Cookie、卡、代理与订单记录分别加密。仅读取 Chrome 的 X `auth_token` / `ct0`。

`sqlite/password-path` 保存密码文件位置，默认密码文件在 `/tmp/xgift-password-*`。请将原密码安全保存，`/tmp` 清理后不能恢复。也可通过 `XGIFT_PASSWORD_FILE` 或 CLI `--password-file` 指定持久路径。目录权限 `0700`，秘密文件 `0600`。

`.private/`、`sqlite/`、数据库、日志、构建产物和环境秘密均不提交 Git。服务器密钥与本地密钥独立。本地 `.private/export/admin-password` 保存本次生成的后台密码，勿提交或分享。

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
| `/etc/xgift/site.env` | 服务配置，`XGIFT_PAYMENTS_ENABLED=false` |
| `/etc/caddy/conf.d/xp.example.com.caddy` | 本站反向代理 |

配置模板在 `deploy/`。程序仅允许绑定回环 IP；Caddy 必须覆盖 `X-Real-IP`，不能透传客户端提供的值。DNS 使用直连 A 记录，Caddy 自动签发 HTTPS 证书。现有站点配置不改动。

```sh
ssh example-server 'sudo systemctl status xgift --no-pager'
ssh example-server 'sudo journalctl -u xgift --since "1 hour ago" --no-pager'
```

更新时先在服务器构建两个二进制，停止 `xgift`，备份数据及密钥，再替换程序并启动。不要覆盖线上 `site.db` 或用旧的本地 vault 覆盖线上订单。备份必须包含 `site.db`（停服或用 SQLite backup API，不能忽略 WAL）、`vault.db` 及独立密钥；所有备份同样限制权限。

**开启付款前**：解决 Stripe HTTP 400、人工核实已有 `unknown` 订单，完成独立安全复查并取得新的实际付款指令。不要仅为试错打开环境开关。银行验证、风控和 X 赠送限制仍可能阻止自动完成。
