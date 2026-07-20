// Login screen: password form plus optional Feishu SSO button.

import { state, api, renderPending, refreshAll, render } from "../app.js";
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
}

function _renderLoginHTML(feishuOn) {
  const lang = getCurrentLanguage();
  $("#app").innerHTML =
    `<section class="login-wrap">` +
      `<div class="login-hero">` +
        `<div class="brand-lg">Multi User Docker Platform</div>` +
        `<div class="brand-subtitle">MUDP</div>` +
        `<p>${t("login.brandSubtitle")}</p>` +
        `<ul>` +
          `<li>${t("login.feature1")}</li>` +
          `<li>${t("login.feature2")}</li>` +
          `<li>${t("login.feature3")}</li>` +
          `<li>${t("login.feature4")}</li>` +
        `</ul>` +
      `</div>` +
      `<div class="login-pane">` +
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
