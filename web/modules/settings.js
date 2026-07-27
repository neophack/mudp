// Settings: Feishu SSO + registries + language preferences. (Port forwarding
// has its own page — see modules/forwards.js.)

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
  if (isAdmin() && !state.mcpRemoteAdmin) {
    loadMCPRemoteAdmin();
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

  $("#view").innerHTML =
    `<div class="stack">` +
      createUserLanguageSettings(currentLanguage) +
      adminLanguagePanel +
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
        toast(res.running ? "External MCP access is live" : "External MCP access saved", true);
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
      `<div class="card"><div class="card-head"><h2>MCP external access</h2></div>` +
        `<div class="card-body"><p class="hint">Loading…</p></div>` +
      `</div>`
    );
  }
  const status = cfg.running
    ? `<span class="badge badge-ok">Listening on ${escapeHtml(cfg.listenAddr || "")}</span>`
    : `<span class="badge">Stopped</span>`;
  const link = cfg.baseUrl
    ? `<p class="hint">Users see <span class="mono">${escapeHtml(cfg.baseUrl)}</span> on the MCP page and append their own token to it.</p>`
    : "";
  return (
    `<div class="card"><div class="card-head"><h2>MCP external access</h2>${status}</div>` +
      `<div class="card-body"><form id="mcpRemoteForm" class="compact">` +
        `<p class="hint">Publishes only the MCP endpoints on a second, loopback-bound port. Point a Cloudflare tunnel hostname at <span class="mono">127.0.0.1:&lt;port&gt;</span> and users can connect an agent from anywhere — the console itself stays off that hostname.</p>` +
        `<input name="domain" placeholder="Public domain, e.g. mcp.example.com" value="${escapeHtml(cfg.domain || "")}">` +
        `<input name="port" type="number" min="1024" max="65535" placeholder="Port" value="${escapeHtml(String(cfg.port || 19090))}">` +
        `<input name="safeNetwork" placeholder="Safe network" value="${escapeHtml(cfg.safeNetwork || "openwrt-lan")}">` +
        `<label class="check"><input type="checkbox" name="enabled" ${cfg.enabled ? "checked" : ""}> Enable external access</label>` +
        `<p class="hint">Only containers attached to the <strong>safe network</strong> are reachable through the domain; every other token is refused there, so turning this on does not by itself expose anything.</p>` +
        `<p class="hint">Tunnel command: <span class="mono">cloudflared tunnel --url http://127.0.0.1:${escapeHtml(String(cfg.port || 19090))}</span></p>` +
        link +
        `<button>Save external access</button>` +
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
