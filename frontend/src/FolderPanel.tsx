import { useState } from "react";
import {
  Alert,
  Box,
  Button,
  Dialog,
  DialogActions,
  DialogContent,
  DialogTitle,
  IconButton,
  Stack,
  TextField,
  Tooltip,
  Typography,
} from "@mui/material";
import FolderOutlined from "@mui/icons-material/FolderOutlined";
import EditOutlined from "@mui/icons-material/EditOutlined";
import { adminApi, type AdminStats, type Folder } from "./adminApi";

type Props = {
  folders: Folder[];
  stats?: AdminStats;
  filter: string;
  disabled: boolean;
  onSelect: (id: string) => void;
  onBusyChange: (value: boolean) => void;
  onChanged: () => Promise<void>;
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
  const [target, setTarget] = useState<Folder | null>(null),
    [name, setName] = useState(""),
    [error, setError] = useState(""),
    [pending, setPending] = useState(false);
  async function save() {
    if (!target || pending) return;
    if (!name.trim() || new TextEncoder().encode(name.trim()).length > 120) {
      setError("名称不能为空，最多 120 字节。");
      return;
    }
    setPending(true);
    onBusyChange(true);
    try {
      await adminApi("/api/admin/folders/rename", {
        id: target.id,
        name: name.trim(),
      });
      await onChanged();
      setTarget(null);
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setPending(false);
      onBusyChange(false);
    }
  }
  return (
    <Box sx={{ mb: 3 }}>
      <Stack
        direction="row"
        justifyContent="space-between"
        alignItems="baseline"
        sx={{ mb: 1.5 }}
      >
        <Typography variant="h2">批次</Typography>
        <Typography variant="body2" color="text.secondary">
          {stats?.total ?? "—"} 枚兑换码
        </Typography>
      </Stack>
      <Typography variant="body2" color="text.secondary" sx={{ mb: 2 }}>
        点击批次文件夹查看兑换码，左右滑动可查看全部批次。
      </Typography>
      <Stack
        direction="row"
        useFlexGap
        gap={1}
        sx={{
          minHeight: 80,
          overflowX: "auto",
          alignItems: "center",
          pb: 1,
        }}
        role="region"
        aria-label="批次文件夹，可横向滚动"
        tabIndex={0}
      >
        {[
          { id: "", name: "全部兑换码", count: stats?.total ?? 0 },
          ...(stats?.unfiled
            ? [{ id: "unfiled", name: "未分类", count: stats.unfiled }]
            : []),
          ...folders,
        ].map((folder) => (
          <Box
            key={folder.id}
            sx={{
              display: "flex",
              border: 1,
              borderColor: filter === folder.id ? "primary.main" : "divider",
              borderRadius: 2,
              bgcolor:
                filter === folder.id ? "action.selected" : "background.paper",
              maxWidth: "100%",
              flexShrink: 0,
            }}
          >
            <Button
              disabled={disabled}
              aria-pressed={filter === folder.id}
              startIcon={<FolderOutlined />}
              onClick={() => onSelect(folder.id)}
              sx={{
                px: 2,
                py: 1.5,
                justifyContent: "flex-start",
                textAlign: "left",
                overflowWrap: "anywhere",
                minWidth: 0,
              }}
            >
              {folder.name} · {folder.count}
            </Button>
            {folder.id && folder.id !== "unfiled" && (
              <Tooltip describeChild title={`重命名批次：${folder.name}`}>
                <span>
                  <IconButton
                    aria-label={`重命名批次：${folder.name}`}
                    disabled={disabled}
                    onClick={() => {
                      setTarget(folder);
                      setName(folder.name);
                      setError("");
                    }}
                    sx={{
                      m: 0.5,
                      border: 1,
                      borderColor: "divider",
                      width: 44,
                      height: 44,
                    }}
                  >
                    <EditOutlined fontSize="small" />
                  </IconButton>
                </span>
              </Tooltip>
            )}
          </Box>
        ))}
      </Stack>
      <Dialog
        open={!!target}
        onClose={() => !pending && setTarget(null)}
        fullWidth
        maxWidth="xs"
        aria-labelledby="batch-rename-title"
        aria-describedby="batch-rename-content"
      >
        <Box
          component="form"
          noValidate
          onSubmit={(e) => {
            e.preventDefault();
            void save();
          }}
        >
          <DialogTitle id="batch-rename-title">重命名批次</DialogTitle>
          <DialogContent id="batch-rename-content">
            <TextField
              autoFocus
              label="批次名称"
              value={name}
              onChange={(e) => {
                setName(e.target.value);
                if (error) setError("");
              }}
              disabled={pending}
              required
              error={!!error}
              sx={{ mt: 1 }}
              helperText="该批次内的兑换码会同步更新名称"
            />
            {error && (
              <Alert severity="error" role="alert" sx={{ mt: 2 }}>
                {error}
              </Alert>
            )}
          </DialogContent>
          <DialogActions sx={{ p: 2 }}>
            <Button
              variant="outlined"
              disabled={pending}
              onClick={() => setTarget(null)}
            >
              取消
            </Button>
            <Button variant="contained" disabled={pending} type="submit">
              保存
            </Button>
          </DialogActions>
        </Box>
      </Dialog>
    </Box>
  );
}
