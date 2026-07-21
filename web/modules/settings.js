// Settings: Feishu SSO + registries + language preferences.

import { state, api, toast, renderView, escapeHtml, isAdmin, t, $ } from "../app.js";
import { showModal, closeModal } from "./ui.js";
import { createUserLanguageSettings, createAdminLanguageSettings } from "../lib/languageUI.js";
import { getCurrentLanguage, switchLanguage } from "../lib/i18n.js";

export function renderSettings() {
  if (isAdmin() && !state.feishuAdmin.loaded) {
    loadFeishuAdmin();
  }
  if (isAdmin() && !state.registries) {
    loadRegistries();
  }
  if (isAdmin() && !state.security.loaded) {
    loadSecurity();
  }

  const currentLanguage = getCurrentLanguage();
  const defaultLanguage = state.me?.defaultLanguage || "en_US";
  const adminLanguagePanel = isAdmin() ? createAdminLanguageSettings(defaultLanguage) : "";
  const registriesPanel = isAdmin()
    ? `<div class="card"><div class="card-head"><h2>Registries</h2>` +
        `<button class="primary" id="newRegistryBtn">+ Add Registry</button>` +
      `</div>` +
        `<table class="data">` +
          `<thead><tr><th>Name</th><th>URL</th><th>Username</th><th class="actions">Actions</th></tr></thead>` +
          `<tbody>${(state.registries || []).map(registryRow).join("") || `<tr class="empty-row"><td colspan="4">No registries configured.</td></tr>`}</tbody>` +
        `</table>` +
      `</div>`
    : "";
  const feishuSettingsPanel = isAdmin()
    ? `<div class="card"><div class="card-head"><h2>Feishu SSO</h2></div>` +
        `<div class="card-body"><form id="feishuForm" class="compact">` +
          `<p class="hint">Configure a Feishu (Lark) app to enable single sign-on. New users auto-join the <strong>pending</strong> group until an admin approves them.</p>` +
          `<input name="appId" placeholder="App ID" value="${escapeHtml(state.feishuAdmin.appId)}">` +
          `<input name="appSecret" type="password" placeholder="${state.feishuAdmin.appSecret ? "(leave blank to keep)" : "App Secret"}">` +
          `<label class="check"><input type="checkbox" name="enabled" ${state.feishuAdmin.enabled ? "checked" : ""}> Enable Feishu login</label>` +
          `<p class="hint">Callback URL: <span class="mono">${location.origin}/api/feishu/callback</span></p>` +
          `<button>Save Feishu Settings</button>` +
        `</form></div>` +
      `</div>`
    : "";
  const securityPanel = isAdmin() ? securitySettingsPanel(state.security) : "";

  $("#view").innerHTML =
    `<div class="stack">` +
      createUserLanguageSettings(currentLanguage) +
      adminLanguagePanel +
      registriesPanel +
      feishuSettingsPanel +
      securityPanel +
    `</div>`;

  // Handle user language settings
  const userLangForm = $("#userLanguageForm");
  if (userLangForm) {
    userLangForm.onsubmit = async (e) => {
      e.preventDefault();
      const fd = new FormData(e.target);
      const lang = fd.get("language");
      try {
        await switchLanguage(lang);
        toast(t("settings.languageChanged"), true);
        // Reload to apply language changes
        setTimeout(() => location.reload(), 500);
      } catch (err) {
        toast(err.message);
      }
    };
  }

  // Handle admin language settings
  const adminLangForm = $("#adminLanguageForm");
  if (adminLangForm) {
    adminLangForm.onsubmit = async (e) => {
      e.preventDefault();
      const fd = new FormData(e.target);
      try {
        await api("/api/admin/settings/language", {
          method: "POST",
          body: JSON.stringify({ defaultLanguage: fd.get("defaultLanguage") }),
        });
        toast(t("admin.saved"), true);
      } catch (err) {
        toast(err.message);
      }
    };
  }

  const feishuForm = $("#feishuForm");
  if (feishuForm) {
    feishuForm.onsubmit = async (e) => {
      e.preventDefault();
      const fd = new FormData(e.target);
      try {
        await api("/api/settings/feishu", {
          method: "POST",
          body: JSON.stringify({
            appId: fd.get("appId"),
            appSecret: fd.get("appSecret") || "",
            enabled: fd.has("enabled"),
          }),
        });
        state.feishu = fd.has("enabled");
        state.feishuAdmin.loaded = false;
        loadFeishuAdmin();
        toast("Feishu settings saved", true);
      } catch (err) {
        toast(err.message);
      }
    };
  }

  const newReg = $("#newRegistryBtn");
  if (newReg) newReg.onclick = () => openRegistryEditor(null);
  document.querySelectorAll("[data-reg-edit]").forEach((btn) => {
    btn.onclick = () => openRegistryEditor(Number(btn.dataset.regEdit));
  });
  document.querySelectorAll("[data-reg-delete]").forEach((btn) => {
    btn.onclick = () => deleteRegistry(Number(btn.dataset.regDelete), btn.dataset.regName);
  });
  document.querySelectorAll("[data-reg-test]").forEach((btn) => {
    btn.onclick = () => testRegistry(Number(btn.dataset.regTest));
  });

  bindSecurityForm();
}

// ---- Security policy card (IP/region gate + login brute-force) ----
//
// Comma/newline-separated allow/block lists are normalized on the server; the
// UI keeps editing simple with free-form text fields plus a datalist of China
// provinces for discoverability.

const CN_PROVINCES = [
  "北京市", "天津市", "河北省", "山西省", "内蒙古自治区",
  "辽宁省", "吉林省", "黑龙江省", "上海市", "江苏省",
  "浙江省", "安徽省", "福建省", "江西省", "山东省",
  "河南省", "湖北省", "湖南省", "广东省", "广西壮族自治区",
  "海南省", "重庆市", "四川省", "贵州省", "云南省",
  "西藏自治区", "陕西省", "甘肃省", "青海省", "宁夏回族自治区",
  "新疆维吾尔自治区", "香港特别行政区", "澳门特别行政区", "台湾省",
];

function securitySettingsPanel(s) {
  if (!s.loaded) {
    return `<div class="card"><div class="card-head"><h2>Security</h2></div><div class="card-body"><p class="hint">Loading…</p></div></div>`;
  }
  const countries = (s.allowedCountries || []).join(", ");
  const provinces = (s.allowedCNProvinces || []).join(", ");
  const allowCIDR = (s.allowedCIDRs || []).join("\n");
  const blockCIDR = (s.blockedCIDRs || []).join("\n");

  // Self-check chip: show the admin where their own connection appears to
  // originate and whether the current policy would let them in. The server
  // refuses to save a policy that would block the saver, but surfacing it
  // inline avoids the round-trip.
  const selfChip = s.myIP
    ? `<div class="hint security-self-check">Your IP: <strong class="mono">${escapeHtml(s.myIP)}</strong>` +
      ` · Region: <strong>${escapeHtml(s.myRegion || "unknown")}</strong>` +
      ` · <span class="badge ${s.myAllowed ? "badge-accent" : "badge-danger"}">${s.myAllowed ? "allowed" : "BLOCKED"}</span></div>`
    : "";

  const lockedRows = (s.locked || []).map((l) =>
    `<tr><td class="mono">${escapeHtml(l.kind === "ip" ? l.key : l.key)}</td>` +
    `<td>${escapeHtml(l.kind)}</td>` +
    `<td>${escapeHtml(l.lockedUntil)}</td>` +
    `<td class="hint">${escapeHtml(l.reason)}</td></tr>`,
  ).join("");
  const lockedTable = lockedRows
    ? `<table class="data"><thead><tr><th>Key</th><th>Kind</th><th>Locked until</th><th>Reason</th></tr></thead><tbody>${lockedRows}</tbody></table>`
    : `<p class="hint">No active lockouts.</p>`;

  return `<div class="card"><div class="card-head"><h2>Security · IP & 登录防护</h2></div>` +
    `<div class="card-body">` +
      selfChip +
      `<form id="securityForm" class="compact">` +
        `<label class="check"><input type="checkbox" name="enabled" ${s.enabled ? "checked" : ""}> <strong>启用 IP 地区限制</strong> (Enable region/CIDR gate)</label>` +
        `<p class="hint">开启后，不在白名单的地区访问整站会返回 404。CIDR 白名单优先级最高（避免把自己锁在门外）。</p>` +
        `<label class="check"><input type="checkbox" name="loginGuardEnabled" ${s.loginGuardEnabled ? "checked" : ""}> 启用登录防爆破 (Enable login brute-force protection — 5 fails/15 min per IP → 30 min lock)</label>` +

        `<div class="security-field">` +
          `<label>国家白名单 / Allowed countries</label>` +
          `<input name="allowedCountries" placeholder="CN, HK, TW" value="${escapeHtml(countries)}">` +
          `<p class="hint">ISO 国家码，逗号分隔。留空表示不限国家。</p>` +
        `</div>` +
        `<div class="security-field">` +
          `<label>中国省份白名单 / Allowed China provinces</label>` +
          `<input name="allowedCNProvinces" list="cnProvinces" placeholder="广东省, 北京市" value="${escapeHtml(provinces)}">` +
          `<datalist id="cnProvinces">${CN_PROVINCES.map((p) => `<option value="${escapeHtml(p)}">`).join("")}</datalist>` +
          `<p class="hint">仅当国家白名单含 CN 时生效。留空表示整个中国都允许。输入"广东"也能匹配"广东省"。</p>` +
        `</div>` +
        `<div class="security-field">` +
          `<label>CIDR 白名单 / Always-allow CIDRs (override)</label>` +
          `<textarea name="allowedCIDRs" rows="3" placeholder="10.0.0.0/8&#10;203.0.113.5">${escapeHtml(allowCIDR)}</textarea>` +
          `<p class="hint">每行一个 CIDR 或单个 IP。命中即放行，无视地区规则。</p>` +
        `</div>` +
        `<div class="security-field">` +
          `<label>CIDR 黑名单 / Always-block CIDRs</label>` +
          `<textarea name="blockedCIDRs" rows="3" placeholder="203.0.113.0/24">${escapeHtml(blockCIDR)}</textarea>` +
          `<p class="hint">命中即返回 404，优先级最高。</p>` +
        `</div>` +
        `<button>Save Security Policy</button>` +
      `</form>` +
      `<div class="security-locked">` +
        `<h3>当前锁定 / Active lockouts</h3>` +
        lockedTable +
      `</div>` +
    `</div>` +
  `</div>`;
}

function bindSecurityForm() {
  const form = $("#securityForm");
  if (!form) return;
  form.onsubmit = async (e) => {
    e.preventDefault();
    const fd = new FormData(form);
    const splitList = (val) =>
      String(val || "")
        .split(/[\n,]/)
        .map((s) => s.trim())
        .filter(Boolean);
    const payload = {
      enabled: fd.has("enabled"),
      loginGuardEnabled: fd.has("loginGuardEnabled"),
      allowedCountries: splitList(fd.get("allowedCountries")),
      allowedCNProvinces: splitList(fd.get("allowedCNProvinces")),
      allowedCIDRs: splitList(fd.get("allowedCIDRs")),
      blockedCIDRs: splitList(fd.get("blockedCIDRs")),
    };
    try {
      await api("/api/settings/security", { method: "POST", body: JSON.stringify(payload) });
      state.security.loaded = false;
      await loadSecurity();
      toast(t("settings.saved"), true);
    } catch (err) {
      toast(err.message);
    }
  };
}

export async function loadSecurity() {
  if (!isAdmin()) {
    state.security = { loaded: true };
    return;
  }
  try {
    const data = await api("/api/settings/security");
    state.security = {
      loaded: true,
      enabled: !!data.enabled,
      loginGuardEnabled: !!data.loginGuardEnabled,
      allowedCountries: data.allowedCountries || [],
      allowedCNProvinces: data.allowedCNProvinces || [],
      allowedCIDRs: data.allowedCIDRs || [],
      blockedCIDRs: data.blockedCIDRs || [],
      locked: data.locked || [],
      myIP: data.myIp || "",
      myRegion: data.myRegion || "",
      myAllowed: !!data.myAllowed,
    };
  } catch {
    state.security = { loaded: true };
  }
  if (state.tab === "settings") renderView();
}

function registryRow(r) {
  return (
    `<tr>` +
      `<td><div class="primary-line">${escapeHtml(r.name)}</div></td>` +
      `<td><div class="secondary-line mono">${escapeHtml(r.url)}</div></td>` +
      `<td><div class="secondary-line">${escapeHtml(r.username || "-")}</div></td>` +
      `<td class="actions">` +
        `<button class="ghost" data-reg-test="${r.id}">Test</button>` +
        `<button class="ghost" data-reg-edit="${r.id}">Edit</button>` +
        `<button class="icon danger" data-reg-delete="${r.id}" data-reg-name="${escapeHtml(r.name)}">Delete</button>` +
      `</td>` +
    `</tr>`
  );
}

function openRegistryEditor(existingId) {
  const r = existingId ? (state.registries || []).find((x) => x.id === existingId) : null;
  showModal({
    kind: "registry",
    title: r ? `Edit ${r.name}` : "Add Registry",
    body:
      `<form id="regForm" class="compact">` +
        `<input name="name" placeholder="Name, e.g. GitHub Container Registry" value="${escapeHtml(r?.name || "")}" required>` +
        `<input name="url" placeholder="Registry URL, e.g. ghcr.io" value="${escapeHtml(r?.url || "")}" required>` +
        `<input name="username" placeholder="Username" value="${escapeHtml(r?.username || "")}">` +
        `<input name="token" type="password" placeholder="${r ? "(leave blank to keep)" : "Access token / password"}">` +
      `</form>`,
    foot: `<button class="ghost" data-close>Cancel</button><button class="primary" id="saveReg">Save</button>`,
  });
  $("#saveReg").onclick = async () => {
    const fd = new FormData($("#regForm"));
    const payload = {
      name: fd.get("name"),
      url: fd.get("url"),
      username: fd.get("username"),
      token: fd.get("token") || "",
    };
    if (r) payload.id = r.id;
    try {
      await api("/api/registries", { method: "POST", body: JSON.stringify(payload) });
      await loadRegistries();
      renderView();
      closeModal();
      toast("Registry saved", true);
    } catch (err) {
      toast(err.message);
    }
  };
}

async function deleteRegistry(id, name) {
  if (!confirm(`Delete registry "${name}"?`)) return;
  try {
    await api("/api/registries/delete", { method: "POST", body: JSON.stringify({ id }) });
    await loadRegistries();
    renderView();
    toast("Registry deleted", true);
  } catch (err) {
    toast(err.message);
  }
}

async function testRegistry(id) {
  try {
    await api("/api/registries/test", { method: "POST", body: JSON.stringify({ id }) });
    toast("Login successful", true);
  } catch (err) {
    toast(err.message);
  }
}

export async function loadFeishuAdmin() {
  if (!isAdmin()) {
    state.feishuAdmin = { appId: "", appSecret: "", enabled: false, loaded: true };
    return;
  }
  try {
    const cfg = await api("/api/settings/feishu");
    state.feishuAdmin = {
      appId: cfg.appId || "",
      appSecret: cfg.appSecret || "",
      enabled: !!cfg.enabled,
      loaded: true,
    };
  } catch {
    state.feishuAdmin = { appId: "", appSecret: "", enabled: false, loaded: true };
  }
  if (state.tab === "scripts") renderView();
}

export async function loadRegistries() {
  if (!isAdmin()) {
    state.registries = [];
    return;
  }
  try {
    state.registries = (await api("/api/registries")) || [];
  } catch {
    state.registries = [];
  }
  if (state.tab === "scripts") renderView();
}
