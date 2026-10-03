import { useEffect, useRef, useState } from "react";
import {
  Accordion,
  AccordionDetails,
  AccordionSummary,
  Alert,
  Box,
  Button,
  Checkbox,
  Chip,
  Dialog,
  DialogActions,
  DialogContent,
  DialogTitle,
  FormControlLabel,
  Paper,
  Stack,
  Table,
  TableBody,
  TableCell,
  TableContainer,
  TableHead,
  TableRow,
  Typography,
} from "@mui/material";
import ExpandMoreRounded from "@mui/icons-material/ExpandMoreRounded";
import { adminApi } from "./adminApi";
import type { RecoverySelection } from "./CustomerPanel";

type Item = {
  payment_node?: string;
  hint: string;
  checkout_url?: string;
  needs_unpaid_verification?: boolean;
  id: string;
  username: string;
  months: number;
  amount: number;
  currency: string;
  state: string;
  detail: string;
};
type Batch = {
  mode: string;
  id: string;
  state: string;
  created: number;
  updated?: number;
  last4: string;
  paused: boolean;
  message: string;
  items: Item[];
};
type Network = { mode: "pool" | "direct"; nodes: number; available?: number; cooling?: number };
type QueueSummary = { review: number; processing: number };
type Response = { batch: Batch | null; network?: Network; summary?: QueueSummary };
const labels: Record<string, string> = {
  preview: "待确认",
  pending: "待处理",
  running: "处理中",
  stopping: "正在停止",
  stopped: "已停止",
  completed: "批次已结束",
  interrupted: "服务重启后暂停",
  succeeded: "已确认成功",
  declined: "付款被拒",
  requires_action: "需银行验证",
  skipped: "已跳过",
  blocked: "待核实",
  link_ready: "链接已就绪 · 未付款",
  needs_verification: "需核对原扣款",
};
function resultSummary(items: Item[]) {
  const count = (states: string[]) => items.filter((i) => states.includes(i.state)).length;
  const parts: [string, number][] = [
    ["成功", count(["succeeded"])],
    ["链接就绪", count(["link_ready"])],
    ["跳过", count(["skipped"])],
    ["待核实", count(["declined", "requires_action", "blocked", "needs_verification"])],
    ["未处理", count(["pending"])],
    ["处理中", count(["running"])],
  ];
  return [`共 ${items.length} 笔`, ...parts.filter(([, n]) => n > 0).map(([label, n]) => `${label} ${n} 笔`)].join(" · ");
}
function taskTime(seconds?: number) {
  return seconds ? new Date(seconds * 1000).toLocaleString("zh-CN", { hour12: false }) : "时间未记录";
}
function BatchDetails({ batch }: { batch: Batch }) {
  return <>
    <Stack direction="row" flexWrap="wrap" gap={1} alignItems="center">
      <Chip size="small" label={labels[batch.state] || batch.state} />
      <Typography variant="body2">{batch.mode === "links" ? "仅生成链接" : `付款卡尾号 ${batch.last4}`} · {resultSummary(batch.items)}</Typography>
    </Stack>
    <Typography variant="caption" color="text.secondary" display="block" sx={{ mt: 1 }}>
      创建于 {taskTime(batch.created)} · 更新于 {taskTime(batch.updated || batch.created)}
    </Typography>
    <Typography variant="body2" color="text.secondary" sx={{ my: 1 }}>{batch.message}</Typography>
    {batch.items.length > 0 && <Orders items={batch.items} />}
  </>;
}
function totals(items: Item[]) {
  const values: Record<string, number> = {};
  for (const item of items)
    if (item.state === "pending")
      values[item.currency] = (values[item.currency] || 0) + item.amount;
  return (
    Object.entries(values)
      .map(([currency, amount]) => `${currency} ${(amount / 100).toFixed(2)}`)
      .join(" + ") || "0.00"
  );
}
function Orders({ items }: { items: Item[] }) {
  return (
    <>
      <Stack spacing={1} sx={{ display: { xs: "flex", sm: "none" }, mb: 1 }}>
        {items.map((item) => (
          <Box
            key={item.id}
            sx={{ p: 1.5, border: 1, borderColor: "divider", borderRadius: 1 }}
          >
            <Typography
              variant="body2"
              fontWeight={600}
              sx={{ overflowWrap: "anywhere" }}
            >
              @{item.username} · 卡密尾号 {item.hint}
            </Typography>
            {item.payment_node && (
              <Typography variant="caption">付款节点 {item.payment_node}</Typography>
            )}
            <Typography variant="body2">
              {item.months} 个月 · {item.currency}{" "}
              {(item.amount / 100).toFixed(2)}
            </Typography>
            <Typography variant="body2" fontWeight={600} sx={{ mt: 1 }}>
              {labels[item.state] || item.state}
            </Typography>
            <Typography
              variant="caption"
              color="text.secondary"
              sx={{ overflowWrap: "anywhere" }}
            >
              {item.detail}
            </Typography>
            {item.state === "link_ready" && item.checkout_url && (
              <Button
                component="a"
                href={item.checkout_url}
                target="_blank"
                rel="noopener noreferrer"
                size="small"
              >
                打开付款链接
              </Button>
            )}
          </Box>
        ))}
      </Stack>
      <TableContainer
        sx={{ maxHeight: 380, display: { xs: "none", sm: "block" } }}
      >
        <Table size="small" stickyHeader aria-label="补单订单清单">
          <TableHead>
            <TableRow>
              <TableCell>客户 / 套餐</TableCell>
              <TableCell>账单金额</TableCell>
              <TableCell>处理结果</TableCell>
            </TableRow>
          </TableHead>
          <TableBody>
            {items.map((item) => (
              <TableRow key={item.id}>
                <TableCell sx={{ verticalAlign: "top", minWidth: 140 }}>
                  <Typography variant="body2">@{item.username}</Typography>
                  <Typography variant="caption" display="block">
                    卡密尾号 {item.hint}
                    {item.payment_node && ` · 付款节点 ${item.payment_node}`}
                  </Typography>
                  <Typography variant="caption" color="text.secondary">
                    {item.months} 个月
                  </Typography>
                </TableCell>
                <TableCell sx={{ whiteSpace: "nowrap", verticalAlign: "top" }}>
                  {item.currency} {(item.amount / 100).toFixed(2)}
                </TableCell>
                <TableCell sx={{ minWidth: 180 }}>
                  <Typography variant="body2" fontWeight={600}>
                    {labels[item.state] || item.state}
                  </Typography>
                  <Typography
                    variant="caption"
                    color="text.secondary"
                    sx={{ overflowWrap: "anywhere" }}
                  >
                    {item.detail}
                  </Typography>
                  {item.state === "link_ready" && item.checkout_url && (
                    <Button
                      component="a"
                      href={item.checkout_url}
                      target="_blank"
                      rel="noopener noreferrer"
                      size="small"
                    >
                      打开付款链接
                    </Button>
                  )}
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </TableContainer>
    </>
  );
}
export function RecoveryPanel({
  selection,
}: {
  selection?: RecoverySelection | null;
}) {
  const [batch, setBatch] = useState<Batch | null>(null);
  const [network, setNetwork] = useState<Network | null>(null);
  const [summary, setSummary] = useState<QueueSummary | null>(null);
  const [statusError, setStatusError] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [open, setOpen] = useState(false);
  const [consent, setConsent] = useState(false);
  const [reset, setReset] = useState(false);
  const [verifiedUnpaid, setVerifiedUnpaid] = useState(false);
  const linksOnly = batch?.mode === "links";
  const mutating = useRef(false);
  const sequence = useRef(0);
  const active = batch?.state === "running" || batch?.state === "stopping";
  const pending =
    batch?.items.filter((item) => item.state === "pending").length || 0;
  useEffect(() => {
    let disposed = false;
    let timer: ReturnType<typeof setTimeout>;
    async function poll() {
      const seq = sequence.current;
      try {
        const data = await adminApi<Response>("/api/admin/recovery");
        if (!disposed && !mutating.current && seq === sequence.current) {
          setBatch(data.batch);
          setNetwork(data.network || null);
          setSummary(data.summary || null);
          setStatusError("");
        }
      } catch (e) {
        if (!disposed && !mutating.current && seq === sequence.current) {
          setStatusError((e as Error).message);
          setSummary(null);
        }
      } finally {
        if (!disposed) timer = setTimeout(poll, 5000);
      }
    }
    void poll();
    return () => {
      disposed = true;
      clearTimeout(timer);
    };
  }, []);
  useEffect(() => {
    if (selection) void act("preview", selection);
  }, [selection]);
  async function act(
    action: "preview" | "start" | "stop",
    target?: { id?: string; mode: "pay" | "links" },
  ) {
    if (mutating.current) return;
    mutating.current = true;
    sequence.current++;
    setBusy(true);
    setError("");
    try {
      const data = await adminApi<Response>(
        `/api/admin/recovery/${action}`,
        action === "preview"
          ? { id: target?.id || "", mode: target?.mode || "pay" }
          : action === "start"
            ? {
                id: batch?.id,
                confirm: consent,
                reset_pause: reset,
                verified_unpaid: verifiedUnpaid,
              }
            : { id: batch?.id },
      );
      setBatch(data.batch);
      if (action === "preview") {
        setConsent(false);
        setReset(false);
        setVerifiedUnpaid(false);
        setOpen(true);
      }
      if (action === "start") setOpen(false);
    } catch (e) {
      setError((e as Error).message);
    } finally {
      mutating.current = false;
      sequence.current++;
      setBusy(false);
    }
  }
  return (
    <Paper variant="outlined" sx={{ p: { xs: 2, sm: 3 }, mb: 3 }}>
      <Stack
        direction={{ xs: "column", sm: "row" }}
        justifyContent="space-between"
        spacing={2}
      >
        <Box>
          <Typography variant="h2" sx={{ fontSize: 21 }}>
            管理员手动补单
          </Typography>
          <Typography color="text.secondary" variant="body2" sx={{ mt: 1 }}>
            支持只生成补单链接，或确认后使用已保存的银行卡付款。服务器逐笔处理，间隔至少
            30 秒。
          </Typography>
          <Typography color="text.secondary" variant="body2" sx={{ mt: 1 }}>
            X 生成链接走配置代理。
            {network?.mode === "pool"
              ? `Stripe 节点池：${network.nodes} 个出口，可用 ${network.available ?? network.nodes} 个，冷却 ${network.cooling ?? 0} 个。连接故障冷却 6 小时，安全查询最多尝试 3 个出口；付款提交失败不自动重扣。`
              : network?.mode === "direct"
                ? "新订单的 Stripe 请求走服务器直连；已有节点绑定的订单保留原节点。"
                : "正在读取 Stripe 付款网络配置。"}
            手动打开付款链接时，使用当前浏览器的网络。
          </Typography>
        </Box>
        <Stack
          direction="row"
          spacing={1}
          alignItems="center"
          sx={{ flexWrap: "wrap", rowGap: 1 }}
        >
          <Button
            variant="outlined"
            disabled={busy || active}
            onClick={() => void act("preview", { mode: "links" })}
          >
            仅生成链接
          </Button>
          <Button
            variant="contained"
            disabled={busy || active}
            onClick={() => void act("preview")}
          >
            预览并补单
          </Button>
          {active && (
            <Button
              variant="outlined"
              color="error"
              disabled={busy || batch?.state === "stopping"}
              onClick={() => void act("stop")}
            >
              停止后续订单
            </Button>
          )}
        </Stack>
      </Stack>
      {error && !open && (
        <Alert severity="error" sx={{ mt: 2 }}>
          {error}
        </Alert>
      )}
      {statusError && <Alert severity="error" sx={{ mt: 2 }}>当前状态读取失败：{statusError}</Alert>}
      <Box sx={{ mt: 2 }} aria-live="polite">
        <Typography variant="body2" fontWeight={600}>
          {statusError ? "当前状态暂不可用" : active ? "当前有补单任务运行中" : summary ? "当前没有运行中的补单任务" : "正在读取当前状态…"}
        </Typography>
        {summary && <Typography variant="body2" color="text.secondary" sx={{ mt: 0.5 }}>
          当前订单：待核实 {summary.review} 笔 · 充值处理中 {summary.processing} 笔
        </Typography>}
      </Box>
      {batch && active && (
        <Box sx={{ mt: 2 }}>
          <BatchDetails batch={batch} />
        </Box>
      )}
      {batch && !active && (
        <Accordion key={`${batch.id}-${batch.state}`} disableGutters elevation={0} sx={{ mt: 2, border: 1, borderColor: "divider", "&::before": { display: "none" } }}>
          <AccordionSummary expandIcon={<ExpandMoreRounded />} aria-controls="recovery-history-content" id="recovery-history-heading">
            <Box>
              <Typography variant="body2" fontWeight={600}>
                {batch.state === "preview" ? "上次预览（未启动）" : "上次补单记录（历史）"}
              </Typography>
              <Typography variant="caption" color="text.secondary">
                {taskTime(batch.updated || batch.created)} · {resultSummary(batch.items)}
              </Typography>
            </Box>
          </AccordionSummary>
          <AccordionDetails>
            <Typography variant="body2" color="text.secondary" sx={{ mb: 1.5 }}>
              以下为这次任务保存的处理结果。当前订单数量显示在上方；刷新页面不会重新执行此任务。
            </Typography>
            <BatchDetails batch={batch} />
          </AccordionDetails>
        </Accordion>
      )}
      <Dialog
        open={open}
        onClose={() => {
          if (!busy) setOpen(false);
        }}
        fullWidth
        maxWidth="md"
        slotProps={{
          paper: {
            sx: {
              m: { xs: 2, sm: 4 },
              width: { xs: "calc(100% - 32px)", sm: "calc(100% - 64px)" },
            },
          },
        }}
        aria-labelledby="recovery-title"
      >
        <DialogTitle id="recovery-title">
          {linksOnly ? "确认生成补单链接（不付款）" : "确认手动补单"}
        </DialogTitle>
        <DialogContent>
          {error && (
            <Alert severity="error" sx={{ mb: 2 }}>
              {error}
            </Alert>
          )}
          <Typography sx={{ mb: 1 }}>
            {linksOnly ? (
              "只生成或更新付款链接，不提交银行卡付款；核验并处理"
            ) : (
              <>
                使用银行卡尾号 <strong>{batch?.last4}</strong>，核验并处理
              </>
            )}{" "}
            <strong>{pending}</strong> 笔订单，账单总额{" "}
            <strong>{totals(batch?.items || [])}</strong>。
          </Typography>
          <Alert severity="warning" sx={{ mb: 2 }}>
            {linksOnly
              ? "这一步只准备付款链接，不付款。有效链接会复用；旧链接失效时，核验并保留旧记录后生成新链接。"
              : "确认后会尝试真实付款。旧链接失效时可先生成新链接；已付款只同步结果，不符合条件的订单跳过。普通拒付只标记该单失败；结果不明、需银行验证或支付方明确禁止重试时停止任务。"}
          </Alert>
          {batch && <Orders items={batch.items} />}
          {batch?.paused && !linksOnly && (
            <FormControlLabel
              control={
                <Checkbox
                  checked={reset}
                  disabled={busy}
                  onChange={(e) => setReset(e.target.checked)}
                />
              }
              label="我已检查付款方式，确认解除本轮暂停并重新尝试；拒付保护继续生效。"
            />
          )}
          {batch?.items.some((item) => item.needs_unpaid_verification) && (
            <FormControlLabel
              control={
                <Checkbox
                  checked={verifiedUnpaid}
                  disabled={busy}
                  onChange={(e) => setVerifiedUnpaid(e.target.checked)}
                />
              }
              label="我已核对原订单未扣款；若旧链接失效，允许保留旧记录并生成新链接。未勾选则不会替换无法在线确认的账单。"
            />
          )}
          <FormControlLabel
            control={
              <Checkbox
                checked={consent}
                disabled={busy}
                onChange={(e) => setConsent(e.target.checked)}
              />
            }
            label={
              linksOnly
                ? "我已核对客户和套餐，确认仅生成链接，不付款。"
                : "我已核对客户、套餐、账单金额和银行卡，确认启动本批次付款。"
            }
          />
        </DialogContent>
        <DialogActions>
          <Button disabled={busy} onClick={() => setOpen(false)}>
            取消
          </Button>
          <Button
            variant="contained"
            disabled={
              busy ||
              !consent ||
              !pending ||
              (!linksOnly && batch?.paused && !reset) ||
              batch?.state !== "preview"
            }
            onClick={() => void act("start")}
          >
            {busy
              ? "正在提交…"
              : linksOnly
                ? "确认生成链接（不付款）"
                : "确认付款并启动补单"}
          </Button>
        </DialogActions>
      </Dialog>
    </Paper>
  );
}
