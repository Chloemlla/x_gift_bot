import { useCallback, useEffect, useRef, useState } from "react";
import {
  Alert,
  Box,
  Button,
  Chip,
  Checkbox,
  Dialog,
  DialogActions,
  DialogContent,
  DialogContentText,
  DialogTitle,
  LinearProgress,
  IconButton,
  Tooltip,
  Snackbar,
  MenuItem,
  Paper,
  Stack,
  Table,
  TableBody,
  TableCell,
  TableContainer,
  TableHead,
  TableRow,
  TextField,
  Typography,
} from "@mui/material";
import BlockOutlined from "@mui/icons-material/BlockOutlined";
import ContentCopyOutlined from "@mui/icons-material/ContentCopyOutlined";
import DriveFileMoveOutlined from "@mui/icons-material/DriveFileMoveOutlined";
import AddRounded from "@mui/icons-material/AddRounded";
import DownloadRounded from "@mui/icons-material/DownloadRounded";
import RefreshRounded from "@mui/icons-material/RefreshRounded";
import ConfirmationNumberOutlined from "@mui/icons-material/ConfirmationNumberOutlined";
import { mount, Shell } from "./shared";
import { AppearanceMenu } from "./AppearanceMenu";
import { CopyableCodes } from "./CopyableCodes";
import { adminApi as api, type AdminStats, type Folder } from "./adminApi";
import { FolderPanel } from "./FolderPanel";
import { FilterBar } from "./FilterBar";
import { parseFilter } from "./filter";
import { StatsPanel } from "./StatsPanel";

type Code = {
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
};
type Listing = {
  folder: string;
  folders: Folder[];
  stats: AdminStats;
  codes: Code[];
  page: number;
  has_more: boolean;
  payments_enabled: boolean;
};
type Generated = {
  codes: string[];
  batch: string;
  months: number;
  folder: string;
};
type Confirmation = { kind: "revoke"; code: Code } | null;
const statuses: Record<
  string,
  { label: string; color: "default" | "primary" | "success" | "warning" }
> = {
  active: { label: "可使用", color: "primary" },
  processing: { label: "处理中", color: "primary" },
  succeeded: { label: "已完成", color: "success" },
  review: { label: "待核实", color: "warning" },
  revoked: { label: "已停用", color: "default" },
};
function Admin() {
  const [listing, setListing] = useState<Listing | null>(null);
  const [loading, setLoading] = useState(true);
  const [listError, setListError] = useState("");
  const [busy, setBusy] = useState(false);
  const [months, setMonths] = useState(6);
  const [count, setCount] = useState("10");
  const [batch, setBatch] = useState("");
  const [copyNotice, setCopyNotice] = useState("");
  const copying = useRef(false);
  const [selectedIDs, setSelectedIDs] = useState<string[]>([]);
  const [moveOpen, setMoveOpen] = useState(false);
  const [moveTarget, setMoveTarget] = useState("");
  const filterRef = useRef("");
  const [expression, setExpression] = useState("");
  const [filterError, setFilterError] = useState("");
  const [activeFilter, setActiveFilter] = useState<{
    source: string;
    match: (code: Code) => boolean;
  } | null>(null);
  const [allCodes, setAllCodes] = useState<Code[] | null>(null);
  const [filterPage, setFilterPage] = useState(0);
  const [generated, setGenerated] = useState<Generated | null>(null);
  const [notice, setNotice] = useState<{ text: string; error: boolean } | null>(
    null,
  );
  const [confirmation, setConfirmation] = useState<Confirmation>(null);
  const [batchError, setBatchError] = useState(false);
  const [countError, setCountError] = useState(false);
  const [focusTarget, setFocusTarget] = useState<"form" | "list" | null>(null);
  const [statsSignal, setStatsSignal] = useState(0);
  const listSequence = useRef(0);
  const mutation = useRef(false);
  const form = useRef<HTMLFormElement>(null);
  const generatedPanel = useRef<HTMLDivElement>(null);
  const refreshButton = useRef<HTMLButtonElement>(null);
  const refresh = useCallback(
    async (page: number, folder = filterRef.current) => {
      const sequence = ++listSequence.current;
      setLoading(true);
      setListError("");
      try {
        const data = await api<Listing>(
          `/api/admin/codes?page=${page}&folder=${encodeURIComponent(folder)}`,
        );
        if (sequence === listSequence.current) {
          setListing(data);
          filterRef.current = data.folder;
          setSelectedIDs([]);
          // The panel fetches on mount; only nudge it for later refreshes.
          if (sequence > 1) setStatsSignal((value) => value + 1);
        }
      } catch (error) {
        if (sequence === listSequence.current)
          setListError((error as Error).message);
      } finally {
        if (sequence === listSequence.current) setLoading(false);
      }
    },
    [],
  );
  useEffect(() => {
    void refresh(0);
  }, [refresh]);

  // Filter mode: parse the expression, fetch every page client-side, then
  // evaluate the predicate locally. Shares listSequence with refresh() so
  // overlapping loads from either mode cancel each other.
  const applyFilter = useCallback(async (source: string) => {
    let match: (code: Code) => boolean;
    try {
      match = parseFilter(source);
    } catch (error) {
      setFilterError((error as Error).message);
      return;
    }
    setFilterError("");
    const sequence = ++listSequence.current;
    setLoading(true);
    setListError("");
    try {
      const codes: Code[] = [];
      let page = 0;
      for (;;) {
        const data = await api<Listing>(`/api/admin/codes?page=${page}`);
        if (sequence !== listSequence.current) return;
        codes.push(...data.codes);
        if (!data.has_more) break;
        page++;
      }
      setAllCodes(codes);
      setActiveFilter({ source, match });
      setFilterPage(0);
      setSelectedIDs([]);
    } catch (error) {
      if (sequence === listSequence.current)
        setListError((error as Error).message);
    } finally {
      if (sequence === listSequence.current) setLoading(false);
    }
  }, []);

  function clearFilter(reload = true) {
    setActiveFilter(null);
    setAllCodes(null);
    setExpression("");
    setFilterError("");
    setFilterPage(0);
    setSelectedIDs([]);
    if (reload) void refresh(0);
  }

  function reloadCurrent() {
    if (activeFilter) void applyFilter(activeFilter.source);
    else void refresh(listing?.page ?? 0);
  }

  useEffect(() => {
    if (!focusTarget || confirmation || busy || loading) return;
    // Restore to a surviving control after the dialog's exit transition.
    const timer = setTimeout(() => {
      if (focusTarget === "list") refreshButton.current?.focus();
      else form.current?.querySelector<HTMLElement>("[role=combobox]")?.focus();
      setFocusTarget(null);
    }, 250);
    return () => clearTimeout(timer);
  }, [focusTarget, confirmation, busy, loading]);

  async function copyRow(code: Code) {
    if (!code.copyable) {
      setCopyNotice("历史兑换码未保存完整内容，请使用原先下载的 TXT。");
      return;
    }
    if (copying.current) return;
    copying.current = true;
    try {
      const result = api<{ code: string }>("/api/admin/codes/copy", {
        id: code.id,
      });
      // Start the clipboard operation within the user's gesture (including Safari).
      if (typeof ClipboardItem !== "undefined" && navigator.clipboard?.write) {
        await navigator.clipboard.write([
          new ClipboardItem({
            "text/plain": result.then(
              (data) => new Blob([data.code], { type: "text/plain" }),
            ),
          }),
        ]);
      } else {
        await navigator.clipboard.writeText((await result).code);
      }
      setCopyNotice(`已复制尾号 ${code.hint} 的兑换码。`);
    } catch {
      setCopyNotice("复制失败，请检查剪贴板权限或稍后重试。");
    } finally {
      copying.current = false;
    }
  }
  async function generate() {
    if (mutation.current) return;
    mutation.current = true;
    setBusy(true);
    setConfirmation(null);
    setNotice(null);
    try {
      const data = await api<Generated>("/api/admin/codes", {
        months,
        count: Number(count),
        batch: batch.trim(),
      });
      setGenerated(data);
      setNotice({
        text: `已生成 ${data.codes.length} 枚兑换码，已归入批次「${data.batch}」。点击列表条目即可复制。`,
        error: false,
      });
      requestAnimationFrame(() =>
        generatedPanel.current?.scrollIntoView({ block: "nearest" }),
      );
      if (activeFilter) reloadCurrent();
      else await refresh(0, data.folder || "unfiled");
    } catch (error) {
      setNotice({
        text: `${(error as Error).message} 若连接中断，请先刷新列表核实批次，不要立即重复生成。`,
        error: true,
      });
    } finally {
      mutation.current = false;
      setBusy(false);
    }
  }
  async function confirm() {
    if (confirmation?.kind !== "revoke" || mutation.current) return;
    mutation.current = true;
    setBusy(true);
    try {
      await api("/api/admin/revoke", { id: confirmation.code.id });
      setNotice({ text: "兑换码已停用。", error: false });
      setFocusTarget("list");
      setConfirmation(null);
      reloadCurrent();
    } catch (error) {
      setNotice({ text: (error as Error).message, error: true });
      setConfirmation(null);
    } finally {
      mutation.current = false;
      setBusy(false);
    }
  }
  async function moveSelected() {
    if (mutation.current || !selectedIDs.length) return;
    mutation.current = true;
    setBusy(true);
    setNotice(null);
    try {
      await api("/api/admin/codes/move", {
        ids: selectedIDs,
        folder: moveTarget,
      });
      setNotice({
        text: `已移动 ${selectedIDs.length} 枚兑换码。`,
        error: false,
      });
      setMoveOpen(false);
      setFocusTarget("list");
      reloadCurrent();
    } catch (e) {
      setMoveOpen(false);
      setNotice({
        text: `${(e as Error).message} 若连接中断，请刷新列表核实。`,
        error: true,
      });
    } finally {
      mutation.current = false;
      setBusy(false);
    }
  }
  function download() {
    if (!generated) return;
    const url = URL.createObjectURL(
      new Blob([generated.codes.join("\n") + "\n"], {
        type: "text/plain;charset=utf-8",
      }),
    );
    const link = document.createElement("a");
    link.href = url;
    link.download = `xgift-${generated.months}mo-${Date.now()}.txt`;
    link.click();
    setTimeout(() => URL.revokeObjectURL(url), 1000);
  }

  const matched =
    activeFilter && allCodes ? allCodes.filter(activeFilter.match) : null;
  const filterTotal = matched?.length ?? 0;
  const filterPages = Math.max(1, Math.ceil(filterTotal / 100));
  const safeFilterPage = Math.min(filterPage, filterPages - 1);
  const visibleCodes = matched
    ? matched.slice(safeFilterPage * 100, safeFilterPage * 100 + 100)
    : (listing?.codes ?? []);

  return (
    <Shell admin>
      <Stack
        direction={{ xs: "column", sm: "row" }}
        justifyContent="space-between"
        alignItems={{ xs: "start", sm: "center" }}
        spacing={2}
        sx={{ mb: 4 }}
      >
        <Box>
          <Typography
            variant="h1"
            sx={{ fontSize: { xs: 30, sm: 36 }, mt: 0.5 }}
          >
            兑换码管理
          </Typography>
          <Typography color="text.secondary" sx={{ mt: 1 }}>
            生成兑换码、查看兑换状态和管理批次。
          </Typography>
        </Box>
        <Stack direction="row" alignItems="center" spacing={1}>
          <Chip
            variant="outlined"
            color={listing?.payments_enabled ? "success" : "default"}
            label={
              listing
                ? listing.payments_enabled
                  ? "充值已开放"
                  : "充值暂停 · 不会付款"
                : "正在获取服务状态"
            }
          />
          <AppearanceMenu />
        </Stack>
      </Stack>
      <StatsPanel refreshSignal={statsSignal} />
      <FolderPanel
        folders={listing?.folders ?? []}
        stats={listing?.stats}
        filter={listing?.folder ?? ""}
        disabled={busy || loading}
        muted={!!activeFilter}
        onSelect={(folder) => {
          // Clicking a folder chip while the expression filter is active
          // exits filter mode and returns to normal folder filtering.
          if (activeFilter) clearFilter(false);
          void refresh(0, folder);
        }}
        onBusyChange={(value) => {
          mutation.current = value;
          setBusy(value);
        }}
        onChanged={async (deleted) => {
          if (deleted === filterRef.current) filterRef.current = "";
          if (activeFilter) reloadCurrent();
          else await refresh(0);
        }}
      />
      {activeFilter && (
        <Typography
          variant="body2"
          color="text.secondary"
          sx={{ mt: -1.5, mb: 3 }}
        >
          表达式筛选生效中，点击批次文件夹可清除筛选并查看该批次。
        </Typography>
      )}
      <FilterBar
        value={expression}
        onChange={(value) => {
          setExpression(value);
          if (filterError) setFilterError("");
        }}
        onApply={() => {
          if (expression.trim()) void applyFilter(expression.trim());
        }}
        onClear={() => clearFilter()}
        applied={!!activeFilter}
        error={filterError}
        disabled={busy || loading}
        folders={listing?.folders ?? []}
      />
      <Paper variant="outlined" sx={{ p: { xs: 2.5, sm: 3 }, mb: 3 }}>
        <Stack direction="row" spacing={1} alignItems="center" sx={{ mb: 1 }}>
          <ConfirmationNumberOutlined color="primary" />
          <Typography variant="h2">生成兑换码</Typography>
        </Stack>
        <Typography variant="body2" color="text.secondary" sx={{ mb: 3 }}>
          每批最多 500 枚。同名批次自动归入同一个文件夹。
        </Typography>
        <Box
          component="form"
          ref={form}
          noValidate
          onSubmit={(event) => {
            event.preventDefault();
            const invalidBatch =
              new TextEncoder().encode(batch.trim()).length > 120;
            const invalidCount =
              !Number.isInteger(Number(count)) ||
              Number(count) < 1 ||
              Number(count) > 500;
            setBatchError(invalidBatch);
            setCountError(invalidCount);
            if (
              invalidBatch ||
              invalidCount ||
              !form.current!.reportValidity() ||
              busy
            )
              return;
            void generate();
          }}
        >
          <Box
            sx={{
              display: "grid",
              gridTemplateColumns: {
                xs: "1fr",
                sm: "1fr 1fr",
                md: "1fr 1fr 2fr",
              },
              gap: 2,
              alignItems: "start",
            }}
          >
            <TextField
              select
              label="套餐时长"
              value={months}
              onChange={(event) => setMonths(Number(event.target.value))}
              disabled={busy}
              helperText="绑定后不可更改"
            >
              <MenuItem value={3}>3 个月 Premium</MenuItem>
              <MenuItem value={6}>6 个月 Premium</MenuItem>
            </TextField>
            <TextField
              label="生成数量"
              type="number"
              required
              value={count}
              onChange={(event) => setCount(event.target.value)}
              disabled={busy}
              error={countError}
              helperText={
                countError ? "请输入 1–500 之间的整数" : "每批 1–500 枚"
              }
              slotProps={{ htmlInput: { min: 1, max: 500, step: 1 } }}
            />
            <TextField
              label="批次名称（可选）"
              placeholder="例如：十月赠礼"
              value={batch}
              onChange={(event) => setBatch(event.target.value)}
              disabled={busy}
              error={batchError}
              helperText={
                batchError
                  ? "批次名称不能超过 120 字节（约 40 个汉字）"
                  : "批次名称就是文件夹名称，留空自动命名"
              }
            />
            <Button
              type="submit"
              variant="contained"
              startIcon={<AddRounded />}
              disabled={busy || loading || !!listError}
              sx={{ gridColumn: { sm: "1 / -1" }, justifySelf: { sm: "end" } }}
            >
              {busy ? "正在处理…" : "生成兑换码"}
            </Button>
          </Box>
        </Box>
      </Paper>
      {notice && (
        <Alert
          severity={notice.error ? "error" : "success"}
          role="status"
          sx={{ mb: 3 }}
        >
          {notice.text}
        </Alert>
      )}
      {generated && (
        <Paper
          ref={generatedPanel}
          variant="outlined"
          sx={{ p: 3, mb: 3, borderColor: "primary.main" }}
        >
          <Typography variant="h2">本批兑换码</Typography>
          <Typography
            variant="body2"
            color="text.secondary"
            sx={{ my: 1, overflowWrap: "anywhere" }}
          >
            {generated.batch} · {generated.codes.length} 枚 · {generated.months}{" "}
            个月
          </Typography>
          <CopyableCodes key={generated.codes[0]} codes={generated.codes} />
          <Stack direction={{ xs: "column", sm: "row" }} spacing={1.5}>
            <Button
              variant="contained"
              startIcon={<DownloadRounded />}
              onClick={download}
            >
              下载 TXT
            </Button>
            <Button
              variant="outlined"
              disabled={busy}
              onClick={() => setGenerated(null)}
            >
              收起本批兑换码
            </Button>
          </Stack>
        </Paper>
      )}
      <Paper variant="outlined" sx={{ overflow: "hidden" }}>
        <Stack
          direction="row"
          justifyContent="space-between"
          alignItems="center"
          sx={{ px: { xs: 2, sm: 3 }, py: 2 }}
        >
          <Box>
            <Typography variant="h2">
              {matched
                ? "表达式筛选结果"
                : listing?.folder === "unfiled"
                  ? "未命名批次"
                  : (listing?.folders.find(
                      (folder) => folder.id === listing.folder,
                    )?.name ?? "全部兑换码")}
            </Typography>
            <Typography variant="body2" color="text.secondary">
              点击条目复制兑换码 · 每页最多 100 条
            </Typography>
          </Box>
          <Tooltip describeChild title="刷新列表">
            <span>
              <IconButton
                ref={refreshButton}
                aria-label="刷新列表"
                disabled={loading || busy}
                onClick={() => reloadCurrent()}
                sx={{
                  border: 1,
                  borderColor: "divider",
                  width: 44,
                  height: 44,
                }}
              >
                <RefreshRounded />
              </IconButton>
            </span>
          </Tooltip>
        </Stack>
        {loading && <LinearProgress aria-label="正在加载兑换码" />}
        {listError && (
          <Alert severity="error" sx={{ m: 2 }}>
            {listError}
            {(listing || allCodes) && " 以下保留上次加载的数据。"}
            <Button onClick={() => reloadCurrent()}>重新加载</Button>
          </Alert>
        )}
        <Typography
          variant="caption"
          color="text.secondary"
          sx={{ display: { xs: "block", md: "none" }, px: 2, pb: 1 }}
        >
          左右滑动表格，可查看账号和操作。
        </Typography>
        {selectedIDs.length > 0 && (
          <Stack
            direction="row"
            justifyContent="space-between"
            alignItems="center"
            spacing={1}
            sx={{ px: 2, py: 1, bgcolor: "action.selected" }}
          >
            <Typography variant="body2">
              已选择 {selectedIDs.length} 枚
            </Typography>
            <Button
              color="secondary"
              variant="outlined"
              startIcon={<DriveFileMoveOutlined />}
              disabled={busy || loading || !!listError}
              onClick={() => {
                setMoveTarget("");
                setMoveOpen(true);
              }}
            >
              移动到批次
            </Button>
          </Stack>
        )}
        <TableContainer
          tabIndex={0}
          role="region"
          aria-label="兑换码列表，可横向滚动"
          sx={{ "&:focus-visible": { outlineOffset: -3 } }}
        >
          <Table sx={{ minWidth: 750 }} aria-label="兑换码列表">
            <TableHead sx={{ bgcolor: "background.default" }}>
              <TableRow>
                <TableCell padding="checkbox">
                  <Checkbox
                    slotProps={{
                      input: { "aria-label": "选择本页全部兑换码" },
                    }}
                    disabled={
                      busy || loading || !!listError || !visibleCodes.length
                    }
                    checked={
                      !!visibleCodes.length &&
                      selectedIDs.length === visibleCodes.length
                    }
                    indeterminate={
                      selectedIDs.length > 0 &&
                      selectedIDs.length < visibleCodes.length
                    }
                    onChange={(event) =>
                      setSelectedIDs(
                        event.target.checked
                          ? visibleCodes.map((code) => code.id)
                          : [],
                      )
                    }
                  />
                </TableCell>
                {[
                  "兑换码 / 批次",
                  "套餐",
                  "状态",
                  "接收账号",
                  "生成时间",
                  "操作",
                ].map((label) => (
                  <TableCell
                    key={label}
                    sx={{ fontWeight: 600, whiteSpace: "nowrap" }}
                  >
                    {label}
                  </TableCell>
                ))}
              </TableRow>
            </TableHead>
            <TableBody>
              {visibleCodes.map((code) => (
                <TableRow
                  key={code.id}
                  hover
                  selected={selectedIDs.includes(code.id)}
                  onClick={() => {
                    if (!busy && !loading && !listError) void copyRow(code);
                  }}
                  sx={{ cursor: code.copyable ? "pointer" : "default" }}
                >
                  <TableCell
                    padding="checkbox"
                    onClick={(e) => e.stopPropagation()}
                  >
                    <Checkbox
                      slotProps={{
                        input: {
                          "aria-label": `选择尾号 ${code.hint} 的兑换码`,
                        },
                      }}
                      checked={selectedIDs.includes(code.id)}
                      disabled={busy || loading || !!listError}
                      onChange={(event) =>
                        setSelectedIDs((ids) =>
                          event.target.checked
                            ? [...ids, code.id]
                            : ids.filter((id) => id !== code.id),
                        )
                      }
                    />
                  </TableCell>
                  <TableCell sx={{ maxWidth: 260 }}>
                    <Typography
                      variant="body2"
                      sx={{ fontFamily: "monospace", fontWeight: 600 }}
                    >
                      …{code.hint}
                    </Typography>
                    <Typography
                      variant="caption"
                      color="text.secondary"
                      sx={{ overflowWrap: "anywhere" }}
                    >
                      {code.batch}
                    </Typography>
                    <Typography
                      variant="caption"
                      component="p"
                      color="text.secondary"
                    >
                      {code.copyable ? "点击复制" : "历史码未保存"}
                    </Typography>
                  </TableCell>
                  <TableCell sx={{ whiteSpace: "nowrap" }}>
                    {code.months} 个月
                  </TableCell>
                  <TableCell sx={{ minWidth: 100, maxWidth: 240 }}>
                    <Chip
                      size="small"
                      variant="outlined"
                      color={statuses[code.status]?.color ?? "default"}
                      label={statuses[code.status]?.label ?? code.status}
                    />
                    {code.message && (
                      <Typography
                        variant="caption"
                        color="text.secondary"
                        component="p"
                        sx={{ mt: 1, overflowWrap: "anywhere" }}
                      >
                        {code.message}
                      </Typography>
                    )}
                  </TableCell>
                  <TableCell>
                    {code.username ? `@${code.username}` : "—"}
                  </TableCell>
                  <TableCell>
                    <Typography variant="body2" sx={{ whiteSpace: "nowrap" }}>
                      {new Date(code.created * 1000).toLocaleDateString(
                        "zh-CN",
                      )}
                    </Typography>
                    <Typography variant="caption" color="text.secondary">
                      {new Date(code.created * 1000).toLocaleTimeString(
                        "zh-CN",
                      )}
                    </Typography>
                  </TableCell>
                  <TableCell onClick={(e) => e.stopPropagation()}>
                    <Stack direction="row" spacing={1}>
                      <Tooltip
                        describeChild
                        title={
                          code.copyable
                            ? "复制完整兑换码"
                            : "历史码未保存完整内容，请使用原 TXT"
                        }
                      >
                        <span>
                          <IconButton
                            aria-label={`复制尾号 ${code.hint} 的兑换码`}
                            disabled={
                              !code.copyable || busy || loading || !!listError
                            }
                            onClick={() => void copyRow(code)}
                            sx={{
                              border: 1,
                              borderColor: "divider",
                              width: 44,
                              height: 44,
                            }}
                          >
                            <ContentCopyOutlined fontSize="small" />
                          </IconButton>
                        </span>
                      </Tooltip>
                      {code.status === "active" && (
                        <Tooltip describeChild title="停用兑换码">
                          <span>
                            <IconButton
                              color="error"
                              disabled={busy || loading || !!listError}
                              aria-label={`停用尾号 ${code.hint} 的兑换码`}
                              onClick={() =>
                                setConfirmation({ kind: "revoke", code })
                              }
                              sx={{
                                border: 1,
                                borderColor: "divider",
                                width: 44,
                                height: 44,
                              }}
                            >
                              <BlockOutlined fontSize="small" />
                            </IconButton>
                          </span>
                        </Tooltip>
                      )}
                    </Stack>
                  </TableCell>
                </TableRow>
              ))}
              {!loading && !listError && !visibleCodes.length && (
                <TableRow>
                  <TableCell colSpan={7} align="center" sx={{ py: 7 }}>
                    <ConfirmationNumberOutlined
                      sx={{ fontSize: 40, color: "text.secondary", mb: 1 }}
                    />
                    {matched ? (
                      <>
                        <Typography fontWeight={600}>
                          没有符合筛选条件的兑换码
                        </Typography>
                        <Typography variant="body2" color="text.secondary">
                          请调整筛选表达式，或清除筛选查看全部兑换码。
                        </Typography>
                      </>
                    ) : (
                      <>
                        <Typography fontWeight={600}>
                          当前分类没有兑换码
                        </Typography>
                        <Typography variant="body2" color="text.secondary">
                          可在上方生成兑换码，或从其他分类移动到这里。
                        </Typography>
                      </>
                    )}
                  </TableCell>
                </TableRow>
              )}
            </TableBody>
          </Table>
        </TableContainer>
        <Stack
          direction={{ xs: "column", sm: "row" }}
          justifyContent="space-between"
          alignItems={{ xs: "start", sm: "center" }}
          spacing={1}
          sx={{ p: 2, borderTop: 1, borderColor: "divider" }}
        >
          {matched ? (
            <>
              <Stack direction="row" spacing={1.5} alignItems="center">
                <Typography variant="body2" color="text.secondary">
                  {`筛选 ${filterTotal} / 共 ${allCodes?.length ?? 0} 条 · 第 ${safeFilterPage + 1} / ${filterPages} 页`}
                </Typography>
                <Button
                  variant="outlined"
                  disabled={loading || busy}
                  onClick={() => clearFilter()}
                  sx={{ px: 1.5, minHeight: 36 }}
                >
                  清除筛选
                </Button>
              </Stack>
              <Stack direction="row" spacing={1}>
                <Button
                  disabled={loading || busy || safeFilterPage === 0}
                  onClick={() => setFilterPage(safeFilterPage - 1)}
                  sx={{ px: 1.5 }}
                >
                  上一页
                </Button>
                <Button
                  disabled={loading || busy || safeFilterPage >= filterPages - 1}
                  onClick={() => setFilterPage(safeFilterPage + 1)}
                  sx={{ px: 1.5 }}
                >
                  下一页
                </Button>
              </Stack>
            </>
          ) : (
            <>
              <Typography variant="body2" color="text.secondary">
                {listing?.folder
                  ? `第 ${listing.page + 1} 页`
                  : `共 ${listing?.stats.total ?? 0} 条 · 第 ${(listing?.page ?? 0) + 1} / ${Math.max(1, Math.ceil((listing?.stats.total ?? 0) / 100))} 页`}
              </Typography>
              <Stack direction="row" spacing={1}>
                <Button
                  disabled={loading || busy || !listing?.page}
                  onClick={() => void refresh((listing?.page ?? 0) - 1)}
                  sx={{ px: 1.5 }}
                >
                  上一页
                </Button>
                <Button
                  disabled={loading || busy || !listing?.has_more}
                  onClick={() => void refresh((listing?.page ?? 0) + 1)}
                  sx={{ px: 1.5 }}
                >
                  下一页
                </Button>
              </Stack>
            </>
          )}
        </Stack>
      </Paper>
      <Dialog
        open={moveOpen}
        disableRestoreFocus={focusTarget !== null}
        onClose={() => {
          if (!busy) setMoveOpen(false);
        }}
        fullWidth
        maxWidth="xs"
        aria-labelledby="move-dialog-title"
      >
        <DialogTitle id="move-dialog-title">
          移动 {selectedIDs.length} 枚兑换码
        </DialogTitle>
        <DialogContent>
          <TextField
            select
            label="目标批次"
            value={moveTarget}
            disabled={busy}
            onChange={(event) => setMoveTarget(event.target.value)}
            sx={{ mt: 1 }}
          >
            <MenuItem value="">未命名批次</MenuItem>
            {(listing?.folders ?? []).map((folder) => (
              <MenuItem
                value={folder.id}
                key={folder.id}
                sx={{ whiteSpace: "normal" }}
              >
                {folder.name}
              </MenuItem>
            ))}
          </TextField>
          <DialogContentText sx={{ mt: 2 }}>
            只修改归属分类，不改变兑换码、账号和订单状态。
          </DialogContentText>
        </DialogContent>
        <DialogActions sx={{ p: 2 }}>
          <Button autoFocus disabled={busy} onClick={() => setMoveOpen(false)}>
            取消
          </Button>
          <Button
            variant="contained"
            disabled={busy || !selectedIDs.length}
            onClick={() => void moveSelected()}
          >
            {busy ? "正在移动…" : "确认移动"}
          </Button>
        </DialogActions>
      </Dialog>
      <Dialog
        disableRestoreFocus={focusTarget !== null}
        open={!!confirmation}
        onClose={() => {
          if (!busy) setConfirmation(null);
        }}
        aria-labelledby="admin-dialog-title"
        maxWidth="xs"
        fullWidth
      >
        <DialogTitle id="admin-dialog-title">停用这枚兑换码？</DialogTitle>
        <DialogContent>
          <DialogContentText>
            {`尾号 ${confirmation?.code.hint ?? ""} 的兑换码将无法使用。此操作不可撤销。`}
          </DialogContentText>
        </DialogContent>
        <DialogActions sx={{ p: 2 }}>
          <Button
            onClick={() => setConfirmation(null)}
            disabled={busy}
            autoFocus
          >
            取消
          </Button>
          <Button
            variant="contained"
            color="error"
            disabled={busy}
            onClick={() => void confirm()}
          >
            {busy ? "正在处理…" : "确认停用"}
          </Button>
        </DialogActions>
      </Dialog>
      <Snackbar
        open={!!copyNotice}
        autoHideDuration={4000}
        onClose={() => setCopyNotice("")}
        message={copyNotice}
      />
    </Shell>
  );
}

mount(<Admin />);
