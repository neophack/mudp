// Login screen: password form plus optional Feishu SSO button.

import { state, api, renderPending, refreshAll, render, escapeHtml } from "../app.js";
import { LANG_CHINESE, SUPPORTED_LANGS, getCurrentLanguage, switchLanguage, t, initI18n } from "../lib/i18n.js";

export async function renderLogin() {
  // Apply saved language for the login page (no user session yet)
  const savedLang = localStorage.getItem("mudp_language");
  initI18n(savedLang, null);

  let feishuOn = state.feishu;
  try {
    feishuOn = (await api("/api/feishu/config")).enabled;
    state.feishu = feishuOn;
  } catch {}

  _renderLoginHTML(feishuOn);

  // Record this page view for the security monitor. Only sent when the admin
  // has enabled client collection, and fire-and-forget so a slow/blocked
  // request never blocks the login form.
  trackLoginView();
}

// trackLoginView reports the device hints the security monitor can't get from
// the server side: the browser's local timezone, screen, CPU, memory, platform.
// These describe the real device and so travel with the user even through a
// VPN — they're the basis for the timezone-mismatch detection.
function trackLoginView() {
  let collect = false;
  try {
    // Respect the server-side setting; avoid sending anything if collection
    // is off.
    const xhr = new XMLHttpRequest();
    xhr.open("GET", "/api/security/config", true);
    xhr.onreadystatechange = () => {
      if (xhr.readyState !== 4) return;
      try {
        collect = JSON.parse(xhr.responseText).collectClient === true;
      } catch {}
      if (!collect) return;
      const hints = collectDeviceHints();
      // sendBeacon fires on page unload too; fall back to fetch if unavailable.
      if (navigator.sendBeacon) {
        const ok = navigator.sendBeacon(
          "/api/login/track",
          new Blob([JSON.stringify(hints)], { type: "application/json" })
        );
        if (ok) return;
      }
      api("/api/login/track", { method: "POST", body: JSON.stringify(hints) }).catch(() => {});
    };
    xhr.send();
  } catch {}
}

// collectDeviceHints gathers the local signals a browser can read. Everything
// here is non-sensitive device metadata — no cookies, no credentials.
function collectDeviceHints() {
  const hints = {};
  try {
    hints.timezone = Intl.DateTimeFormat().resolvedOptions().timeZone || "";
  } catch {}
  try {
    hints.language = navigator.language || "";
  } catch {}
  try {
    hints.screen = `${window.screen.width}x${window.screen.height}`;
  } catch {}
  try {
    hints.platform = navigator.platform || (navigator.userAgentData && navigator.userAgentData.platform) || "";
  } catch {}
  try {
    hints.cpuCore = navigator.hardwareConcurrency || 0;
  } catch {}
  try {
    hints.memoryGB = navigator.deviceMemory || 0;
  } catch {}
  try {
    hints.touch = navigator.maxTouchPoints > 0 || "ontouchstart" in window;
  } catch { hints.touch = false; }
  try {
    hints.dnt = navigator.doNotTrack === "1" || navigator.doNotTrack === "yes";
  } catch { hints.dnt = false; }
  return hints;
}

function _renderLoginHTML(feishuOn) {
  const lang = getCurrentLanguage();
  $("#app").innerHTML =
    `<section class="login-wrap">` +
      `<div class="login-hero">` +
        `<div class="brand-lg">Multi User Docker Platform</div>` +
        `<div class="brand-subtitle">${escapeHtml(state.siteName || "MUDP")}</div>` +
        `<p>${t("login.brandSubtitle")}</p>` +
        `<ul>` +
          `<li>${t("login.feature1")}</li>` +
          `<li>${t("login.feature2")}</li>` +
          `<li>${t("login.feature3")}</li>` +
          `<li>${t("login.feature4")}</li>` +
        `</ul>` +
      `</div>` +
      `<div class="login-pane">` +
        `<div class="login-mobile-brand">${escapeHtml(state.siteName || "MUDP")}</div>` +
        `<form class="login-card" id="loginForm">` +
          `<h1>${t("login.title")}</h1>` +
          `<input name="username" placeholder="${t("login.username")}" autocomplete="username" required>` +
          `<input name="password" type="password" placeholder="${t("login.password")}" autocomplete="current-password" required>` +
          `<button class="primary">${t("login.signIn")}</button>` +
          (feishuOn
            ? `<div class="login-divider">${t("login.or")}</div><button type="button" class="login-feishu" id="feishuLogin">${t("login.feishu")}</button>`
            : ``) +
          `<div class="login-lang">` +
            SUPPORTED_LANGS.map((l) =>
              `<button type="button" class="lang-btn${l === lang ? " active" : ""}" data-lang="${l}">${l === LANG_CHINESE ? "中文" : "English"}</button>`
            ).join("") +
          `</div>` +
        `</form>` +
      `</div>` +
    `</section>`;

  // Language switcher
  $("#app").querySelectorAll(".lang-btn").forEach((btn) => {
    btn.onclick = () => {
      localStorage.setItem("mudp_language", btn.dataset.lang);
      switchLanguage(btn.dataset.lang);
      _renderLoginHTML(feishuOn);
    };
  });

  $("#loginForm").onsubmit = async (e) => {
    e.preventDefault();
    const fd = new FormData(e.target);
    try {
      const login = await api("/api/login", { method: "POST", body: JSON.stringify(Object.fromEntries(fd)) });
      state.me = login.user || login;
      state.csrfToken = login.csrfToken || "";
      if (state.me.pending) {
        renderPending();
        return;
      }
      // A forward-auth redirect sends the browser here with ?next=<original URL>;
      // once logged in, send it straight back to the forwarded port it came from.
      // Only same-origin or absolute http(s) URLs are honoured, so the parameter
      // cannot be abused as an open redirect to an arbitrary scheme.
      const next = new URLSearchParams(location.search).get("next");
      if (next && /^https?:\/\//.test(next)) {
        window.location.href = next;
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
