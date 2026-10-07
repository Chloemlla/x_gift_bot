import { useEffect, useState } from "react";
import { Alert, Box, Button } from "@mui/material";
import ReceiptLongOutlined from "@mui/icons-material/ReceiptLongOutlined";
import { request } from "./shared";

type OrderStatus = { username: string; months: number; created: number; state: "paid" | "open" | "ended" | "not_created"; expires_at?: number; stripe_checked?: boolean; message?: string };

function time(seconds: number) {
  return new Date(seconds * 1000).toLocaleString("zh-CN", { month: "numeric", day: "numeric", hour: "2-digit", minute: "2-digit" });
}

// Read-only: never joins the queue or creates an order. Only orders created in this browser are found.
export function PublicOrderLookup({ username }: { username: string }) {
  const [busy, setBusy] = useState(false);
  const [found, setFound] = useState<{ severity: "success" | "info" | "warning" | "error"; text: string } | null>(null);
  useEffect(() => setFound(null), [username]);
  const valid = /^[a-z0-9_]{1,15}$/.test(username);
  async function lookup() {
    setBusy(true);
    try {
      const { ok, data } = await request<OrderStatus>(`/api/manual-link/order?username=${encodeURIComponent(username)}`);
      if (!ok) { setFound({ severity: "info", text: data.message || "暂时无法查询，请稍后重试。" }); return; }
      const plan = `@${data.username} · ${data.months} 个月 Premium（${time(data.created)} 生成链接）`;
      setFound({
        paid: { severity: "success" as const, text: `${plan}：已确认付款。Premium 直接赠送到该账号，没有兑换码，可登录该账号在 X 的 Premium 页面查看。` },
        open: { severity: "info" as const, text: `${plan}：付款链接仍在 3 分钟有效期内，请回到本页的付款入口完成付款。` },
        ended: data.stripe_checked
          ? { severity: "warning" as const, text: `${plan}：链接已过期，Stripe 显示这笔订单未付款。需要时可重新排队获取新链接。` }
          : { severity: "warning" as const, text: `${plan}：链接已过期，暂时无法向 Stripe 核实付款结果。如已扣款，请登录该账号在 X 的 Premium 页面确认到账，切勿重复付款；可稍后再查询。` },
        not_created: { severity: "info" as const, text: `${plan}：未生成付款链接，没有产生付款。` },
      }[data.state] ?? { severity: "info", text: "暂时无法确认订单状态。" });
    } catch { setFound({ severity: "error", text: "连接中断，请稍后重试。" }); }
    finally { setBusy(false); }
  }
  return (
    <Box sx={{ mt: 2 }}>
      <Button size="small" startIcon={<ReceiptLongOutlined />} disabled={!valid || busy} onClick={() => void lookup()} sx={{ ml: -1 }}>
        {busy ? "正在查询…" : valid ? `查询 @${username} 的付款状态` : "填写用户名后可查询付款状态"}
      </Button>
      {found ? <Alert severity={found.severity} sx={{ mt: 1 }} role="status">{found.text}</Alert>
        : <Box component="p" sx={{ m: 0, mt: 0.5, color: "text.secondary", fontSize: 12 }}>只查询本浏览器生成过的订单，只向 Stripe 核实付款，不会排队或建单。</Box>}
    </Box>
  );
}
