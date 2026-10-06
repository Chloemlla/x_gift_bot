import { useEffect, useRef, useState } from "react";
import { Alert, Box, Button, Card, CardContent, Checkbox, Dialog, DialogActions, DialogContent, DialogContentText, DialogTitle, Divider, FormControlLabel, LinearProgress, MenuItem, Paper, Stack, TextField, Typography } from "@mui/material";
import LinkRounded from "@mui/icons-material/LinkRounded";
import ContentCopyOutlined from "@mui/icons-material/ContentCopyOutlined";
import CheckCircleOutlineRounded from "@mui/icons-material/CheckCircleOutlineRounded";
import { PaymentQueueCard, type QueueProgress } from "./PaymentQueueCard";
import OpenInNewRounded from "@mui/icons-material/OpenInNewRounded";
import { adminApi } from "./adminApi";
import { request } from "./shared";
import { readQueueWithReconnect } from "./queueReconnect";
import { isPaymentResult } from "./manualPaymentResult";

type Plan = { months: number; amount: number; currency: string };
type Result = Plan & { username: string; status: string; checkout_url?: string; expires_at?: number; message?: string; needs_unpaid_verification?: boolean; ticket?: string; position?: number; ahead?: number; estimated_wait_seconds?: number };
type QueueSession = { ticket: string; username: string; months: number };
const queueStorageKey = "xgift-public-queue";
function saveQueue(value: QueueSession | null) {
  try { if (value) sessionStorage.setItem(queueStorageKey, JSON.stringify(value)); else sessionStorage.removeItem(queueStorageKey); } catch { /* Cookie-based recovery remains available. */ }
}
function savedQueue(): QueueSession | null {
  try { const value = JSON.parse(sessionStorage.getItem(queueStorageKey) || "null"); return value && typeof value.ticket === "string" && /^[a-z0-9_]{1,15}$/.test(value.username) && [3,6].includes(value.months) ? value : null; } catch { return null; }
}
function price(p: Plan) { return `${p.currency} ${(p.amount / 100).toFixed(2)}`; }
export function ManualPaymentPanel({ publicMode = false, onShow }: { publicMode?: boolean; onShow?: () => void }) {
  const endpoint = publicMode ? "/api/manual-link" : "/api/admin/manual-link";
  const [plans, setPlans] = useState<Plan[]>([]);
  const [months, setMonths] = useState(6);
  const [username, setUsername] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [planError, setPlanError] = useState("");
  const [result, setResult] = useState<Result | null>(null);
  const [needsVerification, setNeedsVerification] = useState(false);
  const [verified, setVerified] = useState(false);
  const [notice, setNotice] = useState("");
  const [queueProgress, setQueueProgress] = useState<QueueProgress>({ status: "submitting" });
  const resultHeading = useRef<HTMLHeadingElement>(null);
  const usernameInput = useRef<HTMLInputElement>(null);
  const activeRequest = useRef<AbortController | null>(null);
  const inFlight = useRef(false);
  const queueTicket = useRef<string | null>(null);
  const [cancelling, setCancelling] = useState(false);
  const [confirmOpen, setConfirmOpen] = useState(false);
  const [reconnecting, setReconnecting] = useState(false);
  const [notifyReady, setNotifyReady] = useState(false);
  const notifiedTicket = useRef<string | null>(null);
  const mounted = useRef(true);
  const cleanUser = username.trim().replace(/^@/, "").toLowerCase();
  const valid = /^[a-z0-9_]{1,15}$/.test(cleanUser);
  async function loadPlans(recover = false) {
    setPlanError("");
    try {
      const data = await adminApi<{ plans: Plan[] }>(endpoint + "/plans");
      setPlans(data.plans);
      setMonths(data.plans.some((p) => p.months === 6) ? 6 : (data.plans[0]?.months ?? 0));
      if (!data.plans.length) setPlanError("暂无可用套餐。");
      if (recover && publicMode && data.plans.length && mounted.current && !inFlight.current) {
        let pending = savedQueue();
        if (!pending) {
          const recovered = await request<QueueSession>(endpoint + "/queue/current");
          if (recovered.ok && recovered.data?.ticket) pending = recovered.data;
        }
        if (pending && mounted.current && !inFlight.current) {
          const plan = data.plans.find((p) => p.months === pending.months);
          if (plan) { onShow?.(); setUsername(pending.username); setMonths(pending.months); void generate(pending, plan); }
        }
      }
    } catch (e) { setPlanError((e as Error).message); }
  }
  useEffect(() => { mounted.current = true; void loadPlans(true); return () => { mounted.current = false; }; }, []);
  useEffect(() => {
    const leave = () => {
      const ticket = queueTicket.current;
      if (ticket) {
        const url = `/api/manual-link/queue/${encodeURIComponent(ticket)}/leave`;
        if (!navigator.sendBeacon(url, "")) void fetch(url, { method: "POST", keepalive: true, credentials: "same-origin" }).catch(() => {});
        activeRequest.current?.abort();
      }
    };
    const returned = (event: PageTransitionEvent) => {
      if (event.persisted) { inFlight.current = false; void loadPlans(true); }
    };
    window.addEventListener("pagehide", leave);
    window.addEventListener("pageshow", returned);
    return () => { activeRequest.current?.abort(); window.removeEventListener("pagehide", leave); window.removeEventListener("pageshow", returned); };
  }, []);
  async function cancelQueue() {
    const ticket = queueTicket.current;
    if (!ticket || cancelling) return;
    setCancelling(true);
    try {
      const response = await request<{ cancelled?: boolean; message?: string }>(`${endpoint}/queue/${encodeURIComponent(ticket)}/cancel`, {});
      if (!response.ok) { setError(response.data.message || "暂时无法退出排队，请重试。"); return; }
      if (response.data.cancelled) {
        queueTicket.current = null;
        saveQueue(null);
        activeRequest.current?.abort();
        setNotice("已退出排队。");
        setError("");
      }
    } catch { setError("暂时无法退出排队，请重试。"); }
    finally { setCancelling(false); }
  }
  useEffect(() => { if (result && publicMode) resultHeading.current?.focus({ preventScroll: true }); }, [result, publicMode]);
  useEffect(() => { if (!busy && error) usernameInput.current?.focus({ preventScroll: true }); }, [busy, error]);
  function reset() { setResult(null); setError(""); setNotice(""); setNeedsVerification(false); setVerified(false); }
  async function enableNotification() {
    if (!("Notification" in window)) { setNotice("此浏览器不支持系统通知，请保持页面打开，链接生成后页面标题也会提醒。"); return; }
    try { const permission = await Notification.requestPermission(); if (permission === "granted" && "serviceWorker" in navigator) await navigator.serviceWorker.register("/payment-notifications.js"); setNotifyReady(permission === "granted"); setNotice(permission === "granted" ? "已开启链接就绪通知，请保持网页打开。" : "未开启系统通知，请留意此页面的排队进度。"); } catch { setNotice("暂时无法开启系统通知，请留意此页面。"); }
  }
  async function generate(resume?: QueueSession, restoredPlan?: Plan) {
    const selectedPlan = restoredPlan ?? plans.find((p) => p.months === months);
    const user = resume?.username ?? cleanUser;
    if (inFlight.current || !/^[a-z0-9_]{1,15}$/.test(user) || !selectedPlan) return;
    inFlight.current = true; setBusy(true); setError(""); setNotice(""); setResult(null); setQueueProgress({ status: "submitting" });
    const controller = new AbortController();
    activeRequest.current = controller;
    if (resume) queueTicket.current = resume.ticket; else saveQueue(null);
    try {
      const poll = (path: string) => readQueueWithReconnect(() => request<Result>(path, undefined, AbortSignal.any([controller.signal, AbortSignal.timeout(20000)])), controller.signal, undefined, undefined, {onRetry: () => setReconnecting(true)});
      let { ok, data } = resume
        ? await poll(`${endpoint}/queue/${encodeURIComponent(resume.ticket)}`)
        : await request<Result>(endpoint, { username: user, months: selectedPlan.months, verified_unpaid: needsVerification && verified, queue_protocol: publicMode ? 1 : undefined }, AbortSignal.any([controller.signal, AbortSignal.timeout(120000)]));
      if (resume) queueTicket.current = resume.ticket;
      while (ok && typeof data?.ticket === "string" && data.ticket && (data.status === "queued" || data.status === "processing")) {
        queueTicket.current = data.ticket;
        saveQueue({ticket: data.ticket, username: user, months: selectedPlan.months});
        setReconnecting(false);
        setQueueProgress({ status: data.status as "queued" | "processing", ahead: data.ahead ?? Math.max(0, (data.position ?? 1) - 1), estimated_wait_seconds: data.estimated_wait_seconds });
        await new Promise<void>((resolve) => setTimeout(resolve, 3000));
        controller.signal.throwIfAborted();
        const queuePath = `${endpoint}/queue/${encodeURIComponent(data.ticket)}`;
        ({ ok, data } = await poll(queuePath));
      }
      const completedTicket = queueTicket.current;
      queueTicket.current = null;
      setReconnecting(false);
      if (!ok) saveQueue(null);
      if (!ok) { setNeedsVerification(Boolean(data?.needs_unpaid_verification)); setError(data?.message || "生成失败，请稍后重试。"); return; }
      if (!isPaymentResult(data, user, selectedPlan)) {
        setError("尚未取得完整的付款订单，请刷新页面后重试。系统会先检查已有链接。"); return;
      }
      setNeedsVerification(false); setVerified(false); setResult(data); onShow?.();
      if (publicMode && completedTicket && notifiedTicket.current !== completedTicket) {
        notifiedTicket.current = completedTicket;
        document.title = data.status === "succeeded" ? "订单已付款 · XGift" : "付款链接已就绪 · XGift";
        if ("Notification" in window && Notification.permission === "granted") {
          void (async () => { try {
            const title = data.status === "succeeded" ? "订单已付款" : "付款链接已就绪";
            const options = {body: "请返回网页查看订单及付款状态。", tag: "xgift-payment-ready"};
            if ("serviceWorker" in navigator) {
              await navigator.serviceWorker.register("/payment-notifications.js");
              const registration = await navigator.serviceWorker.ready;
              await registration.showNotification(title, options);
            } else { const notification = new Notification(title, options); notification.onclick = () => { window.focus(); notification.close(); }; }
          } catch { setNotice("系统通知未能显示，请留意此页面的订单状态。"); } })();
        }
      }
    } catch (e) { if (!controller.signal.aborted) setError((e as Error).name === "TimeoutError" ? "请求超时，请使用同一用户名和套餐重试，系统会检查已有订单。" : "连接暂时中断，请刷新页面恢复原排队；无需重新生成订单。"); }
    finally { if (activeRequest.current === controller) { inFlight.current = false; setBusy(false); setQueueProgress({ status: "submitting" }); activeRequest.current = null; queueTicket.current = null; } }
  }
  async function copy() {
    if (!result?.checkout_url) return;
    try { await navigator.clipboard.writeText(result.checkout_url); setNotice("付款链接已复制。"); }
    catch { setNotice("复制失败，请选中下方链接手动复制。"); }
  }
  return (
    <Paper variant="outlined" component="section" aria-labelledby="manual-payment-title" sx={{ p: publicMode ? 0 : { xs: 2, sm: 3 }, mb: publicMode ? 0 : 3, ...(publicMode ? { border: 0, bgcolor: "transparent" } : {}) }}>
      <Stack direction="row" spacing={1} alignItems="center" sx={{ mb: 1 }}>
        <LinkRounded color="primary" aria-hidden="true" />
        <Typography id="manual-payment-title" variant="h2" sx={{ fontSize: 21 }}>手动付款链接</Typography>
      </Stack>
      {!(publicMode && (busy || result)) && <Typography variant="body2" color="text.secondary" sx={{ mb: 3 }}>{publicMode ? "为指定的 X 账号生成付款链接，随后前往 Stripe 自行付款。" : "填写 X 用户名和套餐时长，生成 Stripe 链接后手动付款，无需兑换码。"}</Typography>}
      <Dialog open={confirmOpen} onClose={() => setConfirmOpen(false)} aria-labelledby="confirm-payment-title" aria-describedby="confirm-payment-description" fullWidth maxWidth="xs">
        <DialogTitle id="confirm-payment-title">确认生成付款链接？</DialogTitle>
        <DialogContent>
          <DialogContentText id="confirm-payment-description" color="text.primary">如果不想要付款，请不要点击生成链接。</DialogContentText>
          <DialogContentText sx={{ mt: 2 }}>刷新页面会保留排队；关闭或离开网站后会自动退出排队。</DialogContentText>
        </DialogContent>
        <DialogActions sx={{ px: 3, pb: 2.5, gap: 1 }}>
          <Button onClick={() => setConfirmOpen(false)}>暂不生成</Button>
          <Button variant="contained" onClick={() => { setConfirmOpen(false); void generate(); }}>确认生成</Button>
        </DialogActions>
      </Dialog>
      {planError && <Alert severity="error" sx={{ mb: 2 }} action={<Button color="inherit" onClick={() => void loadPlans(true)}>重新加载</Button>}>{planError}</Alert>}
      {!(publicMode && (busy || result)) && <Box component="form" onSubmit={(e) => { e.preventDefault(); if (publicMode) setConfirmOpen(true); else void generate(); }} aria-busy={busy}>
        <Box sx={{
          display: "grid",
          gridTemplateColumns: { xs: "minmax(0, 1fr)", sm: "minmax(0, 1fr) minmax(0, 1fr)", md: publicMode ? "minmax(0, 1fr) minmax(0, 1fr)" : "minmax(260px, 1fr) minmax(235px, 320px) auto" },
          gap: 2,
          alignItems: "start",
        }}>
          <TextField inputRef={usernameInput} label="X 用户名" placeholder="例如 username 或 @username" value={username} disabled={busy} required onChange={(e) => { setUsername(e.target.value); reset(); }} error={username.trim().length > 0 && !valid} helperText={username.trim() && !valid ? "用户名须为 1–15 位字母、数字或下划线" : "填写用户名，不是显示名称"} autoComplete="off" sx={{ minWidth: 0 }} slotProps={{ htmlInput: { maxLength: 32, autoCapitalize: "none", spellCheck: false } }} />
          <TextField select label="套餐时长" value={plans.length ? months : ""} disabled={busy || !plans.length} onChange={(e) => { setMonths(Number(e.target.value)); reset(); }} sx={{ minWidth: 0 }}>
            {plans.map((p) => <MenuItem key={p.months} value={p.months}>{p.months} 个月 · {price(p)}</MenuItem>)}
          </TextField>
          <Button type="submit" variant="contained" startIcon={<LinkRounded />} disabled={busy || !valid || !plans.length || (needsVerification && !verified)} sx={{ minHeight: 56, px: 3, whiteSpace: "nowrap", gridColumn: { sm: "1 / -1", md: publicMode ? "1 / -1" : "auto" }, justifySelf: { xs: "stretch", sm: "end", md: publicMode ? "end" : "stretch" } }}>{busy ? "正在生成…" : "生成付款链接"}</Button>
        </Box>
        {needsVerification && <FormControlLabel control={<Checkbox checked={verified} disabled={busy} onChange={(e) => setVerified(e.target.checked)} />} label="我已核实原订单未付款，也没有正在处理的扣款或银行验证，允许生成新链接" />}
      </Box>}
      {publicMode && busy && <PaymentQueueCard reconnecting={reconnecting} onNotify={() => void enableNotification()} notifyReady={notifyReady} onCancel={queueProgress.status === "submitting" ? undefined : () => void cancelQueue()} cancelling={cancelling} progress={queueProgress} username={cleanUser} months={months} price={plans.find((p) => p.months === months) ? price(plans.find((p) => p.months === months)!) : ""} />}
      {!publicMode && busy && <LinearProgress aria-label="正在核对账号并生成付款链接" sx={{ mt: 1 }} />}
      {error && <Alert severity="error" sx={{ mt: 2 }}>{error}</Alert>}
      {publicMode && result && <Card variant="outlined" sx={{ borderRadius: 2, bgcolor: "background.paper" }}>
        <CardContent sx={{ p: { xs: 2.5, sm: 3 }, "&:last-child": { pb: { xs: 2.5, sm: 3 } } }}>
          <Stack direction="row" spacing={1.5} alignItems="center" sx={{ mb: 3 }}>
            <Box sx={{ display: "grid", placeItems: "center", width: 48, height: 48, flexShrink: 0, borderRadius: "50%", bgcolor: "action.selected", color: "success.main" }}><CheckCircleOutlineRounded aria-hidden="true" /></Box>
            <Box>
              <Typography ref={resultHeading} tabIndex={-1} variant="h3">{result.status === "succeeded" ? "订单已付款" : "付款链接已就绪"}</Typography>
              <Typography variant="body2" color="text.secondary" sx={{ mt: 0.5 }}>{result.status === "succeeded" ? "无需再次付款" : "生成链接不代表付款成功，请前往 Stripe 付款"}</Typography>
            </Box>
          </Stack>
          <Stack direction={{ xs: "column", sm: "row" }} justifyContent="space-between" spacing={0.5} sx={{ mb: 2 }}>
            <Typography fontWeight={600}>@{result.username}</Typography>
            <Typography color="text.secondary">{result.months} 个月 Premium · {price(result)}</Typography>
          </Stack>
          <Divider sx={{ mb: 3 }} />
          {result.checkout_url && <>
            <Stack direction={{ xs: "column", sm: "row" }} spacing={1.5}>
              <Button component="a" href={result.checkout_url} target="_blank" rel="noopener noreferrer" variant="contained" endIcon={<OpenInNewRounded />} sx={{ minHeight: 48 }}>前往 Stripe 付款</Button>
              <Button variant="outlined" onClick={() => void copy()} startIcon={<ContentCopyOutlined />}>复制链接</Button>
            </Stack>
            <Typography variant="body2" color="text.secondary" sx={{ mt: 2 }}>{result.expires_at ? `请在 ${new Date(result.expires_at * 1000).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" })} 前完成付款。` : "请尽快完成付款。"}若 Stripe 提示付款被拒绝，则尚未支付成功。已扣款或正在银行验证时，请勿重复支付。</Typography>
            <Box component="details" sx={{ mt: 1 }}>
              <Box component="summary" sx={{ cursor: "pointer", color: "text.secondary", fontSize: 13, py: 1.5, minHeight: 44 }}>查看完整链接</Box>
              <TextField label="Stripe 付款链接" value={result.checkout_url} slotProps={{ input: { readOnly: true } }} onFocus={(e) => e.target.select()} />
            </Box>
          </>}
          <Stack direction="row" spacing={1} sx={{ mt: 2, ml: -2 }}>
            {result.checkout_url && <Button onClick={() => { reset(); requestAnimationFrame(() => usernameInput.current?.focus()); }}>更换套餐</Button>}
            <Button onClick={() => { reset(); setUsername(""); requestAnimationFrame(() => usernameInput.current?.focus()); }}>为其他账号生成链接</Button>
          </Stack>
        </CardContent>
      </Card>}
      {!publicMode && result && <Box sx={{ mt: 2 }} aria-live="polite">
        <Alert severity={result.status === "requires_action" ? "info" : "success"} sx={{ mb: 2 }}>{result.status === "succeeded" ? result.message : result.status === "requires_action" ? "已有付款等待验证，请打开原付款页面完成验证。" : publicMode ? "新付款链接已生成，已核对账号、套餐、金额及可支付状态。请尽快打开付款。" : "付款链接已就绪，尚未自动扣款。"}</Alert>
        <Typography sx={{ mb: 1, fontWeight: 600 }}>@{result.username} · {result.months} 个月 · {price(result)}</Typography>
        {result.checkout_url && <>
          <TextField fullWidth label="Stripe 付款链接" value={result.checkout_url} slotProps={{ input: { readOnly: true } }} onFocus={(e) => e.target.select()} />
          <Stack direction={{ xs: "column", sm: "row" }} spacing={1} sx={{ mt: 2 }}>
            <Button component="a" href={result.checkout_url} target="_blank" rel="noopener noreferrer" variant="contained" startIcon={<OpenInNewRounded />}>打开付款页面</Button>
            <Button variant="outlined" onClick={() => void copy()} startIcon={<ContentCopyOutlined />}>复制链接</Button>
          </Stack>
        </>}
      </Box>}
      {notice && <Alert severity="info" sx={{ mt: 2 }} onClose={() => setNotice("")}>{notice}</Alert>}
    </Paper>
  );
}
