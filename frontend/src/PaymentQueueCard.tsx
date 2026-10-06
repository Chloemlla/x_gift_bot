import { useEffect, useRef } from "react";
import { Box, Card, CardContent, Divider, LinearProgress, Stack, Step, StepLabel, Stepper, Typography } from "@mui/material";
import ScheduleRounded from "@mui/icons-material/ScheduleRounded";
import LockOutlined from "@mui/icons-material/LockOutlined";

export type QueueProgress = {
  status: "submitting" | "queued" | "processing";
  ahead?: number;
  estimated_wait_seconds?: number;
};

export function PaymentQueueCard({ progress, username, months, price }: {
  progress: QueueProgress;
  username: string;
  months: number;
  price: string;
}) {
  const card = useRef<HTMLDivElement>(null);
  useEffect(() => { card.current?.focus({ preventScroll: true }); }, []);
  const submitting = progress.status === "submitting";
  const processing = progress.status === "processing";
  const seconds = progress.estimated_wait_seconds;
  const hasEstimate = typeof seconds === "number" && Number.isFinite(seconds) && seconds > 0;
  const unit = hasEstimate && seconds >= 60 ? "分钟" : "秒";
  const value = hasEstimate ? (seconds >= 60 ? Math.ceil(seconds / 60) : Math.ceil(seconds)) : null;
  const title = submitting ? "正在提交请求" : processing ? "正在为你生成链接" : "已加入队列";
  return (
    <Card ref={card} tabIndex={-1} role="region" variant="outlined" aria-labelledby="payment-queue-title" sx={{ borderRadius: 2, bgcolor: "background.paper", outlineOffset: 4 }}>
      <CardContent sx={{ p: { xs: 2.5, sm: 3 }, "&:last-child": { pb: { xs: 2.5, sm: 3 } } }}>
        <Stack direction="row" spacing={1.5} alignItems="center" sx={{ mb: 3 }}>
          <Box sx={{ display: "grid", placeItems: "center", width: 48, height: 48, flexShrink: 0, borderRadius: "50%", bgcolor: "action.selected", color: "primary.main" }}>
            <ScheduleRounded aria-hidden="true" />
          </Box>
          <Box role="status" aria-live="polite" aria-atomic="true">
            <Typography id="payment-queue-title" variant="h3">{title}</Typography>
            <Typography variant="body2" color="text.secondary" sx={{ mt: 0.25 }}>{submitting ? "请稍候，正在确认你的请求" : processing ? "完成后将在这里显示付款入口" : "你的请求将按顺序处理"}</Typography>
          </Box>
        </Stack>

        <Box sx={{ display: "grid", gridTemplateColumns: "minmax(0, 1.35fr) minmax(0, 1fr)", gap: 2, mb: 3 }}>
          <Box>
            <Typography variant="body2" color="text.secondary">预计还需</Typography>
            <Stack direction="row" spacing={0.75} alignItems="baseline" sx={{ mt: 0.5 }}>
              {value === null ? <Typography sx={{ fontSize: 28, lineHeight: 1.5, fontWeight: 600 }}>估算中</Typography> : <>
                <Typography component="span" sx={{ fontSize: { xs: 44, sm: 52 }, lineHeight: 1.15, fontWeight: 600, letterSpacing: "-0.03em", fontVariantNumeric: "tabular-nums", color: "primary.main" }}>{value}</Typography>
                <Typography component="span" color="text.secondary">{unit}</Typography>
              </>}
            </Stack>
            <Typography variant="caption" color="text.secondary" sx={{ display: "block", mt: 1 }}>预估时间会随进度更新</Typography>
          </Box>
          <Box sx={{ borderLeft: 1, borderColor: "divider", pl: { xs: 2, sm: 3 } }}>
            <Typography variant="body2" color="text.secondary">{processing ? "当前进度" : "前方等待"}</Typography>
            {processing ? <Typography sx={{ mt: 1, fontSize: 24, fontWeight: 600 }}>已轮到你</Typography> : <Stack direction="row" spacing={0.75} alignItems="baseline" sx={{ mt: 1 }}>
              <Typography component="span" sx={{ fontSize: 32, lineHeight: 1.25, fontWeight: 600, fontVariantNumeric: "tabular-nums" }}>{progress.ahead ?? "—"}</Typography>
              <Typography component="span" color="text.secondary">人</Typography>
            </Stack>}
          </Box>
        </Box>

        <LinearProgress aria-label={processing ? "正在生成付款链接" : "正在等待处理"} sx={{ height: 4, borderRadius: 2, mb: 3 }} />
        <Stepper alternativeLabel activeStep={submitting ? 0 : processing ? 2 : 1} sx={{ mx: -1, mb: 3, "& .MuiStepLabel-label": { fontSize: 12, mt: 1 }, "& .MuiStepIcon-root": { fontSize: 22 } }}>
          {["提交请求", "排队等待", "生成链接"].map((label) => <Step key={label}><StepLabel>{label}</StepLabel></Step>)}
        </Stepper>

        <Divider sx={{ mb: 2 }} />
        <Stack direction={{ xs: "column", sm: "row" }} justifyContent="space-between" spacing={0.5}>
          <Typography variant="body2" fontWeight={600} sx={{ overflowWrap: "anywhere" }}>@{username}</Typography>
          <Typography variant="body2" color="text.secondary">{months} 个月 Premium · {price}</Typography>
        </Stack>
        <Stack direction="row" spacing={0.75} alignItems="flex-start" sx={{ mt: 2 }}>
          <LockOutlined sx={{ fontSize: 16, color: "text.secondary", mt: "3px" }} aria-hidden="true" />
          <Typography variant="caption" color="text.secondary">请保持页面打开，无需重复提交。生成链接不会扣款。</Typography>
        </Stack>
      </CardContent>
    </Card>
  );
}
