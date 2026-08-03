// Full-screen "waiting for admin approval" page shown to users still in the
// pending group after a Feishu login.

import { state, api, renderLogin, displayName } from "../app.js";
import { t } from "../lib/i18n.js";

export function renderPending() {
  $("#app").innerHTML =
    `<section class="pending-wrap">` +
      `<div class="pending-card card" style="padding:32px;">` +
        `<div class="pending-icon"></div>` +
        `<h1>${t("pending.title")}</h1>` +
        `<p>${t("pending.greeting", { name: escapeHtml(displayName(state.me)) })}<br>` +
        `${t("pending.hint")}</p>` +
        `<button class="ghost" id="pendingLogout">${t("user.logout")}</button>` +
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
