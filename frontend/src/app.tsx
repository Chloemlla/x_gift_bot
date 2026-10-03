import { useEffect, useRef, useState } from "react";
import {
  Accordion,
  AccordionDetails,
  AccordionSummary,
  Alert,
  Box,
  Button,
  CircularProgress,
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
import FactCheckRounded from "@mui/icons-material/FactCheckRounded";
import ShieldOutlined from "@mui/icons-material/ShieldOutlined";
import HistoryRounded from "@mui/icons-material/HistoryRounded";
import { mount, request, Shell } from "./shared";
import { AppearanceMenu } from "./AppearanceMenu";

type Result = {
  status?: string;
  message?: string;
  progress?: number;
  months?: number;
  rechecking?: boolean;
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
  const [result, setResult] = useState<Result | null>(null);
  const [severity, setSeverity] = useState<
    "info" | "success" | "warning" | "error"
  >("info");
  const [progress, setProgress] = useState<number | null>(null);
  const [locked, setLocked] = useState(false);
  const [exhausted, setExhausted] = useState(false);
  const [validation, setValidation] = useState(false);
  const [checking, setChecking] = useState(false);
  const [checkResult, setCheckResult] = useState<{
    severity: "success" | "warning" | "info";
    message: string;
  } | null>(null);
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const controller = useRef<AbortController | null>(null);
  const inFlight = useRef(false);
  const progressPanel = useRef<HTMLDivElement>(null);
  const form = useRef<HTMLFormElement>(null);
  const current = useRef<Input>({ code: "", username: "" });
  const attempt = useRef(0);
  const cleanCode = code.trim().toUpperCase();
  const cleanUser = username.trim().replace(/^@/, "").toLowerCase();
  const codeValid = /^XG-[A-F0-9]{48}$/.test(cleanCode);
  const userValid = /^[a-z0-9_]{1,15}$/.test(cleanUser);

  useEffect(() => {
    const health = new AbortController();
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
    return () => {
      health.abort();
      clearTimeout(timer.current);
      controller.current?.abort();
    };
  }, []);

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
          : data.status === "review" || data.status === "revoked"
            ? "warning"
            : "info",
    );
    if (["processing", "review", "succeeded"].includes(data.status ?? "")) {
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
  function start() {
    if (inFlight.current) return false;
    clearTimeout(timer.current);
    current.current = { code: cleanCode, username: cleanUser };
    attempt.current = 0;
    inFlight.current = true;
    setBusy(true);
    setExhausted(false);
    setSeverity("info");
    setProgress(5);
    scroll();
    return true;
  }
  async function redeem() {
    setConfirmation(false);
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
    else {
      setUsername(value);
      setCheckResult(null);
    }
    setResult(null);
    setProgress(null);
    setExhausted(false);
    // A lost response stays query-only until the server returns a known state.
  }
  // Advisory only: the result never blocks or alters the redemption flow.
  async function runCheck() {
    if (checking || busy || !userValid) return;
    setChecking(true);
    setCheckResult(null);
    try {
      const { ok, data } = await request<{
        eligible?: boolean;
        message?: string;
      }>("/api/check", { username: cleanUser }, AbortSignal.timeout(45000));
      if (ok && typeof data.eligible === "boolean") {
        setCheckResult(
          data.eligible
            ? { severity: "success", message: "该账号当前可以接收赠送。" }
            : {
                severity: "warning",
                message: `该账号当前无法接收赠送：${data.message || "原因未知。"}`,
              },
        );
      } else {
        setCheckResult({
          severity: "info",
          message: data.message || "暂时无法检测，请稍后再试。",
        });
      }
    } catch {
      setCheckResult({ severity: "info", message: "暂时无法检测，请稍后再试。" });
    } finally {
      setChecking(false);
    }
  }

  return (
    <Shell>
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
          <Alert severity="info" sx={{ mb: 3 }}>
            充值暂未开放。你可以先核实赠送资格，兑换码不会被使用。
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
            if (valid() && !locked && !busy) setConfirmation(true);
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
                  ? "请输入 XG- 开头、后接 48 位字母或数字的完整兑换码。"
                  : "请保留兑换码，后续查询仍需使用。"
              }
              slotProps={{
                htmlInput: { maxLength: 80, spellCheck: false },
                input: { sx: { fontFamily: "monospace", fontSize: 14 } },
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
            <Box>
              <Button
                variant="text"
                size="small"
                disabled={!userValid || busy || checking}
                onClick={() => void runCheck()}
                startIcon={
                  checking ? (
                    <CircularProgress size={16} aria-hidden="true" />
                  ) : (
                    <FactCheckRounded />
                  )
                }
              >
                {checking ? "正在检测…" : "检测可否接收赠送"}
              </Button>
              {checkResult && (
                <Alert
                  severity={checkResult.severity}
                  role="status"
                  aria-live="polite"
                  sx={{ mt: 1 }}
                >
                  {checkResult.message}
                </Alert>
              )}
            </Box>
            <Button
              type="submit"
              variant="contained"
              size="large"
              disabled={busy || locked || service === "loading"}
              endIcon={<ArrowForwardRounded />}
            >
              {busy
                ? "正在处理，请稍候"
                : locked
                  ? "请查询原订单进度"
                  : service === "paused"
                    ? "核实赠送资格"
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
              aria-label="兑换进度"
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
                <Typography variant="body2" fontWeight={600}>
                  {result?.status === "succeeded"
                    ? "兑换完成"
                    : busy
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
                aria-label="兑换阶段进度"
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
                百分比表示流程阶段，不是预计耗时。请勿重复提交。
              </Typography>
            </Box>
          )}
          {result && (
            <Alert
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
                if (valid() && start()) {
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
              href="https://x.com/"
              target="_blank"
              rel="noopener noreferrer"
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
            "X 会根据账号情况决定是否允许接收 Premium 赠送。兑换前可先点击用户名旁的「检测可否接收赠送」确认当前资格。首次建单前资格未通过，兑换码不会使用；已有待核实订单时，请使用原兑换码和账号重新检查，符合条件且尚未付款时会继续兑换。",
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
            <AccordionDetails sx={{ px: 0, pb: 3 }}>
              <Typography variant="body2" color="text.secondary">
                {text}
              </Typography>
            </AccordionDetails>
          </Accordion>
        ))}
      </Box>
      <Dialog
        open={confirmation}
        onClose={() => setConfirmation(false)}
        aria-labelledby="redeem-dialog-title"
        fullWidth
        maxWidth="xs"
      >
        <DialogTitle id="redeem-dialog-title">
          {service === "paused"
            ? "核实这个账号的赠送资格？"
            : result?.status === "review"
              ? "重新检查并继续这笔兑换？"
              : "确认接收 Premium 的账号"}
        </DialogTitle>
        <DialogContent>
          <DialogContentText>
            接收账号为 <strong>@{cleanUser}</strong>。
            {service === "paused"
              ? "服务暂停期间只核实资格，兑换码不会使用。"
              : result?.status === "review"
                ? "将重新核对账号资格和原订单；符合条件且尚未付款时继续付款，已提交过付款的订单只核实结果。"
                : "提交后将开始兑换，具体时长以兑换码为准。赠送成功后无法更换账号。"}
          </DialogContentText>
        </DialogContent>
        <DialogActions sx={{ p: 2 }}>
          <Button onClick={() => setConfirmation(false)} autoFocus>
            返回核对
          </Button>
          <Button variant="contained" onClick={() => void redeem()}>
            {service === "paused" ? "确认核实" : "确认兑换"}
          </Button>
        </DialogActions>
      </Dialog>
    </Shell>
  );
}

mount(<App />);
