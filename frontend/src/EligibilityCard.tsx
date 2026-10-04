import { useRef, useState } from "react";
import {
  Alert,
  Box,
  Button,
  CircularProgress,
  InputAdornment,
  Paper,
  Stack,
  TextField,
  Typography,
} from "@mui/material";
import FactCheckRounded from "@mui/icons-material/FactCheckRounded";
import { request } from "./shared";

// Standalone eligibility probe next to the redemption card: a visitor can
// verify an X account before involving any redemption code. Advisory only.
export function EligibilityCard() {
  const [username, setUsername] = useState("");
  const [checking, setChecking] = useState(false);
  const [invalid, setInvalid] = useState(false);
  const [result, setResult] = useState<{
    severity: "success" | "warning" | "info";
    message: string;
  } | null>(null);
  const field = useRef<HTMLInputElement>(null);
  const root = useRef<HTMLDivElement>(null);
  const alertRef = useRef<HTMLDivElement>(null);
  const cleanUser = username.trim().replace(/^@/, "").toLowerCase();
  const userValid = /^[a-z0-9_]{1,15}$/.test(cleanUser);

  async function run() {
    setInvalid(!userValid);
    if (!userValid || checking) {
      if (!userValid) field.current?.focus();
      return;
    }
    // 检测期间输入框与按钮禁用,焦点会跌落;完成后泊放到结果提示。
    const refocus = root.current?.contains(document.activeElement) ?? false;
    setChecking(true);
    setResult(null);
    try {
      const { ok, data } = await request<{
        eligible?: boolean;
        message?: string;
      }>("/api/check", { username: cleanUser }, AbortSignal.timeout(45000));
      if (ok && typeof data.eligible === "boolean") {
        setResult(
          data.eligible
            ? { severity: "success", message: "该账号当前可以接收赠送。" }
            : {
                severity: "warning",
                message: `该账号当前无法接收赠送：${data.message || "原因未知。"}`,
              },
        );
      } else {
        setResult({
          severity: "info",
          message: data.message || "暂时无法检测，请稍后再试。",
        });
      }
    } catch {
      setResult({ severity: "info", message: "暂时无法检测，请稍后再试。" });
    } finally {
      setChecking(false);
      // 仅当焦点确已跌落 body 时泊放;用户等待期间移走焦点不抢夺。
      if (refocus)
        requestAnimationFrame(() => {
          if (document.activeElement === document.body)
            alertRef.current?.focus();
        });
    }
  }

  return (
    <Paper
      ref={root}
      variant="outlined"
      component="section"
      aria-labelledby="eligibility-title"
      sx={{
        p: { xs: 2.5, sm: 4 },
        borderRadius: "28px",
        display: "flex",
        flexDirection: "column",
      }}
    >
      <Stack direction="row" spacing={1.5} alignItems="center" sx={{ mb: 1 }}>
        <FactCheckRounded color="primary" aria-hidden="true" />
        <Typography id="eligibility-title" component="h2" variant="h3">
          检测赠送资格
        </Typography>
      </Stack>
      <Typography variant="body2" color="text.secondary" sx={{ mb: 3 }}>
        兑换前先确认 X 账号当前能否接收 Premium 赠送。检测不消耗兑换码。
      </Typography>
      <Stack
        component="form"
        spacing={2.5}
        noValidate
        sx={{ flexGrow: 1 }}
        onSubmit={(event) => {
          event.preventDefault();
          void run();
        }}
      >
        <TextField
          label="X 用户名"
          value={username}
          onChange={(event) => {
            setUsername(event.target.value);
            setResult(null);
            if (invalid) setInvalid(false);
          }}
          disabled={checking}
          autoComplete="off"
          placeholder="your_username"
          error={invalid}
          helperText={
            invalid
              ? "请输入 1–15 位英文字母、数字或下划线，不是显示名称。"
              : "填写 @ 后的用户名。"
          }
          slotProps={{
            htmlInput: {
              maxLength: 16,
              spellCheck: false,
              autoCapitalize: "none",
              ref: field,
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
          variant="outlined"
          size="large"
          disabled={checking}
          startIcon={
            checking ? (
              <CircularProgress size={18} aria-hidden="true" />
            ) : undefined
          }
        >
          {checking ? "正在检测…" : "开始检测"}
        </Button>
        {result && (
          <Alert
            ref={alertRef}
            tabIndex={-1}
            severity={result.severity}
            role="status"
            aria-live="polite"
          >
            {result.message}
          </Alert>
        )}
        <Box sx={{ flexGrow: 1 }} aria-hidden="true" />
        <Typography variant="caption" color="text.secondary" component="p">
          结果仅供参考，提交兑换时系统会向 X 再次核实。
        </Typography>
      </Stack>
    </Paper>
  );
}
