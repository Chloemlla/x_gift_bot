import { useEffect, useRef, useState } from "react";
import type { KeyboardEvent, ReactNode } from "react";
import {
  Alert,
  Box,
  Button,
  Chip,
  Paper,
  Stack,
  TextField,
  Tooltip,
  Typography,
} from "@mui/material";
import type { Theme } from "@mui/material";
import type { SystemStyleObject } from "@mui/system";
import { visuallyHidden } from "@mui/utils";
import BackspaceOutlined from "@mui/icons-material/BackspaceOutlined";
import CodeOutlined from "@mui/icons-material/CodeOutlined";
import FilterAltOutlined from "@mui/icons-material/FilterAltOutlined";
import PaletteOutlined from "@mui/icons-material/PaletteOutlined";
import type { Folder } from "./adminApi";
import {
  FILTER_HINT,
  parseExpressionTokens,
  type ExpressionToken,
} from "./filter";

type Props = {
  value: string;
  onChange: (value: string) => void;
  onApply: () => void;
  onClear: () => void;
  applied: boolean;
  error: string;
  disabled: boolean;
  folders: Folder[];
  // 当前生效的表达式原文与结果数，用于「已修改」指示与屏幕阅读器播报。
  appliedSource?: string;
  resultCount?: number;
  // 每次成功应用递增，保证重复应用也会播报最新结果数。
  applySeq?: number;
  // 从批次文件夹退出筛选时的定制播报（缺省为「完整列表」）。
  clearNotice?: { seq: number; text: string };
};

type Token = ExpressionToken;
type Tone = "primary" | "secondary" | "success" | "warning" | "neutral";

// 调色板模式不向用户展示文本语法；文本模式沿用 FILTER_HINT。
const PALETTE_HINT =
  "点击调色板按钮拼接条件；点击画布中的条件可切换 = / ≠；画布聚焦时按 Backspace 撤销、Enter 应用。";

const STATUS_ORDER = [
  "active",
  "processing",
  "review",
  "succeeded",
  "revoked",
] as const;
const STATUS_META: Record<string, { label: string; tone: Tone }> = {
  active: { label: "可使用", tone: "primary" },
  processing: { label: "处理中", tone: "primary" },
  review: { label: "待核实", tone: "warning" },
  succeeded: { label: "已完成", tone: "success" },
  revoked: { label: "已停用", tone: "neutral" },
};

function paletteOf(theme: Theme) {
  return theme.vars ? theme.vars.palette : theme.palette;
}

function toneMain(theme: Theme, tone: Tone) {
  const palette = paletteOf(theme);
  return tone === "neutral" ? palette.text.secondary : palette[tone].main;
}

// 条件词元：语义色淡底 + 同色文字。
function conditionTint(tone: Tone): (theme: Theme) => SystemStyleObject<Theme> {
  return (theme) => {
    const main = toneMain(theme, tone);
    return {
      color: main,
      bgcolor: theme.alpha(main, 0.14),
      "& .MuiChip-deleteIcon": {
        color: theme.alpha(main, 0.65),
        "&:hover": { color: main },
      },
    };
  };
}

function logicTokenSx(theme: Theme): SystemStyleObject<Theme> {
  return {
    color: paletteOf(theme).text.primary,
    borderColor: paletteOf(theme).divider,
    fontFamily: "monospace",
    letterSpacing: ".06em",
  };
}

const parenTokenSx: SystemStyleObject<Theme> = {
  border: "none",
  color: "text.secondary",
  bgcolor: "transparent",
  fontFamily: "monospace",
  fontSize: "1rem",
  minWidth: 30,
  "& .MuiChip-label": { px: 0.5 },
  "&:hover": { bgcolor: "action.hover" },
};

// NOT 词元：反色（深底浅字），与条件 / 连接符明显区分。
function notTokenSx(theme: Theme): SystemStyleObject<Theme> {
  const palette = paletteOf(theme);
  return {
    bgcolor: palette.text.primary,
    color: palette.background.paper,
    fontFamily: "monospace",
    letterSpacing: ".06em",
    "&:hover": { bgcolor: palette.text.secondary },
    "& .MuiChip-deleteIcon": {
      color: theme.alpha(palette.background.paper, 0.65),
      "&:hover": { color: palette.background.paper },
    },
  };
}

const chipSizeSx: SystemStyleObject<Theme> = {
  height: 30,
  minHeight: 30,
  borderRadius: "16px",
  fontWeight: 600,
  // 扩大删除按钮命中区域到 ≥24×24（WCAG 2.5.8），负外边距保持视觉位置。
  "& .MuiChip-deleteIcon": {
    boxSizing: "content-box",
    width: "18px",
    height: "18px",
    padding: "3px",
    margin: "-3px",
  },
};

function tokenTone(token: Token): Tone {
  if (token.kind !== "condition") return "neutral";
  if (token.field === "folder") return "secondary";
  if (token.field === "months") return "success";
  if (token.field === "username") return "warning";
  return STATUS_META[token.value]?.tone ?? "neutral";
}

function conditionLabel(token: Extract<Token, { kind: "condition" }>): string {
  const op = token.negate ? "≠" : ":";
  switch (token.field) {
    case "folder":
      return token.value === "-" && !token.literal
        ? `批次${op}未分类`
        : `批次${op}${token.value}`;
    case "status":
      return `状态${op}${STATUS_META[token.value]?.label ?? token.value}`;
    case "months":
      return `时长${op}${token.value} 个月`;
    case "username":
      return token.value === "-" && !token.literal
        ? `账号${op}未绑定`
        : `账号${op}${token.value}`;
  }
}

function tokenLabel(token: Token): string {
  switch (token.kind) {
    case "condition":
      return conditionLabel(token);
    case "not":
      return "NOT";
    case "and":
      return "AND";
    case "or":
      return "OR";
    case "lparen":
      return "（";
    case "rparen":
      return "）";
  }
}

function quoteValue(name: string, literal = false) {
  // 批次名/账号可含任意字符；保留字、引号与反斜杠特殊处理，保证序列化结果
  // 可被 tokenizer 还原。哨兵值 "-"（未分类/未绑定）保持裸写；仅当名称本身
  // 就叫 "-" 时才加引号（literal，与 parse 的哨兵分支互逆）。
  if (/^(and|or|not|非|&&|\|\|)$/i.test(name) || (name === "-" && literal))
    return `"${name}"`;
  return /[\s()":=!≠\\：（）！＝＂“”]/.test(name)
    ? `"${name.replace(/\\/g, "\\\\").replace(/"/g, '\\"').replace(/[＂“”]/g, "\\$&")}"`
    : name;
}

function serializeTokens(tokens: Token[]): string {
  // 词元间用单空格连接，但 （ 后与 ） 前不留内侧空格。
  return tokens
    .map((token, index) => {
      let text: string;
      switch (token.kind) {
        case "condition": {
          const op = token.negate ? "!=" : ":";
          text =
            token.field === "folder" || token.field === "username"
              ? `${token.field}${op}${quoteValue(token.value, token.literal)}`
              : `${token.field}${op}${token.value}`;
          break;
        }
        case "lparen":
          text = "(";
          break;
        case "rparen":
          text = ")";
          break;
        default:
          text = token.kind;
      }
      if (index === 0) return text;
      const prev = tokens[index - 1];
      return token.kind === "rparen" || prev.kind === "lparen"
        ? text
        : ` ${text}`;
    })
    .join("");
}

function PaletteButton({
  label,
  ariaLabel,
  tooltip,
  disabled,
  onClick,
  dot,
  mono,
}: {
  label: ReactNode;
  ariaLabel: string;
  tooltip: string;
  disabled?: boolean;
  onClick: () => void;
  dot?: Tone;
  mono?: boolean;
}) {
  return (
    <Tooltip title={tooltip} describeChild>
      <span>
        <Button
          variant="outlined"
          size="small"
          disabled={disabled}
          onClick={onClick}
          aria-label={ariaLabel}
          startIcon={
            dot ? (
              <Box
                component="span"
                aria-hidden
                sx={(theme) => ({
                  width: 8,
                  height: 8,
                  borderRadius: "50%",
                  bgcolor: toneMain(theme, dot),
                })}
              />
            ) : undefined
          }
          sx={{
            minHeight: 30,
            minWidth: 44,
            px: 1.5,
            py: 0,
            borderRadius: "16px",
            fontSize: ".8125rem",
            lineHeight: 1.5,
            fontFamily: mono ? "monospace" : undefined,
            letterSpacing: mono ? ".05em" : undefined,
            "& .MuiButton-startIcon": { mr: 0.75, ml: 0 },
            // 禁用时色点随文字一起降不透明度。
            "&:disabled .MuiButton-startIcon": { opacity: 0.38 },
          }}
        >
          {label}
        </Button>
      </span>
    </Tooltip>
  );
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
  appliedSource = "",
  resultCount,
  applySeq = 0,
  clearNotice,
}: Props) {
  const [tokens, setTokens] = useState<Token[]>([]);
  const [mode, setMode] = useState<"palette" | "text">("palette");
  // seq 递增保证相同文案重复播报时 live region 仍会重建触发朗读。
  const [announce, setAnnounce] = useState({ seq: 0, text: "" });
  const speak = (text: string) =>
    setAnnounce((current) => ({ seq: current.seq + 1, text }));
  const [desyncWarning, setDesyncWarning] = useState("");
  const [username, setUsername] = useState("");
  const canvasRef = useRef<HTMLDivElement>(null);

  // 外部 value 变化（如清除、文本模式往返）时重新同步词元；
  // 本组件自己 onChange 出去的序列化结果与词元一致，不会触发重建。
  useEffect(() => {
    if (mode !== "palette" || value === serializeTokens(tokens)) return;
    try {
      setTokens(value.trim() ? parseExpressionTokens(value) : []);
    } catch {
      // 文本无法解析：恢复为调色板内容并提示，保证画布与表达式永不脱节。
      const note = "无法解析文本表达式，已恢复为调色板中的条件。";
      setDesyncWarning(note);
      speak(note);
      onChange(serializeTokens(tokens));
    }
    // tokens 刻意不作为依赖：只在 value / mode 变化时同步。
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [value, mode]);

  // 播报：每次成功应用（含重复应用）都播报最新结果数；
  // 清除时优先使用定制文案（如从批次文件夹退出），否则为完整列表。
  // 失败路径由 error Alert（role=alert）负责。
  const wasApplied = useRef(applied);
  const lastApplySeq = useRef(0);
  const lastNoticeSeq = useRef(0);
  useEffect(() => {
    if (applySeq > lastApplySeq.current) {
      lastApplySeq.current = applySeq;
      speak(
        resultCount == null
          ? "筛选已应用"
          : `筛选已应用，共 ${resultCount} 条结果`,
      );
    }
    if (!applied && wasApplied.current) {
      let note = "筛选已清除，已显示完整列表";
      if (clearNotice && clearNotice.seq > lastNoticeSeq.current) {
        lastNoticeSeq.current = clearNotice.seq;
        note = clearNotice.text;
      }
      speak(note);
    }
    wasApplied.current = applied;
  }, [applied, applySeq, resultCount, clearNotice]);

  function commit(next: Token[], note: string) {
    setTokens(next);
    speak(note);
    setDesyncWarning("");
    onChange(serializeTokens(next));
    // 激活的调色板按钮可能在提交后被禁用，导致焦点跌回 <body>；
    // 统一把焦点移到画布（Enter 应用 / Backspace 撤销）。
    requestAnimationFrame(() => canvasRef.current?.focus());
  }

  function add(token: Token) {
    if (disabled) return;
    commit([...tokens, token], `已添加 ${tokenLabel(token)}`);
  }

  const usernameValid = /^[A-Za-z0-9_]+$/.test(username.trim());

  function addUsername() {
    if (disabled || !usernameValid || conditionDisabled) return;
    add({
      kind: "condition",
      field: "username",
      value: username.trim(),
      negate: false,
    });
    setUsername("");
  }

  function removeAt(index: number) {
    if (disabled) return;
    const removed = tokens[index];
    if (!removed) return;
    commit(
      tokens.filter((_, i) => i !== index),
      `已删除 ${tokenLabel(removed)}`,
    );
  }

  function undo() {
    if (disabled || tokens.length === 0) return;
    commit(
      tokens.slice(0, -1),
      `已撤销 ${tokenLabel(tokens[tokens.length - 1])}`,
    );
  }

  function toggleNegate(index: number) {
    if (disabled) return;
    const token = tokens[index];
    if (token?.kind !== "condition") return;
    const next = tokens.map((t, i) =>
      i === index && t.kind === "condition" ? { ...t, negate: !t.negate } : t,
    );
    const updated = next[index];
    commit(next, `已切换为 ${tokenLabel(updated)}`);
  }

  // 其他词元 chip 暴露 role="button"，Enter/Space 与 Delete/Backspace 一样触发删除。
  function chipKeyDown(index: number) {
    return (event: KeyboardEvent) => {
      if (disabled) return;
      if (event.key === "Enter" || event.key === " ") {
        event.preventDefault();
        removeAt(index);
      }
    };
  }

  const last = tokens[tokens.length - 1] as Token | undefined;
  const startsOperand =
    !last ||
    last.kind === "and" ||
    last.kind === "or" ||
    last.kind === "not" ||
    last.kind === "lparen";
  const endsOperand =
    !!last && (last.kind === "condition" || last.kind === "rparen");
  const depth = tokens.reduce(
    (d, token) =>
      d + (token.kind === "lparen" ? 1 : token.kind === "rparen" ? -1 : 0),
    0,
  );
  const conditionDisabled = disabled || !startsOperand;
  const operatorDisabled = disabled || !endsOperand;
  const rparenDisabled = disabled || !endsOperand || depth <= 0;
  const undoDisabled = disabled || tokens.length === 0;
  const incompleteTail =
    !!last &&
    (last.kind === "and" ||
      last.kind === "or" ||
      last.kind === "not" ||
      last.kind === "lparen");
  // 结构问题优先于解析问题：先指出括号/末尾，再做完整语法校验，
  // 保证筛选被禁用时用户总能看到原因（包括从中间删除词元后的断裂）。
  let invalidReason = "";
  if (tokens.length > 0) {
    if (depth > 0) invalidReason = `括号未闭合，还差 ${depth} 个右括号 ）`;
    else if (depth < 0) invalidReason = "存在多余的右括号 ）";
    else if (incompleteTail) invalidReason = "表达式不完整，末尾还需要一个条件";
    else {
      try {
        parseExpressionTokens(serializeTokens(tokens));
      } catch (error) {
        const message = (error as Error).message;
        // 「X 后/前缺少条件」类错误的修复路径是删掉多余的运算符/括号，
        // 而不是追加（调色板只能末尾拼接），给出可执行的指引。
        if (/[后前]缺少条件/.test(message))
          invalidReason = `${message}可删除多余的运算符或括号。`;
        else if (/前缺少逻辑运算符/.test(message))
          invalidReason = `${message}可从末尾撤销后重新用 AND / OR 连接。`;
        else invalidReason = message;
      }
    }
  }
  const applyDisabled =
    disabled ||
    (mode === "palette" ? tokens.length === 0 || !!invalidReason : !value.trim());

  const operandReason = tokens.length
    ? "请先添加 AND / OR 连接符"
    : "表达式为空，可直接添加";
  const operatorReason = tokens.length
    ? "运算符需跟在条件或 ）之后"
    : "请先添加条件";

  // 画布内容与已应用的表达式不一致时给出「已修改」指示。
  const dirty = applied && !!appliedSource && value !== appliedSource;

  return (
    <Paper variant="outlined" sx={{ p: { xs: 2, sm: 2.5 }, mb: 3 }}>
      <Stack
        direction="row"
        spacing={1}
        alignItems="center"
        sx={{ mb: 0.5, flexWrap: "wrap", rowGap: 0.5 }}
      >
        <FilterAltOutlined color="primary" sx={{ flexShrink: 0 }} />
        <Typography variant="h2" sx={{ whiteSpace: "nowrap", flexShrink: 0, fontSize: 21 }}>
          高级筛选
        </Typography>
        {applied && (
          <Chip
            size="small"
            color={dirty ? "warning" : "primary"}
            variant="outlined"
            label={dirty ? "筛选生效中 · 已修改" : "筛选生效中"}
            title={dirty ? "含未应用的更改，点击 筛选 重新应用" : undefined}
            sx={{ flexShrink: 0, "& .MuiChip-label": { whiteSpace: "nowrap" } }}
          />
        )}
        <Box sx={{ flexGrow: 1 }} />
        <Button
          variant="text"
          size="small"
          startIcon={mode === "palette" ? <CodeOutlined /> : <PaletteOutlined />}
          aria-label={
            mode === "palette" ? "切换到文本模式" : "切换到调色板模式"
          }
          onClick={() => {
            setDesyncWarning("");
            setMode((current) => (current === "palette" ? "text" : "palette"));
          }}
          sx={{ minHeight: 36, px: 1.5, flexShrink: 0 }}
        >
          {mode === "palette" ? "文本模式" : "调色板"}
        </Button>
      </Stack>
      <Typography variant="body2" color="text.secondary" sx={{ mb: 2 }}>
        组合批次与状态条件，在所有兑换码中筛选。
      </Typography>
      {mode === "text" ? (
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
          slotProps={{
            htmlInput: {
              "aria-label": "筛选表达式",
              spellCheck: false,
              sx: { fontFamily: "monospace" },
            },
          }}
        />
      ) : (
        <>
          <Box
            ref={canvasRef}
            tabIndex={disabled ? -1 : 0}
            role="group"
            aria-label="筛选表达式画布；按 Backspace 撤销末尾词元，按 Enter 应用筛选"
            onKeyDown={(event) => {
              if (disabled) return;
              // 词元 chip 的按键会冒泡到画布：chip 有自己的删除逻辑，跳过。
              if (event.target !== event.currentTarget) return;
              if (event.key === "Backspace") {
                event.preventDefault();
                undo();
              } else if (event.key === "Enter") {
                event.preventDefault();
                if (!applyDisabled) onApply();
                // 表达式无效时 Enter 不再静默，播报当前不可应用的原因。
                else if (invalidReason) speak(invalidReason);
              }
            }}
            sx={(theme) => ({
              display: "flex",
              flexWrap: "wrap",
              gap: 1,
              alignItems: "center",
              minHeight: 56,
              p: 1.25,
              borderRadius: 2,
              border: 1,
              borderColor:
                error || invalidReason ? "error.main" : "divider",
              bgcolor: paletteOf(theme).background.default,
              opacity: disabled ? 0.6 : 1,
              "&:focus-visible": {
                outline: "none",
                borderColor: "primary.main",
                boxShadow: `0 0 0 3px ${theme.alpha(paletteOf(theme).primary.main, 0.3)}`,
              },
            })}
          >
            {tokens.length === 0 ? (
              <Typography
                variant="body2"
                color="text.secondary"
                sx={{ px: 0.5 }}
              >
                点击下方调色板拼接筛选条件
              </Typography>
            ) : (
              tokens.map((token, index) =>
                token.kind === "lparen" || token.kind === "rparen" ? (
                  <Chip
                    key={index}
                    variant="outlined"
                    label={tokenLabel(token)}
                    aria-label={`括号 ${tokenLabel(token)}，按 Enter、Delete 或 Backspace 删除`}
                    onDelete={disabled ? undefined : () => removeAt(index)}
                    onKeyDown={chipKeyDown(index)}
                    sx={[chipSizeSx, parenTokenSx]}
                  />
                ) : token.kind === "condition" ? (
                  <Tooltip key={index} title="点击或按 Enter 切换 = / ≠" describeChild>
                    <Chip
                      label={tokenLabel(token)}
                      aria-label={`${tokenLabel(token)}，按 Enter 或点击切换 = / ≠，按 Delete 或 Backspace 删除`}
                      onClick={disabled ? undefined : () => toggleNegate(index)}
                      onDelete={disabled ? undefined : () => removeAt(index)}
                      sx={[
                        chipSizeSx,
                        conditionTint(tokenTone(token)),
                        { cursor: disabled ? "default" : "pointer" },
                      ]}
                    />
                  </Tooltip>
                ) : token.kind === "not" ? (
                  <Chip
                    key={index}
                    label={tokenLabel(token)}
                    aria-label={`逻辑 ${tokenLabel(token)}（补集），按 Enter、Delete 或 Backspace 删除`}
                    onDelete={disabled ? undefined : () => removeAt(index)}
                    onKeyDown={chipKeyDown(index)}
                    sx={[chipSizeSx, notTokenSx]}
                  />
                ) : (
                  <Chip
                    key={index}
                    variant="outlined"
                    label={tokenLabel(token)}
                    aria-label={`逻辑 ${tokenLabel(token)}，按 Enter、Delete 或 Backspace 删除`}
                    onDelete={disabled ? undefined : () => removeAt(index)}
                    onKeyDown={chipKeyDown(index)}
                    sx={[chipSizeSx, logicTokenSx]}
                  />
                ),
              )
            )}
          </Box>
          {desyncWarning && (
            <Alert
              severity="warning"
              closeText="关闭"
              sx={{ mt: 1.5 }}
              onClose={() => setDesyncWarning("")}
            >
              {desyncWarning}
            </Alert>
          )}
          <Box
            sx={{
              display: "grid",
              gridTemplateColumns: { xs: "1fr", md: "auto 1fr" },
              columnGap: 2,
              rowGap: 1.25,
              alignItems: "start",
              mt: 2,
            }}
          >
            <Typography
              variant="caption"
              color="text.secondary"
              sx={{ mt: { md: 0.75 } }}
            >
              状态
            </Typography>
            <Stack direction="row" useFlexGap gap={1} flexWrap="wrap">
              {STATUS_ORDER.map((status) => {
                const meta = STATUS_META[status];
                return (
                  <PaletteButton
                    key={status}
                    label={meta.label}
                    dot={meta.tone}
                    disabled={conditionDisabled}
                    tooltip={
                      conditionDisabled
                        ? operandReason
                        : `添加 状态:${meta.label}`
                    }
                    onClick={() =>
                      add({
                        kind: "condition",
                        field: "status",
                        value: status,
                        negate: false,
                      })
                    }
                    ariaLabel={`添加状态条件 ${meta.label}`}
                  />
                );
              })}
            </Stack>
            <Typography
              variant="caption"
              color="text.secondary"
              sx={{ mt: { md: 0.75 } }}
            >
              批次
            </Typography>
            <Stack direction="row" useFlexGap gap={1} flexWrap="wrap">
              {folders.map((folder) => (
                <PaletteButton
                  key={folder.id}
                  label={folder.name}
                  dot="secondary"
                  disabled={conditionDisabled}
                  tooltip={
                    conditionDisabled
                      ? operandReason
                      : `添加 批次:${folder.name}`
                  }
                  onClick={() =>
                    add({
                      kind: "condition",
                      field: "folder",
                      value: folder.name,
                      negate: false,
                      // 批次名恰为哨兵值 "-" 时保持字法语义。
                      literal: folder.name === "-" ? true : undefined,
                    })
                  }
                  ariaLabel={`添加批次条件 ${folder.name}`}
                />
              ))}
              <PaletteButton
                label="未分类"
                dot="secondary"
                disabled={conditionDisabled}
                tooltip={
                  conditionDisabled ? operandReason : "添加 批次:未分类"
                }
                onClick={() =>
                  add({
                    kind: "condition",
                    field: "folder",
                    value: "-",
                    negate: false,
                  })
                }
                ariaLabel="添加批次条件 未分类"
              />
            </Stack>
            <Typography
              variant="caption"
              color="text.secondary"
              sx={{ mt: { md: 0.75 } }}
            >
              时长
            </Typography>
            <Stack direction="row" useFlexGap gap={1} flexWrap="wrap">
              {["3", "6"].map((months) => (
                <PaletteButton
                  key={months}
                  label={`${months} 个月`}
                  dot="success"
                  disabled={conditionDisabled}
                  tooltip={
                    conditionDisabled
                      ? operandReason
                      : `添加 时长:${months} 个月`
                  }
                  onClick={() =>
                    add({
                      kind: "condition",
                      field: "months",
                      value: months,
                      negate: false,
                    })
                  }
                  ariaLabel={`添加时长条件 ${months} 个月`}
                />
              ))}
            </Stack>
            <Typography
              variant="caption"
              color="text.secondary"
              sx={{ mt: { md: 0.75 } }}
            >
              账号
            </Typography>
            <Stack
              direction="row"
              useFlexGap
              gap={1}
              flexWrap="wrap"
              alignItems="center"
            >
              <TextField
                size="small"
                fullWidth={false}
                placeholder="接收账号"
                value={username}
                disabled={disabled}
                onChange={(event) => setUsername(event.target.value)}
                onKeyDown={(event) => {
                  if (event.key === "Enter") {
                    event.preventDefault();
                    if (username.trim() && !usernameValid)
                      speak("账号名需为字母、数字或下划线。");
                    else addUsername();
                  }
                }}
                slotProps={{
                  htmlInput: {
                    "aria-label": "接收账号用户名（字母、数字、下划线）",
                    spellCheck: false,
                    sx: { fontFamily: "monospace", py: 0.5 },
                  },
                }}
                sx={{ width: 160 }}
              />
              <PaletteButton
                label="添加账号"
                dot="warning"
                disabled={disabled || !usernameValid || conditionDisabled}
                tooltip={
                  !usernameValid
                    ? "请输入字母、数字或下划线组成的账号名"
                    : conditionDisabled
                      ? operandReason
                      : `添加 账号:${username.trim()}`
                }
                onClick={addUsername}
                ariaLabel="添加账号条件"
              />
              <PaletteButton
                label="未绑定"
                dot="warning"
                disabled={conditionDisabled}
                tooltip={
                  conditionDisabled ? operandReason : "添加 账号:未绑定"
                }
                onClick={() =>
                  add({
                    kind: "condition",
                    field: "username",
                    value: "-",
                    negate: false,
                  })
                }
                ariaLabel="添加账号条件 未绑定"
              />
            </Stack>
            <Typography
              variant="caption"
              color="text.secondary"
              sx={{ mt: { md: 0.75 } }}
            >
              逻辑
            </Typography>
            <Stack direction="row" useFlexGap gap={1} flexWrap="wrap">
              <PaletteButton
                label="AND"
                mono
                disabled={operatorDisabled}
                tooltip={
                  operatorDisabled ? operatorReason : "添加逻辑与 AND"
                }
                onClick={() => add({ kind: "and" })}
                ariaLabel="添加逻辑与 AND"
              />
              <PaletteButton
                label="OR"
                mono
                disabled={operatorDisabled}
                tooltip={
                  operatorDisabled ? operatorReason : "添加逻辑或 OR"
                }
                onClick={() => add({ kind: "or" })}
                ariaLabel="添加逻辑或 OR"
              />
              <PaletteButton
                label="非 NOT"
                mono
                disabled={conditionDisabled}
                tooltip={
                  conditionDisabled ? operandReason : "添加补集 NOT（非）"
                }
                onClick={() => add({ kind: "not" })}
                ariaLabel="添加补集 NOT"
              />
              <PaletteButton
                label="（"
                mono
                disabled={conditionDisabled}
                tooltip={conditionDisabled ? operandReason : "添加左括号"}
                onClick={() => add({ kind: "lparen" })}
                ariaLabel="添加左括号"
              />
              <PaletteButton
                label="）"
                mono
                disabled={rparenDisabled}
                tooltip={
                  rparenDisabled
                    ? depth <= 0
                      ? "没有未闭合的左括号"
                      : "右括号需跟在条件或 ）之后"
                    : "添加右括号"
                }
                onClick={() => add({ kind: "rparen" })}
                ariaLabel="添加右括号"
              />
              <PaletteButton
                label={
                  <Stack
                    component="span"
                    direction="row"
                    spacing={0.5}
                    alignItems="center"
                  >
                    <BackspaceOutlined sx={{ fontSize: "1rem" }} aria-hidden />
                    撤销
                  </Stack>
                }
                disabled={undoDisabled}
                tooltip={
                  undoDisabled ? "没有可撤销的词元" : "撤销末尾词元"
                }
                onClick={undo}
                ariaLabel="撤销末尾词元"
              />
            </Stack>
          </Box>
        </>
      )}
      {error && (
        <Alert severity="error" role="alert" sx={{ mt: 2 }}>
          {error}
        </Alert>
      )}
      <Stack
        direction={{ xs: "column", sm: "row" }}
        spacing={1.5}
        sx={{
          mt: 2,
          justifyContent: "space-between",
          alignItems: { xs: "stretch", sm: "center" },
        }}
      >
        <Typography
          variant="caption"
          color={
            mode === "palette" && invalidReason ? "error.main" : "text.secondary"
          }
          role={mode === "palette" && invalidReason ? "alert" : undefined}
        >
          {mode === "palette" && invalidReason
            ? invalidReason
            : mode === "palette"
              ? PALETTE_HINT
              : FILTER_HINT}
        </Typography>
        <Stack
          direction="row"
          spacing={1}
          sx={{ flexShrink: 0, justifyContent: { xs: "end", sm: "auto" } }}
        >
          <Button
            variant="outlined"
            disabled={disabled || (!value && !applied)}
            onClick={onClear}
            aria-label="清除筛选并返回完整列表"
          >
            清除
          </Button>
          <Button
            variant="contained"
            disabled={applyDisabled}
            onClick={onApply}
            aria-label="应用筛选表达式"
          >
            筛选
          </Button>
        </Stack>
      </Stack>
      <Box
        component="span"
        key={announce.seq}
        role="status"
        aria-live="polite"
        sx={visuallyHidden}
      >
        {announce.text}
      </Box>
    </Paper>
  );
}
