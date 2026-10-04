import { request } from "./shared";
export async function adminApi<T>(path: string, body?: unknown): Promise<T> {
  const { ok, data } = await request<T & { message?: string }>(path, body);
  if (!ok) throw new Error(data.message || "请求失败，请稍后重试。");
  return data;
}
export type Folder = { id: string; name: string; count: number };
export type AdminStats = {
  total: number;
  active: number;
  processing: number;
  succeeded: number;
  review: number;
  revoked: number;
  unfiled: number;
};
export type AdminStatsDetail = {
  codes: AdminStats & { redeemed: number };
  rates: { redeemed: number; success: number };
  months: { months: number; total: number; succeeded: number }[];
  daily: { date: string; created: number; redeemed: number; succeeded: number }[];
  review_stages: { progress: number; count: number }[];
};
export function stats() {
  return adminApi<AdminStatsDetail>("/api/admin/stats");
}
export type AdminCode = {
  copyable: boolean;
  folder: string;
  id: string;
  hint: string;
  batch: string;
  months: number;
  status: string;
  username: string;
  message: string;
  created: number;
  updated?: number;
  progress?: number;
};
// 全站统一的时间呈现:YYYY-MM-DD HH:mm:ss(与统计表日期风格一致)。
export function formatTime(seconds: number): string {
  const d = new Date(seconds * 1000);
  const p = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`;
}
