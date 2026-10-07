import { useEffect, useRef } from "react";
import { Alert, Box, Button, Card, CardContent, Divider, LinearProgress, Stack, Step, StepLabel, Stepper, Typography } from "@mui/material";
import ScheduleRounded from "@mui/icons-material/ScheduleRounded";
import LockOutlined from "@mui/icons-material/LockOutlined";

export type QueueProgress = {
  status: "submitting" | "queued" | "processing";
  ahead?: number;
  estimated_wait_seconds?: number;
};

export function PaymentQueueCard({ progress, username, months, price, onCancel, cancelling, reconnecting, onNotify, notifyReady }: {
  progress: QueueProgress;
  username: string;
  months: number;
  price: string;
  onCancel?: () => void;
  cancelling?: boolean;
  reconnecting?: boolean;
  onNotify?: () => void;
  notifyReady?: boolean;
}) {
  const card = useRef<HTMLDivElement>(null);
  useEffect(() => { card.current?.focus({ preventScroll: true }); }, []);
  const submitting = progress.status === "submitting";
  const processing = progress.status === "processing";
  const seconds = progress.estimated_wait_seconds;
  const hasEstimate = typeof seconds === "number" && Number.isFinite(seconds) && seconds > 0;
  const unit = hasEstimate && seconds >= 3600 ? "小时" : hasEstimate && seconds >= 60 ? "分钟" : "秒";
  const value = hasEstimate ? Math.ceil(seconds / (seconds >= 3600 ? 3600 : seconds >= 60 ? 60 : 1)) : null;
  const title = reconnecting ? "连接中断，正在重连" : submitting ? "正在提交请求" : processing ? "正在为你生成链接" : "已加入队列";
  return (
    <Card ref={card} tabIndex={-1} role="region" variant="outlined" aria-labelledby="payment-queue-title" sx={{ borderRadius: 2, bgcolor: "background.paper", outlineOffset: 4 }}>
      <CardContent sx={{ p: { xs: 2.5, sm: 3 }, "&:last-child": { pb: { xs: 2.5, sm: 3 } } }}>
        <Stack direction="row" spacing={1.5} alignItems="center" sx={{ mb: 3 }}>
          <Box sx={{ display: "grid", placeItems: "center", width: 48, height: 48, flexShrink: 0, borderRadius: "50%", bgcolor: "action.selected", color: "primary.main" }}>
            <ScheduleRounded aria-hidden="true" />
          </Box>
          <Box role="status" aria-live="polite" aria-atomic="true">
            <Typography id="payment-queue-title" variant="h3">{title}</Typography>
            <Typography variant="body2" color="text.secondary" sx={{ mt: 0.25 }}>{reconnecting ? "正在恢复原排队，请勿重复提交" : submitting ? "请稍候，正在确认你的请求" : processing ? "完成后将在这里显示付款入口" : "你的请求将按顺序处理"}</Typography>
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
            <Typography variant="caption" color="text.secondary" sx={{ display: "block", mt: 1 }}>前方订单完成后，等待时间会缩短</Typography>
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

        {onNotify && <Button variant="outlined" fullWidth onClick={onNotify} disabled={notifyReady} sx={{ mb: 2, minHeight: 44 }}>{notifyReady ? "已开启就绪通知" : "链接就绪时通知我"}</Button>}
        <Alert severity="warning" icon={false} sx={{ mb: 3 }}>
          <Typography variant="body2" fontWeight={600}>轮到你后，付款链接只保留 3 分钟</Typography>
          <Typography variant="body2" sx={{ mt: 0.5 }}>请提前准备好银行卡，超时未付款链接会作废，需要重新排队。等待时请让本页保持打开：手机切到其他应用超过约 1.5 分钟会暂停你的排队，超过 5 分钟会被移出队列。页面标题会显示排队进度和付款倒计时。</Typography>
        </Alert>

        <Divider sx={{ mb: 2 }} />
        <Stack direction={{ xs: "column", sm: "row" }} justifyContent="space-between" spacing={0.5}>
          <Typography variant="body2" fontWeight={600} sx={{ overflowWrap: "anywhere" }}>@{username}</Typography>
          <Typography variant="body2" color="text.secondary">{months} 个月 Premium · {price}</Typography>
        </Stack>
        <Stack direction="row" spacing={0.75} alignItems="flex-start" sx={{ mt: 2 }}>
          <LockOutlined sx={{ fontSize: 16, color: "text.secondary", mt: "3px" }} aria-hidden="true" />
          <Typography variant="caption" color="text.secondary">刷新页面不会退出排队；关闭或离开网站后会自动退出。生成链接不会扣款。</Typography>
        </Stack>
        {onCancel && <Button variant="outlined" fullWidth onClick={onCancel} disabled={cancelling} sx={{ mt: 2, minHeight: 44 }}>{cancelling ? "正在退出…" : "放弃排队"}</Button>}
      </CardContent>
    </Card>
  );
}
