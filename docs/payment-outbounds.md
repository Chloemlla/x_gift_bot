# Stripe 付款节点池

X 资格检查、报价和生成链接继续使用 vault 中的 `proxy` 配置。Stripe 账单初始化、核验、卡信息提交、付款确认和结果查询使用独立的 `payment-outbounds`。后台批量、单客户补单与 CLI 共用此配置。

## 配置格式

最外层直接是 JSON **数组**，每个元素是一个完整 sing-box outbound。不要套 `{"outbounds": ...}`，也不要放入 `inbounds`、`route`、`dns` 或订阅地址。

例如 `/etc/xgift/payment-outbounds.json`（以下全部是虚构值）：

```json
[
  { "type": "direct", "tag": "direct" },
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

支持独立的 HTTP、SOCKS、Shadowsocks、VMess、VLESS、Trojan、Hysteria、Hysteria2、TUIC、AnyTLS outbound，以及服务器 `direct` 出口。代理节点需要唯一非空 `tag`、`server` 和有效 `server_port`，协议认证、TLS、Reality 等参数沿用 sing-box 格式。`direct` 只接受 `type` 和 `tag` 两个字段，不使用环境代理。最多 128 个节点、1 MiB。拒绝 selector、urltest、block、dns 和依赖其他节点的 `detour`，避免隐式切换。

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

检查命令会把取得的出口 IP 保存到加密的 `payment-egress:*` 中，用于识别共享出口的节点。部署节点池或更改出口后应执行一次检查；单次公开探测失败本身不会写入冷却，也不会提前解除已有冷却。

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

## 卡池轮换、节点选择和订单绑定

付款卡保存在加密的 `cards` 数组（兼容旧的单张 `card` 记录），Stripe 付款按「卡 × 节点」组合随机轮换：

- 每 3 个连续付款订单使用同一组合（不按客户，按提交顺序）。第 4 个订单重新随机选择卡和可用节点；同一张卡不会连续被选为相邻组合（可用卡足够时）。
- 任一订单被银行拒付后，当前组合立即作废；下一次提交（包括管理员补单）自动换到另一张卡和一个可用节点。被拒的那张卡进入 30 分钟冷却，期间不再参与轮换；其他卡不够用时会自动回退重新启用。普通拒付不再因累计次数暂停全站，也不影响其他组合。
- 只有支付方明确返回 `do_not_try_again` 时才永久封锁对应那张卡；其余卡继续轮换，全部卡都被永久封锁才暂停全站，可用 `xgift cards unblock` 显式解除（同时清除拒绝冷却）。
- 旧单卡时代的已拒订单没有卡指纹记录，首次补单会按旧 `card` 记录给对应卡补上冷却，避免用旧卡反复重试。
- 补单与普通付款共用同一套轮换逻辑；已被拒订单不会重复使用原卡重试，而未提交付款的订单重复尝试会继续沿用已绑定的组合，不重复占用轮换名额。
- 卡池变化（增删卡）会形成新的集合指纹，自动重置支付保护状态和后台补单预览；仅轮换不会改变指纹。CLI 用 `xgift cards list|add|remove|unblock|rotate` 管理。

节点选择与旧版一致：

- 新绑定订单首次进入 Stripe 流程时，从未冷却的池内节点随机选择，`direct` 与代理均可参与。若有不同 `server` 的节点，会避开上一新订单的服务器；如果全部同服务器，则尽量避开上一节点。
- 一次批量补单中的不同客户分别选择。随机选择不保证各节点次数相等；不同协议节点也可能共享同一公网 IP。
- 选择发生在第一个 Stripe 请求前，所以“仅生成链接”中的 Stripe 核验也可能建立节点绑定。
- `stripe-route:<recipient_id>` 保存加密的节点配置快照、节点指纹、卡指纹和时间，正常情况下持续沿用。节点因连接故障进入冷却后，可原子归档到 `stripe-route-history:*` 并切换到可用出口；卡指纹保持不变。后台显示 `direct` 或 `node-` 加指纹前 12 位，完整凭据不返回网页。
- 以前没有节点绑定的待处理订单，在功能启用后首次访问 Stripe 时建立绑定。绑定不代表已付款。
- 明确银行卡拒付不会在同一个组合上重复扣款。金额校验、幂等、付款间隔和支付方禁止重试指令继续生效。

## 网络重试与 6 小时冷却

- Stripe 连接失败、TLS/连接超时或成功响应中断，会把当前节点冷却 6 小时。冷却按节点、服务器地址，以及最近探测到的公网 IP 共同排除；共享出口 IP 的节点一起退出可选池。`direct` 也适用。取消操作和 API 重定向拒绝不触发冷却。
- 冷却记录保存在加密的 `payment-node-cooldown:*`，服务重启不会清除；到期自动恢复候选资格。所有节点均冷却时停止，不偷偷走未配置出口。
- **GET 查询及 `payment_pages/<session>/init` 初始化核验**可换节点重试，每个请求最多 3 次（含首次）。这不包含生成新账单的 X mutation。
- **卡信息提交、付款确认等写请求不自动重放**。即使网络出错并记录冷却，也立即返回失败/待核实状态；之后只能查询原结果，不会在别的出口重新扣款。
- `card_declined`、`generic_decline` 等银行卡拒付，以及 HTTP 403/429/5xx 等已收到的 API 错误，不被推断为 IP 故障，不触发本机制的自动轮换。验证码或银行验证也不会被绕过。
- 后台显示总出口数、可用数和冷却数。失败节点不会被本次重试再次选中。

## 更新、停用与故障

修改数组并重新 `put` 后，后续未绑定订单立即使用新配置，无需重启。已有订单正常保留原节点快照，即使节点被移出数组也不会仅因此换出口；确有连接故障并进入冷却时，安全查询才使用新池中的可用节点。不要直接删除绑定或冷却记录来重试付款。

导入 `[]` 可让后续未绑定订单使用服务器直连；已有池节点绑定继续保留。无此配置时行为相同。无效 JSON、损坏的绑定和节点启动配置错误会报错。已有绑定进入冷却且池为空时不会绕过冷却直连。要让直连参与选择、冷却和重试，必须显式加入 `{"type":"direct","tag":"direct"}`。付款网络不读取 `HTTP_PROXY` / `HTTPS_PROXY`。

升级不会自动解除以前保存的全站暂停，也不会修改公开充值开关。管理员可在核实后解除普通旧暂停；支付方明确要求停止重试的暂停不能通过恢复命令解除。

节点池是出口配置，不保证银行接受交易或订单成功。停止付款仍使用现有付款暂停开关和后台停止按钮。手动在浏览器打开付款链接时，使用浏览器自身的网络，不使用服务器节点池。
