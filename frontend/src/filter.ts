// 兑换码筛选表达式：词法分析 + 递归下降解析 + 求值。
// 语法：expr := orExpr；orExpr := andExpr (('or'|'||') andExpr)*；
// andExpr := unary (('and'|'&&') unary)*；
// unary := ('not'|'!'|'非') unary | factor（补集，可叠加，not not X = X）；
// factor := '(' expr ')' | condition；
// condition := field (':'|'='|'!='|'≠') value。
// 字段：folder、status、months、username；字段与运算符不区分大小写。

export type CodeRow = {
  batch: string;
  status: string;
  months?: number;
  username?: string;
};

const STATUSES = ["active", "processing", "review", "succeeded", "revoked"];
const FIELDS = ["folder", "status", "months", "username"] as const;
type Field = (typeof FIELDS)[number];

type Token =
  | { kind: "lparen" }
  | { kind: "rparen" }
  | { kind: "colon" }
  | { kind: "eq" }
  | { kind: "neq" }
  | { kind: "and" }
  | { kind: "or" }
  | { kind: "not" }
  | { kind: "word"; text: string }
  | { kind: "string"; text: string };

function tokenize(src: string): Token[] {
  // 中文输入法容错：全角括号、冒号、引号、叹号、等号统一归一为半角。
  // 引号内的字面内容保持原样（批次名可能本身含全角标点）。
  let normalized = "";
  let inString = false;
  let escaped = false;
  for (const raw of src) {
    if (inString) {
      if (escaped) {
        normalized += raw;
        escaped = false;
        continue;
      }
      if (raw === "\\") {
        normalized += raw;
        escaped = true;
        continue;
      }
      // 未转义的全角引号在字符串内视为闭合（中文输入法自动配对场景）；
      // 名称字面的全角引号由 quoteValue 转义为 \” 绕过。
      if (raw === '"' || /[＂“”]/.test(raw)) {
        normalized += '"';
        inString = false;
        continue;
      }
      normalized += raw;
      continue;
    }
    const char = /[＂“”]/.test(raw) ? '"' : raw;
    if (char === '"') inString = true;
    normalized += char
      .replace(/（/g, "(")
      .replace(/）/g, ")")
      .replace(/：/g, ":")
      .replace(/！/g, "!")
      .replace(/＝/g, "=");
  }
  src = normalized;
  const tokens: Token[] = [];
  let index = 0;
  while (index < src.length) {
    const char = src[index];
    if (/\s/.test(char)) {
      index++;
      continue;
    }
    if (char === "(") {
      tokens.push({ kind: "lparen" });
      index++;
      continue;
    }
    if (char === ")") {
      tokens.push({ kind: "rparen" });
      index++;
      continue;
    }
    if (char === ":") {
      tokens.push({ kind: "colon" });
      index++;
      continue;
    }
    if (char === "=") {
      tokens.push({ kind: "eq" });
      index++;
      continue;
    }
    if (char === "≠") {
      tokens.push({ kind: "neq" });
      index++;
      continue;
    }
    if (char === "!") {
      if (src[index + 1] === "=") {
        tokens.push({ kind: "neq" });
        index += 2;
      } else {
        tokens.push({ kind: "not" });
        index++;
      }
      continue;
    }
    if (char === '"') {
      // 支持 \" 与 \\ 转义，与 quoteValue 的序列化互逆。
      let end = index + 1;
      let text = "";
      for (;;) {
        if (end >= src.length)
          throw new Error("引号不匹配：批次名称的英文双引号没有闭合。");
        if (src[end] === "\\" && end + 1 < src.length) {
          text += src[end + 1];
          end += 2;
          continue;
        }
        if (src[end] === '"') break;
        text += src[end];
        end++;
      }
      tokens.push({ kind: "string", text });
      index = end + 1;
      continue;
    }
    const match = /^[^\s()":=!≠]+/.exec(src.slice(index))!;
    const word = match[0];
    const lower = word.toLowerCase();
    if (lower === "and" || word === "&&") tokens.push({ kind: "and" });
    else if (lower === "or" || word === "||") tokens.push({ kind: "or" });
    // 「非」只有作为独立词（两侧是空白 / 括号等边界）才是 NOT 运算符；
    // 出现在其他字符中间（如批次名「非活动」）时按普通词处理。
    // 因此无法直接匹配名为「非」的批次，需要加引号：folder:"非"。
    else if (lower === "not" || word === "非") tokens.push({ kind: "not" });
    else tokens.push({ kind: "word", text: word });
    index += word.length;
  }
  return tokens;
}

type Node =
  | { type: "or"; left: Node; right: Node }
  | { type: "and"; left: Node; right: Node }
  | { type: "not"; operand: Node }
  // literal: 值来自引号串,哨兵值("-")按字面名称处理。
  | { type: "condition"; field: Field; value: string; negate: boolean; literal?: boolean };

// 调色板模式使用的扁平词元，按表达式中的出现顺序记录。
// condition.literal: 值来自引号串（序列化时保持引号，哨兵 "-" 按字面名称处理）。
export type ExpressionToken =
  | { kind: "condition"; field: Field; value: string; negate: boolean; literal?: boolean }
  | { kind: "not" }
  | { kind: "and" }
  | { kind: "or" }
  | { kind: "lparen" }
  | { kind: "rparen" };

function parse(src: string): { root: Node; flat: ExpressionToken[] } {
  const tokens = tokenize(src);
  const flat: ExpressionToken[] = [];
  let position = 0;
  const peek = () => tokens[position];
  const next = () => tokens[position++];

  function parseExpr(): Node {
    return parseOr();
  }
  function parseOr(): Node {
    let left = parseAnd();
    while (peek()?.kind === "or") {
      next();
      flat.push({ kind: "or" });
      left = { type: "or", left, right: parseAnd() };
    }
    return left;
  }
  function parseAnd(): Node {
    let left = parseUnary();
    while (peek()?.kind === "and") {
      next();
      flat.push({ kind: "and" });
      left = { type: "and", left, right: parseUnary() };
    }
    return left;
  }
  function parseUnary(): Node {
    if (peek()?.kind === "not") {
      next();
      flat.push({ kind: "not" });
      if (!peek()) throw new Error("「not」后缺少条件。");
      return { type: "not", operand: parseUnary() };
    }
    return parseFactor();
  }
  function parseFactor(): Node {
    const token = peek();
    if (!token) throw new Error("表达式意外的结尾：缺少条件。");
    if (token.kind === "and" || token.kind === "or")
      throw new Error(
        `「${token.kind === "and" ? "and" : "or"}」后缺少条件。`,
      );
    if (token.kind === "rparen")
      throw new Error("括号不匹配：右括号前缺少条件。");
    if (token.kind === "lparen") {
      next();
      flat.push({ kind: "lparen" });
      const inner = parseExpr();
      if (peek()?.kind !== "rparen") throw new Error("括号不匹配：缺少右括号。");
      next();
      flat.push({ kind: "rparen" });
      return inner;
    }
    return parseCondition();
  }
  function parseCondition(): Node {
    const field = next()!;
    if (field.kind !== "word")
      throw new Error(
        "无法识别的条件：请使用 folder:批次名、status:状态、months:时长或 username:账号。",
      );
    const name = field.text.toLowerCase() as Field;
    if (!FIELDS.includes(name))
      throw new Error(
        `未知字段「${field.text}」，仅支持 ${FIELDS.join("、")}。`,
      );
    const op = peek();
    const negate = op?.kind === "neq";
    if (op?.kind !== "colon" && op?.kind !== "eq" && op?.kind !== "neq")
      throw new Error(`「${field.text}」后缺少 : 或 !=。`);
    next();
    const value = peek();
    if (!value || (value.kind !== "word" && value.kind !== "string")) {
      if (
        value &&
        (value.kind === "and" || value.kind === "or" || value.kind === "not")
      )
        throw new Error(
          `「${field.text}:」后遇到逻辑运算符；如果名称本身含 and / or / not / 非，请用英文引号包裹，如 ${field.text}:"非"。`,
        );
      throw new Error(`「${field.text}:」后缺少值。`);
    }
    next();
    let normalized = value.text;
    if (name === "status") {
      normalized = value.text.toLowerCase();
      if (!STATUSES.includes(normalized))
        throw new Error(
          `不支持的状态值「${value.text}」，可用：${STATUSES.join("、")}。`,
        );
    } else if (name === "months") {
      if (!/^\d+$/.test(value.text))
        throw new Error(
          `「months:」后需要数字（如 3、6），收到「${value.text}」。`,
        );
    } else if (name === "folder") {
      if (!value.text)
        throw new Error("「folder:」后缺少批次名；未分类请使用 folder:-。");
    } else if (!value.text) {
      throw new Error("「username:」后需要账号名；未绑定请使用 username:-。");
    }
    flat.push({
      kind: "condition",
      field: name,
      value: normalized,
      negate,
      literal: value.kind === "string",
    });
    return {
      type: "condition",
      field: name,
      value: normalized,
      negate,
      literal: value.kind === "string",
    };
  }

  const root = parseExpr();
  if (position < tokens.length) {
    const rest = tokens[position];
    if (rest.kind === "rparen") throw new Error("括号不匹配：多余的右括号。");
    if (rest.kind === "colon" || rest.kind === "eq" || rest.kind === "neq")
      throw new Error(
        '值中包含 : 或 = 等特殊字符；请用英文引号包裹名称，如 folder:"国庆:活动"。',
      );
    if (rest.kind === "lparen")
      throw new Error("「（」前缺少逻辑运算符（and / or）。");
    if (rest.kind === "word" || rest.kind === "string") {
      // 尽量引用完整条件（field:value）而不仅是字段名。
      let text = rest.text;
      const op = tokens[position + 1];
      const val = tokens[position + 2];
      if (
        (op?.kind === "colon" || op?.kind === "eq" || op?.kind === "neq") &&
        (val?.kind === "word" || val?.kind === "string")
      )
        text += `${op.kind === "neq" ? "!=" : ":"}${val.text}`;
      throw new Error(`「${text}」前缺少逻辑运算符（and / or）。`);
    }
    throw new Error("表达式意外的结尾：存在无法解析的内容。");
  }
  return { root, flat };
}

export function parseFilter(src: string): (code: CodeRow) => boolean {
  const { root } = parse(src);
  function evaluate(node: Node, code: CodeRow): boolean {
    switch (node.type) {
      case "or":
        return evaluate(node.left, code) || evaluate(node.right, code);
      case "and":
        return evaluate(node.left, code) && evaluate(node.right, code);
      case "not":
        return !evaluate(node.operand, code);
      case "condition": {
        let matched: boolean;
        switch (node.field) {
          case "status":
            matched = code.status.toLowerCase() === node.value;
            break;
          case "folder":
            matched =
              node.value === "-" && !node.literal
                ? !code.batch
                : code.batch.toLowerCase() === node.value.toLowerCase();
            break;
          case "months":
            matched = code.months === Number(node.value);
            break;
          case "username":
            matched =
              node.value === "-" && !node.literal
                ? !code.username
                : (code.username ?? "").toLowerCase() ===
                  node.value.toLowerCase();
            break;
        }
        return node.negate ? !matched : matched;
      }
    }
  }
  return (code: CodeRow) => evaluate(root, code);
}

// 将文本表达式解析为调色板词元；语法错误时抛出与 parseFilter 相同的错误。
export function parseExpressionTokens(src: string): ExpressionToken[] {
  return parse(src).flat;
}

export const FILTER_HINT =
  "条件：folder:批次名、status:状态、months:时长、username:账号；逻辑：and、or、not、括号；!= 取反；folder:- 未分类、username:- 未绑定；名称含空格、冒号或保留字时用英文引号包裹，如 folder:\"and\"。";
