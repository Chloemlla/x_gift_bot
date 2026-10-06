export type PaymentPlan = { months: number; amount: number; currency: string };
export type PaymentResult = PaymentPlan & { username: string; status: "created" | "requires_action" | "succeeded"; checkout_url?: string };

// A 2xx response may only acknowledge queue submission. Never render it as an order.
export function isPaymentResult(value: unknown, username: string, plan: PaymentPlan): value is PaymentResult {
  if (!value || typeof value !== "object") return false;
  const r = value as Record<string, unknown>;
  if (r.username !== username || !Number.isSafeInteger(r.months) || (r.months as number) < 1 || (r.months as number) > 24 || !Number.isSafeInteger(r.amount) || (r.amount as number) <= 0 || typeof r.currency !== "string" || !/^[A-Z]{3}$/.test(r.currency)) return false;
  if (r.status === "succeeded") return true;
  if ((r.status !== "created" && r.status !== "requires_action") || r.months !== plan.months || r.amount !== plan.amount || r.currency !== plan.currency || typeof r.checkout_url !== "string") return false;
  try {
    const url = new URL(r.checkout_url);
    return url.protocol === "https:" && url.hostname === "checkout.stripe.com" && !url.port && !url.username && !url.password && /^\/(?:c\/)?pay\/cs_(?:live|test)_[A-Za-z0-9]+$/.test(url.pathname);
  } catch { return false; }
}
