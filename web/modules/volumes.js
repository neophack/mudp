// Volumes: list, create, delete, prune. mudp-managed volumes are namespaced
// per user; admins see everyone's.

import { state, api, toast, refreshSection, renderView, canMutate, displayNameForUsername, t } from "../app.js";
import { showModal, closeModal } from "./ui.js";
import { openVolumeFiles } from "./volume_files.js";

export function renderVolumes() {
  const rows = (state.volumes || []).map(volumeRow).join("") ||
    `<tr class="empty-row"><td colspan="6">${t("volumes.noVolumesCreate")}</td></tr>`;
  $("#view").innerHTML =
    `<div class="card">` +
      `<div class="card-head"><h2>${t("volumes.title")}</h2>` +
        `<div class="head-tools">` +
          (canMutate() ? `<button class="ghost" id="pruneVolumes">${t("volumes.pruneUnused")}</button>` : "") +
          (canMutate() ? `<button class="primary" id="newVolumeBtn">${t("volumes.newVolume")}</button>` : "") +
        `</div>` +
      `</div>` +
      `<table class="data">` +
        `<thead><tr><th>${t("common.name")}</th><th>${t("volumes.colDriver")}</th><th>${t("common.size")}</th><th>${t("volumes.colInUse")}</th><th>${t("common.owner")}</th><th class="actions">${t("common.actions")}</th></tr></thead>` +
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
      `<td>${v.inUse ? `<span class="badge badge-ok">${t("volumes.inUse")}</span>` : `<span class="badge badge-muted">${t("volumes.free")}</span>`}</td>` +
      `<td><div class="secondary-line">${escapeHtml(displayNameForUsername(v.owner) || "—")}</div></td>` +
      `<td class="actions">` +
        `<button class="icon" title="${t("volumes.browseFiles")}" data-vol-name="${escapeHtml(v.name)}" data-vol-fullname="${escapeHtml(v.fullName || v.name)}" data-vol-files="1">📁</button>` +
        (canMutate() ? `<button class="icon danger" title="${t("volumes.delete")}" data-vol-name="${escapeHtml(v.name)}" data-vol-fullname="${escapeHtml(v.fullName || v.name)}">✕</button>` : "") +
      `</td>` +
    `</tr>`
  );
}

function openCreateVolume() {
  showModal({
    kind: "volume",
    title: t("volumes.newTitle"),
    body:
      `<form id="volForm" class="compact">` +
        `<input name="name" placeholder="${t("volumes.namePlaceholder")}" required>` +
        `<select name="driver"><option value="local">local</option><option value="nfs">nfs</option></select>` +
        `<input name="subnet" placeholder="${t("volumes.localHint")}" disabled>` +
        `<p class="hint">${t("volumes.ownedHint")}</p>` +
      `</form>`,
    foot: `<button class="ghost" data-close>${t("common.cancel")}</button><button class="primary" id="volSubmit">${t("common.create")}</button>`,
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
      toast(t("volumes.created"), true);
    } catch (err) {
      toast(err.message);
    }
  };
}

async function deleteVolume(fullName, name) {
  if (!confirm(t("volumes.deleteConfirm", { name }))) return;
  try {
    await api("/api/volumes/delete", { method: "POST", body: JSON.stringify({ name: fullName, force: false }) });
    await refreshSection("volumes");
    renderView();
    toast(t("volumes.deleted"), true);
  } catch (err) {
    toast(err.message);
  }
}

async function pruneVolumes() {
  if (!confirm(t("volumes.pruneConfirm"))) return;
  try {
    const r = await api("/api/volumes/prune", { method: "POST" });
    await refreshSection("volumes");
    renderView();
    toast(t("volumes.pruneResult", { n: r.removed || 0, size: fmtMB((r.bytesFreed || 0) / 1024 / 1024) }), true);
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
