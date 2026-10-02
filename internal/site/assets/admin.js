"use strict";
const statusNames = {
  active: "可使用",
  processing: "处理中",
  succeeded: "已完成",
  review: "待核实",
  revoked: "已停用",
};
const result = document.querySelector("#admin-result"),
  form = document.querySelector("#generate-form");
let downloadText = "",
  batchName = "codes",
  page = 0;
function show(text, error = false) {
  result.hidden = false;
  result.textContent = text;
  result.classList.toggle("error", error);
}
async function api(path, body) {
  const response = await fetch(
    path,
    body
      ? {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify(body),
        }
      : {},
  );
  let data;
  try {
    data = await response.json();
  } catch {
    throw new Error("服务暂时不可用。");
  }
  if (!response.ok) throw new Error(data.message || "请求失败");
  return data;
}
async function refresh() {
  try {
    const data = await api("/api/admin/codes?page=" + page);
    document.querySelector("#prev").disabled = page === 0;
    document.querySelector("#next").disabled = !data.has_more;
    document.querySelector("#page-label").textContent =
      "第 " + (page + 1) + " 页";
    document.querySelector("#service-status").textContent =
      data.payments_enabled ? "充值已开放" : "充值暂停 · 不会付款";
    const body = document.querySelector("#codes-body");
    body.replaceChildren();
    for (const c of data.codes) {
      const tr = document.createElement("tr");
      const id = document.createElement("td");
      id.textContent = "…" + c.hint;
      const batch = document.createElement("small");
      batch.textContent = c.batch;
      id.append(batch);
      tr.append(id);
      const months = document.createElement("td");
      months.textContent = c.months + " 个月";
      tr.append(months);
      const state = document.createElement("td");
      const badge = document.createElement("span");
      badge.className = "badge " + c.status;
      badge.textContent = statusNames[c.status] || c.status;
      badge.title = c.message;
      state.append(badge);
      tr.append(state);
      const user = document.createElement("td");
      user.textContent = c.username ? "@" + c.username : "—";
      tr.append(user);
      const date = document.createElement("td");
      date.textContent = new Date(c.created * 1000).toLocaleString();
      tr.append(date);
      const action = document.createElement("td");
      if (c.status === "active") {
        const b = document.createElement("button");
        b.type = "button";
        b.className = "secondary compact";
        b.textContent = "停用";
        b.addEventListener("click", async () => {
          if (!confirm("停用尾号 " + c.hint + " 的兑换码？此操作不可撤销。"))
            return;
          b.disabled = true;
          try {
            await api("/api/admin/revoke", { id: c.id });
            await refresh();
          } catch (e) {
            show(e.message, true);
            b.disabled = false;
          }
        });
        action.append(b);
      } else {
        action.textContent = "—";
      }
      tr.append(action);
      body.append(tr);
    }
    if (!data.codes.length) {
      const tr = document.createElement("tr"),
        td = document.createElement("td");
      td.colSpan = 6;
      td.textContent = "还没有兑换码，在上方生成第一批。";
      tr.append(td);
      body.append(tr);
    }
  } catch (e) {
    show(e.message, true);
  }
}
form.addEventListener("submit", async (e) => {
  e.preventDefault();
  if (
    downloadText &&
    !confirm("上一批兑换码已下载保存？生成下一批会替换当前显示。")
  )
    return;
  const button = document.querySelector("#generate-button");
  button.disabled = true;
  try {
    const data = await api("/api/admin/codes", {
      months: Number(document.querySelector("#months").value),
      count: Number(document.querySelector("#count").value),
      batch: document.querySelector("#batch").value,
    });
    downloadText = data.codes.join("\n") + "\n";
    batchName = "xgift-" + data.months + "mo-" + Date.now();
    document.querySelector("#new-codes").value = downloadText;
    document.querySelector("#generated").hidden = false;
    show(
      "已生成 " +
        data.codes.length +
        " 枚兑换码，请立即下载。本页刷新后无法恢复明文。",
    );
    page = 0;
    await refresh();
  } catch (e) {
    show(e.message + " 若连接中断，请先核实批次列表，不要立即重复生成。", true);
  } finally {
    button.disabled = false;
  }
});
document.querySelector("#download").addEventListener("click", () => {
  const url = URL.createObjectURL(
    new Blob([downloadText], { type: "text/plain;charset=utf-8" }),
  );
  const a = document.createElement("a");
  a.href = url;
  a.download = batchName + ".txt";
  a.click();
  setTimeout(() => URL.revokeObjectURL(url), 1000);
});
document.querySelector("#clear-codes").addEventListener("click", () => {
  if (!confirm("确认已保存兑换码？清除后无法恢复明文。")) return;
  downloadText = "";
  document.querySelector("#new-codes").value = "";
  document.querySelector("#generated").hidden = true;
});
document.querySelector("#refresh").addEventListener("click", refresh);
refresh();
window.addEventListener("beforeunload", (e) => {
  if (downloadText) {
    e.preventDefault();
    e.returnValue = "";
  }
});

document.querySelector("#prev").addEventListener("click", () => {
  if (page > 0) {
    page--;
    refresh();
  }
});
document.querySelector("#next").addEventListener("click", () => {
  page++;
  refresh();
});
