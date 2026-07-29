// Settings: site name + Feishu SSO + registries + language preferences. (Port
// forwarding has its own page — see modules/forwards.js.)

import { state, api, toast, render, renderView, escapeHtml, isAdmin, t, $, applySiteName } from "../app.js";
import { showModal, closeModal } from "./ui.js";
import { createUserLanguageSettings, createAdminLanguageSettings } from "../lib/languageUI.js";
import { getCurrentLanguage, switchLanguage } from "../lib/i18n.js";

export function renderSettings() {
  if (isAdmin() && !state.siteAdmin.loaded) {
    loadSiteAdmin();
  }
  if (isAdmin() && !state.feishuAdmin.loaded) {
    loadFeishuAdmin();
  }
  if (isAdmin() && !state.registries) {
    loadRegistries();
  }
  if (isAdmin() && !state.mcpRemoteAdmin) {
    loadMCPRemoteAdmin();
  }

  const currentLanguage = getCurrentLanguage();
  const defaultLanguage = state.me?.defaultLanguage || "en_US";
  const adminLanguagePanel = isAdmin() ? createAdminLanguageSettings(defaultLanguage) : "";
  const siteSettingsPanel = isAdmin()
    ? `<div class="card"><div class="card-head"><h2>${t("settings.siteSettings")}</h2></div>` +
        `<div class="card-body"><form id="siteForm" class="compact">` +
          `<p class="hint">${t("settings.siteNameHint")}</p>` +
          `<input name="siteName" placeholder="${t("settings.siteNamePlaceholder")}" value="${escapeHtml(state.siteAdmin.siteName)}">` +
          `<button>${t("settings.saveSite")}</button>` +
        `</form></div>` +
      `</div>`
    : "";
  const registriesPanel = isAdmin()
    ? `<div class="card"><div class="card-head"><h2>${t("settings.registries")}</h2>` +
        `<button class="primary" id="newRegistryBtn">${t("settings.addRegistry")}</button>` +
      `</div>` +
        `<table class="data">` +
          `<thead><tr><th>${t("common.name")}</th><th>${t("settings.colUrl")}</th><th>${t("settings.colUsername")}</th><th class="actions">${t("common.actions")}</th></tr></thead>` +
          `<tbody>${(state.registries || []).map(registryRow).join("") || `<tr class="empty-row"><td colspan="4">${t("settings.noRegistries")}</td></tr>`}</tbody>` +
        `</table>` +
      `</div>`
    : "";
  const feishuSettingsPanel = isAdmin()
    ? `<div class="card"><div class="card-head"><h2>${t("settings.feishuSso")}</h2></div>` +
        `<div class="card-body"><form id="feishuForm" class="compact">` +
          `<p class="hint">${t("settings.feishuHint")}</p>` +
          `<input name="appId" placeholder="${t("settings.appIdPlaceholder")}" value="${escapeHtml(state.feishuAdmin.appId)}">` +
          `<input name="appSecret" type="password" placeholder="${state.feishuAdmin.appSecret ? t("settings.appSecretKeep") : t("settings.appSecretPlaceholder")}">` +
          `<label class="check"><input type="checkbox" name="enabled" ${state.feishuAdmin.enabled ? "checked" : ""}> ${t("settings.enableFeishu")}</label>` +
          `<p class="hint">${t("settings.callbackUrl")}<span class="mono">${location.origin}/api/feishu/callback</span></p>` +
          `<button>${t("settings.saveFeishu")}</button>` +
        `</form></div>` +
      `</div>`
    : "";

  $("#view").innerHTML =
    `<div class="stack">` +
      createUserLanguageSettings(currentLanguage) +
      adminLanguagePanel +
      siteSettingsPanel +
      registriesPanel +
      mcpRemotePanel() +
      feishuSettingsPanel +
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

  const siteForm = $("#siteForm");
  if (siteForm) {
    siteForm.onsubmit = async (e) => {
      e.preventDefault();
      const fd = new FormData(e.target);
      try {
        const res = await api("/api/admin/settings/site", {
          method: "POST",
          body: JSON.stringify({ siteName: fd.get("siteName") || "" }),
        });
        state.siteAdmin = { siteName: res.siteName || "", loaded: true };
        applySiteName(res.siteName || "");
        render();
        toast(t("settings.siteSaved"), true);
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
        toast(t("settings.feishuSaved"), true);
      } catch (err) {
        toast(err.message);
      }
    };
  }

  const mcpRemoteForm = $("#mcpRemoteForm");
  if (mcpRemoteForm) {
    mcpRemoteForm.onsubmit = async (e) => {
      e.preventDefault();
      const fd = new FormData(e.target);
      try {
        const res = await api("/api/admin/mcp/remote", {
          method: "POST",
          body: JSON.stringify({
            enabled: fd.has("enabled"),
            port: Number(fd.get("port")) || 0,
            domain: fd.get("domain") || "",
            safeNetwork: fd.get("safeNetwork") || "",
          }),
        });
        state.mcpRemoteAdmin = null;
        // The MCP page builds its links from the user-facing copy of this
        // config, so drop it too rather than show a stale domain for 15s.
        state.mcpRemote = null;
        await loadMCPRemoteAdmin();
        renderView();
        toast(res.running ? t("settings.mcpLive") : t("settings.mcpSaved"), true);
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
}

// mcpRemotePanel is the admin control for publishing MCP outside the LAN. The
// listener it starts binds to loopback only and serves nothing but the MCP
// endpoints — a Cloudflare tunnel is what makes it reachable, and the safe
// network is what limits which containers answer on it.
function mcpRemotePanel() {
  if (!isAdmin()) return "";
  const cfg = state.mcpRemoteAdmin;
  if (!cfg) {
    return (
      `<div class="card"><div class="card-head"><h2>${t("settings.mcpExternal")}</h2></div>` +
        `<div class="card-body"><p class="hint">${t("common.loadingDots")}</p></div>` +
      `</div>`
    );
  }
  const status = cfg.running
    ? `<span class="badge badge-ok">${t("settings.mcpListening", { addr: escapeHtml(cfg.listenAddr || "") })}</span>`
    : `<span class="badge">${t("settings.mcpStopped")}</span>`;
  const link = cfg.baseUrl
    ? `<p class="hint">${t("settings.mcpUsersSee", { url: `<span class="mono">${escapeHtml(cfg.baseUrl)}</span>` })}</p>`
    : "";
  return (
    `<div class="card"><div class="card-head"><h2>${t("settings.mcpExternal")}</h2>${status}</div>` +
      `<div class="card-body"><form id="mcpRemoteForm" class="compact">` +
        `<p class="hint">${t("settings.mcpHint")}</p>` +
        `<input name="domain" placeholder="${t("settings.mcpDomainPlaceholder")}" value="${escapeHtml(cfg.domain || "")}">` +
        `<input name="port" type="number" min="1024" max="65535" placeholder="${t("settings.mcpPortPlaceholder")}" value="${escapeHtml(String(cfg.port || 19090))}">` +
        `<input name="safeNetwork" placeholder="${t("settings.mcpSafeNetPlaceholder")}" value="${escapeHtml(cfg.safeNetwork || "openwrt-lan")}">` +
        `<label class="check"><input type="checkbox" name="enabled" ${cfg.enabled ? "checked" : ""}> ${t("settings.mcpEnableExternal")}</label>` +
        `<p class="hint">${t("settings.mcpSafeHint")}</p>` +
        `<p class="hint">${t("settings.mcpTunnel")}<span class="mono">cloudflared tunnel --url http://127.0.0.1:${escapeHtml(String(cfg.port || 19090))}</span></p>` +
        link +
        `<button>${t("settings.saveExternal")}</button>` +
      `</form></div>` +
    `</div>`
  );
}

// loadMCPRemoteAdmin caches the config for the panel above. It always leaves a
// non-null value behind — renderSettings loads whenever the cache is empty, so
// a null on failure would spin render → fetch → render.
export async function loadMCPRemoteAdmin() {
  const fallback = { enabled: false, port: 19090, domain: "", safeNetwork: "openwrt-lan", running: false };
  if (!isAdmin()) {
    state.mcpRemoteAdmin = fallback;
    return;
  }
  try {
    state.mcpRemoteAdmin = (await api("/api/admin/mcp/remote")) || fallback;
  } catch {
    state.mcpRemoteAdmin = fallback;
  }
  if (state.tab === "scripts") renderView();
}

function registryRow(r) {
  return (
    `<tr>` +
      `<td><div class="primary-line">${escapeHtml(r.name)}</div></td>` +
      `<td><div class="secondary-line mono">${escapeHtml(r.url)}</div></td>` +
      `<td><div class="secondary-line">${escapeHtml(r.username || "-")}</div></td>` +
      `<td class="actions">` +
        `<button class="ghost" data-reg-test="${r.id}">${t("settings.test")}</button>` +
        `<button class="ghost" data-reg-edit="${r.id}">${t("common.edit")}</button>` +
        `<button class="icon danger" data-reg-delete="${r.id}" data-reg-name="${escapeHtml(r.name)}">${t("common.delete")}</button>` +
      `</td>` +
    `</tr>`
  );
}

function openRegistryEditor(existingId) {
  const r = existingId ? (state.registries || []).find((x) => x.id === existingId) : null;
  showModal({
    kind: "registry",
    title: r ? t("settings.editRegistry", { name: r.name }) : t("settings.addRegistryTitle"),
    body:
      `<form id="regForm" class="compact">` +
        `<input name="name" placeholder="${t("settings.regNamePlaceholder")}" value="${escapeHtml(r?.name || "")}" required>` +
        `<input name="url" placeholder="${t("settings.regUrlPlaceholder")}" value="${escapeHtml(r?.url || "")}" required>` +
        `<input name="username" placeholder="${t("settings.regUserPlaceholder")}" value="${escapeHtml(r?.username || "")}">` +
        `<input name="token" type="password" placeholder="${r ? t("settings.appSecretKeep") : t("settings.regTokenPlaceholder2")}">` +
      `</form>`,
    foot: `<button class="ghost" data-close>${t("common.cancel")}</button><button class="primary" id="saveReg">${t("common.save")}</button>`,
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
      toast(t("settings.registrySaved"), true);
    } catch (err) {
      toast(err.message);
    }
  };
}

async function deleteRegistry(id, name) {
  if (!confirm(t("settings.deleteRegistryConfirm", { name }))) return;
  try {
    await api("/api/registries/delete", { method: "POST", body: JSON.stringify({ id }) });
    await loadRegistries();
    renderView();
    toast(t("settings.registryDeleted"), true);
  } catch (err) {
    toast(err.message);
  }
}

async function testRegistry(id) {
  try {
    await api("/api/registries/test", { method: "POST", body: JSON.stringify({ id }) });
    toast(t("settings.loginSuccessful"), true);
  } catch (err) {
    toast(err.message);
  }
}

export async function loadSiteAdmin() {
  if (!isAdmin()) {
    state.siteAdmin = { siteName: "", loaded: true };
    return;
  }
  try {
    const cfg = await api("/api/admin/settings/site");
    state.siteAdmin = { siteName: cfg.siteName || "", loaded: true };
  } catch {
    state.siteAdmin = { siteName: "", loaded: true };
  }
  if (state.tab === "scripts") renderView();
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
