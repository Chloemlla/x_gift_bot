# xpg —— 独立生成链接站点（xpg.kfcv50.today）

xpg 是一个独立于 xgift 主站（xp.kfcv50.today）的**纯链接生成**服务：输入接收方 X 用户名，
先实时检测接收账号的赠送资格，检测通过后才创建 Stripe 支付链接，把链接发给对方由 TA 自行付款。
它复用主项目的 checkout 引擎，但**从不代付、不存卡、不消耗兑换码**。

- 独立加密 vault（`/var/lib/xpg/vault.db`），凭据与主站完全隔离
- 使用独立的 X 账号与代理出口生成 BD 区域报价（anytls 节点）
- 访问密钥保护 API，防止任意访客滥用赠送额度

## 线上部署（服务器 nz.mizore.blog）

| 内容 | 路径 |
|---|---|
| 二进制 | `/opt/xpg/bin/xpg` |
| 加密 vault | `/var/lib/xpg/vault.db`（0700，属主 xgift） |
| 密码/密钥文件 | `/etc/xpg/vault-password`、`/etc/xpg/passcode`（0600） |
| 环境配置 | `/etc/xpg/xpg.env` |
| systemd | `/etc/systemd/system/xpg.service`，监听 `127.0.0.1:8788` |
| HTTPS | Caddy `/etc/caddy/conf.d/xpg.kfcv50.today.caddy` → 8788 |
| DNS | Cloudflare 橙云 A 记录 `xpg` → `208.84.103.254` |

`deploy/xpg.env.example` 列出全部环境变量；`deploy/xpg.service` 是 unit 文件模板。

## 常用运维命令

```sh
# 状态与连通性（在服务器上）
sudo -u xgift /opt/xgift/bin/xgift status --db /var/lib/xpg/vault.db --password-file /etc/xpg/vault-password
sudo -u xgift /opt/xgift/bin/xgift check  --db /var/lib/xpg/vault.db --password-file /etc/xpg/vault-password

# 日志 / 重启
journalctl -u xpg -f
sudo systemctl restart xpg

# 更新二进制（在构建目录）
go build -tags with_quic,with_utls -o /opt/xpg/bin/xpg ./cmd/xpg && systemctl restart xpg
```

## 刷新 X 凭据

账号切换器里的非当前账号，token 存于 Chrome 的 `auth_multi` 加密 cookie，
用本仓库任意含 `chrome` 包的二进制提取后重新签名 ct0，再写入服务器 vault：

```sh
# 1) 解出 auth_multi 里目标账号的 auth_token（与 internal/chrome 相同的 Keychain 解密）
# 2) 只带 auth_token 访问 x.com/home，响应会为该会话签发新的 ct0
# 3) 写入 vault
{ "cookies": {"cookies": [{"name":"auth_token","value":"<token>","domain":".x.com"},
                            {"name":"ct0","value":"<新ct0>","domain":".x.com"}],"origins":[]} } |
  ssh root@nz.mizore.blog sudo -u xgift /opt/xgift/bin/xgift put --name cookies \
  --db /var/lib/xpg/vault.db --password-file /etc/xpg/vault-password
```

## 本地调试

```sh
./bin/xpg outbound '<anytls 链接>'                 # 解析并探测节点（出口国家）
./bin/xpg show-account --profile 'Profile 1'      # 识别 Chrome profile 的 X 账号
./bin/xpg export --name stripe-key --db <vault> --password-file <pw>
go test ./internal/xpg/
```

服务 API（需要 `X-XPG-Key: <passcode>`）：

| 端点 | 作用 |
|---|---|
| `GET /api/plans` | 套餐与价格（BDT） |
| `POST /api/check` | 检测接收账号资格（只读） |
| `POST /api/link` | 生成链接（NDJSON 流式进度），返回 Stripe URL |
| `GET /healthz` | 健康检查 |

## 设计与安全边界

- 「先检测再生成」：`/api/check` 与 `/api/link` 都会实时核对接收账号；
  不可接收（已订阅/收过近期赠品/受限）或不存在时不会创建任何链接。
- 链接创建走主项目 `PublicLinkForUsername`：Stripe 页面金额/商户/商品逐项比对，
  过期未付款的链接可安全重建并归档证据；创建间隔与付款窗口按主项目限速。
- 无卡、无支付提交路径（`pay=false`），因此不需要 cards 配置；
  vault 校验用 `xgift status` 时会提示“无可用卡”，属预期。