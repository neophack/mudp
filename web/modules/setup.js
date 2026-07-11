// First-run setup wizard shown when no administrator account exists.

import { state, api, renderLogin } from "../app.js";

export function renderSetup() {
  state.me = null;
  state.csrfToken = "";
  $("#app").innerHTML =
    `<section class="setup-wrap">` +
      `<div class="setup-hero">` +
        `<div class="brand-lg">Multi User Docker Platform</div>` +
        `<div class="brand-subtitle">MUDP</div>` +
        `<p>Welcome! Complete the initial configuration to create the first administrator account and the default user group.</p>` +
      `</div>` +
      `<div class="setup-pane">` +
        `<form class="setup-card" id="setupForm">` +
          `<h1>Initial Setup</h1>` +
          `<label class="field-label">Administrator username</label>` +
          `<input name="adminUsername" placeholder="admin" value="admin" required autocomplete="username">` +
          `<label class="field-label">Administrator password</label>` +
          `<input name="adminPassword" type="password" placeholder="Create a strong password" required autocomplete="new-password">` +
          `<label class="field-label">Site name <span class="hint">(optional)</span></label>` +
          `<input name="siteName" placeholder="MUDP Workspace">` +
          `<label class="field-label">Default users group netdisk path <span class="hint">(optional)</span></label>` +
          `<input name="usersGroupNetdiskPath" placeholder="/srv/mudp/netdisk/users">` +
          `<p class="hint">This path is used as the root for members of the default <strong>users</strong> group. It will be created if it does not exist.</p>` +
          `<button class="primary">Complete Setup</button>` +
        `</form>` +
      `</div>` +
    `</section>`;

  $("#setupForm").onsubmit = async (e) => {
    e.preventDefault();
    const fd = new FormData(e.target);
    const payload = {
      adminUsername: fd.get("adminUsername"),
      adminPassword: fd.get("adminPassword"),
      siteName: fd.get("siteName"),
      usersGroupNetdiskPath: fd.get("usersGroupNetdiskPath"),
    };
    try {
      await api("/api/setup/init", { method: "POST", body: JSON.stringify(payload) });
      renderLogin();
      // eslint-disable-next-line no-undef
      if (typeof toast !== "undefined") toast("Setup complete. Please sign in.", true);
    } catch (err) {
      showSetupError(err.message);
    }
  };
}

function showSetupError(msg) {
  const existing = document.querySelector(".setup-error");
  if (existing) existing.remove();
  const el = document.createElement("div");
  el.className = "toast setup-error";
  el.textContent = msg;
  document.body.appendChild(el);
  setTimeout(() => el.remove(), 5000);
}

function $(selector) {
  return document.querySelector(selector);
}
