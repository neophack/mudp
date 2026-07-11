// Volumes: list, create, delete, prune. mudp-managed volumes are namespaced
// per user; admins see everyone's.

import { state, api, toast, refreshSection, renderView, canMutate, displayNameForUsername } from "../app.js";
import { showModal, closeModal } from "./ui.js";
import { openVolumeFiles } from "./volume_files.js";

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
  // Delete buttons carry data-vol-fullname but NOT data-vol-files, so scope the
  // selector to avoid matching the browse button (which carries both attributes).
  document.querySelectorAll("[data-vol-fullname]:not([data-vol-files])").forEach((btn) => {
    btn.onclick = () => deleteVolume(btn.dataset.volFullname, btn.dataset.volName);
  });
  document.querySelectorAll("[data-vol-files]").forEach((btn) => {
    btn.onclick = () => openVolumeFiles(btn.dataset.volFullname, btn.dataset.volName);
  });
}

function volumeRow(v) {
  return (
    `<tr>` +
      `<td><div class="primary-line">${escapeHtml(v.name)}</div><div class="secondary-line mono">${escapeHtml(v.fullName || v.name)}</div></td>` +
      `<td><div class="secondary-line">${escapeHtml(v.driver)}</div></td>` +
      `<td>${fmtMB(v.sizeMb)}</td>` +
      `<td>${v.inUse ? `<span class="badge badge-ok">in use</span>` : `<span class="badge badge-muted">free</span>`}</td>` +
      `<td><div class="secondary-line">${escapeHtml(displayNameForUsername(v.owner) || "—")}</div></td>` +
      `<td class="actions">` +
        `<button class="icon" title="Browse files" data-vol-name="${escapeHtml(v.name)}" data-vol-fullname="${escapeHtml(v.fullName || v.name)}" data-vol-files="1">📁</button>` +
        (canMutate() ? `<button class="icon danger" title="Delete" data-vol-name="${escapeHtml(v.name)}" data-vol-fullname="${escapeHtml(v.fullName || v.name)}">✕</button>` : "") +
      `</td>` +
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
      await refreshSection("volumes");
      renderView();
      closeModal();
      toast("Volume created", true);
    } catch (err) {
      toast(err.message);
    }
  };
}

async function deleteVolume(fullName, name) {
  if (!confirm(`Delete volume “${name}”? Data inside is lost.`)) return;
  try {
    await api("/api/volumes/delete", { method: "POST", body: JSON.stringify({ name: fullName, force: false }) });
    await refreshSection("volumes");
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
    await refreshSection("volumes");
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
