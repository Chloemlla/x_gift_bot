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
