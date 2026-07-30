import { state, api, toast, escapeHtml, fmtBytes, isAdmin, canMutate, displayNameForUsername, readCSRFCookie, t } from "../app.js";
import { showModalNoShell, closeModal, onModalClose } from "./ui.js";
import { uploadWithProgress, showUploadOverlay } from "../lib/upload.js";
import { makeFileIterator, prependIterator, makeDropIterator } from "../lib/uploadStream.js";
import { hashFileCRC32 } from "../lib/hashfile.js";
import { uploadLargeFile } from "../lib/chunkupload.js";
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
  if (!msg) return t("netdisk.backupUnavailable");
  if (msg.toLowerCase().includes("not configured")) return msg;
  return t("netdisk.backupUnavailableMsg", { msg });
}

export async function renderNetdisk() {
  restoreModeState(netdiskMode);
  const tabAtEntry = state.tab;
  // Only the first load shows a placeholder; background refreshes keep the
  // current content until the new data arrives (no white flash).
  const firstLoad = !$("#netdiskCard");
  if (firstLoad) {
    $("#view").innerHTML = `<div class="card"><div class="card-body"><p class="hint">${t("netdisk.loadingFiles")}</p></div></div>`;
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
              `<button class="netdisk-mode-btn${backup ? "" : " active"}" data-mode="netdisk" role="tab" aria-selected="${backup ? "false" : "true"}">${t("netdisk.modeNetdisk")}</button>` +
              `<button class="netdisk-mode-btn${backup ? " active" : ""}" data-mode="backup" role="tab" aria-selected="${backup ? "true" : "false"}">${t("netdisk.modeBackup")}</button>` +
            `</div>` +
            `<span class="netdisk-count">${t("netdisk.counts", { folders: folderCount, files: fileCount })}</span>` +
          `</div>` +
          `<div class="head-tools netdisk-actions">` +
            `<button class="ghost" id="upDir">${t("netdisk.up")}</button>` +
            `<button class="ghost" id="mkdirBtn">${t("netdisk.newFolder")}</button>` +
            // Upload and Folder-upload are netdisk-only: a backup disk is a
            // mirror target, not a place to drop arbitrary new files.
            (backup ? "" : `<label class="buttonlike"><input id="uploadFiles" type="file" multiple> ${t("netdisk.upload")}</label>`) +
            (backup ? "" : `<label class="buttonlike"><input id="uploadFolder" type="file" webkitdirectory directory multiple> ${t("netdisk.folder")}</label>`) +
            (mutable
              ? `<button class="ghost danger" id="batchDelete" ${selectionSize ? "" : "disabled"}>${t("netdisk.deleteN", { n: selectionSize })}</button>` +
                `<button class="ghost" id="batchCopy" ${selectionSize ? "" : "disabled"}>${t("netdisk.copyN", { n: selectionSize })}</button>` +
                `<button class="ghost" id="batchMove" ${selectionSize ? "" : "disabled"}>${t("netdisk.moveN", { n: selectionSize })}</button>` +
                // Share and Backup-to-backup don't apply on the backup disk.
                (backup ? "" : `<button class="ghost" id="batchShare" ${selectionSize ? "" : "disabled"}>${t("netdisk.shareN", { n: selectionSize })}</button>`) +
                `<button class="ghost" id="batchDownload" ${selectionSize ? "" : "disabled"}>${t("netdisk.downloadN", { n: selectionSize })}</button>`
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
          `<th>${t("common.name")}</th><th class="size-col">${t("common.size")}</th><th class="time-col">${t("netdisk.colModified")}</th><th class="actions">${t("common.actions")}</th>` +
        `</tr></thead><tbody>${rows}</tbody></table>` +
      `</div>` +
      // External Links cards are netdisk-only — backup files can't be shared.
      (backup ? "" :
        `<div class="card"><div class="card-head"><h2>${t("netdisk.externalLinks")}</h2></div>` +
        `<table class="data netdisk-shares"><thead><tr><th class="share-name-col">${t("common.name")}</th><th class="share-link-col">${t("netdisk.colLink")}</th><th class="share-expires-col">${t("netdisk.colExpires")}</th><th class="share-access-col">${t("netdisk.colAccess")}</th><th class="actions">${t("common.actions")}</th></tr></thead><tbody>${shareRows(state.netdisk.shares || [])}</tbody></table></div>` +
        (isAdmin() ? `<div class="card"><div class="card-head"><h2>${t("netdisk.allExternalLinks")}</h2><div class="head-tools"><label class="check compact-check" for="selectAllShares"><input type="checkbox" id="selectAllShares"><span>${t("netdisk.selectAll")}</span></label><button class="danger" id="deleteSelectedShares">${t("netdisk.deleteSelected")}</button></div></div>` +
        `<table class="data netdisk-admin-shares"><thead><tr><th class="chk-col"></th><th class="share-owner-col">${t("netdisk.colOwner")}</th><th class="share-name-col">${t("common.name")}</th><th class="share-link-col">${t("netdisk.colLink")}</th><th class="share-expires-col">${t("netdisk.colExpires")}</th><th class="share-access-col">${t("netdisk.colAccess")}</th></tr></thead><tbody>${adminShareRows(state.netdisk.adminShares || [])}</tbody></table></div>` : "")
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
    const name = prompt(t("netdisk.folderNamePrompt"));
    if (!name) return;
    await api(`${base}/mkdir`, { method: "POST", body: JSON.stringify({ path: joinPath(state.netdisk.path, name) }) });
    toast(t("netdisk.folderCreated"), true);
    renderNetdisk();
  };

  // Upload labels are only rendered in netdisk mode; guard so a missing element
  // (backup mode) doesn't throw.
  const uploadFiles = $("#uploadFiles");
  if (uploadFiles) {
    uploadFiles.onchange = async (e) => {
      // Plain multi-file pick: each File has no relative path, so it lands flat
      // in the current directory. The FileList is iterated lazily by the queue
      // (no giant array is built), which keeps large picks light.
      await uploadFilesHandler(makeFileIterator([...e.target.files]), state.netdisk.path || "");
      e.target.value = "";
    };
  }
  const uploadFolder = $("#uploadFolder");
  if (uploadFolder) {
    uploadFolder.onchange = async (e) => {
      // webkitdirectory populates webkitRelativePath as "TopFolder/sub/.../file",
      // which the backend reconstructs into nested directories. Keep the top
      // folder name so the selected folder appears as a subfolder of the current
      // directory (matching drag-drop and most file-manager conventions). The
      // FileList is iterated lazily by the queue so a folder with many files
      // never materializes one big array.
      await uploadFilesHandler(makeFileIterator([...e.target.files]), state.netdisk.path || "");
      e.target.value = "";
    };
  }

  // Drag-drop upload is netdisk-only. Dragged folders are not exposed via
  // dataTransfer.files with usable paths (webkitRelativePath is empty for drops
  // and deep subfolders are often missing entirely), so walk the drop tree via
  // the FileSystemEntry API to recover the folder structure. The walker streams
  // into a bounded buffer the queue drains, so traversal and upload run in
  // parallel and only ~one batch of files is held in memory at a time.
  if (!backup) {
    const card = $("#netdiskCard");
    card.ondragover = (e) => { e.preventDefault(); card.classList.add("drag-over"); };
    card.ondragleave = () => card.classList.remove("drag-over");
    card.ondrop = async (e) => {
      e.preventDefault();
      card.classList.remove("drag-over");
      if (!mutable) return toast(t("netdisk.readonlyUpload"));
      const items = e.dataTransfer?.items;
      if (!items || !items.length) return;
      await uploadFilesHandler(dropStream(items), state.netdisk.path || "");
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
        if (!confirm(t("netdisk.deleteConfirmOne", { name: btn.dataset.name }))) return;
        await api(`${base}/delete`, { method: "POST", body: JSON.stringify({ paths: [btn.dataset.del] }) });
        toast(t("netdisk.deleted"), true);
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
      const text = code ? `${btn.dataset.copyLink}\n${t("netdisk.extractionCode")}: ${code}` : btn.dataset.copyLink;
      await navigator.clipboard.writeText(text);
      toast(code ? t("netdisk.linkCodeCopied") : t("netdisk.linkCopied"), true);
    };
  });
  document.querySelectorAll("[data-share-del]").forEach((btn) => {
    btn.onclick = async () => {
      await api("/api/netdisk/share/delete", { method: "POST", body: JSON.stringify({ token: btn.dataset.shareDel }) });
      toast(t("netdisk.externalLinkDeleted"), true);
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
      if (!tokens.length) return toast(t("netdisk.selectLink"));
      if (!confirm(t("netdisk.deleteLinksConfirm", { n: tokens.length }))) return;
      await api("/api/admin/netdisk/shares/delete", { method: "POST", body: JSON.stringify({ tokens }) });
      toast(t("netdisk.externalLinksDeleted"), true);
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
      ? `<button class="linklike netdisk-name-link" data-view="${escapeHtml(f.path)}" data-backup="${backup ? "1" : ""}" title="${t("netdisk.preview")}">${escapeHtml(f.name)}</button>`
      : `<a class="netdisk-name-link" href="${href}">${escapeHtml(f.name)}</a>`;
  const checkCell = mutable
    ? `<td class="chk-col"><input type="checkbox" class="netdisk-row-check" data-path="${escapeHtml(f.path)}" ${checked}></td>`
    : "";
  // On the backup disk we drop Share (no sharing from backup) and the
  // Backup-to-backup button (it's already there). Everything else — download,
  // rename, copy/move within the disk, delete — stays for full management.
  const actionCell = mutable
    ? `<a class="icon" href="${href}" title="${t("netdisk.download")}">⬇</a>` +
      `<button class="icon" title="${t("netdisk.rename")}" data-ren="${escapeHtml(f.path)}" data-name="${escapeHtml(f.name)}">✎</button>` +
      `<button class="icon" title="${t("netdisk.copy")}" data-copy="${escapeHtml(f.path)}">⧉</button>` +
      `<button class="icon" title="${t("netdisk.move")}" data-move="${escapeHtml(f.path)}">↗</button>` +
      (backup ? "" : `<button class="icon" title="${t("netdisk.share")}" data-share="${escapeHtml(f.path)}" data-name="${escapeHtml(f.name)}">⤴</button>`) +
      `<button class="icon danger" title="${t("common.delete")}" data-del="${escapeHtml(f.path)}" data-name="${escapeHtml(f.name)}">✕</button>`
    : `<a class="icon" href="${href}" title="${t("netdisk.download")}">⬇</a>`;
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
  if (!confirm(t("netdisk.batchDeleteConfirm", { n: paths.length }))) return;
  const endpoint = isBackupMode() ? "/api/netdisk/backup/delete" : "/api/netdisk/delete";
  await api(endpoint, { method: "POST", body: JSON.stringify({ paths }) });
  clearCurrentSelection();
  toast(t("netdisk.deleted"), true);
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
  const action = move ? t("netdisk.move") : t("netdisk.copy");
  const diskLabel = sourceDisk === "backup" ? t("netdisk.backupDisk") : t("netdisk.myNetdisk");
  const netdiskActive = sourceDisk === "netdisk" ? " active" : "";
  const backupActive = sourceDisk === "backup" ? " active" : "";
  showModalNoShell(
    "netdisk-picker",
    "wide picker-modal",
    `<div class="modal-head">` +
      `<div><h2>${t("netdisk.pickTitle", { action, n: paths.length })}</h2><p class="hint" style="margin-top:4px">${t("netdisk.pickHint", { disk: diskLabel.toLowerCase() })}</p></div>` +
      `<button class="icon" data-close>✕</button>` +
    `</div>` +
    `<div class="picker-layout">` +
      `<aside class="picker-tree">` +
        `<button class="picker-tree-item picker-disk${netdiskActive}" data-picker-disk="netdisk" type="button">${t("netdisk.myNetdisk")}</button>` +
        `<button class="picker-tree-item picker-disk${backupActive}" data-picker-disk="backup" type="button">${t("netdisk.backupDisk")}</button>` +
      `</aside>` +
      `<section class="picker-main">` +
        `<div class="picker-toolbar">` +
          `<button class="ghost" id="pickerUp">${t("netdisk.pickUp")}</button>` +
          `<div class="picker-path" id="pickerPath">/</div>` +
          `<button class="ghost" id="pickerMkdir">${t("netdisk.pickNewFolder")}</button>` +
        `</div>` +
        `<div class="picker-list" id="pickerList"><div class="picker-empty">${t("netdisk.pickLoading")}</div></div>` +
      `</section>` +
    `</div>` +
    `<div class="modal-foot">` +
      `<span class="hint" id="pickerHint"></span>` +
      `<div class="modal-tools">` +
        `<button class="ghost" data-close>${t("netdisk.pickCancel")}</button>` +
        `<button class="primary" id="pickerConfirm">${t("netdisk.pickConfirm", { action })}</button>` +
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
  listEl.innerHTML = `<div class="picker-empty">${t("netdisk.pickLoading")}</div>`;
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
  listEl.innerHTML = rows || `<div class="picker-empty">${t("netdisk.pickNoSubfolders")}</div>`;
  listEl.querySelectorAll("[data-open]").forEach((row) => {
    row.onclick = () => loadPickerFolder(row.dataset.open);
  });

  // Moving items onto their current location is a no-op; block it.
  const alreadyHere = folderPicker.move && folderPicker.paths.every((p) => parentDir(p) === path);
  $("#pickerConfirm").disabled = alreadyHere;
  $("#pickerHint").textContent = alreadyHere ? t("netdisk.pickAlreadyHere") : t("netdisk.pickDest", { path });
}

async function pickerMkdirFolder() {
  const name = prompt(t("netdisk.newFolderNamePrompt"));
  if (!name || !folderPicker) return;
  try {
    const endpoint = folderPicker.targetDisk === "backup" ? "/api/netdisk/backup/mkdir" : "/api/netdisk/mkdir";
    await api(endpoint, { method: "POST", body: JSON.stringify({ path: joinPath(folderPicker.path, name) }) });
    toast(t("netdisk.folderCreated"), true);
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
  toast(errors ? t("netdisk.copiedNFailed", { n: data.count || 0, err: errors }) : t("netdisk.copiedN", { n: data.count || 0 }), !errors);
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
  toast(errors ? t("netdisk.movedNFailed", { n: data.count || 0, err: errors }) : t("netdisk.movedN", { n: data.count || 0 }), !errors);
  renderNetdisk();
}

async function batchShare() {
  if (isBackupMode()) return toast(t("netdisk.backupNoShare"));
  const paths = [...state.netdisk.selected];
  if (!paths.length) return;
  const name = paths.length === 1 ? paths[0].split("/").pop() : t("netdisk.sharedItems");
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
  const crumbs = [`<button class="linklike" data-crumb="">${t("netdisk.allFiles")}</button>`];
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
    const access = s.hasPassword ? s.password || t("netdisk.password") : t("netdisk.public");
    const pathsText = (s.paths || []).join(", ");
    const copyCode = s.password ? ` data-copy-code="${escapeHtml(s.password)}"` : "";
    return `<tr class="${s.expired ? "row-muted" : ""}"><td class="share-name-cell" title="${escapeHtml(s.name)}${pathsText ? ` · ${escapeHtml(pathsText)}` : ""}"><div class="primary-line">${escapeHtml(s.name)}</div><div class="secondary-line">${(s.paths || []).map(escapeHtml).join(", ")}</div></td><td class="share-link-cell" title="${escapeHtml(link)}"><a href="${link}" target="_blank">${escapeHtml(link)}</a></td><td>${shareExpiry(s)}</td><td${s.password ? ` title="${t("netdisk.extractionCode")}"` : ""}>${escapeHtml(access)}</td><td class="actions"><button class="ghost" data-copy-link="${escapeHtml(link)}"${copyCode}>${t("common.copy")}</button><button class="icon danger" data-share-del="${escapeHtml(s.token)}">Del</button></td></tr>`;
  }).join("") || `<tr class="empty-row"><td colspan="5">${t("netdisk.noExternalLinks")}</td></tr>`;
}

function adminShareRows(shares) {
  return (shares || []).map((s) => {
    const link = `${location.origin}/pan/${encodeURIComponent(s.token)}`;
    const access = s.hasPassword ? s.password || t("netdisk.password") : t("netdisk.public");
    const pathsText = (s.paths || []).join(", ");
    return `<tr class="${s.expired ? "row-muted" : ""}"><td><input type="checkbox" data-admin-share-token value="${escapeHtml(s.token)}"></td><td>${escapeHtml(displayNameForUsername(s.owner) || s.ownerId)}</td><td class="share-name-cell" title="${escapeHtml(s.name)}${pathsText ? ` · ${escapeHtml(pathsText)}` : ""}"><div class="primary-line">${escapeHtml(s.name)}</div><div class="secondary-line">${(s.paths || []).map(escapeHtml).join(", ")}</div></td><td class="share-link-cell" title="${escapeHtml(link)}"><a href="${link}" target="_blank">${escapeHtml(link)}</a></td><td>${shareExpiry(s)}</td><td${s.password ? ` title="${t("netdisk.extractionCode")}"` : ""}>${escapeHtml(access)}</td></tr>`;
  }).join("") || `<tr class="empty-row"><td colspan="6">${t("netdisk.noExternalLinks")}</td></tr>`;
}

function shareExpiry(s) {
  if (s.permanent) return `<span class="badge badge-accent">${t("netdisk.permanent")}</span>`;
  if (s.expired) return `<span class="badge badge-danger">${t("netdisk.expired")}</span>`;
  return escapeHtml(s.expiresAt ? new Date(s.expiresAt).toLocaleString() : t("netdisk.7days"));
}

async function renameItem(path, oldName) {
  const next = prompt(t("netdisk.renamePrompt"), oldName);
  if (!next || next === oldName) return;
  const parts = path.split("/");
  parts.pop();
  const to = joinPath(parts.join("/"), next);
  const endpoint = isBackupMode() ? "/api/netdisk/backup/rename" : "/api/netdisk/rename";
  await api(endpoint, { method: "POST", body: JSON.stringify({ from: path, to }) });
  toast(t("netdisk.renamed"), true);
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
    toast(t("netdisk.backupNoShare"));
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
    { d: 1, label: t("netdisk.1day") },
    { d: 7, label: t("netdisk.7days") },
    { d: 30, label: t("netdisk.30days") },
    { d: 0, label: t("netdisk.permanent") },
  ];
  const expiryButtons = expiryOptions
    .map((o) => `<button class="share-seg-btn${s.expiry === o.d ? " active" : ""}" data-expiry="${o.d}">${escapeHtml(o.label)}</button>`)
    .join("");
  const oneItem = s.paths.length === 1;
  return (
    `<div class="modal-head">` +
      `<div class="share-head-text"><h2>${oneItem ? t("netdisk.shareFile") : t("netdisk.shareItems")}</h2>` +
      `<p class="hint share-name-line" title="${escapeHtml(s.name)}">${escapeHtml(s.name)}</p></div>` +
      `<button class="icon" data-close>✕</button>` +
    `</div>` +
    `<div class="modal-body share-form">` +
      `<div class="share-row">` +
        `<div class="share-row-label">${t("netdisk.validFor")}</div>` +
        `<div class="share-seg">${expiryButtons}</div>` +
      `</div>` +
      `<div class="share-row">` +
        `<div class="share-row-label">${t("netdisk.extractionCodeLabel")}</div>` +
        `<div class="share-row-body share-code-row">` +
          `<label class="check"><input type="checkbox" id="shareUseCode" ${s.usePassword ? "checked" : ""}><span>${t("netdisk.protectWithCode")}</span></label>` +
          `<div class="share-code-field ${s.usePassword ? "" : "is-hidden"}">` +
            `<input id="shareCode" value="${escapeHtml(s.code)}" maxlength="8" autocomplete="off">` +
            `<button class="ghost" id="shareRandomCode" type="button">${t("netdisk.random")}</button>` +
          `</div>` +
        `</div>` +
      `</div>` +
    `</div>` +
    `<div class="modal-foot">` +
      `<button class="ghost" data-close>${t("common.cancel")}</button>` +
      `<button class="primary" id="shareCreate"${s.creating ? " disabled" : ""}>${s.creating ? t("netdisk.creating2") : t("netdisk.createLink")}</button>` +
    `</div>`
  );
}

function shareLinkHtml() {
  const s = shareModal;
  const link = `${location.origin}/pan/${encodeURIComponent(s.created.token)}`;
  return (
    `<div class="modal-head">` +
      `<div class="share-head-text"><h2>${t("netdisk.shareLinkReady")}</h2>` +
      `<p class="hint share-name-line" title="${escapeHtml(s.name)}">${escapeHtml(s.name)}</p></div>` +
      `<button class="icon" data-close>✕</button>` +
    `</div>` +
    `<div class="modal-body share-result">` +
      `<div class="share-link-row">` +
        `<div class="share-row-label">${t("netdisk.linkLabel")}</div>` +
        `<div class="copy-chip share-copy-chip" title="${escapeHtml(link)}">` +
          `<code>${escapeHtml(link)}</code>` +
          `<button class="copy-btn" data-copy="${escapeHtml(link)}" data-copy-target="link" type="button">${t("common.copy")}</button>` +
        `</div>` +
      `</div>` +
      (s.usePassword
        ? `<div class="share-link-row">` +
          `<div class="share-row-label">${t("netdisk.codeLabel")}</div>` +
          `<div class="copy-chip share-copy-chip">` +
            `<code>${escapeHtml(s.code)}</code>` +
            `<button class="copy-btn" data-copy="${escapeHtml(s.code)}" data-copy-target="code" type="button">${t("common.copy")}</button>` +
          `</div>` +
        `</div>`
        : "") +
      `<button class="subtle share-copy-all" id="shareCopyAll" type="button">${t("netdisk.copyAll", { code: s.usePassword ? ` & ${t("netdisk.codeLabel")}` : "" })}</button>` +
    `</div>` +
    `<div class="modal-foot">` +
      `<button class="ghost" data-close>${t("common.close")}</button>` +
      `<button class="primary" id="shareDone" type="button">${t("common.done")}</button>` +
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
      const text = s.usePassword ? `${link}\n${t("netdisk.extractionCode")}: ${s.code}` : link;
      await navigator.clipboard.writeText(text);
      flashCopy(btn);
    };
    $("#shareDone").onclick = () => { closeModal(); renderNetdisk(); };
  }
}

function flashCopy(btn) {
  const prev = btn.textContent;
  btn.textContent = t("common.copied");
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

// Guard against concurrent uploads: the overlay is shared, so a second pick
// while one is running would race it.
let uploading = false;

// Per-batch limits. A single multipart request carries at most UPLOAD_BATCH_FILES
// files / UPLOAD_BATCH_BYTES bytes. The byte cap keeps a batch of large files
// well under the 2 GiB ParseMultipartForm cap (and any reverse proxy body
// limit); it can make a batch smaller than the file cap, never larger.
const UPLOAD_BATCH_BYTES = 256 << 20; // 256 MiB
const UPLOAD_BATCH_FILES = 20;
// How many batches run concurrently. Folder upload used to be strictly serial
// (one batch, await, next batch) which was slow on big folders. UPLOAD_CONCURRENCY
// batches (~60 files) are now in flight at once; the pool pulls the next batch
// from the stream as soon as a slot frees.
const UPLOAD_CONCURRENCY = 3;
// Files at or above this size use the chunked/resumable protocol instead of one
// multipart request, so a multi-GB file resumes after a drop and is verified in
// 100 MB pieces rather than as a single giant blob.
const CHUNK_THRESHOLD = 1 << 30; // 1 GiB

// walkEntry recursively reads a FileSystemEntry, calling push(entry) for every
// file it discovers. push may return a Promise (a gate): when it does, walkEntry
// awaits it before reading the next entry. The upload queue supplies a gate that
// resolves only once the in-memory buffer has been drained below its cap, so a
// million-file folder is traversed at the speed of the wire rather than dumped
// into memory all at once — this is what stops the walk phase from freezing the
// page. `prefix` is the directory path accumulated so far (no trailing slash).
function walkEntry(entry, prefix, push) {
  return new Promise((resolve) => {
    if (!entry) return resolve();
    const fullPath = prefix ? `${prefix}/${entry.name}` : entry.name;
    if (entry.isFile) {
      entry.file(
        (file) => { Promise.resolve(push({ file, relPath: fullPath })).then(resolve); },
        () => resolve(), // unreadable file: skip rather than abort the batch
      );
      return;
    }
    if (entry.isDirectory) {
      // A directory's own name is part of the structure, so files inside it
      // are prefixed with fullPath. Don't push an entry for the dir itself.
      const reader = entry.createReader();
      const readAll = () => {
        // readEntries returns a page of results; an empty page signals the end.
        reader.readEntries(
          async (page) => {
            if (!page || page.length === 0) return resolve();
            for (const child of page) await walkEntry(child, fullPath, push);
            readAll(); // keep paging until the directory is exhausted
          },
          () => resolve(), // unreadable dir: skip it, keep the rest
        );
      };
      readAll();
      return;
    }
    resolve();
  });
}

// uploadFilesHandler uploads files to the netdisk (never the backup disk —
// uploads are netdisk-only). `source` is an async iterable yielding entries
// ({ file, relPath }) one at a time: either an async generator walking a dropped
// folder, or a sync iterator over a picked FileList wrapped by fileIterable().
// Renamed from uploadFiles so the bindEvents scope can use a local `uploadFiles`
// DOM handle without colliding.
//
// Streaming, not batching-up-front: entries are pulled from `source` only fast
// enough to fill the next request, so at most one batch (~UPLOAD_BATCH_FILES
// files) is held in memory at any moment regardless of how large the folder is.
// For a drop, the walker is gated on the same drain — traversal proceeds at the
// speed of the wire, so a million-file folder never materializes a giant array.
//
// Files are sent in batches of <= UPLOAD_BATCH_FILES (and <= UPLOAD_BATCH_BYTES)
// and batches run strictly one after another: a batch is only started once the
// previous one has been answered, so at most a handful of files are ever in
// flight. The backend reconstructs nested directories from the "paths" field.
async function uploadFilesHandler(source, path) {
  if (!canMutate()) return toast(t("netdisk.readonlyUpload"));
  if (uploading) return toast(t("netdisk.uploadInProgress"));
  uploading = true;
  const overlay = showUploadOverlay();

  const csrfToken = readCSRFCookie() || state.csrfToken || "";
  // totalBytes is accumulated as files are seen (not pre-reduced over the whole
  // list), so the progress bar is meaningful even when N is unknown up front
  // (e.g. while a dropped folder is still being walked).
  let totalBytes = 0;
  let doneBytes = 0;
  let ok = 0;
  let failed = 0;
  let seen = 0;
  const batchStart = performance.now();
  let iter;
  try {
    iter = source[Symbol.asyncIterator]
      ? source[Symbol.asyncIterator]()
      : makeFileIterator(source);
  } catch {
    uploading = false;
    overlay.close();
    return;
  }

  const nextEntry = () => Promise.resolve(iter.next()).then((r) => (r.done ? null : r.value));

  // failedFiles collects entries the server rejected (write error or CRC32
  // mismatch) so they can be retried. It is appended to while batches resolve,
  // so it must not be re-traversed concurrently — only after the run completes.
  const failedFiles = [];

  // uploadBatch claims overlay rows for `cur` and delegates to sendBatch. It is
  // the unit of concurrency: up to UPLOAD_CONCURRENCY of these run at once.
  function uploadBatch(cur, curBytes) {
    // Claim an overlay slot per file in the batch (active rows render up top).
    const slots = cur.map((e) =>
      overlay.addActive({ name: e.relPath || e.file.name, size: e.file.size }),
    );
    return sendBatch(cur, slots, curBytes);
  }

  // sendBatch does the real work for an already-claimed set of rows: hash, POST,
  // and verify per-file results. `slots` parallel `cur` (one row per file). Used
  // by both the normal pool path (uploadBatch) and single-file retries (which
  // reactivate an existing failed row instead of allocating a new slot).
  async function sendBatch(cur, slots, _curBytes) {
      // Large files (>= CHUNK_THRESHOLD) skip the multipart batch entirely and go
      // through the chunked/resumable protocol: they never fit a single request
      // comfortably and benefit from per-chunk resume. Each large file is handled
      // on its own slot; small files proceed as one multipart batch below.
      const large = [];
      const small = [];
      const smallSlots = [];
      for (let i = 0; i < cur.length; i++) {
        if ((cur[i].file.size || 0) >= CHUNK_THRESHOLD) {
          large.push({ entry: cur[i], slot: slots[i] });
        } else {
          small.push(cur[i]);
          smallSlots.push(slots[i]);
        }
      }
      // Run large files concurrently with the small-file batch. Each large file
      // drives its own slot's progress; on success/failure it is settled the same
      // way as a small file (done / armRetry), so retry UX is uniform.
      const largePromises = large.map(({ entry, slot }) => uploadOneLarge(entry, slot));
      let smallResp = null;
      if (small.length) smallResp = await sendSmallBatch(small, smallSlots, sumSizes(small));
      await Promise.allSettled(largePromises);
      return smallResp;
  }

  // uploadOneLarge drives the chunked upload for a single large file on its slot.
  async function uploadOneLarge(entry, slot) {
    let lastLoaded = 0;
    try {
      const res = await uploadLargeFile(entry.file, entry.relPath, {
        base: "/api/netdisk", dir: path, csrfToken,
        onProgress: ({ loaded, total }) => {
          const delta = Math.max(0, loaded - lastLoaded);
          lastLoaded = loaded;
          doneBytes += delta;
          overlay.updateActive(slot, {
            loaded, total,
            percent: total > 0 ? Math.min(100, Math.round((loaded / total) * 100)) : 0,
            speedBps: 0,
          });
        },
      });
      // The server verified + assembled the file; settle as done. (res.crc32 is
      // the whole-file digest computed during assembly.)
      void res;
      overlay.settleActive(slot, "done");
      ok++;
    } catch (err) {
      failedFiles.push(entry);
      failed++;
      armRetry(slot, entry, err.message);
    }
  }

  // sumSizes sums the byte sizes of an entry list (for the small-batch cap).
  function sumSizes(entries) {
    let n = 0;
    for (const e of entries) n += e.file.size || 0;
    return n;
  }

  // sendSmallBatch is the original multipart path, factored out so sendBatch can
  // route large files to the chunked protocol instead.
  async function sendSmallBatch(cur, slots, curBytes) {
      // Compute each file's CRC32 up front (off-main-thread, capped at a few
      // workers) so the server can verify integrity. Files that can't be hashed
      // yield "" and are still uploaded — the server checksums them.
      const hashes = await Promise.all(cur.map((e) => hashFileCRC32(e.file)));

      const fd = new FormData();
      fd.append("path", path);
      for (let i = 0; i < cur.length; i++) {
        // The relative path travels in its own "paths" field, one value per file
        // part in the same order: a part's filename cannot carry directories
        // (RFC 7578 §4.2), and the backend's multipart parser strips them, so
        // relying on the filename alone would flatten "sub/f.txt" to "f.txt".
        // The filename is still set so it stays a sensible fallback.
        fd.append("paths", cur[i].relPath);
        fd.append("hashes", hashes[i] || "");
        fd.append("files", cur[i].file, cur[i].relPath);
      }
      // loaded[i] = bytes of file i already on the wire (for per-file progress).
      const loaded = new Array(cur.length).fill(0);
      let resp;
      try {
        resp = await uploadWithProgress("/api/netdisk/upload", fd, {
          csrfToken,
          onProgress: (p) => {
            // uploadWithProgress reports bytes for the whole batch request;
            // attribute them proportionally across the batch's files so each row
            // still advances instead of freezing on "uploading…".
            const batchLoaded = p.loaded;
            const batchTotal = p.total || curBytes;
            const ratio = batchTotal > 0 ? batchLoaded / batchTotal : 0;
            for (let i = 0; i < cur.length; i++) {
              const fileTotal = cur[i].file.size || 0;
              const fileLoaded = fileTotal > 0 ? Math.min(fileTotal, Math.round(fileTotal * ratio)) : 0;
              doneBytes += Math.max(0, fileLoaded - loaded[i]);
              loaded[i] = fileLoaded;
              overlay.updateActive(slots[i], {
                loaded: fileLoaded,
                total: fileTotal,
                percent: fileTotal > 0 ? Math.min(100, Math.round((fileLoaded / fileTotal) * 100)) : 100,
                speedBps: p.speedBps,
              });
            }
          },
        });
      } catch (err) {
        // Network/HTTP failure for the whole request: every file is retriable.
        for (let i = 0; i < cur.length; i++) {
          failedFiles.push(cur[i]);
          failed++;
          armRetry(slots[i], cur[i], err.message);
        }
        return;
      }
      // The server returns per-file results: each has {path, crc32, error?}. A
      // file is considered delivered only when it has no error AND (the client
      // hash was empty OR it matches the server crc32). Anything else is a
      // corrupt/failed upload the user can retry.
      const results = Array.isArray(resp?.results) ? resp.results : [];
      for (let i = 0; i < cur.length; i++) {
        const r = results[i] || {};
        const serverCrc32 = (r.crc32 || "").toLowerCase();
        const clientCrc32 = (hashes[i] || "").toLowerCase();
        const okFile = !r.error && (!clientCrc32 || clientCrc32 === serverCrc32);
        if (okFile) {
          overlay.settleActive(slots[i], "done");
          ok++;
        } else {
          failedFiles.push(cur[i]);
          failed++;
          const why = r.error || (clientCrc32 ? t("netdisk.crc32Mismatch") : "Failed");
          armRetry(slots[i], cur[i], why);
        }
      }
      return resp;
  }

  // armRetry marks a failed row with a Retry button that re-queues just that
  // file. It reuses the SAME overlay row (flipping it back to uploading) and
  // re-runs the single file through sendBatch so CRC32 verification + result
  // checking apply identically to the original upload.
  function armRetry(slot, entry, msg) {
    overlay.markFailedWithRetry(slot, msg, async () => {
      // Reactivate the existing row in place rather than spawning a new one.
      overlay.reactivate(slot, { name: entry.relPath || entry.file.name, size: entry.file.size });
      // Pull this file out of the failed set while it's being retried; it will
      // be re-added by sendBatch if it fails again.
      const idx = failedFiles.indexOf(entry);
      if (idx >= 0) failedFiles.splice(idx, 1);
      failed = Math.max(0, failed - 1);
      refreshOverall();
      await sendBatch([entry], [slot], entry.file.size || 0);
      setCurrentSelection(state.netdisk.selected || new Set());
      renderNetdisk();
    });
  }

  function refreshOverall() {
    const elapsed = (performance.now() - batchStart) / 1000;
    const speedBps = elapsed > 0 ? doneBytes / elapsed : 0;
    const percent = totalBytes > 0 ? Math.min(100, (doneBytes / totalBytes) * 100) : 0;
    const etaSec = speedBps > 0 ? (totalBytes - doneBytes) / speedBps : 0;
    overlay.updateOverall({
      done: ok, failed, total: seen, loaded: doneBytes, bytesTotal: totalBytes, speedBps, etaSec, percent,
    });
  }

  // Concurrent pool: keep up to UPLOAD_CONCURRENCY batches in flight, pulling the
  // next batch from the stream whenever a slot frees.
  const inflight = new Set();
  for (;;) {
    while (inflight.size < UPLOAD_CONCURRENCY) {
      // Assemble the next batch from the stream.
      const cur = [];
      let curBytes = 0;
      while (cur.length < UPLOAD_BATCH_FILES) {
        const entry = await nextEntry();
        if (!entry) break;
        const size = entry.file.size || 0;
        // A single oversized file (larger than the byte cap) forms its own batch
        // rather than being split — the backend streams it via ParseMultipartForm.
        if (cur.length > 0 && curBytes + size > UPLOAD_BATCH_BYTES) {
          iter = prependIterator(iter, entry); // push back for the next batch
          break;
        }
        cur.push(entry);
        curBytes += size;
        totalBytes += size;
        seen++;
      }
      if (!cur.length) break; // stream exhausted
      overlay.setLabel(t("netdisk.uploadingRange", { lo: ok + failed + 1, hi: ok + failed + cur.length, total: seen }));
      const p = uploadBatch(cur, curBytes).then(() => {
        inflight.delete(p);
        refreshOverall();
      });
      inflight.add(p);
    }
    if (!inflight.size) break; // no batch assembled and none in flight: done
    // Wait for at least one in-flight batch to settle before topping up the pool.
    await Promise.race(inflight);
    refreshOverall();
  }
  // Drain any remaining batches.
  if (inflight.size) { await Promise.allSettled(inflight); refreshOverall(); }

  overlay.setLabel(t("netdisk.uploadedN", { ok, total: seen }));
  const summary = failed
    ? t("netdisk.uploadedNFailed", { ok, fail: failed })
    : t("netdisk.uploadedN", { ok, total: seen });
  toast(summary, !failed);
  setCurrentSelection(state.netdisk.selected || new Set());
  renderNetdisk();
  refreshOverall();
  // The card stays open until the user closes it (via the × button): an
  // auto-dismiss can hide a failure before the user gets to click Retry.
  uploading = false;
}

// makeFileIterator / prependIterator / makeDropIterator live in lib/uploadStream.js
// (imported above). The drop iterator is wired to walkEntry here so traversal of
// a dropped folder back-pressures the upload and only ~one batch of File
// references is held in memory at a time.
function dropStream(itemList) {
  return makeDropIterator(itemList, walkEntry, UPLOAD_BATCH_FILES * 2);
}

function quotaBar(q) {
  if (!q) return t("netdisk.usedN", { used: "0 B" });
  const used = q.usedBytes || 0;
  const estimating = q.usedEstimating ? ` <span class="hint">${t("netdisk.estimating")}</span>` : "";
  if (q.totalBytes > 0) {
    const pct = Math.min(100, Math.round((used / q.totalBytes) * 100));
    return t("netdisk.usedOfTotal", { used: fmtBytes(used), total: fmtBytes(q.totalBytes) }) + ` <span class="quota-bar"><span style="width:${pct}%"></span></span> ${pct}%${estimating}`;
  }
  if (q.diskFreeBytes != null) {
    return t("netdisk.usedFree", { used: fmtBytes(used), free: fmtBytes(q.diskFreeBytes) }) + estimating;
  }
  return t("netdisk.usedN", { used: fmtBytes(used) }) + estimating;
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
        (supportsFullscreen ? `<button class="ghost" id="viewerFullscreen" title="${t("netdisk.viewerFullscreen")}">⛶ ${t("netdisk.viewerFullscreen")}</button>` : ``) +
        `<a class="ghost" href="${dl}" title="${t("netdisk.download")}">⬇ ${t("netdisk.download")}</a>` +
        `<button class="icon" data-close>✕</button>` +
      `</div>` +
    `</div>` +
    `<div class="modal-body viewer-body" id="viewerBody"><div class="viewer-loading">${t("netdisk.viewerLoading")}</div></div>`,
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
        `<p>${t("netdisk.viewerUnsupported")}</p>` +
        `<p class="hint">${t("netdisk.viewerDownloadToView")}</p>` +
        `<a class="primary" href="${href}" download>⬇ ${t("netdisk.download")}</a>` +
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
  if (fsBtn) fsBtn.textContent = isFs ? t("netdisk.viewerExitFullscreen") : t("netdisk.viewerFullscreen");
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
    body.innerHTML = `<div class="viewer-error">${t("netdisk.viewerPdfFail")}</div>`;
    return;
  }
  pdfjs.workerSrc = "/vendor/pdf.worker.min.js";
  body.innerHTML = `<div class="viewer-loading">${t("netdisk.viewerRenderPdf")}</div>`;
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
    body.innerHTML = `<div class="viewer-error">${t("netdisk.viewerLarge")}</div>`;
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
  body.innerHTML = `<div class="viewer-loading">${t("netdisk.viewerLoadYuv")}</div>`;
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
