// Isolated UI preview: synthetic data only. No upstream API or payment access.
import { createServer } from "node:http";
import { readFile } from "node:fs/promises";
import { gzipSync } from "node:zlib";
import { randomBytes } from "node:crypto";

const port = Number(process.env.PREVIEW_PORT || 4173);
const paused = process.env.PREVIEW_PAUSED === "true";
const states = new Map();
let folders = [{ id: "a".repeat(32), name: "本地预览 · 示例批次" }];
const plaintext = new Map();
const now = Math.floor(Date.now() / 1000);
let codes = ["active", "processing", "succeeded", "review", "revoked"].map(
  (status, index) => ({
    id: (index + 1).toString(16).padStart(32, "0"),
    folder: "a".repeat(32),
    copyable: false,
    hint: `DEMO000${index}`,
    batch: "本地预览 · 示例批次",
    months: index % 2 ? 3 : 6,
    status,
    username: index > 0 && index < 4 ? "demo_user" : "",
    message: status === "review" ? "示例：结果正在核实，请勿重复兑换。" : "",
    created: now - index * 3600,
  }),
);
const files = {
  "/": ["index.html", "text/html"],
  "/admin": ["admin.html", "text/html"],
  "/appearance.js": ["appearance.js", "application/javascript"],
  "/app.js": ["app.js", "application/javascript"],
  "/admin.js": ["admin.js", "application/javascript"],
  "/favicon.svg": ["favicon.svg", "image/svg+xml"],
};

createServer(async (req, res) => {
  const nonce = randomBytes(16).toString("hex");
  res.setHeader("Cache-Control", "no-store");
  res.setHeader(
    "Content-Security-Policy",
    `default-src 'none'; script-src 'self'; style-src 'self' 'nonce-${nonce}'; style-src-attr 'unsafe-inline'; connect-src 'self'; img-src 'self'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'`,
  );
  res.setHeader("X-Content-Type-Options", "nosniff");
  const url = new URL(req.url, `http://127.0.0.1:${port}`);
  function json(status, data) {
    res.writeHead(status, { "Content-Type": "application/json" });
    res.end(JSON.stringify(data));
  }
  try {
    if (req.method === "GET" && files[url.pathname]) {
      const [file, type] = files[url.pathname];
      let data = await readFile(
        new URL(`../../internal/site/assets/${file}`, import.meta.url),
      );
      if (type === "text/html")
        data = data
          .toString()
          .replaceAll("__XGIFT_NONCE__", nonce)
          .replace("</title>", " · 本地模拟预览</title>");
      res.setHeader("Vary", "Accept-Encoding");
      if (/\bgzip\b/.test(req.headers["accept-encoding"] || "")) {
        data = gzipSync(data, { level: 9 });
        res.setHeader("Content-Encoding", "gzip");
      }
      res.writeHead(200, { "Content-Type": type });
      res.end(data);
      return;
    }
    if (req.method === "GET" && url.pathname === "/healthz") {
      json(200, { ok: true, payments_enabled: !paused });
      return;
    }
    if (req.method === "GET" && url.pathname === "/api/admin/codes") {
      const page = Math.max(0, Number(url.searchParams.get("page")) || 0);
      const folder = url.searchParams.get("folder") || "";
      if (
        folder &&
        folder !== "unfiled" &&
        !folders.some((item) => item.id === folder)
      ) {
        json(404, { message: "文件夹不存在，请刷新列表。" });
        return;
      }
      const filtered = codes.filter(
        (code) =>
          !folder ||
          (folder === "unfiled" ? !code.folder : code.folder === folder),
      );
      const stats = {
        total: codes.length,
        active: 0,
        processing: 0,
        succeeded: 0,
        review: 0,
        revoked: 0,
        unfiled: codes.filter((code) => !code.folder).length,
      };
      for (const code of codes) stats[code.status]++;
      json(200, {
        codes: filtered.slice(page * 100, (page + 1) * 100),
        page,
        folder,
        stats,
        folders: folders
          .map((item) => ({
            ...item,
            count: codes.filter((code) => code.folder === item.id).length,
          }))
          .sort((a, b) => a.name.localeCompare(b.name)),
        has_more: filtered.length > (page + 1) * 100,
        payments_enabled: !paused,
      });
      return;
    }
    if (req.method !== "POST") {
      json(404, { message: "预览路由不存在" });
      return;
    }
    let raw = "";
    for await (const chunk of req) {
      raw += chunk;
      if (raw.length > 4096) {
        json(413, { message: "请求过大" });
        return;
      }
    }
    const body = JSON.parse(raw);
    if (
      url.pathname === "/api/admin/folders" ||
      url.pathname === "/api/admin/folders/rename"
    ) {
      const name = typeof body.name === "string" ? body.name.trim() : "";
      if (!name || Buffer.byteLength(name) > 120) {
        json(400, { message: "请输入有效文件夹名称（最多 120 字节）。" });
        return;
      }
      if (
        folders.some(
          (folder) =>
            folder.name.toLowerCase() === name.toLowerCase() &&
            folder.id !== body.id,
        )
      ) {
        json(409, { message: "已存在同名文件夹。" });
        return;
      }
      if (url.pathname.endsWith("/rename")) {
        const folder = folders.find((item) => item.id === body.id);
        if (!folder) {
          json(404, { message: "文件夹不存在。" });
          return;
        }
        folder.name = name;
        for (const code of codes)
          if (code.folder === folder.id) code.batch = name;
        json(200, { message: "已重命名。" });
        return;
      }
      const folder = { id: randomBytes(16).toString("hex"), name };
      folders.push(folder);
      json(201, { ...folder, count: 0 });
      return;
    }
    if (url.pathname === "/api/admin/folders/delete") {
      if (!folders.some((folder) => folder.id === body.id)) {
        json(404, { message: "文件夹不存在。" });
        return;
      }
      folders = folders.filter((folder) => folder.id !== body.id);
      for (const code of codes)
        if (code.folder === body.id) {
          code.folder = "";
          code.batch = "";
        }
      json(200, { message: "文件夹已删除，兑换码已移至未分类。" });
      return;
    }
    if (url.pathname === "/api/admin/codes/move") {
      if (
        !Array.isArray(body.ids) ||
        !body.ids.length ||
        body.ids.length > 100 ||
        new Set(body.ids).size !== body.ids.length ||
        !body.ids.every((id) => codes.some((code) => code.id === id))
      ) {
        json(409, { message: "兑换码选择无效，请刷新列表。" });
        return;
      }
      if (body.folder && !folders.some((folder) => folder.id === body.folder)) {
        json(404, { message: "文件夹不存在。" });
        return;
      }
      for (const code of codes)
        if (body.ids.includes(code.id)) {
          code.folder = body.folder || "";
          code.batch = folders.find((f) => f.id === code.folder)?.name || "";
        }
      json(200, { message: "已更新分类。" });
      return;
    }
    if (url.pathname === "/api/admin/codes") {
      if (
        ![3, 6].includes(body.months) ||
        !Number.isInteger(body.count) ||
        body.count < 1 ||
        body.count > 500 ||
        typeof body.batch !== "string" ||
        Buffer.byteLength(body.batch) > 120
      ) {
        json(400, { message: "请检查套餐、数量和批次名称。" });
        return;
      }
      if (body.folder && !folders.some((folder) => folder.id === body.folder)) {
        json(404, { message: "文件夹不存在。" });
        return;
      }
      let batch = body.batch || "本地预览-" + Date.now();
      let folder = folders.find(
        (f) => f.name.toLowerCase() === batch.toLowerCase(),
      );
      if (!folder) {
        folder = { id: randomBytes(16).toString("hex"), name: batch };
        folders.push(folder);
      }
      batch = folder.name;
      body.folder = folder.id;
      const generated = Array.from(
        { length: body.count },
        () => "XG-" + randomBytes(24).toString("hex").toUpperCase(),
      );
      codes = [
        ...generated.map((code) => {
          const id = randomBytes(16).toString("hex");
          plaintext.set(id, code);
          return {
            id,
            copyable: true,
            folder: body.folder || "",
            hint: code.slice(-8),
            batch,
            months: body.months,
            status: "active",
            username: "",
            message: "",
            created: now,
          };
        }),
        ...codes,
      ];
      json(201, {
        codes: generated,
        batch,
        months: body.months,
        folder: body.folder || "",
      });
      return;
    }
    if (url.pathname === "/api/admin/codes/copy") {
      const code = plaintext.get(body.id);
      json(
        code ? 200 : 409,
        code ? { code } : { message: "历史兑换码未保存完整内容。" },
      );
      return;
    }
    if (url.pathname === "/api/admin/revoke") {
      const code = codes.find((item) => item.id === body.id);
      if (!code || code.status !== "active") {
        json(409, { message: "只能停用未使用的兑换码。" });
        return;
      }
      code.status = "revoked";
      json(200, { message: "兑换码已停用。" });
      return;
    }
    if (url.pathname === "/api/redeem" || url.pathname === "/api/status") {
      if (
        !/^XG-[A-F0-9]{48}$/.test(body.code) ||
        !/^[a-z0-9_]{1,15}$/.test(body.username)
      ) {
        json(400, { message: "请检查兑换码和用户名。" });
        return;
      }
      const ending = body.code.slice(-1);
      if (ending === "C") {
        json(422, {
          message: "示例：X 当前不允许向这个账号赠送 Premium。兑换码未使用。",
        });
        return;
      }
      if (ending === "D") {
        json(200, {
          status: "revoked",
          message: "兑换码已停用，请联系提供方。",
        });
        return;
      }
      if (req.url === "/api/redeem" && paused) {
        json(503, {
          status: "paused",
          message: "账号可以接收赠送，但充值服务暂未开放。兑换码未使用。",
        });
        return;
      }
      const key = body.code + ":" + body.username;
      if (req.url === "/api/redeem") states.set(key, Date.now());
      if (!states.has(key)) {
        json(200, { status: "active", progress: 0, months: 6 });
        return;
      }
      const elapsed = Date.now() - states.get(key);
      if (ending === "B" && elapsed > 3000) {
        json(200, {
          status: "review",
          progress: 90,
          months: 6,
          message: "示例：结果暂未确认，请查询原订单或联系管理员。",
        });
        return;
      }
      const progress =
        ending === "F"
          ? 70
          : Math.min(100, 20 + Math.floor(elapsed / 1500) * 20);
      json(200, {
        status: progress === 100 ? "succeeded" : "processing",
        progress,
        months: 6,
        message:
          progress === 100
            ? `本地模拟：已为 @${body.username} 完成 6 个月 Premium 赠送（未执行真实付款）。`
            : "本地模拟：正在核验订单，请稍候。",
      });
      return;
    }
    json(404, { message: "预览路由不存在" });
  } catch {
    json(400, { message: "预览请求无法处理。" });
  }
}).listen(port, "127.0.0.1", () =>
  console.log(
    `本地模拟预览：http://127.0.0.1:${port} /admin；所有数据均为示例，不连接生产或付款服务。`,
  ),
);
