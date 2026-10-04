import { useEffect, useRef, useState } from "react";
import {
  Alert,
  Box,
  Button,
  Dialog,
  DialogActions,
  DialogContent,
  DialogTitle,
  Paper,
  Stack,
  TextField,
  Typography,
} from "@mui/material";
import { adminApi } from "./adminApi";
import { codeStatus } from "./codeStatus";
import PersonSearchRounded from "@mui/icons-material/PersonSearchRounded";

type Detail = {
  order: {
    id: string;
    username: string;
    hint: string;
    months: number;
    status: string;
    message: string;
    batch: string;
  };
  code: string;
  checkout_url: string;
  previous_checkout_url: string;
  can_recover: boolean;
  replacement_count: number;
};
export type RecoverySelection = {
  id: string;
  mode: "links" | "pay";
  seq: number;
};
export function CustomerPanel({
  selected,
  onPrepare,
}: {
  selected: { id: string; seq: number } | null;
  onPrepare: (id: string, mode: "links" | "pay") => void;
}) {
  const [username, setUsername] = useState("");
  const [detail, setDetail] = useState<Detail | null>(null);
  const [open, setOpen] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const sequence = useRef(0);
  async function lookup(id?: string) {
    const seq = ++sequence.current;
    setBusy(true);
    setError("");
    setNotice("");
    setDetail(null);
    setOpen(true);
    try {
      const data = await adminApi<Detail>(
        `/api/admin/customer?${id ? `id=${encodeURIComponent(id)}` : `username=${encodeURIComponent(username.trim())}`}`,
      );
      if (seq === sequence.current) setDetail(data);
    } catch (e) {
      if (seq === sequence.current) setError((e as Error).message);
    } finally {
      if (seq === sequence.current) setBusy(false);
    }
  }
  useEffect(() => {
    if (selected) void lookup(selected.id);
  }, [selected]);
  function close() {
    sequence.current++;
    setOpen(false);
    setDetail(null);
    setBusy(false);
    setError("");
    setNotice("");
  }
  async function copy() {
    try {
      await navigator.clipboard.writeText(detail?.code || "");
      setNotice("完整兑换码已复制。");
    } catch {
      setNotice("复制失败，请选中下方完整兑换码手动复制。");
    }
  }
  function prepare(mode: "links" | "pay") {
    if (!detail) return;
    const id = detail.order.id;
    close();
    onPrepare(id, mode);
  }
  return (
    <>
      <Paper
        variant="outlined"
        component="section"
        aria-labelledby="customer-panel-title"
        sx={{ p: { xs: 2, sm: 3 }, height: "100%" }}
      >
        <Stack direction="row" spacing={1} alignItems="center">
          <PersonSearchRounded color="primary" aria-hidden="true" />
          <Typography
            id="customer-panel-title"
            variant="h2"
            sx={{ fontSize: 21 }}
          >
            按客户查询
          </Typography>
        </Stack>
        <Box
          component="form"
          onSubmit={(e) => {
            e.preventDefault();
            void lookup();
          }}
          sx={{ mt: 2 }}
        >
          <Stack direction={{ xs: "column", sm: "row" }} spacing={1}>
            <TextField
              label="客户 X 用户名"
              placeholder="输入 @用户名"
              value={username}
              onChange={(e) => setUsername(e.target.value)}
              size="small"
              fullWidth
              autoComplete="off"
              slotProps={{ htmlInput: { maxLength: 16, spellCheck: false } }}
            />
            <Button
              type="submit"
              variant="outlined"
              disabled={busy || !username.trim()}
              sx={{ whiteSpace: "nowrap" }}
            >
              查询客户订单
            </Button>
          </Stack>
        </Box>
        <Typography variant="body2" color="text.secondary" sx={{ mt: 1 }}>
          直接查看客户对应的完整兑换码和付款链接，无需按批次翻页。
        </Typography>
      </Paper>
      <Dialog
        open={open}
        onClose={close}
        fullWidth
        maxWidth="sm"
        aria-labelledby="customer-title"
        aria-describedby="customer-dialog-description"
      >
        <DialogTitle id="customer-title">
          {detail
            ? detail.order.username
              ? `@${detail.order.username} 的订单`
              : "兑换码详情"
            : error
              ? "查询结果"
              : "客户订单详情"}
        </DialogTitle>
        <DialogContent id="customer-dialog-description">
          {busy && <Typography>正在读取订单…</Typography>}
          {error && <Alert severity="error" role="alert">{error}</Alert>}
          {detail && (
            <Stack spacing={2}>
              <Typography>
                {detail.order.months} 个月 · {detail.order.batch || "未分类"} ·{" "}
                {codeStatus[detail.order.status]?.label || detail.order.status}
              </Typography>
              {detail.order.message && (
                <Typography variant="body2" color="text.secondary">
                  {detail.order.message}
                </Typography>
              )}
              {detail.code ? (
                <>
                  <TextField
                    label="客户对应的完整兑换码"
                    multiline
                    value={detail.code}
                    slotProps={{ input: { readOnly: true } }}
                    sx={{
                      "& textarea": {
                        fontFamily: "monospace",
                        overflowWrap: "anywhere",
                      },
                    }}
                  />
                  <Button variant="outlined" onClick={() => void copy()}>
                    复制完整兑换码
                  </Button>
                </>
              ) : (
                <Alert severity="info">
                  此历史兑换码仅保存了校验值，无法还原完整内容。
                  {detail.can_recover && "仍可按此客户订单单独补单。"}
                </Alert>
              )}
              {notice && <Alert severity="info" role="status">{notice}</Alert>}
              {detail.checkout_url && (
                <>
                  <TextField
                    label="当前付款链接（打开前请先核对付款结果）"
                    multiline
                    value={detail.checkout_url}
                    slotProps={{ input: { readOnly: true } }}
                  />
                  <Button
                    component="a"
                    href={detail.checkout_url}
                    target="_blank"
                    rel="noopener noreferrer"
                  >
                    打开当前付款链接
                  </Button>
                </>
              )}
              {detail.previous_checkout_url && (
                <Typography variant="body2" color="text.secondary">
                  已生成过 {detail.replacement_count}{" "}
                  次替代链接，旧账单记录已保留。
                </Typography>
              )}
              {detail.can_recover && (
                <Alert severity="info">
                  「仅生成补单链接」不会付款；「单独补单」会先展示这一笔订单，确认后才尝试付款。
                </Alert>
              )}
            </Stack>
          )}
        </DialogContent>
        <DialogActions sx={{ flexWrap: "wrap", gap: 1 }}>
          <Button onClick={close}>关闭</Button>
          {detail?.can_recover && (
            <>
              <Button variant="outlined" onClick={() => prepare("links")}>
                仅生成补单链接
              </Button>
              <Button variant="contained" onClick={() => prepare("pay")}>
                单独补单
              </Button>
            </>
          )}
        </DialogActions>
      </Dialog>
    </>
  );
}
