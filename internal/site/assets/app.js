"use strict";
const form = document.querySelector("#redeem-form");
const result = document.querySelector("#result");
const panel = document.querySelector("#progress-panel");
const track = document.querySelector("#progress-track");
const fill = document.querySelector("#progress-fill");
const title = document.querySelector("#progress-title");
const percent = document.querySelector("#progress-percent");
const help = document.querySelector("#progress-help");
const openX = document.querySelector("#open-x");
const buttons = [
  document.querySelector("#redeem-button"),
  document.querySelector("#status-button"),
];
const fields = [
  document.querySelector("#code"),
  document.querySelector("#username"),
];
let timer = null,
  attempt = 0,
  current = null,
  lastProgress = 0;
function scrollToProgress() {
  panel.scrollIntoView({
    behavior: matchMedia("(prefers-reduced-motion: reduce)").matches
      ? "instant"
      : "smooth",
    block: "nearest",
  });
}
function show(text, error = false) {
  result.hidden = false;
  result.textContent = text;
  result.classList.toggle("error", error);
}
function progress(value, message, state = "processing") {
  panel.hidden = false;
  lastProgress = Math.max(
    lastProgress,
    Math.min(state === "succeeded" ? 100 : 95, Number(value) || 0),
  );
  fill.style.width = lastProgress + "%";
  percent.textContent = lastProgress + "%";
  track.setAttribute("aria-valuenow", String(lastProgress));
  track.setAttribute("aria-valuetext", lastProgress + "% · " + message);
  title.textContent = message;
  panel.classList.toggle(
    "waiting",
    state === "review" || state === "interrupted",
  );
  panel.classList.toggle("complete", state === "succeeded");
  panel.setAttribute("aria-busy", String(state === "processing"));
  document
    .querySelectorAll(".progress-steps li")
    .forEach((el) =>
      el.classList.toggle("done", lastProgress >= Number(el.dataset.threshold)),
    );
  openX.hidden = state !== "succeeded";
  help.textContent =
    state === "succeeded"
      ? "兑换已完成，无需再次提交。打开 X 查看会员状态；如未刷新，请重新打开 X。"
      : state === "review" || state === "interrupted"
        ? "请保留兑换码，点击「查询兑换进度」核实原订单。查询不会再次扣款；请勿换码重复兑换。"
        : "百分比表示已完成的流程阶段，不是预计耗时。请勿重复提交。";
}
function busy(value) {
  [...buttons, ...fields].forEach((el) => (el.disabled = value));
}
function input() {
  return { code: fields[0].value.trim(), username: fields[1].value.trim() };
}
function reset() {
  clearTimeout(timer);
  attempt = 0;
  lastProgress = 0;
  fill.style.width = "0%";
  openX.hidden = true;
  current = input();
  busy(true);
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
function render(response, data) {
  const status = data.status;
  const text =
    data.message ||
    {
      active: "兑换码尚未使用，可以开始兑换。",
      revoked: "兑换码已停用，请联系提供方。",
    }[status] ||
    "暂时无法确认状态，请稍后查询。";
  show(text, !response.ok || (status === "review" && !data.rechecking));
  if (status === "succeeded") {
    progress(100, "兑换成功，Premium 已赠送", status);
    scrollToProgress();
  } else if (status === "processing") {
    progress(data.progress || 20, text);
  } else if (status === "review" && data.rechecking) {
    progress(Math.max(data.progress || 0, 90), "正在自动核实付款结果，请稍候…");
  } else if (status === "review") {
    progress(
      data.progress || lastProgress,
      "结果暂未确认，请查询原订单",
      "review",
    );
  } else {
    panel.hidden = true;
    openX.hidden = true;
  }
  return status;
}
async function query() {
  try {
    const { response, data } = await request("/api/status", current);
    const status = render(response, data);
    if ((status === "processing" || data.rechecking) && attempt++ < 150) {
      timer = setTimeout(query, 2000);
    } else {
      if (status === "processing" || data.rechecking) {
        progress(data.progress, "订单仍在处理，可以稍后查询", "interrupted");
        show(
          "等待时间较长，不代表付款失败。请保留兑换码，稍后查询进度，勿重复兑换。",
        );
      }
      busy(false);
    }
  } catch (e) {
    progress(lastProgress, "查询暂时中断，原订单不会重付", "interrupted");
    show(e.message, true);
    busy(false);
  }
}
form.addEventListener("submit", async (e) => {
  e.preventDefault();
  reset();
  progress(5, "正在核实 X 账号和赠送资格…");
  show("正在检查你的兑换码和接收账号，请稍候。");
  scrollToProgress();
  try {
    const { response, data } = await request("/api/redeem", current);
    if (render(response, data) === "processing" || data.rechecking)
      timer = setTimeout(query, 1500);
    else busy(false);
  } catch {
    progress(lastProgress, "连接中断，请查询原订单", "interrupted");
    show(
      "连接中断，结果尚不确定。请点击「查询兑换进度」，不要更换兑换码重复提交。",
      true,
    );
    busy(false);
  }
});
buttons[1].addEventListener("click", () => {
  if (!form.reportValidity()) return;
  reset();
  progress(5, "正在查询原订单，不会再次扣款…");
  scrollToProgress();
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
