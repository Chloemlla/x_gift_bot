import { useEffect, useRef, useState } from "react";
import { Alert, Box, Button, Card, CardContent, Checkbox, Divider, FormControlLabel, LinearProgress, MenuItem, Paper, Stack, TextField, Typography } from "@mui/material";
import LinkRounded from "@mui/icons-material/LinkRounded";
import ContentCopyOutlined from "@mui/icons-material/ContentCopyOutlined";
import CheckCircleOutlineRounded from "@mui/icons-material/CheckCircleOutlineRounded";
import { PaymentQueueCard, type QueueProgress } from "./PaymentQueueCard";
import OpenInNewRounded from "@mui/icons-material/OpenInNewRounded";
import { adminApi } from "./adminApi";
import { request } from "./shared";

type Plan = { months: number; amount: number; currency: string };
type Result = Plan & { username: string; status: string; checkout_url?: string; message?: string; needs_unpaid_verification?: boolean; ticket?: string; position?: number; ahead?: number; estimated_wait_seconds?: number };
function price(p: Plan) { return `${p.currency} ${(p.amount / 100).toFixed(2)}`; }
export function ManualPaymentPanel({ publicMode = false }: { publicMode?: boolean }) {
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
  const cleanUser = username.trim().replace(/^@/, "").toLowerCase();
  const valid = /^[a-z0-9_]{1,15}$/.test(cleanUser);
  async function loadPlans() {
    setPlanError("");
    try {
      const data = await adminApi<{ plans: Plan[] }>(endpoint + "/plans");
      setPlans(data.plans);
      setMonths(data.plans.some((p) => p.months === 6) ? 6 : (data.plans[0]?.months ?? 0));
      if (!data.plans.length) setPlanError("暂无可用套餐。");
    } catch (e) { setPlanError((e as Error).message); }
  }
  useEffect(() => { void loadPlans(); }, []);
  useEffect(() => () => { activeRequest.current?.abort(); }, []);
  useEffect(() => { if (result && publicMode) resultHeading.current?.focus({ preventScroll: true }); }, [result, publicMode]);
  useEffect(() => { if (!busy && error) usernameInput.current?.focus({ preventScroll: true }); }, [busy, error]);
  function reset() { setResult(null); setError(""); setNotice(""); setNeedsVerification(false); setVerified(false); }
  async function generate() {
    if (inFlight.current || !valid || !plans.some((p) => p.months === months)) return;
    inFlight.current = true; setBusy(true); setError(""); setNotice(""); setResult(null); setQueueProgress({ status: "submitting" });
    const controller = new AbortController();
    activeRequest.current = controller;
    try {
      let { ok, data } = await request<Result>(endpoint, { username: cleanUser, months, verified_unpaid: needsVerification && verified }, AbortSignal.any([controller.signal, AbortSignal.timeout(120000)]));
      while (ok && data.ticket && (data.status === "queued" || data.status === "processing")) {
        setQueueProgress({ status: data.status as "queued" | "processing", ahead: data.ahead ?? Math.max(0, (data.position ?? 1) - 1), estimated_wait_seconds: data.estimated_wait_seconds });
        await new Promise<void>((resolve) => setTimeout(resolve, 3000));
        controller.signal.throwIfAborted();
        ({ ok, data } = await request<Result>(`${endpoint}/queue/${encodeURIComponent(data.ticket)}`, undefined, AbortSignal.any([controller.signal, AbortSignal.timeout(45000)])));
      }
      if (!ok) { setNeedsVerification(Boolean(data.needs_unpaid_verification)); setError(data.message || "生成失败，请稍后重试。"); return; }
      setNeedsVerification(false); setVerified(false); setResult(data);
    } catch (e) { setError((e as Error).name === "TimeoutError" ? "请求超时，请使用同一用户名和套餐重试，系统会检查已有订单。" : (e as Error).message); }
    finally { inFlight.current = false; setBusy(false); setQueueProgress({ status: "submitting" }); activeRequest.current = null; }
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
      {planError && <Alert severity="error" sx={{ mb: 2 }} action={<Button color="inherit" onClick={() => void loadPlans()}>重新加载</Button>}>{planError}</Alert>}
      {!(publicMode && (busy || result)) && <Box component="form" onSubmit={(e) => { e.preventDefault(); void generate(); }} aria-busy={busy}>
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
      {publicMode && busy && <PaymentQueueCard progress={queueProgress} username={cleanUser} months={months} price={plans.find((p) => p.months === months) ? price(plans.find((p) => p.months === months)!) : ""} />}
      {!publicMode && busy && <LinearProgress aria-label="正在核对账号并生成付款链接" sx={{ mt: 1 }} />}
      {error && <Alert severity="error" sx={{ mt: 2 }}>{error}</Alert>}
      {publicMode && result && <Card variant="outlined" sx={{ borderRadius: 2, bgcolor: "background.paper" }}>
        <CardContent sx={{ p: { xs: 2.5, sm: 3 }, "&:last-child": { pb: { xs: 2.5, sm: 3 } } }}>
          <Stack direction="row" spacing={1.5} alignItems="center" sx={{ mb: 3 }}>
            <Box sx={{ display: "grid", placeItems: "center", width: 48, height: 48, flexShrink: 0, borderRadius: "50%", bgcolor: "action.selected", color: "success.main" }}><CheckCircleOutlineRounded aria-hidden="true" /></Box>
            <Box>
              <Typography ref={resultHeading} tabIndex={-1} variant="h3">{result.status === "succeeded" ? "订单已付款" : "付款链接已就绪"}</Typography>
              <Typography variant="body2" color="text.secondary" sx={{ mt: 0.5 }}>{result.status === "succeeded" ? "无需再次付款" : "下一步：前往 Stripe 完成付款"}</Typography>
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
            <Typography variant="body2" color="text.secondary" sx={{ mt: 2 }}>请尽快完成付款。已扣款或正在银行验证时，请勿重复支付。</Typography>
            <Box component="details" sx={{ mt: 1 }}>
              <Box component="summary" sx={{ cursor: "pointer", color: "text.secondary", fontSize: 13, py: 1.5, minHeight: 44 }}>查看完整链接</Box>
              <TextField label="Stripe 付款链接" value={result.checkout_url} slotProps={{ input: { readOnly: true } }} onFocus={(e) => e.target.select()} />
            </Box>
          </>}
          <Button onClick={() => { reset(); setUsername(""); requestAnimationFrame(() => usernameInput.current?.focus()); }} sx={{ mt: 2, ml: -2 }}>为其他账号生成链接</Button>
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
