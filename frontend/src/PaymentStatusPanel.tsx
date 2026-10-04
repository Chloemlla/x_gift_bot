import {
  Alert,
  Box,
  Chip,
  Paper,
  Skeleton,
  Stack,
  Typography,
} from "@mui/material";
import CreditCardRounded from "@mui/icons-material/CreditCardRounded";
import DnsRounded from "@mui/icons-material/DnsRounded";

export type Network = {
  mode: "pool" | "direct";
  nodes: number;
  available?: number;
  cooling?: number;
};
export type CardStatus = {
  last4: string;
  usable: boolean;
  problem?: string;
  blocked?: string;
  cooling_seconds?: number;
  pair_cooling?: number;
};
export type Rotation = {
  batch_size: number;
  used: number;
  card_last4?: string;
  node?: string;
};

// 后端返回的英文校验/封锁原因译成中文;未知值原样显示,便于对照服务端日志。
const reasons: Record<string, string> = {
  "card, cardholder name or email is incomplete": "卡信息不完整",
  "card number checksum is invalid": "卡号校验失败",
  "card has expired": "卡片已过期",
  "Stripe requires a billing address; supply the card billing country and applicable address fields":
    "缺少账单地址",
  "invalid supplied billing country": "账单国家无效",
  do_not_try_again: "银行要求停止重试",
  declined_multi_node: "多个节点拒付",
};
function reasonText(raw: string) {
  return reasons[raw] || raw;
}

// 订单行与轮换组合里的节点标识:后端原文 direct 显示为直连。
export function nodeName(node?: string) {
  return node === "direct" ? "直连" : node;
}

function Stat({ label, value }: { label: string; value: string }) {
  return (
    <Box
      sx={{ border: 1, borderColor: "divider", borderRadius: 2, p: 2, minWidth: 0 }}
    >
      <Typography variant="caption" color="text.secondary" component="p">
        {label}
      </Typography>
      <Typography variant="h3" component="p" sx={{ mt: 0.25 }}>
        {value}
      </Typography>
    </Box>
  );
}

function Header({
  icon,
  id,
  title,
}: {
  icon: React.ReactNode;
  id: string;
  title: string;
}) {
  return (
    <Stack direction="row" spacing={1} alignItems="center" sx={{ mb: 2 }}>
      {icon}
      <Typography id={id} variant="h2" sx={{ fontSize: 21 }}>
        {title}
      </Typography>
    </Stack>
  );
}

function NetworkCard({
  network,
  failed,
}: {
  network: Network | null;
  failed: boolean;
}) {
  const available = network ? (network.available ?? network.nodes) : 0;
  return (
    <Paper
      component="section"
      aria-labelledby="payment-nodes-title"
      variant="outlined"
      sx={{ p: { xs: 2, sm: 3 } }}
    >
      <Header
        icon={<DnsRounded color="primary" aria-hidden="true" />}
        id="payment-nodes-title"
        title="付款节点"
      />
      {!network && !failed && (
        <Stack
          direction="row"
          spacing={1.5}
          role="status"
          aria-label="正在读取付款节点状态"
        >
          {[0, 1, 2].map((index) => (
            <Skeleton
              key={index}
              variant="rounded"
              height={78}
              sx={{ flex: 1 }}
            />
          ))}
        </Stack>
      )}
      {!network && failed && (
        <Typography variant="body2" color="text.secondary">
          状态暂不可用，正在自动重试。
        </Typography>
      )}
      {network?.mode === "direct" && (
        <Typography variant="body2" color="text.secondary">
          新订单的 Stripe 请求走服务器直连；已有节点绑定的订单保留原节点。
        </Typography>
      )}
      {network?.mode === "pool" && (
        <>
          <Box
            sx={{
              display: "grid",
              gridTemplateColumns: "repeat(3, 1fr)",
              gap: 1.5,
            }}
          >
            <Stat label="节点总数" value={String(network.nodes)} />
            <Stat label="可用" value={String(available)} />
            <Stat label="冷却中" value={String(network.cooling ?? 0)} />
          </Box>
          {available === 0 && (
            <Alert severity="warning" sx={{ mt: 2 }}>
              节点池全部在冷却中，新订单暂时无法付款。
            </Alert>
          )}
          <Typography
            variant="caption"
            color="text.secondary"
            component="p"
            sx={{ mt: 2 }}
          >
            节点连接故障后冷却 6 小时；安全查询每个请求最多尝试 3
            次；付款提交失败不自动重扣。
          </Typography>
        </>
      )}
      {network && (
        <Typography
          variant="caption"
          color="text.secondary"
          component="p"
          sx={{ mt: 1 }}
        >
          X 生成链接走配置代理；手动打开付款链接时，使用当前浏览器的网络。
        </Typography>
      )}
    </Paper>
  );
}

function CardsCard({
  cards,
  rotation,
  paths,
  failed,
}: {
  cards: CardStatus[];
  rotation: Rotation | null;
  paths?: number;
  failed: boolean;
}) {
  const loading = !rotation && !failed;
  // 卡本身不可用、整卡封锁/冷却,或组合冷却已覆盖全部付款路径,都视为无法付款。
  const noneReady =
    cards.length > 0 &&
    cards.every(
      (item) =>
        !item.usable ||
        !!item.blocked ||
        (item.cooling_seconds || 0) > 0 ||
        (paths !== undefined && paths > 0 && (item.pair_cooling || 0) >= paths),
    );
  return (
    <Paper
      component="section"
      aria-labelledby="payment-cards-title"
      variant="outlined"
      sx={{ p: { xs: 2, sm: 3 } }}
    >
      <Header
        icon={<CreditCardRounded color="primary" aria-hidden="true" />}
        id="payment-cards-title"
        title="付款卡"
      />
      {loading && (
        <Stack spacing={1.5} role="status" aria-label="正在读取付款卡状态">
          <Skeleton variant="text" width="60%" />
          <Stack direction="row" spacing={1}>
            {[88, 120, 104].map((width) => (
              <Skeleton
                key={width}
                variant="rounded"
                width={width}
                height={32}
                sx={{ borderRadius: 4 }}
              />
            ))}
          </Stack>
        </Stack>
      )}
      {!loading && !rotation && (
        <Typography variant="body2" color="text.secondary">
          状态暂不可用，正在自动重试。
        </Typography>
      )}
      {rotation && cards.length === 0 && (
        <Typography variant="body2" color="text.secondary">
          尚未配置付款卡；付款卡补单不可用，仍可「仅生成补单链接」由客户自行付款。
        </Typography>
      )}
      {rotation && cards.length > 0 && (
        <>
          <Typography variant="body2" color="text.secondary">
            共 {cards.length} 张
            {rotation.card_last4
              ? rotation.used >= rotation.batch_size
                ? ` · 当前组合：尾号 ${rotation.card_last4} × ${nodeName(rotation.node)}（本组 ${rotation.used}/${rotation.batch_size} 笔已用满，下一笔自动更换）`
                : ` · 当前组合：尾号 ${rotation.card_last4} × ${nodeName(rotation.node)}（本组已用 ${rotation.used}/${rotation.batch_size} 笔）`
              : " · 尚未开始轮换，首笔付款时随机选择组合"}
          </Typography>
          {noneReady && (
            <Alert severity="warning" sx={{ mt: 1.5 }}>
              所有付款卡都在冷却或不可用，新订单暂时无法付款。
            </Alert>
          )}
          <Stack
            direction="row"
            spacing={1}
            useFlexGap
            sx={{ mt: 1.5, flexWrap: "wrap", alignItems: "center" }}
          >
            {cards.map((item) => {
              const cooling = item.cooling_seconds || 0;
              const blocked = !!item.blocked && cooling === 0;
              const label = !item.usable
                ? `尾号 ${item.last4} 不可用${item.problem ? `（${reasonText(item.problem)}）` : ""}`
                : blocked
                  ? `尾号 ${item.last4} 已封锁（${reasonText(item.blocked!)}）`
                  : cooling > 0
                    ? `尾号 ${item.last4} 冷却 ${Math.ceil(cooling / 60)} 分钟`
                    : `尾号 ${item.last4} 可用`;
              const extra = item.pair_cooling
                ? ` · ${item.pair_cooling} 组组合冷却`
                : "";
              return (
                <Chip
                  key={item.last4}
                  size="small"
                  variant="outlined"
                  color={
                    !item.usable || blocked
                      ? "error"
                      : cooling > 0
                        ? "warning"
                        : "success"
                  }
                  label={`${label}${extra}`}
                />
              );
            })}
          </Stack>
          <Typography
            variant="caption"
            color="text.secondary"
            component="p"
            sx={{ mt: 1.5 }}
          >
            「N 组组合冷却」指该卡与部分节点的组合暂不可用；同一卡在多个节点被拒后才会整卡冷却。
          </Typography>
          <Typography
            variant="caption"
            color="text.secondary"
            component="p"
            sx={{ mt: 0.5 }}
          >
            {`每 ${rotation.batch_size} 笔付款共用同一卡与节点的组合，用满或被拒后自动更换。`}
          </Typography>
        </>
      )}
    </Paper>
  );
}

// 补单依赖的付款环境状态：节点与付款卡各一张卡,数据由 RecoveryPanel 轮询共享。
export function PaymentStatusPanels({
  network,
  cards,
  rotation,
  failed,
}: {
  network: Network | null;
  cards: CardStatus[];
  rotation: Rotation | null;
  failed: boolean;
}) {
  // 付款路径数:节点池按可用出口计,直连只有一条。
  const paths = network
    ? network.mode === "pool"
      ? (network.available ?? network.nodes)
      : 1
    : undefined;
  return (
    <Box
      sx={{
        display: "grid",
        gridTemplateColumns: {
          xs: "1fr",
          md: "minmax(0, 1fr) minmax(0, 1fr)",
        },
        gap: 3,
        mb: 3,
        alignItems: "stretch",
      }}
    >
      <NetworkCard network={network} failed={failed} />
      <CardsCard cards={cards} rotation={rotation} paths={paths} failed={failed} />
    </Box>
  );
}
