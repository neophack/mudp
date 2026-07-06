// Bootstrap page: bootstrap scripts + fused layers.

import { state, api, toast, refreshAll, renderView, escapeHtml } from "../app.js";
import { showModal, setModalBody, closeModal, readSSE } from "./ui.js";

export function renderBootstrap() {
  if (!state.fusedLoaded) {
    loadFusedStatus();
  }
  $("#view").innerHTML =
    `<div class="stack">` +
      `<div class="card"><div class="card-head"><h2>Bootstrap Scripts</h2><div class="head-tools">` +
        `<button class="primary" id="buildSshBtn">Build SSH Layer</button>` +
        `<button class="primary" id="buildVscodeBtn">Build VS Code Layer</button>` +
      `</div></div>` +
        `<div class="card-body"><form id="scriptSettings" class="compact">` +
          `<h3>SSH Bootstrap</h3>` +
          `<textarea name="sshScript" class="mono" rows="10" spellcheck="false">${escapeHtml(state.scripts.sshScript || "")}</textarea>` +
          `<h3>VS Code Bootstrap</h3>` +
          `<textarea name="vscodeScript" class="mono" rows="10" spellcheck="false">${escapeHtml(state.scripts.vscodeScript || "")}</textarea>` +
          `<button>Save Scripts</button>` +
        `</form></div>` +
      `</div>` +
      `<div class="card"><div class="card-head"><h2>Fused Layers</h2></div>` +
        `<table class="data">` +
          `<thead><tr><th>Base image</th><th>Service</th><th>Status</th><th class="actions">Actions</th></tr></thead>` +
          `<tbody>${(state.fusedLayers || []).map(layerRow).join("") || `<tr class="empty-row"><td colspan="4">No pre-built layers yet. Use the buttons above to build one.</td></tr>`}</tbody>` +
        `</table>` +
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

  const buildSsh = $("#buildSshBtn");
  if (buildSsh) buildSsh.onclick = () => openFusedBuildModal("ssh");
  const buildVscode = $("#buildVscodeBtn");
  if (buildVscode) buildVscode.onclick = () => openFusedBuildModal("vscode");

  document.querySelectorAll("[data-fused-rebuild]").forEach((btn) => {
    btn.onclick = () => openFusedBuildModal(btn.dataset.fusedRebuild, btn.dataset.fusedBase);
  });
  document.querySelectorAll("[data-fused-delete]").forEach((btn) => {
    btn.onclick = () => deleteLayer(btn.dataset.fusedDelete, btn.dataset.fusedBase);
  });
}

async function loadFusedStatus() {
  state.fusedLoaded = true;
  const [images, layers] = await Promise.all([
    api("/api/scripts/fused/list").catch(() => state.fusedImages || []),
    api("/api/scripts/fused/layers/list").catch(() => state.fusedLayers || []),
  ]);
  state.fusedImages = images || [];
  state.fusedLayers = layers || [];
  if (state.tab === "bootstrap") {
    renderView();
  }
}

function layerRow(f) {
  const service = f.service === "ssh" ? "SSH" : f.service === "vscode" ? "VSCode" : f.service || "-";
  const when = f.createdAt ? new Date(f.createdAt).toLocaleString() : "";
  const name = displayNameFromRef(f.baseRef) || f.baseRef || "-";
  return (
    `<tr>` +
      `<td><div class="primary-line">${escapeHtml(name)}</div><div class="secondary-line mono">${escapeHtml(f.layerRef || "")}</div></td>` +
      `<td>${service}</td>` +
      `<td><span class="ok">Ready</span>${when ? ` <span class="hint">${escapeHtml(when)}</span>` : ""}</td>` +
      `<td class="actions">` +
        `<button class="ghost" data-fused-rebuild="${escapeHtml(f.service)}" data-fused-base="${escapeHtml(f.baseRef)}">Rebuild</button>` +
        `<button class="icon danger" data-fused-delete="${escapeHtml(f.layerRef)}" data-fused-base="${escapeHtml(f.baseRef)}">✕</button>` +
      `</td>` +
    `</tr>`
  );
}

function displayNameFromRef(ref) {
  if (!ref) return "";
  const m = ref.match(/^mudp-(.+?):latest$/);
  return m ? m[1] : ref;
}

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
    title: `Build ${label} Layer`,
    body:
      `<div class="compact">` +
        `<p class="hint">Pre-builds an incremental ${label} layer. This layer is merged into the final fused image when a container with ${label} enabled is created.</p>` +
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

function streamFusedBuild(which, baseImage) {
  const label = which === "ssh" ? "SSH" : "VS Code";
  state.pull = { active: true, logs: "", error: "", finished: false, validated: false, layerRef: "", name: `${label} - ${baseImage}`, which, baseImage };
  renderFusedProgress();
  (async () => {
    let streamError = null;
    try {
      const res = await fetch("/api/scripts/fused/layers/build/stream", {
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
            if (!state.pull.layerRef && data.layerRef) state.pull.layerRef = data.layerRef;
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
            state.pull.layerRef = data.layerRef || state.pull.layerRef;
            state.pull.validated = data.validated === "true";
            state.pull.logs += state.pull.validated
              ? `[done] ${label} layer ready and validated\n`
              : `[done] ${label} layer ready\n`;
            renderFusedProgress();
            toast(state.pull.validated ? `${label} layer ready and validated` : `${label} layer ready`, true);
          }
        });
      }
    } catch (err) {
      streamError = err.message;
    }
    if (!state.pull.finished) {
      const ok = await verifyLayerReady();
      if (ok) {
        state.pull.error = "";
        state.pull.finished = true;
        state.pull.logs += `[done] ${label} layer ready\n`;
        renderFusedProgress();
        toast(`${label} layer ready`, true);
      } else if (streamError) {
        state.pull.error = streamError;
        state.pull.logs += `[error] ${streamError}\n`;
        renderFusedProgress();
        toast(streamError);
      }
    }
    if (state.pull.finished) {
      setTimeout(async () => {
        closeModal();
        state.fusedLoaded = false;
        await refreshAll();
        renderView();
      }, 1000);
    }
  })();
}

async function verifyLayerReady() {
  if (!state.pull.layerRef) return false;
  for (let attempt = 0; attempt < 4; attempt++) {
    try {
      const items = (await api("/api/scripts/fused/layers/list")) || [];
      if (items.some((f) => f.layerRef === state.pull.layerRef)) return true;
    } catch {
    }
    await new Promise((r) => setTimeout(r, 1000));
  }
  return false;
}

function renderFusedProgress() {
  if (state.modal.kind !== "fusedBuild") return;
  const done = state.pull.finished;
  const statusBox = state.pull.error
    ? `<div class="error-box">${escapeHtml(state.pull.error)}</div>`
    : done
      ? `<div class="step done"><span class="step-icon">✓</span><span class="step-label">${escapeHtml(state.pull.name)} layer ${state.pull.validated ? "ready and validated" : "ready"}</span></div>`
      : `<div class="step active"><span class="step-icon"><span class="spinner"></span></span><span class="step-label">Building ${escapeHtml(state.pull.name)} layer</span></div>`;
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

async function deleteLayer(layerRef, baseRef) {
  if (!confirm(`Delete the fused layer for "${baseRef}"? The layer will be rebuilt on the next container create.`)) return;
  try {
    await api("/api/scripts/fused/layers/delete", { method: "POST", body: JSON.stringify({ layerRef }) });
    state.fusedLoaded = false;
    await refreshAll();
    renderView();
    toast("Fused layer deleted", true);
  } catch (err) {
    toast(err.message);
  }
}

function $(selector) {
  return document.querySelector(selector);
}
