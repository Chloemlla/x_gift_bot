// 兑换码筛选表达式：词法分析 + 递归下降解析 + 求值。
// 语法：expr := orExpr；orExpr := andExpr (('or'|'||') andExpr)*；
// andExpr := factor (('and'|'&&') factor)*；factor := '(' expr ')' | condition；
// condition := ('folder'|'status') ':' value。字段与运算符不区分大小写。

export type CodeRow = {
  batch: string;
  status: string;
};

const STATUSES = ["active", "processing", "review", "succeeded", "revoked"];

type Token =
  | { kind: "lparen" }
  | { kind: "rparen" }
  | { kind: "colon" }
  | { kind: "and" }
  | { kind: "or" }
  | { kind: "word"; text: string }
  | { kind: "string"; text: string };

function tokenize(src: string): Token[] {
  // 中文输入法容错：全角括号、冒号、引号统一归一为半角。
  src = src
    .replace(/（/g, "(")
    .replace(/）/g, ")")
    .replace(/：/g, ":")
    .replace(/[＂“”]/g, '"');
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
    if (char === '"') {
      const end = src.indexOf('"', index + 1);
      if (end === -1)
        throw new Error("引号不匹配：批次名称的英文双引号没有闭合。");
      tokens.push({ kind: "string", text: src.slice(index + 1, end) });
      index = end + 1;
      continue;
    }
    const match = /^[^\s()":]+/.exec(src.slice(index))!;
    const word = match[0];
    const lower = word.toLowerCase();
    if (lower === "and" || word === "&&") tokens.push({ kind: "and" });
    else if (lower === "or" || word === "||") tokens.push({ kind: "or" });
    else tokens.push({ kind: "word", text: word });
    index += word.length;
  }
  return tokens;
}

type Node =
  | { type: "or"; left: Node; right: Node }
  | { type: "and"; left: Node; right: Node }
  | { type: "folder"; value: string }
  | { type: "status"; value: string };

// 调色板模式使用的扁平词元，按表达式中的出现顺序记录。
export type ExpressionToken =
  | { kind: "condition"; field: "folder" | "status"; value: string }
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
    let left = parseFactor();
    while (peek()?.kind === "and") {
      next();
      flat.push({ kind: "and" });
      left = { type: "and", left, right: parseFactor() };
    }
    return left;
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
      throw new Error("无法识别的条件：请使用 folder:批次名 或 status:状态。");
    const name = field.text.toLowerCase();
    if (name !== "folder" && name !== "status")
      throw new Error(`未知字段「${field.text}」，仅支持 folder 和 status。`);
    if (peek()?.kind !== "colon")
      throw new Error(`「${field.text}」后缺少冒号。`);
    next();
    const value = peek();
    if (!value || (value.kind !== "word" && value.kind !== "string"))
      throw new Error(`「${field.text}:」后缺少值。`);
    next();
    if (name === "status") {
      const status = value.text.toLowerCase();
      if (!STATUSES.includes(status))
        throw new Error(
          `不支持的状态值「${value.text}」，可用：${STATUSES.join("、")}。`,
        );
      flat.push({ kind: "condition", field: "status", value: status });
      return { type: "status", value: status };
    }
    if (!value.text)
      throw new Error("「folder:」后缺少批次名；未分类请使用 folder:-。");
    flat.push({ kind: "condition", field: "folder", value: value.text });
    return { type: "folder", value: value.text };
  }

  const root = parseExpr();
  if (position < tokens.length) {
    const rest = tokens[position];
    if (rest.kind === "rparen") throw new Error("括号不匹配：多余的右括号。");
    if (rest.kind === "lparen")
      throw new Error("「（」前缺少逻辑运算符（and / or）。");
    if (rest.kind === "word" || rest.kind === "string") {
      // 尽量引用完整条件（field:value）而不仅是字段名。
      let text = rest.text;
      const colon = tokens[position + 1];
      const val = tokens[position + 2];
      if (
        colon?.kind === "colon" &&
        (val?.kind === "word" || val?.kind === "string")
      )
        text += `:${val.text}`;
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
      case "status":
        return code.status.toLowerCase() === node.value;
      case "folder":
        return node.value === "-"
          ? !code.batch
          : code.batch.toLowerCase() === node.value.toLowerCase();
    }
  }
  return (code: CodeRow) => evaluate(root, code);
}

// 将文本表达式解析为调色板词元；语法错误时抛出与 parseFilter 相同的错误。
export function parseExpressionTokens(src: string): ExpressionToken[] {
  return parse(src).flat;
}

export const FILTER_HINT =
  "条件：folder:批次名、status:状态；逻辑：and、or、括号；folder:- 表示未分类。";
