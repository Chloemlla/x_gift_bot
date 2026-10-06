import { useEffect, useRef, useState } from "react";
import {
  Accordion,
  AccordionDetails,
  AccordionSummary,
  Alert,
  Box,
  Button,
  Dialog,
  DialogActions,
  DialogContent,
  DialogContentText,
  DialogTitle,
  Divider,
  InputAdornment,
  LinearProgress,
  Paper,
  Stack,
  Step,
  StepLabel,
  Stepper,
  TextField,
  Typography,
} from "@mui/material";
import ArrowForwardRounded from "@mui/icons-material/ArrowForwardRounded";
import ArrowOutwardRounded from "@mui/icons-material/ArrowOutwardRounded";
import ExpandMoreRounded from "@mui/icons-material/ExpandMoreRounded";
import ShieldOutlined from "@mui/icons-material/ShieldOutlined";
import HistoryRounded from "@mui/icons-material/HistoryRounded";
import { mount, request, Shell } from "./shared";
import { AppearanceMenu } from "./AppearanceMenu";
import { ManualPaymentPanel } from "./ManualPaymentPanel";
import { EligibilityCard } from "./EligibilityCard";

type Result = {
  status?: string;
  message?: string;
  progress?: number;
  months?: number;
  rechecking?: boolean;
  payment_declined?: boolean;
};
type Input = { code: string; username: string };
const steps = ["核对账号", "核验订单", "付款处理", "兑换完成"];

function App() {
  const [code, setCode] = useState("");
  const [username, setUsername] = useState("");
  const [service, setService] = useState<
    "loading" | "enabled" | "paused" | "unknown"
  >("loading");
  const [busy, setBusy] = useState(false);
  const [confirmation, setConfirmation] = useState(false);
  const [pauseNotice, setPauseNotice] = useState(false);
  const [manualOpen, setManualOpen] = useState(false);
  const [result, setResult] = useState<Result | null>(null);
  const [severity, setSeverity] = useState<
    "info" | "success" | "warning" | "error"
  >("info");
  const [progress, setProgress] = useState<number | null>(null);
  const [locked, setLocked] = useState(false);
  const [exhausted, setExhausted] = useState(false);
  const [validation, setValidation] = useState(false);
  // 本次会话的进度来自「兑换」还是「查询」,驱动进度区标题与区域命名。
  const fromRedeem = useRef(false);
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const controller = useRef<AbortController | null>(null);
  const inFlight = useRef(false);
  const progressPanel = useRef<HTMLDivElement>(null);
  const progressHeading = useRef<HTMLParagraphElement>(null);
  const resultAlert = useRef<HTMLDivElement>(null);
  const form = useRef<HTMLFormElement>(null);
  const current = useRef<Input>({ code: "", username: "" });
  const attempt = useRef(0);
  const cleanCode = code.replace(/\s+/g, "").toUpperCase();
  const cleanUser = username.trim().replace(/^@/, "").toLowerCase();
  const codeValid = /^XG-[A-F0-9]{48}$/.test(cleanCode);
  const userValid = /^[a-z0-9_]{1,15}$/.test(cleanUser);

  useEffect(() => {
    const health = new AbortController();
    const refreshService = () =>
      request<{ payments_enabled: boolean }>(
      "/healthz",
      undefined,
      AbortSignal.any([health.signal, AbortSignal.timeout(10000)]),
    )
      .then(({ ok, data }) =>
        setService(
          ok ? (data.payments_enabled ? "enabled" : "paused") : "unknown",
        ),
      )
      .catch(() => {
        if (!health.signal.aborted) setService("unknown");
      });
    void refreshService();
    const refreshTimer = setInterval(refreshService, 30000);
    window.addEventListener("focus", refreshService);
    return () => {
      health.abort();
      clearInterval(refreshTimer);
      window.removeEventListener("focus", refreshService);
      clearTimeout(timer.current);
      controller.current?.abort();
    };
  }, []);

  useEffect(() => {
    setPauseNotice(service === "paused");
    if (service !== "enabled") setConfirmation(false);
  }, [service]);

  function finish() {
    inFlight.current = false;
    setBusy(false);
  }
  function scroll() {
    requestAnimationFrame(() =>
      progressPanel.current?.scrollIntoView({
        block: "nearest",
        behavior: matchMedia("(prefers-reduced-motion: reduce)").matches
          ? "instant"
          : "smooth",
      }),
    );
  }
  function apply(ok: boolean, data: Result) {
    if (data.status === "paused") setService("paused");
    // If focus sits in the progress panel and this update unmounts it
    // (terminal states), hand focus to the result alert instead of <body>.
    const refocus =
      progressPanel.current?.contains(document.activeElement) ?? false;
    setResult({
      ...data,
      message:
        data.message ||
        ({
          active: "兑换码尚未使用，可以开始兑换。",
          revoked: "兑换码已停用，请联系提供方。",
        }[data.status ?? ""] ??
          "暂时无法确认状态，请稍后查询。"),
    });
    setSeverity(
      !ok
        ? "error"
        : data.status === "succeeded"
          ? "success"
          : data.status === "revoked"
            ? "error"
            : data.status === "review"
              ? "warning"
              : "info",
    );
    if (data.payment_declined) {
      setProgress(null);
      setLocked(false);
    } else if (["processing", "review", "succeeded"].includes(data.status ?? "")) {
      setLocked(data.status !== "review");
      setProgress((previous) =>
        data.status === "succeeded"
          ? 100
          : Math.max(
              previous ?? 0,
              Math.min(95, Math.max(0, data.progress ?? 20)),
            ),
      );
    } else {
      setProgress(null);
      if (ok && data.status === "active") setLocked(false);
    }
    if (refocus)
      requestAnimationFrame(() => {
        if (!progressPanel.current?.contains(document.activeElement))
          resultAlert.current?.focus();
      });
  }
  async function send(path: string) {
    controller.current = new AbortController();
    return request<Result>(
      path,
      current.current,
      AbortSignal.any([controller.current.signal, AbortSignal.timeout(45000)]),
    );
  }
  async function query() {
    try {
      const { ok, data } = await send("/api/status");
      apply(ok, data);
      if (
        ok &&
        (data.status === "processing" || data.rechecking) &&
        attempt.current++ < 150
      ) {
        timer.current = setTimeout(query, 2000);
      } else {
        if (data.status === "processing" || data.rechecking) {
          setResult({
            ...data,
            message:
              "等待时间较长，不代表付款失败。请保留兑换码，稍后查询进度，勿重复兑换。",
          });
          setSeverity("warning");
          setExhausted(true);
        }
        finish();
      }
    } catch {
      if (controller.current?.signal.aborted) return;
      setResult({
        status: "interrupted",
        message:
          "查询暂时中断。请保留兑换码，稍后查询原订单；查询不会再次扣款。",
      });
      setSeverity("warning");
      finish();
    }
  }
  function valid() {
    setValidation(true);
    if (!codeValid || !userValid) {
      form.current
        ?.querySelector<HTMLElement>(!codeValid ? "textarea" : "input")
        ?.focus();
      return false;
    }
    return form.current!.reportValidity();
  }
  function start(keepProgress = false) {
    if (inFlight.current) return false;
    clearTimeout(timer.current);
    current.current = { code: cleanCode, username: cleanUser };
    attempt.current = 0;
    inFlight.current = true;
    setBusy(true);
    setExhausted(false);
    setSeverity("info");
    // 继续查询保留已显示的进度,避免进度条倒退(apply 维持单调不降)。
    if (!keepProgress) setProgress(5);
    scroll();
    // The submit/query buttons disable themselves; park focus on the progress
    // heading so keyboard and screen-reader users are not dropped to <body>.
    requestAnimationFrame(() => progressHeading.current?.focus());
    return true;
  }
  async function redeem() {
    setConfirmation(false);
    if (service !== "enabled" || result?.payment_declined) return;
    fromRedeem.current = true;
    if (!start()) return;
    setResult({ status: "processing", message: "正在核实 X 账号和赠送资格…" });
    try {
      const { ok, data } = await send("/api/redeem");
      apply(ok, data);
      if (ok && (data.status === "processing" || data.rechecking))
        timer.current = setTimeout(query, 1500);
      else finish();
    } catch {
      if (controller.current?.signal.aborted) return;
      setLocked(true);
      setResult({
        status: "interrupted",
        message:
          "连接中断，结果尚不确定。请点击「查询兑换进度」，不要更换兑换码重复提交。",
      });
      setSeverity("warning");
      finish();
    }
  }
  function edit(field: "code" | "username", value: string) {
    if (field === "code") setCode(value);
    else setUsername(value);
    // 与已提交的键值对比较（同一套清洗规则）:只在真正变化时解锁并清空
    // 结果;空白位置等外观编辑保留当前展示(锁定也继续生效)。
    const differs =
      field === "code"
        ? value.replace(/\s+/g, "").toUpperCase() !== current.current.code
        : value.trim().replace(/^@/, "").toLowerCase() !==
          current.current.username;
    if (!differs) return;
    if (locked) setLocked(false);
    setResult(null);
    setProgress(null);
    setExhausted(false);
  }

  return (
    <Shell maxWidth="md">
      <Box
        sx={{
          display: "grid",
          gridTemplateColumns: {
            xs: "1fr",
            md: "minmax(0, 7fr) minmax(0, 5fr)",
          },
          gap: 3,
        }}
      >
        <Paper
          variant="outlined"
          sx={{ p: { xs: 2.5, sm: 4 }, borderRadius: "28px" }}
        >
        <Stack
          direction="row"
          alignItems="center"
          justifyContent="space-between"
          spacing={1}
          sx={{ mb: 1 }}
        >
          <Typography component="h1" variant="h2">
            Premium 兑换
          </Typography>
          <AppearanceMenu />
        </Stack>
        <Typography variant="body2" color="text.secondary" sx={{ mb: 3 }}>
          输入兑换码和 X 用户名，套餐时长以兑换码为准。
        </Typography>
        {service === "paused" && (
          <Alert severity="warning" sx={{ mb: 3 }}>
            充值暂时暂停，恢复时间待定。请保留兑换码，已有订单仍可查询进度。
          </Alert>
        )}
        {service === "unknown" && (
          <Alert severity="warning" sx={{ mb: 3 }}>
            暂时无法确认服务状态。已提交的订单可继续查询。
          </Alert>
        )}
        <Box
          component="form"
          ref={form}
          onSubmit={(event) => {
            event.preventDefault();
            if (service === "enabled" && !result?.payment_declined && result?.status !== "revoked" && valid() && !locked && !busy)
              setConfirmation(true);
          }}
          noValidate
        >
          <Stack spacing={2.5}>
            <TextField
              label="兑换码"
              value={code}
              onChange={(event) => edit("code", event.target.value)}
              disabled={busy}
              required
              multiline
              minRows={2}
              placeholder="粘贴 XG- 开头的完整兑换码"
              autoComplete="off"
              error={validation && !codeValid}
              helperText={
                validation && !codeValid
                  ? "请输入 XG- 开头、后接 48 位十六进制字符（0-9、A-F）的完整兑换码。"
                  : "请保留兑换码，后续查询仍需使用。"
              }
              slotProps={{
                htmlInput: { maxLength: 80, spellCheck: false },
                input: { sx: { fontFamily: "monospace", fontSize: 16 } },
                inputLabel: { shrink: true },
              }}
            />
            <TextField
              label="X 用户名"
              value={username}
              onChange={(event) => edit("username", event.target.value)}
              disabled={busy}
              required
              autoComplete="off"
              placeholder="your_username"
              error={validation && !userValid}
              helperText={
                validation && !userValid
                  ? "请输入 1–15 位英文字母、数字或下划线，不是显示名称。"
                  : "填写 @ 后的用户名，不是显示名称。成功后无法更换账号。"
              }
              slotProps={{
                htmlInput: {
                  maxLength: 16,
                  spellCheck: false,
                  autoCapitalize: "none",
                },
                input: {
                  startAdornment: (
                    <InputAdornment position="start">@</InputAdornment>
                  ),
                },
              }}
            />
            <Button
              type="submit"
              variant="contained"
              size="large"
              disabled={busy || locked || service !== "enabled" || result?.payment_declined || result?.status === "revoked"}
              endIcon={<ArrowForwardRounded />}
            >
              {service === "loading"
                ? "正在检查服务状态…"
                : service === "unknown"
                  ? "服务状态未知，请稍后再试"
                  : service === "paused"
                ? "充值已暂停"
                : result?.payment_declined
                  ? "付款被拒，请联系管理员"
                : result?.status === "revoked"
                  ? "兑换码已停用"
                : busy
                  ? "正在处理，请稍候"
                  : locked
                    ? "请查询原订单进度"
                    : result?.status === "review"
                      ? "重新检查并继续兑换"
                      : "兑换 Premium"}
            </Button>
            <Button
              variant="outlined"
              color="secondary"
              disabled={busy}
              startIcon={<HistoryRounded />}
              onClick={() => {
                fromRedeem.current = false;
                if (valid() && start()) {
                  setResult({ message: "正在查询原订单，不会再次扣款…" });
                  void query();
                }
              }}
            >
              查询兑换进度
            </Button>
          </Stack>
        </Box>
        <Box ref={progressPanel} sx={{ scrollMarginTop: 24 }}>
          {progress !== null && (
            <Box
              component="section"
              aria-labelledby="progress-heading"
              sx={{
                mt: 3,
                p: 2,
                bgcolor: "background.default",
                borderRadius: 2,
              }}
            >
              <Stack
                direction="row"
                justifyContent="space-between"
                spacing={1}
                sx={{ mb: 1.5 }}
              >
                <Typography
                  ref={progressHeading}
                  id="progress-heading"
                  tabIndex={-1}
                  variant="body2"
                  fontWeight={600}
                >
                  {result?.status === "succeeded"
                    ? "兑换完成"
                    : fromRedeem.current
                      ? "兑换进度"
                      : "原订单进度"}
                </Typography>
                <Typography variant="body2" fontWeight={600}>
                  {progress}%
                </Typography>
              </Stack>
              <LinearProgress
                variant="determinate"
                value={progress}
                aria-label="流程阶段进度"
                color={result?.status === "succeeded" ? "success" : "primary"}
                sx={{ height: 6, borderRadius: 3 }}
              />
              <Stepper
                alternativeLabel
                activeStep={
                  progress === 100
                    ? 4
                    : progress >= 70
                      ? 2
                      : progress >= 40
                        ? 1
                        : 0
                }
                sx={{
                  mt: 2,
                  "& .MuiStepLabel-label": { fontSize: ".75rem" },
                  "& .MuiStep-root": { px: 0.25 },
                }}
              >
                {steps.map((label) => (
                  <Step key={label}>
                    <StepLabel>{label}</StepLabel>
                  </Step>
                ))}
              </Stepper>
              <Typography
                variant="caption"
                color="text.secondary"
                component="p"
                sx={{ mt: 2 }}
              >
                {result?.status === "succeeded"
                  ? "百分比表示流程阶段，不是预计耗时。请保留兑换码备查。"
                  : result?.status === "review"
                    ? "百分比表示流程阶段，不是预计耗时。"
                    : "百分比表示流程阶段，不是预计耗时。请勿重复提交。"}
              </Typography>
            </Box>
          )}
          {result && (
            <Alert
              ref={resultAlert}
              tabIndex={-1}
              severity={severity}
              role="status"
              aria-live="polite"
              sx={{ mt: 2 }}
            >
              {result.message}
            </Alert>
          )}
          {exhausted && result && !busy && (
            <Button
              variant="outlined"
              fullWidth
              startIcon={<HistoryRounded />}
              sx={{ mt: 2 }}
              onClick={() => {
                // Resume status polling only; never re-submits the redemption.
                fromRedeem.current = false;
                if (valid() && start(true)) {
                  setResult({
                    status: result.status,
                    message: "正在继续查询原订单，不会再次扣款…",
                  });
                  void query();
                }
              }}
            >
              继续查询
            </Button>
          )}
          {result?.status === "succeeded" && (
            <Button
              href="https://x.com/i/premium"
              target="_blank"
              rel="noopener noreferrer"
              variant="contained"
              endIcon={<ArrowOutwardRounded />}
              fullWidth
              sx={{ mt: 2 }}
            >
              打开 X 查看 Premium
            </Button>
          )}
        </Box>
        <Divider sx={{ my: 3 }} />
        <Stack direction="row" spacing={1.5}>
          <ShieldOutlined color="action" fontSize="small" />
          <Typography variant="caption" color="text.secondary">
            我们会先核实账号能否接收赠送。如果 X
            在首次建单前不允许赠送，兑换码不会使用。已有订单会保留原账号绑定。
          </Typography>
        </Stack>
      </Paper>
      <EligibilityCard />
      </Box>
      <Box component="section" aria-labelledby="faq-title" sx={{ mt: 4 }}>
        <Typography id="faq-title" variant="h3" component="h2" sx={{ mb: 1 }}>
          常见问题
        </Typography>
        {[
          [
            "应该填写哪个用户名？",
            "打开 X 个人主页，找到 @ 后面的用户名。请勿填写昵称或显示名称。赠送成功后无法更换账号，请在提交前仔细核对。",
          ],
          [
            "账号暂时无法接收赠送怎么办？",
            "X 会根据账号情况决定是否允许接收 Premium 赠送。兑换前可先用本页的「检测赠送资格」确认账号当前状态。首次建单前资格未通过，兑换码不会使用；已有待核实订单时，请使用原兑换码和账号重新检查，符合条件且尚未付款时会继续兑换。",
          ],
          [
            "等待较久或关闭页面后，如何查询？",
            "重新打开本站，填写原兑换码和用户名，点击「查询兑换进度」。查询只核实原订单，不会再次扣款。结果待核实时请勿换码重复兑换。",
          ],
        ].map(([title, text], index) => (
          <Accordion
            key={title}
            disableGutters
            sx={{
              bgcolor: "transparent",
              borderBottom: 1,
              borderColor: "divider",
              "&:before": { display: "none" },
            }}
          >
            <AccordionSummary
              expandIcon={<ExpandMoreRounded />}
              id={`faq-${index}`}
              aria-controls={`faq-content-${index}`}
              sx={{ px: 0, minHeight: 64 }}
            >
              <Typography fontWeight={500}>{title}</Typography>
            </AccordionSummary>
            <AccordionDetails id={`faq-content-${index}`} sx={{ px: 0, pb: 3 }}>
              <Typography variant="body2" color="text.secondary">
                {text}
              </Typography>
            </AccordionDetails>
          </Accordion>
        ))}
        <Accordion expanded={manualOpen} onChange={(_, expanded) => setManualOpen(expanded)} disableGutters slotProps={{ transition: { unmountOnExit: false } }} sx={{ bgcolor: "transparent", borderBottom: 1, borderColor: "divider", "&:before": { display: "none" } }}>
          <AccordionSummary expandIcon={<ExpandMoreRounded />} id="faq-manual-link" aria-controls="faq-manual-link-content" sx={{ px: 0, minHeight: 64 }}>
            <Typography fontWeight={500}>没有兑换码，可以为某个用户生成 Stripe 付款链接吗？</Typography>
          </AccordionSummary>
          <AccordionDetails id="faq-manual-link-content" sx={{ px: 0, pb: 3 }}>
            <ManualPaymentPanel publicMode onShow={() => setManualOpen(true)} />
          </AccordionDetails>
        </Accordion>
      </Box>
      <Dialog
        open={pauseNotice && service === "paused"}
        onClose={() => setPauseNotice(false)}
        aria-labelledby="pause-dialog-title"
        aria-describedby="pause-dialog-description"
        fullWidth
        maxWidth="xs"
      >
        <DialogTitle id="pause-dialog-title">充值暂时暂停</DialogTitle>
        <DialogContent>
          <DialogContentText id="pause-dialog-description">
            当前充值服务暂时不可用，正在处理中，恢复时间待定。
            暂停期间无法提交新的兑换，请保留兑换码。
            已提交的订单可继续查询进度，请勿重复提交。
          </DialogContentText>
        </DialogContent>
        <DialogActions sx={{ p: 2 }}>
          <Button
            variant="contained"
            onClick={() => setPauseNotice(false)}
            autoFocus
          >
            我知道了
          </Button>
        </DialogActions>
      </Dialog>
      <Dialog
        open={confirmation}
        onClose={() => setConfirmation(false)}
        aria-labelledby="redeem-dialog-title"
        aria-describedby="redeem-dialog-description"
        fullWidth
        maxWidth="xs"
      >
        <DialogTitle id="redeem-dialog-title">
          {result?.status === "review"
            ? "重新检查并继续这笔兑换？"
            : "确认接收 Premium 的账号"}
        </DialogTitle>
        <DialogContent>
          <DialogContentText id="redeem-dialog-description">
            接收账号为 <strong>@{cleanUser}</strong>。
            {result?.status === "review"
              ? "将重新核对账号资格和原订单；符合条件且尚未付款时继续付款，已提交过付款的订单只核实结果。"
              : "提交后将开始兑换，具体时长以兑换码为准。赠送成功后无法更换账号。"}
          </DialogContentText>
        </DialogContent>
        <DialogActions sx={{ p: 2 }}>
          <Button onClick={() => setConfirmation(false)} autoFocus>
            返回核对
          </Button>
          <Button
            variant="contained"
            disabled={service !== "enabled"}
            onClick={() => void redeem()}
          >
            {result?.status === "review" ? "继续兑换" : "确认兑换"}
          </Button>
        </DialogActions>
      </Dialog>
    </Shell>
  );
}

mount(<App />);
