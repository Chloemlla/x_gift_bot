import { useRef, useState } from "react";
import {
  Alert,
  Box,
  Button,
  Chip,
  Dialog,
  DialogActions,
  DialogContent,
  DialogContentText,
  DialogTitle,
  MenuItem,
  Paper,
  Stack,
  TextField,
  Typography,
} from "@mui/material";
import CreateNewFolderOutlined from "@mui/icons-material/CreateNewFolderOutlined";
import FolderOutlined from "@mui/icons-material/FolderOutlined";
import { adminApi, type AdminStats, type Folder } from "./adminApi";

type Props = {
  folders: Folder[];
  stats?: AdminStats;
  filter: string;
  disabled: boolean;
  onSelect: (id: string) => void;
  onBusyChange: (value: boolean) => void;
  onChanged: (deleted?: string) => Promise<void>;
};

export function FolderPanel({
  folders,
  stats,
  filter,
  disabled,
  onSelect,
  onBusyChange,
  onChanged,
}: Props) {
  const [action, setAction] = useState<"create" | "rename" | "delete" | null>(
    null,
  );
  const [target, setTarget] = useState<Folder | null>(null);
  const [name, setName] = useState("");
  const [error, setError] = useState("");
  const [pending, setPending] = useState(false);
  const inFlight = useRef(false);
  const createButton = useRef<HTMLButtonElement>(null);
  const [restoreToCreate, setRestoreToCreate] = useState(false);
  const selected = folders.find((folder) => folder.id === filter);
  function open(kind: "create" | "rename" | "delete") {
    setTarget(selected ?? null);
    setName(kind === "create" ? "" : (selected?.name ?? ""));
    setError("");
    setAction(kind);
  }
  async function save() {
    if (inFlight.current || !action) return;
    if (
      action !== "delete" &&
      (!name.trim() || new TextEncoder().encode(name.trim()).length > 120)
    ) {
      setError("请输入文件夹名称，最多 120 字节（约 40 个汉字）。");
      return;
    }
    if (action !== "create" && !target) return;
    inFlight.current = true;
    setPending(true);
    onBusyChange(true);
    setError("");
    try {
      if (action === "create")
        await adminApi("/api/admin/folders", { name: name.trim() });
      if (action === "rename")
        await adminApi("/api/admin/folders/rename", {
          id: target!.id,
          name: name.trim(),
        });
      if (action === "delete")
        await adminApi("/api/admin/folders/delete", { id: target!.id });
      await onChanged(action === "delete" ? target!.id : undefined);
      if (action === "delete") setRestoreToCreate(true);
      setAction(null);
    } catch (e) {
      setError(
        `${(e as Error).message} 若连接中断，请关闭后刷新列表核实结果。`,
      );
    } finally {
      inFlight.current = false;
      setPending(false);
      onBusyChange(false);
    }
  }
  return (
    <>
      <Paper variant="outlined" sx={{ p: { xs: 2.5, sm: 3 }, mb: 3 }}>
        <Stack
          direction="row"
          spacing={1}
          useFlexGap
          flexWrap="wrap"
          sx={{ mb: 3 }}
          aria-label="全部兑换码统计"
        >
          {[
            ["total", "全部"],
            ["active", "可使用"],
            ["succeeded", "已完成"],
            ["processing", "处理中"],
            ["review", "待核实"],
          ].map(([key, label]) => (
            <Chip
              key={key}
              size="small"
              variant="outlined"
              color={
                key === "review" && (stats?.review ?? 0) > 0
                  ? "warning"
                  : "default"
              }
              label={`${label} ${stats ? stats[key as keyof AdminStats] : "—"}`}
            />
          ))}
        </Stack>
        <Stack direction="row" alignItems="center" spacing={1} sx={{ mb: 2 }}>
          <FolderOutlined color="primary" />
          <Typography variant="h2">文件夹</Typography>
        </Stack>
        <Stack
          direction={{ xs: "column", sm: "row" }}
          spacing={2}
          alignItems={{ sm: "center" }}
        >
          <TextField
            select
            label="查看分类"
            slotProps={{
              select: { displayEmpty: true },
              inputLabel: { shrink: true },
            }}
            value={filter}
            disabled={disabled}
            onChange={(event) => onSelect(event.target.value)}
            sx={{ flex: 1, minWidth: 0 }}
          >
            <MenuItem value="">全部兑换码（{stats?.total ?? "—"}）</MenuItem>
            <MenuItem value="unfiled">
              未分类（{stats?.unfiled ?? "—"}）
            </MenuItem>
            {folders.map((folder) => (
              <MenuItem
                key={folder.id}
                value={folder.id}
                sx={{ whiteSpace: "normal", overflowWrap: "anywhere" }}
              >
                {folder.name}（{folder.count}）
              </MenuItem>
            ))}
          </TextField>
          <Stack
            direction="row"
            spacing={1}
            useFlexGap
            flexWrap="wrap"
            sx={{ flexShrink: 0 }}
          >
            <Button
              ref={createButton}
              startIcon={<CreateNewFolderOutlined />}
              onClick={() => open("create")}
              disabled={disabled}
              sx={{ px: 1.5 }}
            >
              新建
            </Button>
            <Button
              onClick={() => open("rename")}
              disabled={disabled || !selected}
              sx={{ px: 1.5 }}
            >
              重命名
            </Button>
            <Button
              color="error"
              onClick={() => open("delete")}
              disabled={disabled || !selected}
              sx={{ px: 1.5 }}
            >
              删除
            </Button>
          </Stack>
        </Stack>
      </Paper>
      <Dialog
        disableRestoreFocus={restoreToCreate}
        slotProps={{
          transition: {
            onExited: () => {
              if (restoreToCreate)
                createButton.current?.focus({ preventScroll: true });
              setRestoreToCreate(false);
            },
          },
        }}
        open={action !== null}
        onClose={() => {
          if (!pending) setAction(null);
        }}
        fullWidth
        maxWidth="xs"
        aria-labelledby="folder-dialog-title"
      >
        <Box
          component="form"
          onSubmit={(event) => {
            event.preventDefault();
            void save();
          }}
        >
          <DialogTitle id="folder-dialog-title">
            {action === "create"
              ? "新建文件夹"
              : action === "rename"
                ? "重命名文件夹"
                : "删除文件夹？"}
          </DialogTitle>
          <DialogContent>
            {action === "delete" ? (
              <DialogContentText>
                删除「{target?.name}
                」后，里面的兑换码会回到“未分类”。兑换码和订单记录都会保留。
              </DialogContentText>
            ) : (
              <TextField
                label="文件夹名称"
                autoFocus
                value={name}
                onChange={(event) => setName(event.target.value)}
                disabled={pending}
                required
                sx={{ mt: 1 }}
                helperText="最多 120 字节（约 40 个汉字）"
              />
            )}
            {error && (
              <Alert severity="error" sx={{ mt: 2 }}>
                {error}
              </Alert>
            )}
          </DialogContent>
          <DialogActions sx={{ p: 2 }}>
            <Button
              autoFocus={action === "delete"}
              disabled={pending}
              onClick={() => setAction(null)}
            >
              取消
            </Button>
            <Button
              type="submit"
              variant="contained"
              color={action === "delete" ? "error" : "primary"}
              disabled={pending}
            >
              {pending
                ? "正在保存…"
                : action === "delete"
                  ? "删除并保留兑换码"
                  : "保存"}
            </Button>
          </DialogActions>
        </Box>
      </Dialog>
    </>
  );
}
