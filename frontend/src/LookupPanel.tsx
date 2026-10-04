import { useEffect, useRef, useState } from "react";
import {
  Alert,
  Box,
  Button,
  Chip,
  CircularProgress,
  Paper,
  Stack,
  TextField,
  Typography,
} from "@mui/material";
import ManageSearchRounded from "@mui/icons-material/ManageSearchRounded";
import { adminApi, formatTime, type AdminCode } from "./adminApi";
import { codeStatus } from "./codeStatus";

// Direct lookup by full redemption code: status, batch and recipient without
// paging through folders. Actions delegate to the parent's existing flows.
export function LookupPanel({
  disabled,
  refreshSignal,
  onCopy,
  onViewCustomer,
  onRevoke,
}: {
  disabled: boolean;
  refreshSignal: number;
  onCopy: (code: AdminCode) => void;
  onViewCustomer: (id: string) => void;
  onRevoke: (code: AdminCode) => void;
}) {
  const [input, setInput] = useState("");
  const [busy, setBusy] = useState(false);
  const [invalid, setInvalid] = useState(false);
  const [error, setError] = useState("");
  const [result, setResult] = useState<AdminCode | null>(null);
  const queried = useRef("");
  const sequence = useRef(0);
  const field = useRef<HTMLInputElement>(null);
  const clean = input.replace(/\s+/g, "").toUpperCase();
  const valid = /^XG-[A-F0-9]{48}$/.test(clean);

  async function run(code: string) {
    const seq = ++sequence.current;
    setBusy(true);
    setError("");
    try {
      const data = await adminApi<AdminCode>("/api/admin/lookup", { code });
      if (seq === sequence.current) {
        setResult(data);
        queried.current = code;
      }
    } catch (e) {
      if (seq === sequence.current) {
        setResult(null);
        setError((e as Error).message);
      }
    } finally {
      if (seq === sequence.current) setBusy(false);
    }
  }
  function submit() {
    setInvalid(!valid);
    if (!valid || busy || disabled) {
      if (!valid) field.current?.focus();
      return;
    }
    void run(clean);
  }
  // Keep a visible result in sync after mutations (revoke, move, folder ops).
  // Only while the shown result still matches the input: once the admin edits
  // the field, the old result is gone and must not reappear.
  useEffect(() => {
    if (refreshSignal > 0 && result && queried.current === clean)
      void run(queried.current);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [refreshSignal]);

  const rows = result
    ? [
        ["接收账号", result.username ? `@${result.username}` : "—"],
        ["批次", result.batch || "未分类"],
        [
          "生成时间",
          formatTime(result.created),
        ],
        ...(result.status === "processing" || result.status === "review"
          ? [["订单进度", `${Math.min(100, Math.max(0, result.progress ?? 0))}%`]]
          : []),
      ]
    : [];

  return (
    <Paper
      variant="outlined"
      component="section"
      aria-labelledby="lookup-title"
      sx={{ p: { xs: 2, sm: 3 }, height: "100%" }}
    >
      <Stack direction="row" spacing={1} alignItems="center">
        <ManageSearchRounded color="primary" aria-hidden="true" />
        <Typography id="lookup-title" variant="h2" sx={{ fontSize: 21 }}>
          按兑换码查询
        </Typography>
      </Stack>
      <Box
        component="form"
        noValidate
        onSubmit={(event) => {
          event.preventDefault();
          submit();
        }}
        sx={{ mt: 2 }}
      >
        <Stack direction={{ xs: "column", sm: "row" }} spacing={1}>
          <TextField
            label="完整兑换码"
            placeholder="XG-…"
            value={input}
            onChange={(event) => {
              // 编辑即作废在途响应:旧结果不得在输入变化后重新出现。
              sequence.current++;
              setInput(event.target.value);
              setResult(null);
              setError("");
              if (invalid) setInvalid(false);
            }}
            size="small"
            fullWidth
            autoComplete="off"
            error={invalid}
            helperText={
              invalid
                ? "请输入 XG- 开头、后接 48 位十六进制字符（0-9、A-F）的完整兑换码。"
                : " "
            }
            slotProps={{
              htmlInput: { maxLength: 80, spellCheck: false, ref: field },
              input: { sx: { fontFamily: "monospace", fontSize: 16 } },
            }}
          />
          <Button
            type="submit"
            variant="outlined"
            disabled={busy || disabled || !input.trim()}
            startIcon={
              busy ? <CircularProgress size={16} aria-hidden="true" /> : undefined
            }
            sx={{ whiteSpace: "nowrap", alignSelf: { sm: "flex-start" } }}
          >
            {busy ? "正在查询…" : "查询兑换码"}
          </Button>
        </Stack>
      </Box>
      <Typography variant="body2" color="text.secondary">
        粘贴客户提供的完整兑换码，直接查看状态、批次和接收账号，无需按批次翻页。
      </Typography>
      {error && (
        <Alert severity="error" role="alert" sx={{ mt: 2 }}>
          {error}
        </Alert>
      )}
      <Box aria-live="polite">
        {result && (
          <Box
            sx={{
              mt: 2,
              p: 2,
              border: 1,
              borderColor: "divider",
              borderRadius: 2,
              bgcolor: "background.default",
            }}
          >
            <Stack
              direction="row"
              spacing={1}
              alignItems="center"
              flexWrap="wrap"
              useFlexGap
            >
              <Chip
                size="small"
                variant="outlined"
                color={codeStatus[result.status]?.color ?? "default"}
                label={codeStatus[result.status]?.label ?? result.status}
              />
              <Typography variant="body2" fontWeight={600}>
                {result.months} 个月
              </Typography>
              <Typography
                variant="body2"
                color="text.secondary"
                sx={{ fontFamily: "monospace" }}
              >
                尾号 …{result.hint}
              </Typography>
            </Stack>
            <Box
              component="dl"
              sx={{
                display: "grid",
                gridTemplateColumns: "auto 1fr",
                columnGap: 2,
                rowGap: 0.5,
                m: 0,
                mt: 1.5,
              }}
            >
              {rows.map(([label, value]) => (
                <Box component="div" key={label} sx={{ display: "contents" }}>
                  <Typography
                    component="dt"
                    variant="body2"
                    color="text.secondary"
                    sx={{ whiteSpace: "nowrap" }}
                  >
                    {label}
                  </Typography>
                  <Typography
                    component="dd"
                    variant="body2"
                    sx={{ m: 0, overflowWrap: "anywhere" }}
                  >
                    {value}
                  </Typography>
                </Box>
              ))}
            </Box>
            {result.message && (
              <Typography
                variant="body2"
                color="text.secondary"
                sx={{ mt: 1, overflowWrap: "anywhere" }}
              >
                {result.message}
              </Typography>
            )}
            <Stack
              direction="row"
              spacing={1}
              flexWrap="wrap"
              useFlexGap
              sx={{ mt: 1.5 }}
            >
              <Button
                size="small"
                variant="outlined"
                disabled={disabled}
                onClick={() => onViewCustomer(result.id)}
              >
                查看兑换码 / 补单
              </Button>
              {result.copyable && (
                <Button
                  size="small"
                  variant="outlined"
                  disabled={disabled}
                  onClick={() => onCopy(result)}
                >
                  复制完整兑换码
                </Button>
              )}
              {result.status === "active" && (
                <Button
                  size="small"
                  variant="outlined"
                  color="error"
                  disabled={disabled}
                  onClick={() => onRevoke(result)}
                >
                  停用
                </Button>
              )}
            </Stack>
          </Box>
        )}
      </Box>
    </Paper>
  );
}
