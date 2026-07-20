import { state, api, toast, escapeHtml, fmtBytes, isAdmin, canMutate, displayNameForUsername, readCSRFCookie } from "../app.js";
import { showModalNoShell, closeModal, onModalClose } from "./ui.js";
import { uploadWithProgress, showUploadOverlay } from "../lib/upload.js";
import { openYuvViewer } from "../lib/yuv.js";

// netdiskMode is "netdisk" (primary SSD, the default) or "backup" (the slow
// mechanical backup disk). The whole view branches on this: which list/quota
// endpoints it fetches, which action buttons show, and how download/preview
// URLs are built. Toggling also resets the path so each disk starts at its root.
let netdiskMode = "netdisk";

// Keep independent browsing context for each disk so toggling does not lose
// the current folder/selection state of the other side.
const modeState = {
  netdisk: { path: "", selected: new Set() },
  backup: { path: "", selected: new Set() },
};

function currentModeState() {
  return modeState[isBackupMode() ? "backup" : "netdisk"];
}

function saveCurrentModeState() {
  const slot = currentModeState();
  slot.path = state.netdisk?.path || "";
  slot.selected = new Set(state.netdisk?.selected || []);
}

function restoreModeState(mode) {
  const slot = modeState[mode] || { path: "", selected: new Set() };
  state.netdisk = {
    ...(state.netdisk || {}),
    path: slot.path || "",
    selected: new Set(slot.selected || []),
  };
}

function setCurrentPath(path) {
  state.netdisk.path = path;
  currentModeState().path = path;
}

function setCurrentSelection(next) {
  const set = next instanceof Set ? next : new Set(next || []);
  state.netdisk.selected = set;
  currentModeState().selected = new Set(set);
}

function clearCurrentSelection() {
  setCurrentSelection(new Set());
}

function isBackupMode() {
  return netdiskMode === "backup";
}

function backupUnavailableMessage(errOrMsg) {
  const msg = String(errOrMsg?.message || errOrMsg || "").trim();
  if (!msg) return "Backup disk is unavailable or not mounted. Ask an admin to mount the configured backup device.";
  if (msg.toLowerCase().includes("not configured")) return msg;
  return `Backup disk is unavailable or not mounted: ${msg}`;
}

export async function renderNetdisk() {
  restoreModeState(netdiskMode);
  const tabAtEntry = state.tab;
  // Only the first load shows a placeholder; background refreshes keep the
  // current content until the new data arrives (no white flash).
  const firstLoad = !$("#netdiskCard");
  if (firstLoad) {
    $("#view").innerHTML = `<div class="card"><div class="card-body"><p class="hint">Loading files...</p></div></div>`;
  }
  const prevSig = state.netdisk?.sig;
  const prevSelSig = state.netdisk?.selSig;
  let sig;
  try {
    const [list, quota, shares, adminShares] = await Promise.all([
      isBackupMode()
        ? api(`${"/api/netdisk/backup/browse"}?path=${encodeURIComponent(state.netdisk.path || "")}`)
        : api(`/api/netdisk?path=${encodeURIComponent(state.netdisk.path || "")}`),
      // Quota and shares are netdisk-only concepts; skip both in backup mode.
      isBackupMode() ? Promise.resolve(null) : api("/api/netdisk/quota").catch(() => null),
      isBackupMode() ? Promise.resolve([]) : api("/api/netdisk/shares").catch(() => []),
      isBackupMode() || !isAdmin() ? Promise.resolve([]) : api("/api/admin/netdisk/shares").catch(() => []),
    ]);
    const effectiveQuota = isBackupMode() ? (list?.quota || null) : quota;
    const backupWarning = isBackupMode() && list?.unavailable ? backupUnavailableMessage(list.message) : "";
    sig = JSON.stringify([netdiskMode, list.path, list.items, effectiveQuota, shares, adminShares, backupWarning]);
    state.netdisk = {
      path: list.path || "",
      items: list.items || [],
      quota: effectiveQuota,
      shares,
      adminShares,
      backupWarning,
      selected: state.netdisk?.selected || new Set(),
      sig,
    };
    // Keep the mode-local cursor in sync with server-normalized path.
    setCurrentPath(state.netdisk.path || "");
    setCurrentSelection(state.netdisk.selected || new Set());
  } catch (err) {
    if (isBackupMode()) {
      const warning = backupUnavailableMessage(err);
      sig = JSON.stringify([netdiskMode, state.netdisk?.path || "", [], null, [], [], warning]);
      state.netdisk = {
        path: state.netdisk?.path || "",
        items: [],
        quota: null,
        shares: [],
        adminShares: [],
        backupWarning: warning,
        selected: state.netdisk?.selected || new Set(),
        sig,
      };
      setCurrentPath(state.netdisk.path || "");
      setCurrentSelection(state.netdisk.selected || new Set());
    } else {
      // A failed background refresh keeps the previous content; only the first
      // load surfaces the error.
      if (firstLoad) {
        $("#view").innerHTML = `<div class="card"><div class="card-body"><div class="error-box">${escapeHtml(err.message)}</div></div></div>`;
      }
      return;
    }
  }
  // The user switched tabs mid-fetch: drop the result instead of painting
  // netdisk over the newly active view.
  if (state.tab !== tabAtEntry) return;
  // Nothing changed — neither the listing nor the checkbox selection: leave
  // the DOM untouched so scroll position, selections and focus survive quiet
  // background refreshes.
  const selSig = JSON.stringify([...state.netdisk.selected].sort());
  if (!firstLoad && sig === prevSig && selSig === prevSelSig) return;
  state.netdisk.selSig = selSig;

  const mutable = canMutate();
  const backup = isBackupMode();
  const rows = sortedItems(state.netdisk.items).map((f) => fileRow(f, mutable, backup)).join("") ||
    `<tr class="empty-row"><td colspan="${mutable ? 6 : 5}">No files.</td></tr>`;
  const fileCount = state.netdisk.items.filter((f) => !f.dir).length;
  const folderCount = state.netdisk.items.length - fileCount;
  const quotaHtml = quotaBar(state.netdisk.quota);
  const selectionSize = [...state.netdisk.selected].length;

  $("#view").innerHTML =
    `<div class="stack netdisk-stack">` +
      `<div class="card netdisk-card" id="netdiskCard">` +
        `<div class="netdisk-toolbar">` +
          `<div class="netdisk-title">` +
            `<div class="netdisk-mode-toggle" role="tablist">` +
              `<button class="netdisk-mode-btn${backup ? "" : " active"}" data-mode="netdisk" role="tab" aria-selected="${backup ? "false" : "true"}">Netdisk</button>` +
              `<button class="netdisk-mode-btn${backup ? " active" : ""}" data-mode="backup" role="tab" aria-selected="${backup ? "true" : "false"}">Backup Disk</button>` +
            `</div>` +
            `<span class="netdisk-count">${folderCount} folders, ${fileCount} files</span>` +
          `</div>` +
          `<div class="head-tools netdisk-actions">` +
            `<button class="ghost" id="upDir">Up</button>` +
            `<button class="ghost" id="mkdirBtn">New Folder</button>` +
            // Upload and Folder-upload are netdisk-only: a backup disk is a
            // mirror target, not a place to drop arbitrary new files.
            (backup ? "" : `<label class="buttonlike"><input id="uploadFiles" type="file" multiple> Upload</label>`) +
            (backup ? "" : `<label class="buttonlike"><input id="uploadFolder" type="file" webkitdirectory directory multiple> Folder</label>`) +
            (mutable
              ? `<button class="ghost danger" id="batchDelete" ${selectionSize ? "" : "disabled"}>Delete (${selectionSize})</button>` +
                `<button class="ghost" id="batchCopy" ${selectionSize ? "" : "disabled"}>Copy (${selectionSize})</button>` +
                `<button class="ghost" id="batchMove" ${selectionSize ? "" : "disabled"}>Move (${selectionSize})</button>` +
                // Share and Backup-to-backup don't apply on the backup disk.
                (backup ? "" : `<button class="ghost" id="batchShare" ${selectionSize ? "" : "disabled"}>Share (${selectionSize})</button>`) +
                `<button class="ghost" id="batchDownload" ${selectionSize ? "" : "disabled"}>Download (${selectionSize})</button>`
              : "") +
          `</div>` +
        `</div>` +
        (backup && state.netdisk.backupWarning
          ? `<div class="netdisk-backup-hint">${escapeHtml(state.netdisk.backupWarning)}</div>`
          : "") +
        `<div class="netdisk-pathbar">` +
          `<div class="netdisk-crumbs">${breadcrumbs(state.netdisk.path)}</div>` +
          `<div class="netdisk-used">${quotaHtml}</div>` +
        `</div>` +
        `<table class="data netdisk-table"><thead><tr>` +
          (mutable ? `<th class="chk-col"><input type="checkbox" id="selectAllFiles"></th>` : "") +
          `<th>Name</th><th class="size-col">Size</th><th class="time-col">Modified</th><th class="actions">Actions</th>` +
        `</tr></thead><tbody>${rows}</tbody></table>` +
      `</div>` +
      // External Links cards are netdisk-only — backup files can't be shared.
      (backup ? "" :
        `<div class="card"><div class="card-head"><h2>External Links</h2></div>` +
        `<table class="data netdisk-shares"><thead><tr><th class="share-name-col">Name</th><th class="share-link-col">Link</th><th class="share-expires-col">Expires</th><th class="share-access-col">Access</th><th class="actions">Actions</th></tr></thead><tbody>${shareRows(state.netdisk.shares || [])}</tbody></table></div>` +
        (isAdmin() ? `<div class="card"><div class="card-head"><h2>All External Links</h2><div class="head-tools"><label class="check compact-check" for="selectAllShares"><input type="checkbox" id="selectAllShares"><span>Select all</span></label><button class="danger" id="deleteSelectedShares">Delete Selected</button></div></div>` +
        `<table class="data netdisk-admin-shares"><thead><tr><th class="chk-col"></th><th class="share-owner-col">Owner</th><th class="share-name-col">Name</th><th class="share-link-col">Link</th><th class="share-expires-col">Expires</th><th class="share-access-col">Access</th></tr></thead><tbody>${adminShareRows(state.netdisk.adminShares || [])}</tbody></table></div>` : "")
      ) +
    `</div>`;

  bindNetdiskEvents(mutable);
}

function bindNetdiskEvents(mutable) {
  const backup = isBackupMode();
  const base = backup ? "/api/netdisk/backup" : "/api/netdisk";

  // --- Mode toggle: switch between the netdisk and the backup disk. Switching
  // resets path + selection so each disk opens at its own root.
  document.querySelectorAll("[data-mode]").forEach((btn) => {
    btn.onclick = () => {
      if (btn.dataset.mode === netdiskMode) return;
      saveCurrentModeState();
      netdiskMode = btn.dataset.mode;
      restoreModeState(netdiskMode);
      state.netdisk.sig = null; // force a repaint (mode is part of sig anyway)
      renderNetdisk();
    };
  });

  $("#upDir").onclick = () => {
    const parts = (state.netdisk.path || "").split("/").filter(Boolean);
    parts.pop();
    setCurrentPath(parts.join("/"));
    clearCurrentSelection();
    renderNetdisk();
  };

  document.querySelectorAll("[data-crumb]").forEach((btn) => {
    btn.onclick = () => {
      setCurrentPath(btn.dataset.crumb);
      clearCurrentSelection();
      renderNetdisk();
    };
  });

  $("#mkdirBtn").onclick = async () => {
    const name = prompt("Folder name");
    if (!name) return;
    await api(`${base}/mkdir`, { method: "POST", body: JSON.stringify({ path: joinPath(state.netdisk.path, name) }) });
    toast("Folder created", true);
    renderNetdisk();
  };

  // Upload labels are only rendered in netdisk mode; guard so a missing element
  // (backup mode) doesn't throw.
  const uploadFiles = $("#uploadFiles");
  if (uploadFiles) {
    uploadFiles.onchange = async (e) => {
      await uploadFilesHandler(e.target.files, state.netdisk.path || "");
      e.target.value = "";
    };
  }
  const uploadFolder = $("#uploadFolder");
  if (uploadFolder) {
    uploadFolder.onchange = async (e) => {
      await uploadFilesHandler(e.target.files, state.netdisk.path || "");
      e.target.value = "";
    };
  }

  // Drag-drop upload is netdisk-only.
  if (!backup) {
    const card = $("#netdiskCard");
    card.ondragover = (e) => { e.preventDefault(); card.classList.add("drag-over"); };
    card.ondragleave = () => card.classList.remove("drag-over");
    card.ondrop = async (e) => {
      e.preventDefault();
      card.classList.remove("drag-over");
      if (!mutable) return toast("Read-only account cannot upload");
      const files = e.dataTransfer?.files;
      if (files?.length) await uploadFilesHandler(files, state.netdisk.path || "");
    };
  }

  document.querySelectorAll("[data-open]").forEach((btn) => {
    btn.onclick = () => {
      setCurrentPath(btn.dataset.open);
      clearCurrentSelection();
      renderNetdisk();
    };
  });

  document.querySelectorAll("[data-view]").forEach((btn) => {
    btn.onclick = () => openFileViewer(btn.dataset.view, btn.dataset.backup === "1");
  });

  if (mutable) {
    document.querySelectorAll("[data-del]").forEach((btn) => {
      btn.onclick = async () => {
        if (!confirm(`Delete ${btn.dataset.name}?`)) return;
        await api(`${base}/delete`, { method: "POST", body: JSON.stringify({ paths: [btn.dataset.del] }) });
        toast("Deleted", true);
        renderNetdisk();
      };
    });
    document.querySelectorAll("[data-ren]").forEach((btn) => {
      btn.onclick = async () => renameItem(btn.dataset.ren, btn.dataset.name);
    });
    document.querySelectorAll("[data-copy]").forEach((btn) => {
      btn.onclick = () => openFolderPicker(false, [btn.dataset.copy]);
    });
    document.querySelectorAll("[data-move]").forEach((btn) => {
      btn.onclick = () => openFolderPicker(true, [btn.dataset.move]);
    });
    // Share buttons only render in netdisk mode; the selector matches nothing
    // in backup mode and these blocks simply no-op.
    document.querySelectorAll("[data-share]").forEach((btn) => {
      btn.onclick = () => openShareModal([btn.dataset.share], btn.dataset.name);
    });
    document.querySelectorAll(".netdisk-row-check").forEach((cb) => {
      cb.onchange = () => toggleFileSelection(cb.dataset.path, cb.checked);
    });
    $("#selectAllFiles").onchange = (e) => {
      if (e.target.checked) {
        state.netdisk.items.forEach((f) => state.netdisk.selected.add(f.path));
      } else {
        clearCurrentSelection();
        renderNetdisk();
        return;
      }
      setCurrentSelection(state.netdisk.selected);
      renderNetdisk();
    };
    $("#batchDelete").onclick = batchDelete;
    $("#batchCopy").onclick = () => batchCopyMove(false);
    $("#batchMove").onclick = () => batchCopyMove(true);
    const batchShareBtn = $("#batchShare");
    if (batchShareBtn) batchShareBtn.onclick = batchShare;
    $("#batchDownload").onclick = batchDownload;
  }

  document.querySelectorAll("[data-copy-link]").forEach((btn) => {
    btn.onclick = async () => {
      const code = btn.dataset.copyCode;
      const text = code ? `${btn.dataset.copyLink}\nExtraction code: ${code}` : btn.dataset.copyLink;
      await navigator.clipboard.writeText(text);
      toast(code ? "Link & code copied" : "Link copied", true);
    };
  });
  document.querySelectorAll("[data-share-del]").forEach((btn) => {
    btn.onclick = async () => {
      await api("/api/netdisk/share/delete", { method: "POST", body: JSON.stringify({ token: btn.dataset.shareDel }) });
      toast("External link deleted", true);
      renderNetdisk();
    };
  });

  const selectAll = $("#selectAllShares");
  if (selectAll) {
    selectAll.onchange = () => {
      document.querySelectorAll("[data-admin-share-token]").forEach((cb) => cb.checked = selectAll.checked);
    };
  }
  const deleteSelected = $("#deleteSelectedShares");
  if (deleteSelected) {
    deleteSelected.onclick = async () => {
      const tokens = [...document.querySelectorAll("[data-admin-share-token]:checked")].map((i) => i.value);
      if (!tokens.length) return toast("Select at least one external link.");
      if (!confirm(`Delete ${tokens.length} external link(s)?`)) return;
      await api("/api/admin/netdisk/shares/delete", { method: "POST", body: JSON.stringify({ tokens }) });
      toast("External links deleted", true);
      renderNetdisk();
    };
  }
}

function fileRow(f, mutable, backup) {
  const href = backup ? backupDownloadURL(f.path) : downloadURL(f.path);
  const checked = state.netdisk.selected.has(f.path) ? "checked" : "";
  const kind = previewKind(f.name);
  const name = f.dir
    ? `<button class="linklike netdisk-name-link" data-open="${escapeHtml(f.path)}">${escapeHtml(f.name)}</button>`
    : kind
      ? `<button class="linklike netdisk-name-link" data-view="${escapeHtml(f.path)}" data-backup="${backup ? "1" : ""}" title="Preview">${escapeHtml(f.name)}</button>`
      : `<a class="netdisk-name-link" href="${href}">${escapeHtml(f.name)}</a>`;
  const checkCell = mutable
    ? `<td class="chk-col"><input type="checkbox" class="netdisk-row-check" data-path="${escapeHtml(f.path)}" ${checked}></td>`
    : "";
  // On the backup disk we drop Share (no sharing from backup) and the
  // Backup-to-backup button (it's already there). Everything else — download,
  // rename, copy/move within the disk, delete — stays for full management.
  const actionCell = mutable
    ? `<a class="icon" href="${href}" title="Download">⬇</a>` +
      `<button class="icon" title="Rename" data-ren="${escapeHtml(f.path)}" data-name="${escapeHtml(f.name)}">✎</button>` +
      `<button class="icon" title="Copy" data-copy="${escapeHtml(f.path)}">⧉</button>` +
      `<button class="icon" title="Move" data-move="${escapeHtml(f.path)}">↗</button>` +
      (backup ? "" : `<button class="icon" title="Share" data-share="${escapeHtml(f.path)}" data-name="${escapeHtml(f.name)}">⤴</button>`) +
      `<button class="icon danger" title="Delete" data-del="${escapeHtml(f.path)}" data-name="${escapeHtml(f.name)}">✕</button>`
    : `<a class="icon" href="${href}" title="Download">⬇</a>`;
  return `<tr>${checkCell}<td><div class="netdisk-file"><span class="netdisk-icon ${f.dir ? "folder" : "file"}" aria-hidden="true">${fileIcon(f.dir)}</span><div class="primary-line">${name}</div></div></td><td class="netdisk-size">${f.dir ? "-" : fmtBytes(f.size)}</td><td class="netdisk-time" title="${escapeHtml(new Date(f.modTime).toLocaleString())}">${escapeHtml(fmtDate(f.modTime))}</td><td class="actions netdisk-row-actions">${actionCell}</td></tr>`;
}

function toggleFileSelection(path, checked) {
  if (checked) state.netdisk.selected.add(path);
  else state.netdisk.selected.delete(path);
  setCurrentSelection(state.netdisk.selected);
  renderNetdisk();
}

async function batchDelete() {
  const paths = [...state.netdisk.selected];
  if (!paths.length) return;
  if (!confirm(`Delete ${paths.length} item(s)?`)) return;
  const endpoint = isBackupMode() ? "/api/netdisk/backup/delete" : "/api/netdisk/delete";
  await api(endpoint, { method: "POST", body: JSON.stringify({ paths }) });
  clearCurrentSelection();
  toast("Deleted", true);
  renderNetdisk();
}

function batchCopyMove(move) {
  const paths = [...state.netdisk.selected];
  if (!paths.length) return;
  openFolderPicker(move, paths);
}

// ---------- Folder picker (move/copy destination) ----------

// folderPicker holds the in-flight picker's state while the modal is open.
let folderPicker = null;

function openFolderPicker(move, paths) {
  const sourceDisk = isBackupMode() ? "backup" : "netdisk";
  folderPicker = { move, paths, path: state.netdisk.path || "", items: [], sourceDisk, targetDisk: sourceDisk };
  const action = move ? "Move" : "Copy";
  const diskLabel = sourceDisk === "backup" ? "Backup Disk" : "My Netdisk";
  const netdiskActive = sourceDisk === "netdisk" ? " active" : "";
  const backupActive = sourceDisk === "backup" ? " active" : "";
  showModalNoShell(
    "netdisk-picker",
    "wide picker-modal",
    `<div class="modal-head">` +
      `<div><h2>${action} ${paths.length} item(s)</h2><p class="hint" style="margin-top:4px">Choose a destination folder on ${diskLabel.toLowerCase()}</p></div>` +
      `<button class="icon" data-close>✕</button>` +
    `</div>` +
    `<div class="picker-layout">` +
      `<aside class="picker-tree">` +
        `<button class="picker-tree-item picker-disk${netdiskActive}" data-picker-disk="netdisk" type="button">My Netdisk</button>` +
        `<button class="picker-tree-item picker-disk${backupActive}" data-picker-disk="backup" type="button">Backup Disk</button>` +
      `</aside>` +
      `<section class="picker-main">` +
        `<div class="picker-toolbar">` +
          `<button class="ghost" id="pickerUp">↑ Up</button>` +
          `<div class="picker-path" id="pickerPath">/</div>` +
          `<button class="ghost" id="pickerMkdir">+ New Folder</button>` +
        `</div>` +
        `<div class="picker-list" id="pickerList"><div class="picker-empty">Loading…</div></div>` +
      `</section>` +
    `</div>` +
    `<div class="modal-foot">` +
      `<span class="hint" id="pickerHint"></span>` +
      `<div class="modal-tools">` +
        `<button class="ghost" data-close>Cancel</button>` +
        `<button class="primary" id="pickerConfirm">${action} Here</button>` +
      `</div>` +
    `</div>`
  );
  $("#pickerUp").onclick = () => {
    const parts = folderPicker.path.split("/").filter(Boolean);
    parts.pop();
    loadPickerFolder(parts.join("/"));
  };
  document.querySelectorAll("[data-picker-disk]").forEach((btn) => {
    btn.onclick = () => {
      if (!folderPicker) return;
      const nextDisk = btn.dataset.pickerDisk;
      if (!nextDisk || nextDisk === folderPicker.targetDisk) return;
      folderPicker.targetDisk = nextDisk;
      folderPicker.path = "";
      document.querySelectorAll("[data-picker-disk]").forEach((node) => {
        node.classList.toggle("active", node.dataset.pickerDisk === folderPicker.targetDisk);
      });
      loadPickerFolder("");
    };
  });
  $("#pickerMkdir").onclick = pickerMkdirFolder;
  $("#pickerConfirm").onclick = confirmFolderPicker;
  loadPickerFolder(folderPicker.path);
}

async function loadPickerFolder(path) {
  if (!folderPicker) return;
  folderPicker.path = path;
  const listEl = $("#pickerList");
  if (!listEl) return; // modal closed while navigating
  listEl.innerHTML = `<div class="picker-empty">Loading…</div>`;
  try {
    const url = folderPicker.targetDisk === "backup"
      ? `/api/netdisk/backup/browse?path=${encodeURIComponent(path)}`
      : `/api/netdisk?path=${encodeURIComponent(path)}`;
    const data = await api(url);
    folderPicker.items = (data.items || []).filter((f) => f.dir);
    renderFolderPicker();
  } catch (err) {
    if (listEl.isConnected) listEl.innerHTML = `<div class="picker-empty">${escapeHtml(err.message)}</div>`;
  }
}

function renderFolderPicker() {
  const listEl = $("#pickerList");
  if (!folderPicker || !listEl) return; // modal closed mid-load
  const path = folderPicker.path;
  $("#pickerPath").textContent = "/" + path;
  $("#pickerUp").disabled = !path;

  // A folder being moved cannot be its own destination: show it disabled.
  const sources = new Set(folderPicker.paths);
  const rows = (folderPicker.items || []).map((f) => {
    const blocked = folderPicker.move && sources.has(f.path);
    return `<div class="picker-row${blocked ? " disabled" : ""}"${blocked ? "" : ` data-open="${escapeHtml(f.path)}"`}>` +
      `<span class="picker-folder-icon">${fileIcon(true)}</span>` +
      `<span class="picker-folder-name">${escapeHtml(f.name)}</span>` +
    `</div>`;
  }).join("");
  listEl.innerHTML = rows || `<div class="picker-empty">No subfolders</div>`;
  listEl.querySelectorAll("[data-open]").forEach((row) => {
    row.onclick = () => loadPickerFolder(row.dataset.open);
  });

  // Moving items onto their current location is a no-op; block it.
  const alreadyHere = folderPicker.move && folderPicker.paths.every((p) => parentDir(p) === path);
  $("#pickerConfirm").disabled = alreadyHere;
  $("#pickerHint").textContent = alreadyHere ? "Items are already in this folder" : `Destination: /${path}`;
}

async function pickerMkdirFolder() {
  const name = prompt("New folder name");
  if (!name || !folderPicker) return;
  try {
    const endpoint = folderPicker.targetDisk === "backup" ? "/api/netdisk/backup/mkdir" : "/api/netdisk/mkdir";
    await api(endpoint, { method: "POST", body: JSON.stringify({ path: joinPath(folderPicker.path, name) }) });
    toast("Folder created", true);
    await loadPickerFolder(folderPicker.path);
  } catch (err) {
    toast(err.message);
  }
}

async function confirmFolderPicker() {
  if (!folderPicker) return;
  const { move, paths, path, targetDisk } = folderPicker;
  folderPicker = null;
  closeModal();
  const items = paths.map((p) => ({ from: p, to: path }));
  if (move) await moveItems(items, targetDisk);
  else await copyItems(items, targetDisk);
}

function parentDir(p) {
  return (p || "").split("/").filter(Boolean).slice(0, -1).join("/");
}

async function copyItems(items, targetDisk) {
  const sourceDisk = isBackupMode() ? "backup" : "netdisk";
  const sameDisk = sourceDisk === targetDisk;
  const endpoint = sameDisk
    ? (sourceDisk === "backup" ? "/api/netdisk/backup/copy" : "/api/netdisk/copy")
    : "/api/netdisk/transfer";
  const body = sameDisk
    ? { items, move: false, policy: "rename" }
    : { fromDisk: sourceDisk, toDisk: targetDisk, items, move: false, policy: "rename" };
  const data = await api(endpoint, {
    method: "POST",
    body: JSON.stringify(body),
  });
  clearCurrentSelection();
  const errors = (data.results || []).filter((r) => r.status === "error").length;
  toast(errors ? `Copied ${data.count || 0} item(s), ${errors} failed` : `Copied ${data.count || 0} item(s)`, !errors);
  renderNetdisk();
}

async function moveItems(items, targetDisk) {
  const sourceDisk = isBackupMode() ? "backup" : "netdisk";
  const sameDisk = sourceDisk === targetDisk;
  const endpoint = sameDisk
    ? (sourceDisk === "backup" ? "/api/netdisk/backup/copy" : "/api/netdisk/copy")
    : "/api/netdisk/transfer";
  const body = sameDisk
    ? { items, move: true, policy: "rename" }
    : { fromDisk: sourceDisk, toDisk: targetDisk, items, move: true, policy: "rename" };
  const data = await api(endpoint, {
    method: "POST",
    body: JSON.stringify(body),
  });
  clearCurrentSelection();
  const errors = (data.results || []).filter((r) => r.status === "error").length;
  toast(errors ? `Moved ${data.count || 0} item(s), ${errors} failed` : `Moved ${data.count || 0} item(s)`, !errors);
  renderNetdisk();
}

// ---------- Backup picker (mirror of the copy/move picker, rooted at the
// backup disk) ----------
//
// Like openFolderPicker but the list comes from /api/netdisk/backup/browse
// (which reads the caller's backup disk, not their netdisk), and confirming
// starts a detached server-side backup job rather than a synchronous copy.

let backupPicker = null;

let restorePicker = null;

function openBackupPicker(paths) {
  backupPicker = { paths, path: "", items: [], mode: "now" };
  showModalNoShell(
    "netdisk-backup-picker",
    "wide picker-modal",
    `<div class="modal-head">` +
      `<div><h2>Backup ${paths.length} item(s)</h2><p class="hint" style="margin-top:4px">Choose a destination folder on the backup disk</p></div>` +
      `<button class="icon" data-close>✕</button>` +
    `</div>` +
    `<div class="picker-layout">` +
      `<aside class="picker-tree"><div class="picker-tree-item active">Backup Disk</div></aside>` +
      `<section class="picker-main">` +
        `<div class="picker-toolbar">` +
          `<button class="ghost" id="bkPickerUp">↑ Up</button>` +
          `<div class="picker-path" id="bkPickerPath">/</div>` +
          `<button class="ghost" id="bkPickerMkdir">+ New Folder</button>` +
        `</div>` +
        `<div class="picker-list" id="bkPickerList"><div class="picker-empty">Loading…</div></div>` +
      `</section>` +
    `</div>` +
    `<div class="modal-foot">` +
      `<div class="backup-mode-row">` +
        `<label class="check"><input type="radio" name="backupMode" value="now" checked><span>Immediate backup</span></label>` +
        `<label class="check"><input type="radio" name="backupMode" value="idle"><span>Delayed backup (backup window)</span></label>` +
      `</div>` +
      `<span class="hint" id="bkPickerHint"></span>` +
      `<div class="modal-tools">` +
        `<button class="ghost" data-close>Cancel</button>` +
        `<button class="primary" id="bkPickerConfirm">Backup Here</button>` +
      `</div>` +
    `</div>`
  );
  $("#bkPickerUp").onclick = () => {
    const parts = backupPicker.path.split("/").filter(Boolean);
    parts.pop();
    loadBackupPickerFolder(parts.join("/"));
  };
  $("#bkPickerMkdir").onclick = backupPickerMkdir;
  $("#bkPickerConfirm").onclick = confirmBackupPicker;
  document.querySelectorAll("input[name='backupMode']").forEach((input) => {
    input.onchange = () => {
      if (input.checked && backupPicker) backupPicker.mode = input.value || "now";
    };
  });
  loadBackupPickerFolder("");
}

async function loadBackupPickerFolder(path) {
  if (!backupPicker) return;
  backupPicker.path = path;
  const listEl = $("#bkPickerList");
  if (!listEl) return; // modal closed while navigating
  listEl.innerHTML = `<div class="picker-empty">Loading…</div>`;
  try {
    const data = await api(`/api/netdisk/backup/browse?path=${encodeURIComponent(path)}`);
    backupPicker.items = data.items || [];
    renderBackupPicker();
  } catch (err) {
    // No backup path configured → guide the user to it instead of a bare error.
    if (listEl.isConnected) {
      listEl.innerHTML = `<div class="picker-empty">${escapeHtml(err.message)}<br><span class="hint">Ask an admin to set your group's backup path on the Users &amp; Groups page.</span></div>`;
    }
  }
}

function renderBackupPicker() {
  const listEl = $("#bkPickerList");
  if (!backupPicker || !listEl) return;
  const path = backupPicker.path;
  $("#bkPickerPath").textContent = "/" + path;
  $("#bkPickerUp").disabled = !path;
  const rows = (backupPicker.items || []).map((f) =>
    `<div class="picker-row" data-open="${escapeHtml(f.path)}">` +
      `<span class="picker-folder-icon">${fileIcon(true)}</span>` +
      `<span class="picker-folder-name">${escapeHtml(f.name)}</span>` +
    `</div>`
  ).join("");
  listEl.innerHTML = rows || `<div class="picker-empty">No subfolders</div>`;
  listEl.querySelectorAll("[data-open]").forEach((row) => {
    row.onclick = () => loadBackupPickerFolder(row.dataset.open);
  });
  $("#bkPickerHint").textContent = `Destination: /${path}`;
}

async function backupPickerMkdir() {
  const name = prompt("New folder name on the backup disk");
  if (!name || !backupPicker) return;
  // Reuse the browse endpoint's lazy-create-by-MkdirAll on the parent; for a
  // real mkdir we POST to the netdisk mkdir with the backup-relative path. The
  // backup browse root and netdisk root differ, so we make a dedicated call.
  try {
    await api("/api/netdisk/backup/mkdir", { method: "POST", body: JSON.stringify({ path: joinPath(backupPicker.path, name) }) });
    toast("Folder created", true);
    await loadBackupPickerFolder(backupPicker.path);
  } catch (err) {
    toast(err.message);
  }
}

async function confirmBackupPicker() {
  if (!backupPicker) return;
  const { paths, path, mode } = backupPicker;
  backupPicker = null;
  closeModal();
  await submitBackup(paths, path, mode || "now");
}

// submitBackup starts the server-side backup job(s) and registers them with the
// background-jobs panel so the user can follow progress / cancel. We pass the
// selected backup-disk sub-path as `to` so the job writes there rather than the
// user's backup root directly.
async function submitBackup(paths, to, mode = "now") {
  try {
    const data = await api("/api/netdisk/backup", {
      method: "POST",
      body: JSON.stringify({ paths, to, mode }),
    });
    clearCurrentSelection();
    const ids = data.jobIds || [];
    const kind = mode === "idle" ? "Delayed backup queued" : "Backup started";
    toast(`${kind} (${ids.length} job${ids.length === 1 ? "" : "s"}) — see Background jobs`, true);
    renderNetdisk();
  } catch (err) {
    toast(err.message);
  }
}

function openRestorePicker(paths) {
  restorePicker = { paths, path: "", items: [] };
  showModalNoShell(
    "netdisk-restore-picker",
    "wide picker-modal",
    `<div class="modal-head">` +
      `<div><h2>Restore ${paths.length} item(s)</h2><p class="hint" style="margin-top:4px">Choose a destination folder on netdisk</p></div>` +
      `<button class="icon" data-close>✕</button>` +
    `</div>` +
    `<div class="picker-layout">` +
      `<aside class="picker-tree"><div class="picker-tree-item active">My Netdisk</div></aside>` +
      `<section class="picker-main">` +
        `<div class="picker-toolbar">` +
          `<button class="ghost" id="rtPickerUp">↑ Up</button>` +
          `<div class="picker-path" id="rtPickerPath">/</div>` +
          `<button class="ghost" id="rtPickerMkdir">+ New Folder</button>` +
        `</div>` +
        `<div class="picker-list" id="rtPickerList"><div class="picker-empty">Loading…</div></div>` +
      `</section>` +
    `</div>` +
    `<div class="modal-foot">` +
      `<span class="hint" id="rtPickerHint"></span>` +
      `<div class="modal-tools">` +
        `<button class="ghost" data-close>Cancel</button>` +
        `<button class="primary" id="rtPickerConfirm">Restore Here</button>` +
      `</div>` +
    `</div>`
  );
  $("#rtPickerUp").onclick = () => {
    const parts = restorePicker.path.split("/").filter(Boolean);
    parts.pop();
    loadRestorePickerFolder(parts.join("/"));
  };
  $("#rtPickerMkdir").onclick = restorePickerMkdir;
  $("#rtPickerConfirm").onclick = confirmRestorePicker;
  loadRestorePickerFolder("");
}

async function loadRestorePickerFolder(path) {
  if (!restorePicker) return;
  restorePicker.path = path;
  const listEl = $("#rtPickerList");
  if (!listEl) return;
  listEl.innerHTML = `<div class="picker-empty">Loading…</div>`;
  try {
    const data = await api(`/api/netdisk?path=${encodeURIComponent(path)}`);
    restorePicker.items = (data.items || []).filter((f) => f.dir);
    renderRestorePicker();
  } catch (err) {
    if (listEl.isConnected) listEl.innerHTML = `<div class="picker-empty">${escapeHtml(err.message)}</div>`;
  }
}

function renderRestorePicker() {
  const listEl = $("#rtPickerList");
  if (!restorePicker || !listEl) return;
  const path = restorePicker.path;
  $("#rtPickerPath").textContent = "/" + path;
  $("#rtPickerUp").disabled = !path;
  const rows = (restorePicker.items || []).map((f) =>
    `<div class="picker-row" data-open="${escapeHtml(f.path)}">` +
      `<span class="picker-folder-icon">${fileIcon(true)}</span>` +
      `<span class="picker-folder-name">${escapeHtml(f.name)}</span>` +
    `</div>`
  ).join("");
  listEl.innerHTML = rows || `<div class="picker-empty">No subfolders</div>`;
  listEl.querySelectorAll("[data-open]").forEach((row) => {
    row.onclick = () => loadRestorePickerFolder(row.dataset.open);
  });
  $("#rtPickerHint").textContent = `Destination: /${path}`;
}

async function restorePickerMkdir() {
  const name = prompt("New folder name on netdisk");
  if (!name || !restorePicker) return;
  try {
    await api("/api/netdisk/mkdir", { method: "POST", body: JSON.stringify({ path: joinPath(restorePicker.path, name) }) });
    toast("Folder created", true);
    await loadRestorePickerFolder(restorePicker.path);
  } catch (err) {
    toast(err.message);
  }
}

async function confirmRestorePicker() {
  if (!restorePicker) return;
  const { paths, path } = restorePicker;
  restorePicker = null;
  closeModal();
  await submitRestore(paths, path);
}

async function submitRestore(paths, to) {
  try {
    const out = await api("/api/netdisk/backup/restore", {
      method: "POST",
      body: JSON.stringify({ paths, to, policy: "rename" }),
    });
    clearCurrentSelection();
    toast(`Restored ${out.count || 0} item(s) to netdisk`, true);
    renderNetdisk();
  } catch (err) {
    toast(err.message);
  }
}

async function batchShare() {
  if (isBackupMode()) return toast("Backup disk does not support sharing");
  const paths = [...state.netdisk.selected];
  if (!paths.length) return;
  const name = paths.length === 1 ? paths[0].split("/").pop() : "Shared items";
  openShareModal(paths, name);
}

async function batchDownload() {
  const paths = [...state.netdisk.selected];
  if (!paths.length) return;
  const urlFor = isBackupMode() ? backupDownloadURL : downloadURL;
  if (paths.length === 1 && !state.netdisk.items.find((f) => f.path === paths[0])?.dir) {
    location.href = urlFor(paths[0]);
    return;
  }
  // Multiple items: download each as a separate zip/file.
  paths.forEach((p, i) => {
    setTimeout(() => {
      const a = document.createElement("a");
      a.href = urlFor(p);
      a.download = "";
      a.click();
    }, i * 300);
  });
}

function sortedItems(items) {
  return [...(items || [])].sort((a, b) => {
    if (a.dir !== b.dir) return a.dir ? -1 : 1;
    return String(a.name || "").localeCompare(String(b.name || ""), undefined, { numeric: true, sensitivity: "base" });
  });
}

function breadcrumbs(path) {
  const parts = (path || "").split("/").filter(Boolean);
  const crumbs = [`<button class="linklike" data-crumb="">All Files</button>`];
  let acc = "";
  parts.forEach((part) => {
    acc = joinPath(acc, part);
    crumbs.push(`<span>/</span><button class="linklike" data-crumb="${escapeHtml(acc)}">${escapeHtml(part)}</button>`);
  });
  return crumbs.join("");
}

function fileIcon(dir) {
  if (dir) {
    return `<svg viewBox="0 0 24 24" focusable="false"><path d="M3 6.8C3 5.8 3.8 5 4.8 5h5.1l2 2.2h7.3c1 0 1.8.8 1.8 1.8v1H3V6.8Z"/><path d="M3 9h18l-1.2 8.2c-.1 1-1 1.8-2 1.8H6.2c-1 0-1.8-.7-2-1.8L3 9Z"/></svg>`;
  }
  return `<svg viewBox="0 0 24 24" focusable="false"><path d="M6 3.5h8.4L19 8.1v12.1c0 1-.8 1.8-1.8 1.8H6.8c-1 0-1.8-.8-1.8-1.8V5.3c0-1 .8-1.8 1.8-1.8Z"/><path d="M14 3.8V8h4.2"/><path d="M8.5 12h7M8.5 15h7M8.5 18h4.5"/></svg>`;
}

function shareRows(shares) {
  return (shares || []).map((s) => {
    const link = `${location.origin}/pan/${encodeURIComponent(s.token)}`;
    const access = s.hasPassword ? s.password || "Password" : "Public";
    const pathsText = (s.paths || []).join(", ");
    const copyCode = s.password ? ` data-copy-code="${escapeHtml(s.password)}"` : "";
    return `<tr class="${s.expired ? "row-muted" : ""}"><td class="share-name-cell" title="${escapeHtml(s.name)}${pathsText ? ` · ${escapeHtml(pathsText)}` : ""}"><div class="primary-line">${escapeHtml(s.name)}</div><div class="secondary-line">${(s.paths || []).map(escapeHtml).join(", ")}</div></td><td class="share-link-cell" title="${escapeHtml(link)}"><a href="${link}" target="_blank">${escapeHtml(link)}</a></td><td>${shareExpiry(s)}</td><td${s.password ? ` title="Extraction code"` : ""}>${escapeHtml(access)}</td><td class="actions"><button class="ghost" data-copy-link="${escapeHtml(link)}"${copyCode}>Copy</button><button class="icon danger" data-share-del="${escapeHtml(s.token)}">Del</button></td></tr>`;
  }).join("") || `<tr class="empty-row"><td colspan="5">No external links.</td></tr>`;
}

function adminShareRows(shares) {
  return (shares || []).map((s) => {
    const link = `${location.origin}/pan/${encodeURIComponent(s.token)}`;
    const access = s.hasPassword ? s.password || "Password" : "Public";
    const pathsText = (s.paths || []).join(", ");
    return `<tr class="${s.expired ? "row-muted" : ""}"><td><input type="checkbox" data-admin-share-token value="${escapeHtml(s.token)}"></td><td>${escapeHtml(displayNameForUsername(s.owner) || s.ownerId)}</td><td class="share-name-cell" title="${escapeHtml(s.name)}${pathsText ? ` · ${escapeHtml(pathsText)}` : ""}"><div class="primary-line">${escapeHtml(s.name)}</div><div class="secondary-line">${(s.paths || []).map(escapeHtml).join(", ")}</div></td><td class="share-link-cell" title="${escapeHtml(link)}"><a href="${link}" target="_blank">${escapeHtml(link)}</a></td><td>${shareExpiry(s)}</td><td${s.password ? ` title="Extraction code"` : ""}>${escapeHtml(access)}</td></tr>`;
  }).join("") || `<tr class="empty-row"><td colspan="6">No external links.</td></tr>`;
}

function shareExpiry(s) {
  if (s.permanent) return `<span class="badge badge-accent">Permanent</span>`;
  if (s.expired) return `<span class="badge badge-danger">Expired</span>`;
  return escapeHtml(s.expiresAt ? new Date(s.expiresAt).toLocaleString() : "7 days");
}

async function renameItem(path, oldName) {
  const next = prompt("New name", oldName);
  if (!next || next === oldName) return;
  const parts = path.split("/");
  parts.pop();
  const to = joinPath(parts.join("/"), next);
  const endpoint = isBackupMode() ? "/api/netdisk/backup/rename" : "/api/netdisk/rename";
  await api(endpoint, { method: "POST", body: JSON.stringify({ from: path, to }) });
  toast("Renamed", true);
  renderNetdisk();
}

// ---------- Share modal (Baidu-style) ----------
//
// State of the open share modal. Cleared on close. `step` is "form" while the
// user picks expiry/password and "link" once the share has been created and the
// link is shown inline.
let shareModal = null;

function randomCode(len = 4) {
  const chars = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"; // no I, O, 0, 1 — legibility
  let out = "";
  for (let i = 0; i < len; i++) out += chars[Math.floor(Math.random() * chars.length)];
  return out;
}

function openShareModal(paths, name) {
  if (isBackupMode()) {
    toast("Backup disk does not support sharing");
    return;
  }
  shareModal = {
    paths,
    name,
    expiry: 7, // days; 0 = permanent
    usePassword: true,
    code: randomCode(4),
    step: "form",
    creating: false,
    created: null,
  };
  renderShareModal();
}

function renderShareModal() {
  if (!shareModal) return;
  showModalNoShell(
    "netdisk-share",
    "share-modal",
    shareModal.step === "form" ? shareFormHtml() : shareLinkHtml(),
  );
  bindShareModalEvents();
}

function shareFormHtml() {
  const s = shareModal;
  const expiryOptions = [
    { d: 1, label: "1 day" },
    { d: 7, label: "7 days" },
    { d: 30, label: "30 days" },
    { d: 0, label: "Permanent" },
  ];
  const expiryButtons = expiryOptions
    .map((o) => `<button class="share-seg-btn${s.expiry === o.d ? " active" : ""}" data-expiry="${o.d}">${escapeHtml(o.label)}</button>`)
    .join("");
  const oneItem = s.paths.length === 1;
  return (
    `<div class="modal-head">` +
      `<div class="share-head-text"><h2>Share ${oneItem ? "file" : "items"}</h2>` +
      `<p class="hint share-name-line" title="${escapeHtml(s.name)}">${escapeHtml(s.name)}</p></div>` +
      `<button class="icon" data-close>✕</button>` +
    `</div>` +
    `<div class="modal-body share-form">` +
      `<div class="share-row">` +
        `<div class="share-row-label">Valid for</div>` +
        `<div class="share-seg">${expiryButtons}</div>` +
      `</div>` +
      `<div class="share-row">` +
        `<div class="share-row-label">Extraction code</div>` +
        `<div class="share-row-body share-code-row">` +
          `<label class="check"><input type="checkbox" id="shareUseCode" ${s.usePassword ? "checked" : ""}><span>Protect with code</span></label>` +
          `<div class="share-code-field ${s.usePassword ? "" : "is-hidden"}">` +
            `<input id="shareCode" value="${escapeHtml(s.code)}" maxlength="8" autocomplete="off">` +
            `<button class="ghost" id="shareRandomCode" type="button">Random</button>` +
          `</div>` +
        `</div>` +
      `</div>` +
    `</div>` +
    `<div class="modal-foot">` +
      `<button class="ghost" data-close>Cancel</button>` +
      `<button class="primary" id="shareCreate"${s.creating ? " disabled" : ""}>${s.creating ? "Creating…" : "Create link"}</button>` +
    `</div>`
  );
}

function shareLinkHtml() {
  const s = shareModal;
  const link = `${location.origin}/pan/${encodeURIComponent(s.created.token)}`;
  return (
    `<div class="modal-head">` +
      `<div class="share-head-text"><h2>Share link ready</h2>` +
      `<p class="hint share-name-line" title="${escapeHtml(s.name)}">${escapeHtml(s.name)}</p></div>` +
      `<button class="icon" data-close>✕</button>` +
    `</div>` +
    `<div class="modal-body share-result">` +
      `<div class="share-link-row">` +
        `<div class="share-row-label">Link</div>` +
        `<div class="copy-chip share-copy-chip" title="${escapeHtml(link)}">` +
          `<code>${escapeHtml(link)}</code>` +
          `<button class="copy-btn" data-copy="${escapeHtml(link)}" data-copy-target="link" type="button">Copy</button>` +
        `</div>` +
      `</div>` +
      (s.usePassword
        ? `<div class="share-link-row">` +
          `<div class="share-row-label">Code</div>` +
          `<div class="copy-chip share-copy-chip">` +
            `<code>${escapeHtml(s.code)}</code>` +
            `<button class="copy-btn" data-copy="${escapeHtml(s.code)}" data-copy-target="code" type="button">Copy</button>` +
          `</div>` +
        `</div>`
        : "") +
      `<button class="subtle share-copy-all" id="shareCopyAll" type="button">Copy link${s.usePassword ? " & code" : ""}</button>` +
    `</div>` +
    `<div class="modal-foot">` +
      `<button class="ghost" data-close>Close</button>` +
      `<button class="primary" id="shareDone" type="button">Done</button>` +
    `</div>`
  );
}

function bindShareModalEvents() {
  if (!shareModal) return;
  if (shareModal.step === "form") {
    document.querySelectorAll("[data-expiry]").forEach((btn) => {
      btn.onclick = () => {
        shareModal.expiry = parseInt(btn.dataset.expiry, 10) || 0;
        renderShareModal();
      };
    });
    $("#shareUseCode").onchange = (e) => {
      shareModal.usePassword = e.target.checked;
      if (shareModal.usePassword && !shareModal.code) shareModal.code = randomCode(4);
      renderShareModal();
    };
    $("#shareCode").oninput = (e) => { shareModal.code = e.target.value; };
    $("#shareRandomCode").onclick = () => {
      shareModal.code = randomCode(4);
      renderShareModal();
    };
    $("#shareCreate").onclick = submitShare;
  } else {
    // link step
    document.querySelectorAll("[data-copy]").forEach((btn) => {
      btn.onclick = async () => {
        await navigator.clipboard.writeText(btn.dataset.copy);
        flashCopy(btn);
      };
    });
    $("#shareCopyAll").onclick = async (btn) => {
      const s = shareModal;
      const link = `${location.origin}/pan/${encodeURIComponent(s.created.token)}`;
      const text = s.usePassword ? `${link}\nExtraction code: ${s.code}` : link;
      await navigator.clipboard.writeText(text);
      flashCopy(btn);
    };
    $("#shareDone").onclick = () => { closeModal(); renderNetdisk(); };
  }
}

function flashCopy(btn) {
  const prev = btn.textContent;
  btn.textContent = "Copied";
  btn.classList.add("copied");
  setTimeout(() => { btn.textContent = prev; btn.classList.remove("copied"); }, 1200);
}

async function submitShare() {
  if (!shareModal || shareModal.creating) return;
  shareModal.creating = true;
  renderShareModal();
  const s = shareModal;
  const body = { paths: s.paths, name: s.name };
  if (s.expiry > 0) body.expiresDays = s.expiry;
  else body.permanent = true;
  if (s.usePassword && s.code.trim()) body.password = s.code.trim();
  try {
    const out = await api("/api/netdisk/share", { method: "POST", body: JSON.stringify(body) });
    shareModal.created = out;
    shareModal.step = "link";
    shareModal.creating = false;
    renderShareModal();
  } catch (err) {
    shareModal.creating = false;
    toast(err.message);
    renderShareModal();
  }
}

// Guard against concurrent uploads: each file is its own request but the
// overlay is shared, so a second pick while one is running would race it.
let uploading = false;

// uploadFilesHandler uploads a batch of files to the netdisk (never the backup
// disk — uploads are netdisk-only). Renamed from uploadFiles so the bindEvents
// scope can use a local `uploadFiles` DOM handle without colliding.
async function uploadFilesHandler(files, path) {
  if (!files || !files.length) return;
  if (!canMutate()) return toast("Read-only account cannot upload");
  if (uploading) return toast("An upload is already in progress");
  const list = [...files];
  uploading = true;
  const overlay = showUploadOverlay(list);

  // Per-file loaded bytes for the aggregate bar; total = sum of file sizes.
  const loaded = new Array(list.length).fill(0);
  const totalBytes = list.reduce((s, f) => s + (f.size || 0), 0);
  let doneBytes = 0;
  let ok = 0;
  let failed = 0;
  let batchStart = performance.now();

  for (let i = 0; i < list.length; i++) {
    overlay.setLabel(`Uploading ${i + 1}/${list.length}…`);
    overlay.setStatus(i, "uploading");
    const f = list[i];
    const fd = new FormData();
    fd.append("path", path);
    // webkitdirectory supplies webkitRelativePath; fall back to name.
    fd.append("files", f, f.webkitRelativePath || f.name);
    try {
      await uploadWithProgress("/api/netdisk/upload", fd, {
        csrfToken: readCSRFCookie() || state.csrfToken || "",
        onProgress: (p) => {
          doneBytes += Math.max(0, p.loaded - loaded[i]);
          loaded[i] = p.loaded;
          overlay.updateFile(i, p);
          const elapsed = (performance.now() - batchStart) / 1000;
          const speedBps = elapsed > 0 ? doneBytes / elapsed : 0;
          const percent = totalBytes > 0 ? Math.min(100, Math.round((doneBytes / totalBytes) * 100)) : 0;
          const etaSec = speedBps > 0 ? (totalBytes - doneBytes) / speedBps : 0;
          overlay.updateOverall({ percent, loaded: doneBytes, total: totalBytes, speedBps, etaSec });
        },
      });
      overlay.setStatus(i, "done");
      ok++;
    } catch (err) {
      overlay.setStatus(i, "error", err.message);
      failed++;
    }
  }

  overlay.setLabel(`Uploaded ${ok} of ${list.length} file(s)`);
  const summary = failed
    ? `Uploaded ${ok} file(s), ${failed} failed`
    : `Uploaded ${ok} file(s)`;
  toast(summary, !failed);
  setCurrentSelection(state.netdisk.selected || new Set());
  renderNetdisk();
  // Let the user see the final per-file state before the card disappears.
  setTimeout(() => overlay.close(), failed ? 3000 : 1500);
  uploading = false;
}

function quotaBar(q) {
  if (!q) return `Used <strong>0 B</strong>`;
  const used = q.usedBytes || 0;
  const estimating = q.usedEstimating ? ` <span class="hint">(estimating, updates in background)</span>` : "";
  if (q.totalBytes > 0) {
    const pct = Math.min(100, Math.round((used / q.totalBytes) * 100));
    return `Used <strong>${fmtBytes(used)}</strong> / ${fmtBytes(q.totalBytes)} <span class="quota-bar"><span style="width:${pct}%"></span></span> ${pct}%${estimating}`;
  }
  if (q.diskFreeBytes != null) {
    return `Used <strong>${fmtBytes(used)}</strong> · Free ${fmtBytes(q.diskFreeBytes)}${estimating}`;
  }
  return `Used <strong>${fmtBytes(used)}</strong>${estimating}`;
}

function joinPath(a, b) {
  return [a, b].filter(Boolean).join("/");
}

// Compact "date HH:MM" for the Modified column — full timestamp stays
// available via the cell's title attribute.
function fmtDate(ts) {
  const d = new Date(ts);
  if (Number.isNaN(d.getTime())) return "-";
  const pad = (n) => String(n).padStart(2, "0");
  return `${d.toLocaleDateString()} ${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

function downloadURL(path) {
  return `/api/netdisk/download?path=${encodeURIComponent(path)}&ts=${Date.now()}`;
}

function backupDownloadURL(path) {
  return `/api/netdisk/backup/download?path=${encodeURIComponent(path)}&ts=${Date.now()}`;
}

function rawURL(path) {
  return `/api/netdisk/raw?path=${encodeURIComponent(path)}&ts=${Date.now()}`;
}

function backupRawURL(path) {
  return `/api/netdisk/backup/raw?path=${encodeURIComponent(path)}&ts=${Date.now()}`;
}

const TEXT_PREVIEW_EXTS = new Set([
  "txt", "log", "json", "csv", "tsv", "ini", "conf", "cfg", "yml", "yaml", "toml",
  "xml", "html", "htm", "css", "js", "mjs", "ts", "go", "py", "rb", "java", "c",
  "h", "cpp", "hpp", "cc", "sh", "bash", "zsh", "sql", "env", "gitignore",
]);

// previewKind maps a file name to a preview category the viewer understands, or
// null when the type cannot be previewed (the row then keeps its download link).
function previewKind(name) {
  const ext = (name.split(".").pop() || "").toLowerCase();
  if (!ext) return null;
  if (ext === "pdf") return "pdf";
  if (ext === "md" || ext === "markdown") return "markdown";
  if (["png", "jpg", "jpeg", "gif", "webp", "bmp", "svg"].includes(ext)) return "image";
  if (["mp3", "wav", "ogg", "m4a", "flac"].includes(ext)) return "audio";
  if (["mp4", "webm", "m4v", "mov"].includes(ext)) return "video";
  if (ext === "yuv") return "yuv";
  if (TEXT_PREVIEW_EXTS.has(ext)) return "text";
  return null;
}

// ---------- File preview viewer ----------
//
// Renders a single file inline inside a modal. PDF is rendered by pdf.js, text
// and markdown are fetched and shown in a <pre> / formatted block, and media
// types use the native <img>/<audio>/<video> elements pointing at the raw URL.

function openFileViewer(path, fromBackup) {
  const name = path.split("/").pop() || path;
  const kind = previewKind(name);
  const dl = fromBackup ? backupDownloadURL(path) : downloadURL(path);
  const raw = fromBackup ? backupRawURL(path) : rawURL(path);
  if (!kind) {
    // Unsupported type: prompt the user with a small dialog offering a direct
    // download rather than silently doing nothing.
    showUnsupportedPreviewModal(name, dl);
    return;
  }
  const supportsFullscreen = kind === "pdf" || kind === "video" || kind === "image" || kind === "yuv";
  showModalNoShell(
    "netdisk-viewer",
    "wide viewer-modal",
    `<div class="modal-head">` +
      `<div class="viewer-head-text"><h2>${escapeHtml(name)}</h2></div>` +
      `<div class="modal-tools">` +
        (supportsFullscreen ? `<button class="ghost" id="viewerFullscreen" title="Toggle fullscreen">⛶ Fullscreen</button>` : ``) +
        `<a class="ghost" href="${dl}" title="Download">⬇ Download</a>` +
        `<button class="icon" data-close>✕</button>` +
      `</div>` +
    `</div>` +
    `<div class="modal-body viewer-body" id="viewerBody"><div class="viewer-loading">Loading…</div></div>`,
  );
  const fsBtn = document.getElementById("viewerFullscreen");
  if (fsBtn) fsBtn.onclick = () => toggleViewerFullscreen();
  // Drop the cached PDF document + pause media when the modal closes so the
  // next preview starts fresh and audio/video doesn't keep playing in the bg.
  const backdrop = document.querySelector(".modal-backdrop.netdisk-viewer");
  if (backdrop) onModalClose(backdrop, resetViewerState);
  renderViewerContent(kind, path, name, raw).catch((err) => {
    const body = document.getElementById("viewerBody");
    if (body) body.innerHTML = `<div class="viewer-error">${escapeHtml(err.message || "Failed to load file")}</div>`;
  });
}

// showUnsupportedPreviewModal renders a small centered dialog explaining the
// file can't be previewed and offering a one-click download.
function showUnsupportedPreviewModal(name, href) {
  showModalNoShell(
    "netdisk-viewer unsupported-viewer",
    "viewer-modal",
    `<div class="modal-head">` +
      `<div class="viewer-head-text"><h2>${escapeHtml(name)}</h2></div>` +
      `<button class="icon" data-close>✕</button>` +
    `</div>` +
    `<div class="modal-body viewer-body">` +
      `<div class="viewer-unsupported">` +
        `<div class="viewer-unsupported-icon" aria-hidden="true">📄</div>` +
        `<p>Preview is not available for this file type yet.</p>` +
        `<p class="hint">You can download it to view locally.</p>` +
        `<a class="primary" href="${href}" download>⬇ Download</a>` +
      `</div>` +
    `</div>`,
  );
}

// toggleViewerFullscreen flips a class on the modal backdrop so the viewer
// occupies the whole viewport. PDF re-renders crisply at the new size.
function toggleViewerFullscreen() {
  const backdrop = document.querySelector(".modal-backdrop.netdisk-viewer");
  if (!backdrop) return;
  const isFs = backdrop.classList.toggle("viewer-fullscreen");
  const fsBtn = document.getElementById("viewerFullscreen");
  if (fsBtn) fsBtn.textContent = isFs ? "⛶ Exit fullscreen" : "⛶ Fullscreen";
  // For <video>, prefer the native Fullscreen API so controls stay usable.
  if (!isFs && document.fullscreenElement) {
    document.exitFullscreen?.().catch(() => {});
  }
  const media = backdrop.querySelector("video.viewer-media, img.viewer-media");
  if (media && media.requestFullscreen) {
    if (isFs) media.requestFullscreen?.().catch(() => {});
  }
  // Re-render PDF pages to the new container width for crisp output.
  if (backdrop.querySelector("#pdfPages")) {
    rerenderPdf();
  }
  // Re-paint the YUV frame so the canvas adapts to the new viewport.
  if (yuvController) {
    yuvController.repaint();
  }
}

async function renderViewerContent(kind, path, name, url) {
  const body = document.getElementById("viewerBody");
  if (!body) return; // modal closed mid-load
  switch (kind) {
    case "pdf":
      return renderPdf(url, body);
    case "markdown":
      return renderMarkdown(url, body);
    case "text":
      return renderText(url, body);
    case "image":
      body.innerHTML = `<img class="viewer-media" src="${url}" alt="${escapeHtml(name)}" />`;
      return;
    case "audio":
      body.innerHTML = `<audio class="viewer-media" controls preload="metadata" src="${url}"></audio>`;
      return;
    case "video":
      body.innerHTML = `<video class="viewer-media" controls preload="metadata" src="${url}"></video>`;
      return;
    case "yuv":
      return renderYuv(url, body, name);
  }
}

// pdfRenderState holds the current document + viewport scale so a fullscreen
// toggle can re-render every page at the new container width without
// re-downloading the PDF.
let pdfRenderState = null;

// yuvController holds the active YUV viewer's control handle so a fullscreen
// toggle can repaint the frame and resetViewerState can cancel its fetch.
let yuvController = null;

async function renderPdf(url, body) {
  const pdfjs = window.pdfjsLib;
  if (!pdfjs) {
    body.innerHTML = `<div class="viewer-error">PDF viewer failed to load. Try refreshing the page.</div>`;
    return;
  }
  pdfjs.workerSrc = "/vendor/pdf.worker.min.js";
  body.innerHTML = `<div class="viewer-loading">Rendering PDF…</div>`;
  const pdf = await pdfjs.getDocument({ url }).promise;
  pdfRenderState = { pdf, url };
  await paintPdfPages(body);
}

// paintPdfPages lays out each PDF page in a vertical scroll container. The
// canvas is drawn at device-pixel resolution (CSS pixels × devicePixelRatio)
// and then sized down via CSS, which keeps text crisp on Retina/4K displays
// instead of the blurry 1× scaling the default viewport produces.
async function paintPdfPages(body) {
  const { pdf } = pdfRenderState || {};
  if (!pdf) return;
  const wrap = body;
  wrap.innerHTML = `<div class="viewer-canvas-wrap" id="pdfPages"></div>`;
  const pages = document.getElementById("pdfPages");
  // Fit one page to the container width; clamp so tiny documents stay readable.
  const maxWidth = Math.max(360, pages.clientWidth || 820);
  for (let i = 1; i <= pdf.numPages; i++) {
    if (!pages.isConnected) return; // viewer closed mid-render
    const page = await pdf.getPage(i);
    const baseViewport = page.getViewport({ scale: 1 });
    const scale = Math.max(1, maxWidth / baseViewport.width);
    const viewport = page.getViewport({ scale });
    const dpr = window.devicePixelRatio || 1;
    const canvas = document.createElement("canvas");
    canvas.className = "viewer-page";
    // Backing store at device pixels for crispness; CSS size keeps layout.
    canvas.width = Math.floor(viewport.width * dpr);
    canvas.height = Math.floor(viewport.height * dpr);
    canvas.style.width = `${Math.floor(viewport.width)}px`;
    canvas.style.height = `${Math.floor(viewport.height)}px`;
    pages.appendChild(canvas);
    const ctx = canvas.getContext("2d");
    ctx.scale(dpr, dpr);
    await page.render({ canvasContext: ctx, viewport }).promise;
  }
}

// rerenderPdf re-paints the current PDF at the new container size after a
// fullscreen toggle (or window resize). Cheap because the document is cached.
async function rerenderPdf() {
  const body = document.getElementById("viewerBody");
  if (!body || !pdfRenderState) return;
  await paintPdfPages(body);
}

async function renderMarkdown(url, body) {
  const res = await fetch(url, { credentials: "same-origin" });
  const text = await res.text();
  if (!body.isConnected) return;
  const marked = window.marked;
  if (!marked || typeof marked.parse !== "function") {
    // Fallback: show as plain text.
    body.innerHTML = `<pre class="viewer-text"></pre>`;
    body.querySelector("pre").textContent = text;
    return;
  }
  // Render into a sandboxed node via innerHTML, then rebind nothing (no scripts
  // execute from innerHTML assignment, and marked escapes inline code/markup).
  body.innerHTML = `<div class="viewer-markdown md-body"></div>`;
  body.querySelector(".viewer-markdown").innerHTML = marked.parse(text);
}

async function renderText(url, body) {
  const res = await fetch(url, { credentials: "same-origin" });
  const len = Number(res.headers.get("Content-Length") || 0);
  if (len > 2 * 1024 * 1024) {
    body.innerHTML = `<div class="viewer-error">This file is larger than 2 MB. Please download it to view.</div>`;
    return;
  }
  const text = await res.text();
  if (!body.isConnected) return;
  body.innerHTML = `<pre class="viewer-text"></pre>`;
  body.querySelector("pre").textContent = text;
}

// renderYuv builds the YUV viewer. The returned controller is stashed in
// yuvController so toggleViewerFullscreen can ask it to repaint at the new
// container size and resetViewerState can cancel its in-flight fetch on close.
function renderYuv(url, body, name) {
  body.innerHTML = `<div class="viewer-loading">Loading YUV…</div>`;
  yuvController = openYuvViewer({ name, url, bodyEl: body });
}

// resetViewerState releases the cached PDF document and stops any media
// playback. Registered as the modal teardown callback so it runs on close,
// backdrop click, and Esc.
function resetViewerState() {
  pdfRenderState = null;
  if (yuvController) {
    yuvController.destroy();
    yuvController = null;
  }
  const media = document.querySelector(".netdisk-viewer video.viewer-media, .netdisk-viewer audio.viewer-media");
  if (media) {
    try { media.pause(); } catch { /* best-effort */ }
  }
  if (document.fullscreenElement) {
    document.exitFullscreen?.().catch(() => {});
  }
}

function $(selector) {
  return document.querySelector(selector);
}
