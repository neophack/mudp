// Full-screen "waiting for admin approval" page shown to users still in the
// pending group after a Feishu login.

import { state, api, renderLogin } from "../app.js";

export function renderPending() {
  $("#app").innerHTML =
    `<section class="pending-wrap">` +
      `<div class="pending-card card" style="padding:32px;">` +
        `<div class="pending-icon"></div>` +
        `<h1>等待管理员确认</h1>` +
        `<p>你好 <strong>${escapeHtml(state.me?.username || "")}</strong>，你的账号已创建并进入待审批分组。<br>` +
        `请联系管理员将你加入业务分组后即可开始使用。</p>` +
        `<button class="ghost" id="pendingLogout">退出登录</button>` +
      `</div>` +
    `</section>`;
  $("#pendingLogout").onclick = async () => {
    await api("/api/logout", { method: "POST" }).catch(() => {});
    state.me = null;
    renderLogin();
  };
}

function $(selector) {
  return document.querySelector(selector);
}

function escapeHtml(v) {
  return String(v ?? "").replace(/[&<>"']/g, (m) => ({
    "&": "&amp;",
    "<": "&lt;",
    ">": "&gt;",
    '"': "&quot;",
    "'": "&#39;",
  }[m]));
}
