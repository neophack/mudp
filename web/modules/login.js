// Login screen: password form plus optional Feishu SSO button.

import { state, api, renderPending, refreshAll, render, escapeHtml } from "../app.js";
import { LANG_CHINESE, SUPPORTED_LANGS, getCurrentLanguage, switchLanguage, t, initI18n } from "../lib/i18n.js";
import { detectPublicIP } from "../lib/publicip.js";

export async function renderLogin() {
  // Apply saved language for the login page (no user session yet)
  const savedLang = localStorage.getItem("mudp_language");
  initI18n(savedLang, null);

  // Probe the visitor's public IP right away, before anything else, and stash
  // it in a short-lived cookie. The /api/login request (and any later login
  // attempt) carries this cookie automatically, so login_success/login_failed
  // events know the WAN address even on an intranet deployment — where the
  // server only sees the last private hop. Fire-and-forget: a slow/blocked
  // STUN probe never blocks the page or the form.
  probePublicIPCookie();

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

// PUBLIC_IP_COOKIE carries the WebRTC/STUN-reflexive WAN address so it rides
// along on the /api/login request. 30 minutes covers a normal sign-in flow;
// it is a non-sensitive display value (only used for GeoIP on the access map).
const PUBLIC_IP_COOKIE = "mudp_pubip";
const PUBLIC_IP_COOKIE_TTL_MIN = 30;

// probePublicIPCookie resolves the visitor's public IP via WebRTC/STUN and
// writes it into a short-lived cookie. The cookie is then attached to every
// same-origin request — most importantly /api/login — so the security monitor
// can geo-locate login attempts even when the server's view of the source is a
// private address. Does nothing when WebRTC is unavailable or STUN is blocked.
async function probePublicIPCookie() {
  let ip = "";
  try {
    const ips = await detectPublicIP(2500);
    if (ips.length) ip = ips[0];
  } catch {}
  if (ip) {
    // SameSite=Lax so it is sent on the top-level login POST; not HttpOnly
    // because it is a harmless display value the page itself set. urlencoded
    // in case STUN ever reports an IPv6 literal.
    document.cookie =
      `${PUBLIC_IP_COOKIE}=${encodeURIComponent(ip)}` +
      `; max-age=${PUBLIC_IP_COOKIE_TTL_MIN * 60}` +
      `; path=/; SameSite=Lax`;
  }
}

// readPublicIPCookie returns the public IP stashed by probePublicIPCookie, or
// "" when none is set (WebRTC unavailable / cookie expired / blocked).
export function readPublicIPCookie() {
  const prefix = PUBLIC_IP_COOKIE + "=";
  for (const part of document.cookie.split(";")) {
    const trimmed = part.trim();
    if (trimmed.startsWith(prefix)) {
      try { return decodeURIComponent(trimmed.slice(prefix.length)); }
      catch { return ""; }
    }
  }
  return "";
}

// trackLoginView reports the device hints the security monitor can't get from
// the server side: the browser's local timezone, screen, CPU, memory, platform,
// and the visitor's own public IP. These describe the real device and so travel
// with the user even through a VPN — they're the basis for the timezone-mismatch
// detection. The public IP is gathered client-side via WebRTC/STUN because the
// server can only see the source of its last network hop: when the visitor is on
// the same LAN as the server that hop is a private address, which has no GeoIP
// answer, so the access map would show nothing for intranet deployments.
async function trackLoginView() {
  let collect = false;
  try {
    // Respect the server-side setting; avoid sending anything if collection
    // is off.
    const xhr = new XMLHttpRequest();
    xhr.open("GET", "/api/security/config", true);
    await new Promise((resolve) => {
      xhr.onreadystatechange = () => { if (xhr.readyState === 4) resolve(); };
      xhr.send();
    });
    try { collect = JSON.parse(xhr.responseText).collectClient === true; } catch {}
    if (!collect) return;
    const hints = collectDeviceHints();
    // The public IP may already be in the cookie from the early probe; fall
    // back to a fresh probe if the early one hadn't resolved yet.
    let ip = readPublicIPCookie();
    if (!ip) {
      try {
        const ips = await detectPublicIP(2500);
        if (ips.length) ip = ips[0];
      } catch {}
    }
    if (ip) hints.publicIP = ip;
    sendHints(hints);
  } catch {}
}

// sendHints delivers the collected hints to the track endpoint. sendBeacon
// survives page unload; fetch is the fallback.
function sendHints(hints) {
  if (navigator.sendBeacon) {
    const ok = navigator.sendBeacon(
      "/api/login/track",
      new Blob([JSON.stringify(hints)], { type: "application/json" })
    );
    if (ok) return;
  }
  api("/api/login/track", { method: "POST", body: JSON.stringify(hints) }).catch(() => {});
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
      // The forwarded port lives on the same host as the console but on a
      // different port (see forward_auth.go's forwardLoginTarget), so the check
      // is against hostname, not full origin -- but it still has to check the
      // hostname: checking only the scheme, as before, let ?next= redirect to
      // any http(s) URL at all, including an attacker's own domain.
      const next = new URLSearchParams(location.search).get("next");
      if (next) {
        try {
          const target = new URL(next, location.origin);
          if ((target.protocol === "http:" || target.protocol === "https:") && target.hostname === location.hostname) {
            window.location.href = target.href;
            return;
          }
        } catch {
          // Malformed URL: fall through to the normal post-login render.
        }
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
