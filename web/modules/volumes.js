// Volumes: list, create, delete, prune. mudp-managed volumes are namespaced
// per user; admins see everyone's.

import { state, api, toast, refreshAll, renderView, canMutate } from "../app.js";
import { showModal, closeModal } from "./ui.js";

export function renderVolumes() {
  const rows = (state.volumes || []).map(volumeRow).join("") ||
    `<tr class="empty-row"><td colspan="6">No volumes. Click “+ New Volume” to create one.</td></tr>`;
  $("#view").innerHTML =
    `<div class="card">` +
      `<div class="card-head"><h2>Volumes</h2>` +
        `<div class="head-tools">` +
          (canMutate() ? `<button class="ghost" id="pruneVolumes">Prune Unused</button>` : "") +
          (canMutate() ? `<button class="primary" id="newVolumeBtn">+ New Volume</button>` : "") +
        `</div>` +
      `</div>` +
      `<table class="data">` +
        `<thead><tr><th>Name</th><th>Driver</th><th>Size</th><th>In Use</th><th>Owner</th><th class="actions">Actions</th></tr></thead>` +
        `<tbody>${rows}</tbody>` +
      `</table>` +
    `</div>`;
  const nb = $("#newVolumeBtn");
  if (nb) nb.onclick = openCreateVolume;
  const pb = $("#pruneVolumes");
  if (pb) pb.onclick = pruneVolumes;
  document.querySelectorAll("[data-vol-delete]").forEach((btn) => {
    btn.onclick = () => deleteVolume(btn.dataset.volDelete, btn.dataset.volName);
  });
}

function volumeRow(v) {
  return (
    `<tr>` +
      `<td><div class="primary-line">${escapeHtml(v.name)}</div><div class="secondary-line mono">${escapeHtml(v.name)}</div></td>` +
      `<td><div class="secondary-line">${escapeHtml(v.driver)}</div></td>` +
      `<td>${fmtMB(v.sizeMb)}</td>` +
      `<td>${v.inUse ? `<span class="badge badge-ok">in use</span>` : `<span class="badge badge-muted">free</span>`}</td>` +
      `<td><div class="secondary-line">${escapeHtml(v.owner || "—")}</div></td>` +
      `<td class="actions">${canMutate() ? `<button class="icon danger" title="Delete" data-vol-delete="1" data-vol-name="${escapeHtml(v.name)}">✕</button>` : "—"}</td>` +
    `</tr>`
  );
}

function openCreateVolume() {
  showModal({
    kind: "volume",
    title: "New Volume",
    body:
      `<form id="volForm" class="compact">` +
        `<input name="name" placeholder="Volume name, e.g. workspace" required>` +
        `<select name="driver"><option value="local">local</option><option value="nfs">nfs</option></select>` +
        `<input name="subnet" placeholder="(local driver needs no options)" disabled>` +
        `<p class="hint">The volume will be owned by you and visible in the container wizard.</p>` +
      `</form>`,
    foot: `<button class="ghost" data-close>Cancel</button><button class="primary" id="volSubmit">Create</button>`,
  });
  $("#volSubmit").onclick = async () => {
    const fd = new FormData($("#volForm"));
    const payload = {
      name: fd.get("name"),
      driver: fd.get("driver") || "local",
    };
    try {
      await api("/api/volumes", { method: "POST", body: JSON.stringify(payload) });
      await refreshAll();
      renderView();
      closeModal();
      toast("Volume created", true);
    } catch (err) {
      toast(err.message);
    }
  };
}

async function deleteVolume(_, name) {
  if (!confirm(`Delete volume “${name}”? Data inside is lost.`)) return;
  try {
    await api("/api/volumes/delete", { method: "POST", body: JSON.stringify({ name, force: false }) });
    await refreshAll();
    renderView();
    toast("Volume deleted", true);
  } catch (err) {
    toast(err.message);
  }
}

async function pruneVolumes() {
  if (!confirm("Remove all your unused (dangling) volumes?")) return;
  try {
    const r = await api("/api/volumes/prune", { method: "POST" });
    await refreshAll();
    renderView();
    toast(`Reclaimed ${r.removed || 0} volume(s), ${fmtMB((r.bytesFreed || 0) / 1024 / 1024)}`, true);
  } catch (err) {
    toast(err.message);
  }
}

function fmtMB(mb) {
  if (!mb || mb <= 0) return "0 MB";
  if (mb < 1024) return `${Math.round(mb)} MB`;
  return `${(mb / 1024).toFixed(1)} GB`;
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

function $(selector) {
  return document.querySelector(selector);
}
