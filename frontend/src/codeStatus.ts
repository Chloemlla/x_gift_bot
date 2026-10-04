export const codeStatus: Record<
  string,
  { label: string; color: "default" | "primary" | "success" | "warning" }
> = {
  active: { label: "可使用", color: "primary" },
  processing: { label: "处理中", color: "primary" },
  succeeded: { label: "已完成", color: "success" },
  review: { label: "待核实", color: "warning" },
  revoked: { label: "已停用", color: "default" },
};
