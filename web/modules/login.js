// Login screen: password form plus optional Feishu SSO button.

import { state, api, renderPending, refreshAll, render } from "../app.js";

export async function renderLogin() {
  let feishuOn = state.feishu;
  try {
    feishuOn = (await api("/api/feishu/config")).enabled;
    state.feishu = feishuOn;
  } catch {}
  $("#app").innerHTML =
    `<section class="login-wrap">` +
      `<div class="login-hero">` +
        `<div class="brand-lg">Multi User Docker Platform</div>` +
        `<div class="brand-subtitle">MUDP</div>` +
        `<p>A compact, self-hosted container console. Manage Docker workloads, GPU access, SSH and web-based VS Code — all from one clean panel.</p>` +
        `<ul>` +
          `<li>One-click containers with SSH &amp; VS Code</li>` +
          `<li>Live creation progress and web terminal</li>` +
          `<li>GPU passthrough and per-user quotas</li>` +
          `<li>Feishu single sign-on with admin approval</li>` +
        `</ul>` +
      `</div>` +
      `<div class="login-pane">` +
        `<form class="login-card" id="loginForm">` +
          `<h1>Sign in</h1>` +
          `<input name="username" placeholder="Username" autocomplete="username" required>` +
          `<input name="password" type="password" placeholder="Password" autocomplete="current-password" required>` +
          `<button class="primary">Sign In</button>` +
          `<p class="hint">Default: admin / admin123 (override with env vars before first launch).</p>` +
          (feishuOn
            ? `<div class="login-divider">or</div><button type="button" class="login-feishu" id="feishuLogin">Sign in with Feishu</button>`
            : ``) +
        `</form>` +
      `</div>` +
    `</section>`;

  $("#loginForm").onsubmit = async (e) => {
    e.preventDefault();
    const fd = new FormData(e.target);
    try {
      await api("/api/login", { method: "POST", body: JSON.stringify(Object.fromEntries(fd)) });
      state.me = await api("/api/me");
      if (state.me.pending) {
        renderPending();
        return;
      }
      await refreshAll();
      render();
    } catch (err) {
      showError(err.message);
    }
  };
  const fb = $("#feishuLogin");
  if (fb) {
    fb.onclick = () => {
      window.location.href = "/api/feishu/login";
    };
  }
}

function $(selector) {
  return document.querySelector(selector);
}

function showError(msg) {
  const el = document.createElement("div");
  el.className = "toast";
  el.textContent = msg;
  document.body.appendChild(el);
  setTimeout(() => el.remove(), 3400);
}
