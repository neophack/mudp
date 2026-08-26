<template>
  <section class="auth-wrap">
    <!-- Brand hero: macOS-style aurora panel on wide screens, stacked on top
         when narrow. The blob layer is pure decoration behind the glass. -->
    <div class="auth-hero">
      <div class="hero-bg" aria-hidden="true"><span class="g1"></span><span class="g2"></span><span class="g3"></span><span class="g4"></span></div>
      <div class="login-brand">
        <div class="app-icon" aria-hidden="true">
          <img src="/mudp.png" alt="" draggable="false" />
        </div>
        <div class="app-name">{{ s.siteName || "MUDP" }}</div>
        <div class="app-tagline">{{ tt("login.brandSubtitle") }}</div>
        <div class="hero-feats">
          <span v-for="i in 4" :key="i">{{ tt("login.feature" + i) }}</span>
        </div>
      </div>
    </div>
    <div class="auth-pane">
      <form class="auth-card" @submit.prevent="submit">
        <h1>{{ tt("login.title") }}</h1>
        <el-input v-model="form.username" name="username" :placeholder="tt('login.username')" autocomplete="username" />
        <el-input v-model="form.password" name="password" type="password" show-password :placeholder="tt('login.password')" autocomplete="current-password" @keyup.enter="submit" />
        <!-- GIF captcha: image + code side by side; clicking the image or the
             refresh icon pulls a fresh challenge (the old id is single-use). -->
        <div class="captcha-row">
          <el-input
            v-model="form.captcha"
            name="captcha"
            :placeholder="tt('login.captcha')"
            autocomplete="off"
            maxlength="5"
            @keyup.enter="submit"
          />
          <button type="button" class="captcha-img" :title="tt('login.captchaRefresh')" @click="loadCaptcha">
            <img v-if="captchaUrl" :src="captchaUrl" alt="captcha" draggable="false" />
            <span v-else class="captcha-loading"></span>
          </button>
        </div>
        <el-button type="primary" native-type="submit" :loading="busy" class="auth-submit">{{ tt("login.signIn") }}</el-button>
        <template v-if="feishuOn">
          <div class="login-divider">{{ tt("login.or") }}</div>
          <el-button class="login-feishu" @click="feishuLogin">{{ tt("login.feishu") }}</el-button>
        </template>
        <div class="login-lang">
          <button
            v-for="l in langs"
            :key="l"
            type="button"
            class="lang-btn"
            :class="{ active: l === currentLang }"
            @click="switchLang(l)"
          >{{ l === "zh_CN" ? "中文" : "English" }}</button>
        </div>
      </form>
    </div>
  </section>
</template>

<script>
import { ElMessage } from "element-plus";
import { api } from "@/api";
import { store } from "@/store";
import { tt, setLanguage } from "@/i18n";
import { initI18n, getCurrentLanguage, SUPPORTED_LANGS } from "@/lib/i18n.js";
import { detectClientIP } from "@/lib/publicip.js";
import { resolveNextRedirect } from "@/lib/common.js";
import { afterLogin } from "@/boot";

// The public-IP cookie carries the WebRTC/STUN-reflexive WAN address so it
// rides along on the /api/login request. 30 minutes covers a normal sign-in
// flow; it is a non-sensitive display value (only used for GeoIP on the
// access map).
const PUBLIC_IP_COOKIE = "mudp_pubip";
const PUBLIC_IP_COOKIE_TTL_MIN = 30;

function readPublicIPCookie() {
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

async function probePublicIPCookie() {
  let ip = "";
  try {
    const { public: ips } = await detectClientIP(2500);
    if (ips.length) ip = ips[0];
  } catch { /* WebRTC unavailable or STUN blocked */ }
  if (ip) {
    // SameSite=Lax so it is sent on the top-level login POST; not HttpOnly
    // because it is a harmless display value the page itself set.
    document.cookie =
      `${PUBLIC_IP_COOKIE}=${encodeURIComponent(ip)}` +
      `; max-age=${PUBLIC_IP_COOKIE_TTL_MIN * 60}` +
      `; path=/; SameSite=Lax`;
  }
}

// Reports the device hints the security monitor can't get from the server
// side: timezone, screen, CPU, memory, platform, and the visitor's own public
// IP — the basis for timezone-mismatch detection. Only sent when the admin
// has enabled client collection; fire-and-forget.
async function trackLoginView() {
  try {
    const cfg = await api("/api/security/config");
    if (cfg.collectClient !== true) return;
    const hints = collectDeviceHints();
    let ip = readPublicIPCookie();
    if (!ip) {
      try {
        const { public: ips } = await detectClientIP(2500);
        if (ips.length) ip = ips[0];
      } catch { /* ignore */ }
    }
    if (ip) hints.publicIP = ip;
    sendHints(hints);
  } catch { /* best-effort */ }
}

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

function collectDeviceHints() {
  const hints = {};
  try { hints.timezone = Intl.DateTimeFormat().resolvedOptions().timeZone || ""; } catch { /* ignore */ }
  try { hints.language = navigator.language || ""; } catch { /* ignore */ }
  try { hints.screen = `${window.screen.width}x${window.screen.height}`; } catch { /* ignore */ }
  try { hints.platform = navigator.platform || (navigator.userAgentData && navigator.userAgentData.platform) || ""; } catch { /* ignore */ }
  try { hints.cpuCore = navigator.hardwareConcurrency || 0; } catch { /* ignore */ }
  try { hints.memoryGB = navigator.deviceMemory || 0; } catch { /* ignore */ }
  try { hints.touch = navigator.maxTouchPoints > 0 || "ontouchstart" in window; } catch { hints.touch = false; }
  try { hints.dnt = navigator.doNotTrack === "1" || navigator.doNotTrack === "yes"; } catch { hints.dnt = false; }
  return hints;
}

export default {
  name: "Login",
  data() {
    return {
      s: store,
      form: { username: "", password: "", captcha: "" },
      captchaId: "",
      captchaUrl: "",
      feishuOn: false,
      busy: false,
      currentLang: getCurrentLanguage(),
      langs: SUPPORTED_LANGS,
    };
  },
  async created() {
    // Apply saved language for the login page (no user session yet).
    initI18n(localStorage.getItem("mudp_language"), null);
    this.currentLang = getCurrentLanguage();
    // Probe the visitor's public IP before anything else so the /api/login
    // request carries the cookie. Fire-and-forget.
    probePublicIPCookie();
    this.loadCaptcha();
    try {
      this.feishuOn = (await api("/api/feishu/config")).enabled;
      store.feishu = this.feishuOn;
    } catch { /* best-effort */ }
    // Surface Feishu SSO errors returned via redirect (e.g. capacity full).
    const feishuError = new URLSearchParams(location.search).get("feishu_error");
    if (feishuError === "capacity") ElMessage.error(tt("login.feishuCapacityFull"));
    else if (feishuError === "company") ElMessage.error(tt("login.feishuCompanyNotAllowed"));
    trackLoginView();
  },
  methods: {
    tt,
    // Fetch a fresh challenge via raw fetch (api() only speaks JSON): the body
    // is the GIF blob, the id rides the X-Mudp-Captcha-Id header.
    async loadCaptcha() {
      try {
        const res = await fetch("/api/captcha", { cache: "no-store" });
        if (!res.ok) throw new Error("captcha unavailable");
        const blob = await res.blob();
        this.captchaId = res.headers.get("X-Mudp-Captcha-Id") || "";
        if (this.captchaUrl) URL.revokeObjectURL(this.captchaUrl);
        this.captchaUrl = URL.createObjectURL(blob);
        this.form.captcha = "";
      } catch { /* keep the previous image; submit will surface the failure */ }
    },
    switchLang(lang) {
      localStorage.setItem("mudp_language", lang);
      setLanguage(lang);
      this.currentLang = getCurrentLanguage();
    },
    feishuLogin() {
      window.location.href = "/api/feishu/login";
    },
    async submit() {
      if (this.busy) return;
      this.busy = true;
      try {
        const login = await api("/api/login", { method: "POST", body: JSON.stringify({ ...this.form, captchaId: this.captchaId }) });
        const dest = await afterLogin(login.user || login, login.csrfToken || "");
        if (dest === "pending") return this.$router.push("/pending");
        // A forward-auth redirect sends the browser here with ?next=<original
        // URL>; once logged in, send it straight back to the forwarded port.
        const next = new URLSearchParams(location.search).get("next");
        const target = resolveNextRedirect(next, location.origin);
        if (target) {
          window.location.href = target;
          return;
        }
        this.$router.push("/dashboard");
      } catch (err) {
        // The challenge was consumed either way; always show a fresh one and
        // map the bare server string to a localized message.
        this.loadCaptcha();
        ElMessage.error(err.message === "incorrect captcha" ? tt("login.captchaError") : err.message);
      } finally {
        this.busy = false;
      }
    },
  },
};
</script>
