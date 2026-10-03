import { useRef } from "react";
import {
  Alert,
  Box,
  Button,
  Chip,
  Paper,
  Stack,
  TextField,
  Typography,
} from "@mui/material";
import FilterAltOutlined from "@mui/icons-material/FilterAltOutlined";
import type { Folder } from "./adminApi";
import { FILTER_HINT } from "./filter";

type Props = {
  value: string;
  onChange: (value: string) => void;
  onApply: () => void;
  onClear: () => void;
  applied: boolean;
  error: string;
  disabled: boolean;
  folders: Folder[];
};

const STATUS_TOKENS: { label: string; token: string }[] = [
  { label: "可使用", token: "status:active" },
  { label: "处理中", token: "status:processing" },
  { label: "待核实", token: "status:review" },
  { label: "已完成", token: "status:succeeded" },
  { label: "已停用", token: "status:revoked" },
];

function quoteFolder(name: string) {
  return /\s/.test(name) ? `folder:"${name}"` : `folder:${name}`;
}

export function FilterBar({
  value,
  onChange,
  onApply,
  onClear,
  applied,
  error,
  disabled,
  folders,
}: Props) {
  const input = useRef<HTMLInputElement>(null);
  // Cursor-independent helpers, evaluated as if appending at the end; the
  // insertion itself still honors the real cursor position.
  const tail = value.trimEnd();
  const endsWithOperator = /(?:^|[\s(])(?:and|or|&&|\|\|)$/i.test(tail);
  const endsWithOpen = tail.endsWith("(");
  const depth =
    (value.match(/\(/g)?.length ?? 0) - (value.match(/\)/g)?.length ?? 0);
  const logicDisabled = disabled || tail === "" || endsWithOperator;
  const closeDisabled = logicDisabled || endsWithOpen || depth <= 0;

  function insert(token: string, kind: "condition" | "and" | "or" | "close") {
    if (disabled) return;
    const element = input.current;
    const start = element?.selectionStart ?? value.length;
    const end = element?.selectionEnd ?? value.length;
    const left = value.slice(0, start).trimEnd();
    const right = value.slice(end);
    const leftEndsWithOperator = /(?:^|[\s(])(?:and|or|&&|\|\|)$/i.test(left);
    const leftNeedsAnd =
      left !== "" && !leftEndsWithOperator && !left.endsWith("(");
    let inserted: string;
    if (kind === "condition")
      inserted =
        left === "" || left.endsWith("(")
          ? token
          : leftNeedsAnd
            ? ` and ${token}`
            : ` ${token}`;
    else if (kind === "close") {
      if (!leftNeedsAnd) return; // would dangle; button is disabled anyway
      inserted = token;
    } else {
      if (!leftNeedsAnd) return; // operator would dangle; button is disabled anyway
      inserted = ` ${kind} `;
    }
    const next = left + inserted + right;
    onChange(next);
    const cursor = (left + inserted).length;
    requestAnimationFrame(() => {
      element?.focus();
      element?.setSelectionRange(cursor, cursor);
    });
  }

  return (
    <Paper variant="outlined" sx={{ p: { xs: 2, sm: 2.5 }, mb: 3 }}>
      <Stack
        direction="row"
        spacing={1}
        alignItems="center"
        sx={{ mb: 0.5 }}
      >
        <FilterAltOutlined color="primary" />
        <Typography variant="h3">高级筛选</Typography>
        {applied && (
          <Chip
            size="small"
            color="primary"
            variant="outlined"
            label="筛选生效中"
          />
        )}
      </Stack>
      <Typography variant="body2" color="text.secondary" sx={{ mb: 2 }}>
        组合批次与状态条件，在所有兑换码中筛选。
      </Typography>
      <Stack direction={{ xs: "column", sm: "row" }} spacing={1.5}>
        <TextField
          label="筛选表达式"
          placeholder="folder:示例批次 and (status:active or status:review)"
          value={value}
          disabled={disabled}
          onChange={(event) => onChange(event.target.value)}
          onKeyDown={(event) => {
            if (event.key === "Enter") {
              event.preventDefault();
              onApply();
            }
          }}
          error={!!error}
          inputRef={input}
          slotProps={{
            htmlInput: {
              "aria-label": "筛选表达式",
              spellCheck: false,
              sx: { fontFamily: "monospace" },
            },
          }}
        />
        <Stack
          direction="row"
          spacing={1}
          sx={{ flexShrink: 0, alignSelf: { xs: "end", sm: "auto" } }}
        >
          <Button
            variant="contained"
            disabled={disabled || !value.trim()}
            onClick={onApply}
            aria-label="应用筛选表达式"
          >
            筛选
          </Button>
          <Button
            variant="outlined"
            disabled={disabled || (!value && !applied)}
            onClick={onClear}
            aria-label="清除筛选并返回完整列表"
          >
            清除
          </Button>
        </Stack>
      </Stack>
      {error && (
        <Alert severity="error" role="alert" sx={{ mt: 2 }}>
          {error}
        </Alert>
      )}
      <Box
        sx={{
          display: "grid",
          gridTemplateColumns: { xs: "1fr", md: "auto 1fr" },
          columnGap: 2,
          rowGap: 1,
          alignItems: "center",
          mt: 2,
        }}
      >
        <Typography variant="caption" color="text.secondary">
          拼接 · 状态
        </Typography>
        <Stack direction="row" useFlexGap gap={1} flexWrap="wrap">
          {STATUS_TOKENS.map(({ label, token }) => (
            <Chip
              key={token}
              variant="outlined"
              label={label}
              title={`插入 ${token}`}
              disabled={disabled}
              onClick={() => insert(token, "condition")}
              aria-label={`插入条件 ${token}`}
            />
          ))}
        </Stack>
        <Typography variant="caption" color="text.secondary">
          拼接 · 批次
        </Typography>
        <Stack direction="row" useFlexGap gap={1} flexWrap="wrap">
          {folders.map((folder) => (
            <Chip
              key={folder.id}
              variant="outlined"
              label={folder.name}
              title={`插入 ${quoteFolder(folder.name)}`}
              disabled={disabled}
              onClick={() => insert(quoteFolder(folder.name), "condition")}
              aria-label={`插入批次条件 ${folder.name}`}
            />
          ))}
          <Chip
            variant="outlined"
            label="未分类"
            title="插入 folder:-（未分类）"
            disabled={disabled}
            onClick={() => insert("folder:-", "condition")}
            aria-label="插入条件 folder:-（未分类）"
          />
        </Stack>
        <Typography variant="caption" color="text.secondary">
          拼接 · 逻辑
        </Typography>
        <Stack direction="row" useFlexGap gap={1} flexWrap="wrap">
          <Chip
            variant="outlined"
            label="AND"
            disabled={logicDisabled}
            onClick={() => insert("and", "and")}
            aria-label="插入逻辑运算符 and"
          />
          <Chip
            variant="outlined"
            label="OR"
            disabled={logicDisabled}
            onClick={() => insert("or", "or")}
            aria-label="插入逻辑运算符 or"
          />
          <Chip
            variant="outlined"
            label="（"
            disabled={disabled}
            onClick={() => insert("(", "condition")}
            aria-label="插入左括号"
          />
          <Chip
            variant="outlined"
            label="）"
            disabled={closeDisabled}
            onClick={() => insert(")", "close")}
            aria-label="插入右括号"
          />
        </Stack>
      </Box>
      <Typography
        variant="caption"
        color="text.secondary"
        sx={{ display: "block", mt: 2 }}
      >
        {FILTER_HINT}
      </Typography>
    </Paper>
  );
}
