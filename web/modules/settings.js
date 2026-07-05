// Settings: bootstrap scripts + Feishu SSO + registries.

import { state, api, toast, refreshAll, renderView } from "../app.js";
import { showModal, closeModal } from "./ui.js";

export function renderSettings() {
  if (!state.feishuAdmin.loaded) {
    loadFeishuAdmin();
  }
  if (!state.registries) {
    loadRegistries();
  }
  $("#view").innerHTML =
    `<div class="stack">` +
      `<div class="card"><div class="card-head"><h2>Bootstrap Scripts</h2></div>` +
        `<div class="card-body"><form id="scriptSettings" class="compact">` +
          `<h3>SSH Bootstrap</h3>` +
          `<textarea name="sshScript" class="mono" rows="10" spellcheck="false">${escapeHtml(state.scripts.sshScript || "")}</textarea>` +
          `<h3>VS Code Bootstrap</h3>` +
          `<textarea name="vscodeScript" class="mono" rows="10" spellcheck="false">${escapeHtml(state.scripts.vscodeScript || "")}</textarea>` +
          `<button>Save Scripts</button>` +
        `</form></div>` +
      `</div>` +
      `<div class="card"><div class="card-head"><h2>Registries</h2>` +
        `<button class="primary" id="newRegistryBtn">+ Add Registry</button>` +
      `</div>` +
        `<table class="data">` +
          `<thead><tr><th>Name</th><th>URL</th><th>Username</th><th class="actions">Actions</th></tr></thead>` +
          `<tbody>${(state.registries || []).map(registryRow).join("") || `<tr class="empty-row"><td colspan="4">No registries configured.</td></tr>`}</tbody>` +
        `</table>` +
      `</div>` +
      `<div class="card"><div class="card-head"><h2>Feishu SSO</h2></div>` +
        `<div class="card-body"><form id="feishuForm" class="compact">` +
          `<p class="hint">Configure a Feishu (Lark) app to enable single sign-on. New users auto-join the <strong>pending</strong> group until an admin approves them.</p>` +
          `<input name="appId" placeholder="App ID" value="${escapeHtml(state.feishuAdmin.appId)}">` +
          `<input name="appSecret" type="password" placeholder="${state.feishuAdmin.appSecret ? "•••••• (leave blank to keep)" : "App Secret"}">` +
          `<label class="check"><input type="checkbox" name="enabled" ${state.feishuAdmin.enabled ? "checked" : ""}> Enable Feishu login</label>` +
          `<p class="hint">Callback URL: <span class="mono">${location.origin}/api/feishu/callback</span></p>` +
          `<button>Save Feishu Settings</button>` +
        `</form></div>` +
      `</div>` +
    `</div>`;

  $("#scriptSettings").onsubmit = async (e) => {
    e.preventDefault();
    const fd = new FormData(e.target);
    try {
      await api("/api/scripts", {
        method: "POST",
        body: JSON.stringify({ sshScript: fd.get("sshScript"), vscodeScript: fd.get("vscodeScript") }),
      });
      await refreshAll();
      renderView();
      toast("Scripts saved", true);
    } catch (err) {
      toast(err.message);
    }
  };
  $("#feishuForm").onsubmit = async (e) => {
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

function registryRow(r) {
  return (
    `<tr>` +
      `<td><div class="primary-line">${escapeHtml(r.name)}</div></td>` +
      `<td><div class="secondary-line mono">${escapeHtml(r.url)}</div></td>` +
      `<td><div class="secondary-line">${escapeHtml(r.username || "—")}</div></td>` +
      `<td class="actions">` +
        `<button class="ghost" data-reg-test="${r.id}">Test</button>` +
        `<button class="ghost" data-reg-edit="${r.id}">Edit</button>` +
        `<button class="icon danger" data-reg-delete="${r.id}" data-reg-name="${escapeHtml(r.name)}">✕</button>` +
      `</td>` +
    `</tr>`
  );
}

function openRegistryEditor(existingId) {
  const r = existingId ? (state.registries || []).find((x) => x.id === existingId) : null;
  showModal({
    kind: "registry",
    title: r ? `Edit — ${r.name}` : "Add Registry",
    body:
      `<form id="regForm" class="compact">` +
        `<input name="name" placeholder="Name, e.g. GitHub Container Registry" value="${escapeHtml(r?.name || "")}" required>` +
        `<input name="url" placeholder="Registry URL, e.g. ghcr.io" value="${escapeHtml(r?.url || "")}" required>` +
        `<input name="username" placeholder="Username" value="${escapeHtml(r?.username || "")}">` +
        `<input name="token" type="password" placeholder="${r ? "•••••• (leave blank to keep)" : "Access token / password"}">` +
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
  if (!confirm(`Delete registry “${name}”?`)) return;
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
  try {
    state.registries = (await api("/api/registries")) || [];
  } catch {
    state.registries = [];
  }
  if (state.tab === "scripts") renderView();
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
