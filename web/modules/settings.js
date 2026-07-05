// Settings: bootstrap scripts + Feishu SSO + registries.

import { state, api, toast, refreshAll, renderView } from "../app.js";
import { showModal, setModalBody, closeModal, readSSE } from "./ui.js";

export function renderSettings() {
  if (!state.feishuAdmin.loaded) {
    loadFeishuAdmin();
  }
  if (!state.registries) {
    loadRegistries();
  }
  $("#view").innerHTML =
    `<div class="stack">` +
      `<div class="card"><div class="card-head"><h2>Bootstrap Scripts</h2><div class="head-tools">` +
        `<button class="primary" id="buildSshBtn">Build SSH Image</button>` +
        `<button class="primary" id="buildVscodeBtn">Build VS Code Image</button>` +
      `</div></div>` +
        `<div class="card-body"><form id="scriptSettings" class="compact">` +
          `<h3>SSH Bootstrap</h3>` +
          `<textarea name="sshScript" class="mono" rows="10" spellcheck="false">${escapeHtml(state.scripts.sshScript || "")}</textarea>` +
          `<h3>VS Code Bootstrap</h3>` +
          `<textarea name="vscodeScript" class="mono" rows="10" spellcheck="false">${escapeHtml(state.scripts.vscodeScript || "")}</textarea>` +
          `<button>Save Scripts</button>` +
        `</form></div>` +
      `</div>` +
      // Fused-image status card: shows which base images are pre-built and when.
      `<div class="card"><div class="card-head"><h2>Fused Images</h2></div>` +
        `<table class="data">` +
          `<thead><tr><th>Base image</th><th>Services</th><th>Status</th><th class="actions">Actions</th></tr></thead>` +
          `<tbody>${(state.fusedImages || []).map(fusedRow).join("") || `<tr class="empty-row"><td colspan="4">No pre-built images yet. Use the buttons above to build one.</td></tr>`}</tbody>` +
        `</table>` +
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
  const buildSsh = $("#buildSshBtn");
  if (buildSsh) buildSsh.onclick = () => openFusedBuildModal("ssh");
  const buildVscode = $("#buildVscodeBtn");
  if (buildVscode) buildVscode.onclick = () => openFusedBuildModal("vscode");
  document.querySelectorAll("[data-fused-rebuild]").forEach((btn) => {
    btn.onclick = () => openFusedBuildModal(btn.dataset.fusedRebuild, btn.dataset.fusedBase);
  });
  document.querySelectorAll("[data-fused-delete]").forEach((btn) => {
    btn.onclick = () => deleteFused(btn.dataset.fusedDelete, btn.dataset.fusedBase);
  });
}

// fusedRow renders one row of the Fused Images status card.
function fusedRow(f) {
  const services = [];
  if (f.enableSsh) services.push("SSH");
  if (f.enableVscode) services.push("VSCode");
  const when = f.createdAt ? new Date(f.createdAt).toLocaleString() : "";
  return (
    `<tr>` +
      `<td><div class="primary-line">${escapeHtml(f.baseRef || "—")}</div></td>` +
      `<td>${services.join(" + ") || "—"}</td>` +
      `<td><span class="ok">Ready ✓</span>${when ? ` <span class="hint">${escapeHtml(when)}</span>` : ""}</td>` +
      `<td class="actions">` +
        `<button class="ghost" data-fused-rebuild="${f.enableVscode ? "vscode" : "ssh"}" data-fused-base="${escapeHtml(f.baseRef)}">Rebuild</button>` +
        `<button class="icon danger" data-fused-delete="${escapeHtml(f.fusedRef)}" data-fused-base="${escapeHtml(f.baseRef)}">✕</button>` +
      `</td>` +
    `</tr>`
  );
}

// openFusedBuildModal shows a base-image picker then starts the build. If
// presetBase is given (Rebuild button), it's preselected.
function openFusedBuildModal(which, presetBase) {
  const images = (state.images || []).slice();
  if (!images.length) {
    toast("No base images available. Pull an image first.");
    return;
  }
  const opts = images
    .map((img) => `<option value="${escapeHtml(img.name)}"${presetBase && img.name === presetBase ? " selected" : ""}>${escapeHtml(img.name)}</option>`)
    .join("");
  const label = which === "ssh" ? "SSH" : "VS Code";
  showModal({
    kind: "fusedBuild",
    title: `Build ${label} Image`,
    body:
      `<div class="compact">` +
        `<p class="hint">Pre-builds a derived image with ${label} installed so containers boot fast. Build runs once per base image; later changes to the scripts trigger a rebuild automatically.</p>` +
        `<label class="field-label">Base image</label>` +
        `<select id="fusedBaseImage">${opts}</select>` +
      `</div>`,
    foot: `<button class="ghost" data-close>Cancel</button><button class="primary" id="startFusedBuild">Build</button>`,
  });
  $("#startFusedBuild").onclick = () => {
    const baseImage = $("#fusedBaseImage").value;
    streamFusedBuild(which, baseImage);
  };
}

// streamFusedBuild POSTs to the SSE build endpoint and renders the live log.
// After the stream ends (whether via done, error, or a dropped connection), it
// verifies the final image status against /api/scripts/fused/list — a build can
// actually succeed on the server while the SSE stream is killed by an
// intermediary (reverse-proxy timeout, etc.), so we never trust the connection
// state alone.
function streamFusedBuild(which, baseImage) {
  const label = which === "ssh" ? "SSH" : "VS Code";
  state.pull = { active: true, logs: "", error: "", finished: false, fusedRef: "", name: `${label} · ${baseImage}`, which, baseImage };
  renderFusedProgress();
  (async () => {
    let streamError = null;
    try {
      const res = await fetch("/api/scripts/fused/build/stream", {
        method: "POST",
        credentials: "same-origin",
        headers: { "Content-Type": "application/json", Accept: "text/event-stream" },
        body: JSON.stringify({ baseImage, which }),
      });
      if (!res.ok) {
        const data = await res.json().catch(() => ({}));
        streamError = data.error || `Build failed (${res.status})`;
        state.pull.logs += `[error] ${streamError}\n`;
        renderFusedProgress();
        toast(streamError);
      } else {
        await readSSE(res, (event, data) => {
          if (event === "progress") {
            // The first progress event carries the fusedRef so we can verify the
            // build result later even if the stream drops.
            if (!state.pull.fusedRef && data.fusedRef) state.pull.fusedRef = data.fusedRef;
            state.pull.logs += (data.message || "") + "\n";
            renderFusedProgress();
          } else if (event === "error") {
            streamError = data.message || "Build failed";
            state.pull.error = streamError;
            state.pull.logs += `[error] ${streamError}\n`;
            renderFusedProgress();
            toast(streamError);
          } else if (event === "done") {
            state.pull.finished = true;
            state.pull.fusedRef = data.fusedRef || state.pull.fusedRef;
            state.pull.logs += `[done] ${label} image ready\n`;
            renderFusedProgress();
            toast(`${label} image ready`, true);
          }
        });
      }
    } catch (err) {
      // A network error here (e.g. proxy dropping the long SSE connection) does
      // NOT mean the build failed — the server may have finished it. Defer to the
      // status check below instead of surfacing this directly.
      streamError = err.message;
    }
    // Always reconcile with the server's view of the built image. If the fused
    // image is present, the build succeeded regardless of how the stream ended.
    if (!state.pull.finished) {
      const ok = await verifyFusedReady();
      if (ok) {
        state.pull.error = "";
        state.pull.finished = true;
        state.pull.logs += `[done] ${label} image ready\n`;
        renderFusedProgress();
        toast(`${label} image ready`, true);
      } else if (streamError) {
        // Only surface the connection error if the image really isn't there.
        state.pull.error = streamError;
        state.pull.logs += `[error] ${streamError}\n`;
        renderFusedProgress();
        toast(streamError);
      }
    }
    if (state.pull.finished) {
      setTimeout(async () => {
        closeModal();
        await refreshAll();
        renderView();
      }, 1000);
    }
  })();
}

// verifyFusedReady asks the server whether the fused image we kicked off is now
// built and present. Retries a few times because Docker may take a moment to
// make a freshly-built image visible to ImageList. Returns true if it is ready.
// Used after the SSE stream ends to distinguish "stream dropped but build
// succeeded" from real failures.
async function verifyFusedReady() {
  if (!state.pull.fusedRef) return false;
  for (let attempt = 0; attempt < 4; attempt++) {
    try {
      const items = (await api("/api/scripts/fused/list")) || [];
      if (items.some((f) => f.fusedRef === state.pull.fusedRef)) return true;
    } catch {
      // ignore and retry
    }
    await new Promise((r) => setTimeout(r, 1000));
  }
  return false;
}

function renderFusedProgress() {
  if (state.modal.kind !== "fusedBuild") return;
  const done = state.pull.finished;
  const statusBox = state.pull.error
    ? `<div class="error-box">✗ ${escapeHtml(state.pull.error)}</div>`
    : done
      ? `<div class="step done"><span class="step-icon">✓</span><span class="step-label">${escapeHtml(state.pull.name)} image ready</span></div>`
      : `<div class="step active"><span class="step-icon"><span class="spinner"></span></span><span class="step-label">Building ${escapeHtml(state.pull.name)}</span></div>`;
  setModalBody(
    statusBox +
      `<pre class="log-output">${escapeHtml(state.pull.logs || "")}</pre>` +
      `<div style="display:flex;gap:8px;justify-content:flex-end;">` +
        (state.pull.error ? `<button class="primary" id="fusedRetry">Retry</button>` : ``) +
        `<button class="ghost" data-close>${state.pull.error ? "Close" : "Hide"}</button>` +
      `</div>`
  );
  const retry = $("#fusedRetry");
  if (retry) retry.onclick = () => streamFusedBuild(state.pull.which, state.pull.baseImage);
  const log = document.querySelector(".modal-body .log-output");
  if (log) log.scrollTop = log.scrollHeight;
}

async function deleteFused(fusedRef, baseRef) {
  if (!confirm(`Delete the fused image for “${baseRef}”? Containers will fall back to runtime install.`)) return;
  try {
    await api("/api/scripts/fused/delete", { method: "POST", body: JSON.stringify({ fusedRef }) });
    await refreshAll();
    renderView();
    toast("Fused image deleted", true);
  } catch (err) {
    toast(err.message);
  }
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
