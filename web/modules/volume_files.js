// Volume file browser: browse/upload/download/delete/rename files inside a
// mudp-managed volume via its host mountpoint (server-side). Single-pane.
//
// Mirrors the table styling of files.js (the dual-pane container/netdisk
// explorer) but operates only on the volume filesystem.

import { api, toast, canMutate, state, readCSRFCookie, t } from "../app.js";
import { showModalNoShell } from "./ui.js";
import { uploadWithProgress, showUploadOverlay } from "../lib/upload.js";

const $ = (sel, root = document) => root.querySelector(sel);
const $$ = (sel, root = document) => [...root.querySelectorAll(sel)];

// Per-explorer session, reset on each open.
let session = null;

export function openVolumeFiles(fullName, displayName) {
  session = {
    fullName,
    displayName,
    path: "",
    items: [],
    selected: new Set(),
  };
  showModalNoShell(
    "volfiles-modal",
    "wide files-modal",
    `<div class="modal-head">
       <h2>${t("volfiles.title", { name: escapeHtml(displayName) })}</h2>
       <button class="ghost" data-close>${t("common.close")}</button>
     </div>
     <div class="modal-body files-body">
       <div class="files-toolbar">
         <button class="primary" id="volUploadBtn" title="${t("volfiles.uploadTitle")}">${t("volfiles.upload")}</button>
         <input type="file" id="volUploadInput" multiple style="display:none">
         <button class="ghost" id="volMkdirBtn" title="${t("volfiles.newFolder")}">📁 ${t("volfiles.newFolder")}</button>
         <button class="ghost danger" id="volDeleteBtn" title="${t("volfiles.deleteTitle")}">✕ ${t("common.delete")}</button>
         <button class="ghost" id="volUpBtn">${t("volfiles.up")}</button>
         <code class="mono pane-path" id="volPath">/</code>
         <span class="hint" id="volStatus"></span>
       </div>
       <div class="files-panes">
         <section class="files-pane">
           <div class="pane-scroll" id="volList"><p class="hint">Loading…</p></div>
         </section>
       </div>
     </div>`,
    true
  );
  bindChrome();
  load();
}

function bindChrome() {
  const up = $("#volUpBtn");
  const del = $("#volDeleteBtn");
  const mkdir = $("#volMkdirBtn");
  const uploadBtn = $("#volUploadBtn");
  const uploadInput = $("#volUploadInput");
  if (up) up.onclick = goUp;
  if (del) del.onclick = deleteSelected;
  if (mkdir) mkdir.onclick = newFolder;
  if (uploadBtn) uploadBtn.onclick = () => uploadInput && uploadInput.click();
  if (uploadInput) uploadInput.onchange = () => doUpload(uploadInput.files);
}

function setStatus(msg) {
  const el = $("#volStatus");
  if (el) el.textContent = msg || "";
}

function sorted(items) {
  return items.slice().sort((a, b) => (a.dir === b.dir ? a.name.localeCompare(b.name) : a.dir ? -1 : 1));
}

async function load() {
  if (!session) return;
  session.selected = new Set();
  const listEl = $("#volList");
  if (listEl) listEl.innerHTML = `<p class="hint">Loading…</p>`;
  try {
    const res = await api(`/api/volumes/files?name=${encodeURIComponent(session.fullName)}&path=${encodeURIComponent(session.path)}`);
    session.path = res.path || session.path;
    session.items = res.items || [];
    setStatus("");
  } catch (err) {
    session.items = [];
    setStatus(err.message);
  }
  render();
}

function render() {
  if (!session) return;
  const pathEl = $("#volPath");
  const listEl = $("#volList");
  if (!pathEl || !listEl) return; // modal closed while loading
  pathEl.textContent = "/" + (session.path || "");
  const rows = sorted(session.items).map(row).join("");
  listEl.innerHTML = rows
    ? `<table class="data files-table"><thead><tr><th class="chk-col"><input type="checkbox" id="volSelectAll"></th><th class="name-col">${t("common.name")}</th><th class="size-col">${t("common.size")}</th><th class="mode-col">${t("volfiles.colMode")}</th><th class="time-col">${t("volfiles.colModified")}</th><th class="actions"></th></tr></thead><tbody>${rows}</tbody></table>`
    : `<p class="hint">${t("volfiles.emptyFolder")}</p>`;
  const allBox = $("#volSelectAll");
  if (allBox) {
    allBox.onchange = () => {
      const on = allBox.checked;
      $$("#volList [data-pick]").forEach((box) => {
        box.checked = on;
        togglePick(box.dataset.pick, on);
      });
    };
  }
  $$("#volList [data-open]").forEach((btn) => {
    btn.onclick = () => { session.path = btn.dataset.open; load(); };
  });
  $$("#volList [data-pick]").forEach((box) => {
    box.onchange = () => togglePick(box.dataset.pick, box.checked);
  });
  $$("#volList [data-download]").forEach((btn) => {
    btn.onclick = () => window.open(
      `/api/volumes/files/download?name=${encodeURIComponent(session.fullName)}&path=${encodeURIComponent(btn.dataset.download)}`,
      "_blank"
    );
  });
  $$("#volList [data-rename]").forEach((btn) => {
    btn.onclick = () => renameItem(btn.dataset.rename, btn.dataset.renameName);
  });
}

function row(f) {
  const checked = session.selected.has(f.path) ? "checked" : "";
  const icon = f.dir ? "📁" : "📄";
  const nameCell = f.dir
    ? `<button class="link" data-open="${escapeHtml(f.path)}">${icon} ${escapeHtml(f.name)}</button>`
    : `<span>${icon} ${escapeHtml(f.name)}</span>`;
  const mutable = canMutate();
  return (
    `<tr>` +
    `<td class="chk-col"><input type="checkbox" data-pick="${escapeHtml(f.path)}" ${checked}></td>` +
    `<td class="name-col">${nameCell}</td>` +
    `<td class="size-col">${f.dir ? "—" : fmtBytes(f.size)}</td>` +
    `<td class="mode-col mono">${escapeHtml(f.mode || "—")}</td>` +
    `<td class="time-col">${escapeHtml(fmtTime(f.modTime))}</td>` +
    `<td class="actions">` +
      `<a class="ghost-link" data-download="${escapeHtml(f.path)}" title="${t("netdisk.download")}">⬇</a>` +
      (mutable ? `<button class="ghost-link" data-rename="${escapeHtml(f.path)}" data-rename-name="${escapeHtml(f.name)}" title="${t("netdisk.rename")}">✎</button>` : "") +
    `</td>` +
    `</tr>`
  );
}

function togglePick(key, on) {
  if (on) session.selected.add(key);
  else session.selected.delete(key);
}

function goUp() {
  const parts = (session.path || "").split("/").filter(Boolean);
  parts.pop();
  session.path = parts.join("/");
  load();
}

async function newFolder() {
  const name = prompt(t("netdisk.folderNamePrompt"));
  if (!name || !name.trim()) return;
  const dir = (session.path ? session.path + "/" : "") + name.trim();
  try {
    await api("/api/volumes/files/mkdir", {
      method: "POST",
      body: JSON.stringify({ name: session.fullName, path: dir }),
    });
    toast(t("volfiles.folderCreated"), true);
    await load();
  } catch (err) {
    toast(err.message);
  }
}

async function deleteSelected() {
  const paths = [...session.selected];
  if (paths.length === 0) {
    toast(t("volfiles.selectFirst"));
    return;
  }
  if (!confirm(t("volfiles.deleteConfirm", { n: paths.length }))) return;
  try {
    await api("/api/volumes/files/delete", {
      method: "POST",
      body: JSON.stringify({ name: session.fullName, paths }),
    });
    toast(t("volfiles.deletedN", { n: paths.length }), true);
    await load();
  } catch (err) {
    toast(err.message);
  }
}

async function renameItem(path, oldName) {
  const newName = prompt(t("volfiles.newName"), oldName);
  if (!newName || !newName.trim() || newName.trim() === oldName) return;
  try {
    await api("/api/volumes/files/rename", {
      method: "POST",
      body: JSON.stringify({ name: session.fullName, path, newName: newName.trim() }),
    });
    toast(t("volfiles.renamed"), true);
    await load();
  } catch (err) {
    toast(err.message);
  }
}

async function doUpload(fileList) {
  const files = [...(fileList || [])];
  if (files.length === 0) return;
  setStatus(t("volfiles.uploading", { n: files.length }));
  const btns = $$(".files-toolbar button");
  btns.forEach((b) => (b.disabled = true));
  let ok = 0;
  // Files upload sequentially, one request per file. The overlay shows overall
  // progress: completed bytes plus the in-flight file's fraction of its size.
  const totalBytes = files.reduce((sum, f) => sum + (f.size || 0), 0);
  let baseBytes = 0;
  const overlay = showUploadOverlay(t("volfiles.uploading", { n: files.length }));
  try {
    for (let i = 0; i < files.length; i++) {
      const file = files[i];
      overlay.setLabel(`${i + 1}/${files.length}: ${file.name}`);
      const fd = new FormData();
      fd.append("name", session.fullName);
      fd.append("path", session.path || "");
      fd.append("files", file, file.name);
      try {
        await uploadWithProgress("/api/volumes/files/upload", fd, {
          csrfToken: readCSRFCookie() || state.csrfToken || "",
          onProgress: (p) => {
            const frac = p.total > 0 ? p.loaded / p.total : 0;
            const loaded = baseBytes + frac * (file.size || 0);
            overlay.update({
              loaded,
              total: totalBytes,
              percent: totalBytes > 0 ? Math.min(100, Math.round((loaded / totalBytes) * 100)) : 0,
              speedBps: p.speedBps,
            });
          },
        });
        ok++;
      } catch (err) {
        toast(err.message);
      }
      baseBytes += file.size || 0;
    }
    toast(t("volfiles.uploadedN", { n: ok }), true);
    await load();
    setStatus("");
  } catch (err) {
    toast(err.message);
    setStatus(err.message);
  } finally {
    overlay.close();
    btns.forEach((b) => (b.disabled = false));
    const input = $("#volUploadInput");
    if (input) input.value = "";
  }
}

function fmtBytes(n) {
  n = Number(n) || 0;
  if (n < 1024) return n + " B";
  if (n < 1024 * 1024) return (n / 1024).toFixed(1) + " KB";
  if (n < 1024 * 1024 * 1024) return (n / 1024 / 1024).toFixed(1) + " MB";
  return (n / 1024 / 1024 / 1024).toFixed(2) + " GB";
}

function fmtTime(ts) {
  if (!ts) return "—";
  const d = new Date(ts);
  if (Number.isNaN(d.getTime())) return "—";
  return d.toLocaleString();
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
