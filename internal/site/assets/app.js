"use strict";
const form = document.querySelector("#redeem-form"),
  result = document.querySelector("#result");
const buttons = [
  document.querySelector("#redeem-button"),
  document.querySelector("#status-button"),
];
let timer = null,
  attempt = 0,
  current = null;
function show(text, error = false) {
  result.hidden = false;
  result.textContent = text;
  result.classList.toggle("error", error);
}
function busy(value) {
  buttons.forEach((b) => (b.disabled = value));
}
function input() {
  return {
    code: document.querySelector("#code").value.trim(),
    username: document.querySelector("#username").value.trim(),
  };
}
async function request(path, payload) {
  const response = await fetch(path, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(payload),
  });
  let data;
  try {
    data = await response.json();
  } catch {
    throw new Error("服务暂时不可用，请稍后查询兑换进度。");
  }
  return { response, data };
}
async function query() {
  try {
    const { response, data } = await request("/api/status", current);
    show(
      data.message ||
        { active: "兑换码尚未使用。", revoked: "兑换码已停用。" }[
          data.status
        ] ||
        "请联系管理员核实。",
      !response.ok || data.status === "review",
    );
    if (data.status === "processing" && attempt++ < 36) {
      timer = setTimeout(query, 5000);
    } else {
      busy(false);
    }
  } catch (e) {
    show(e.message, true);
    busy(false);
  }
}
form.addEventListener("submit", async (e) => {
  e.preventDefault();
  clearTimeout(timer);
  busy(true);
  show("正在核实 X 账号和赠送资格…");
  current = input();
  attempt = 0;
  try {
    const { response, data } = await request("/api/redeem", current);
    show(data.message, !response.ok || data.status === "review");
    if (data.status === "processing") {
      timer = setTimeout(query, 5000);
    } else {
      busy(false);
    }
  } catch {
    show(
      "连接中断，结果尚不确定。请点击「查询兑换进度」，不要更换兑换码重复提交。",
      true,
    );
    busy(false);
  }
});
buttons[1].addEventListener("click", () => {
  if (!form.reportValidity()) return;
  clearTimeout(timer);
  current = input();
  attempt = 0;
  busy(true);
  query();
});

fetch("/healthz")
  .then((r) => {
    if (!r.ok) throw new Error();
    return r.json();
  })
  .then((data) => {
    if (!data.payments_enabled) {
      const notice = document.querySelector("#service-notice");
      notice.textContent =
        "充值暂未开放。可以先核实账号赠送资格，兑换码不会被使用。";
      notice.hidden = false;
      buttons[0].firstChild.textContent = "核实赠送资格 ";
    }
  })
  .catch(() => {});
