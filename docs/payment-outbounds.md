# Stripe 付款节点池

X 资格检查、报价和生成链接继续使用 vault 中的 `proxy` 配置。Stripe 账单初始化、核验、卡信息提交、付款确认和结果查询使用独立的 `payment-outbounds`。后台批量、单客户补单与 CLI 共用此配置。

## 配置格式

最外层直接是 JSON **数组**，每个元素是一个完整 sing-box outbound。不要套 `{"outbounds": ...}`，也不要放入 `inbounds`、`route`、`dns` 或订阅地址。

例如 `/etc/xgift/payment-outbounds.json`（以下全部是虚构值）：

```json
[
  {
    "type": "http",
    "tag": "payment-a",
    "server": "proxy-a.example.com",
    "server_port": 443,
    "username": "example-user",
    "password": "replace-me",
    "tls": { "enabled": true, "server_name": "proxy-a.example.com" }
  },
  {
    "type": "socks",
    "tag": "payment-b",
    "server": "proxy-b.example.com",
    "server_port": 1080,
    "version": "5",
    "username": "example-user",
    "password": "replace-me"
  }
]
```

支持独立的 HTTP、SOCKS、Shadowsocks、VMess、VLESS、Trojan、Hysteria、Hysteria2、TUIC、AnyTLS outbound。每个节点需要唯一非空 `tag`、`server` 和有效 `server_port`，协议认证、TLS、Reality 等参数沿用 sing-box 格式。最多 128 个节点、1 MiB。拒绝 selector、urltest、direct、block、dns 和依赖其他节点的 `detour`，避免引入隐式切换。

VLESS Reality/uTLS 和 Hysteria2 要使用以下构建命令（前端先执行 `npm run build`）：

```sh
go build -tags with_quic,with_utls -o bin/xgift ./cmd/xgift
go build -tags with_quic,with_utls -o bin/xgift-web ./cmd/xgift-web
```

## 导入与检查

配置文件含节点凭据，请设为 `0600`，不要提交到 Git。导入后以加密 vault 为运行时配置源；仅编辑文件不会自动生效。

```sh
sudo chmod 600 /etc/xgift/payment-outbounds.json
sudo sh -c '/opt/xgift/bin/xgift put --name payment-outbounds \
  --db /var/lib/xgift/vault.db --password-file /etc/xgift/vault-password \
  < /etc/xgift/payment-outbounds.json'

sudo /opt/xgift/bin/xgift check-payment-outbounds \
  --db /var/lib/xgift/vault.db --password-file /etc/xgift/vault-password
```

检查命令逐个节点访问 Stripe 公开根路径和公网 IP 查询服务，每个节点最多 20 秒；不读取卡信息，不生成账单，不分配客户节点，也不付款。Stripe 根路径的正常探测结果是 HTTP 404。输出 JSON 行包含节点编号、协议、连通性和可取得的出口 IP。任一节点连接失败时命令以非零状态退出；此命令不会自动修改节点池。公网 IP 查询失败不等同于 Stripe 不通。

从已下载的 sing-box 订阅提取数组：

```sh
umask 077
python3 - <<'PY'
import json
from pathlib import Path
source = json.loads(Path('subscription.json').read_text())
nodes = source if isinstance(source, list) else source['outbounds']
Path('payment-outbounds.json').write_text(json.dumps(nodes, ensure_ascii=False, indent=2) + '\n')
Path('payment-outbounds.json').chmod(0o600)
PY
```

包含 selector、urltest 等分组的订阅须先移除这些元素，仅保留独立代理节点。运行程序不自动拉取订阅，避免未经核验的订阅更新改变付款路径。

## 节点选择和订单绑定

- 新绑定订单首次进入 Stripe 流程时，从池内节点随机选择。若有不同 `server` 的节点，会避开上一新订单的服务器；如果全部同服务器，则尽量避开上一节点。
- 一次批量补单中的不同客户分别选择。随机选择不保证各节点次数相等；不同协议节点也可能共享同一公网 IP。
- 选择发生在第一个 Stripe 请求前，所以“仅生成链接”中的 Stripe 核验也可能建立节点绑定。
- `stripe-route:<recipient_id>` 保存加密的节点配置快照、节点指纹和时间。同一客户当前绑定订单的重试、失效链接重建、结果查询及服务重启均沿用它。后台显示 `node-` 加指纹前 12 位，完整凭据不返回网页。
- 以前没有节点绑定的待处理订单，在功能启用后首次访问 Stripe 时建立绑定。绑定不代表已付款。
- 连接超时、拒付和未知结果均不会触发换节点再付。已有付款暂停、禁止重试、金额校验、幂等和间隔限制继续生效。

## 更新、停用与故障

修改数组并重新 `put` 后，后续未绑定订单立即使用新配置，无需重启。已有订单保留原节点快照，即使节点被移出数组也不会静默换出口。不要直接删除其绑定记录来重试付款。

导入 `[]` 可让后续未绑定订单使用服务器直连；已有池节点绑定继续保留。无此配置时行为相同。无效 JSON、损坏的绑定、节点启动或连接失败会报错，不会回退到另一个节点。付款网络不读取 `HTTP_PROXY` / `HTTPS_PROXY`。

节点池是出口配置，不保证银行接受交易或订单成功。停止付款仍使用现有付款暂停开关和后台停止按钮。手动在浏览器打开付款链接时，使用浏览器自身的网络，不使用服务器节点池。
