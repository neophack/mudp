// Full-screen "waiting for admin approval" page shown to users still in the
// pending group after a Feishu login.

import { state, api, renderLogin, displayName } from "../app.js";

export function renderPending() {
  $("#app").innerHTML =
    `<section class="pending-wrap">` +
      `<div class="pending-card card" style="padding:32px;">` +
        `<div class="pending-icon"></div>` +
        `<h1>Waiting for Admin Approval</h1>` +
        `<p>Hello <strong>${escapeHtml(displayName(state.me))}</strong>, your account has been created and placed in the pending approval group.<br>` +
        `Please contact an administrator to add you to a business group before you can start using the platform.</p>` +
        `<button class="ghost" id="pendingLogout">Log Out</button>` +
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
