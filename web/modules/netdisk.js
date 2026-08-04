import { state, api, toast, escapeHtml, fmtBytes, isAdmin, canMutate, displayNameForUsername, readCSRFCookie, t } from "../app.js";
import { showModalNoShell, closeModal, onModalClose } from "./ui.js";
import { uploadWithProgress, showUploadOverlay } from "../lib/upload.js";
import { makeFileIterator, prependIterator, makeDropIterator } from "../lib/uploadStream.js";
import { hashFileCRC32 } from "../lib/hashfile.js";
import { uploadLargeFile } from "../lib/chunkupload.js";
import { openYuvViewer } from "../lib/yuv.js";
import { renderMarkdownInto } from "../lib/viewer.js";

// netdiskMode is "netdisk" (primary SSD, the default), "backup" (the slow
// mechanical backup disk), or "shareddisk" (共享盘: one pool per group;
// everyone can browse it, but only the caller's own subfolder — or any
// folder, for an admin — is editable). Named "shareddisk", not "shared" or
// "share", to stay clearly distinct from the netdisk's own external
// share-link feature (分享链接, netdisk.share*/openShareModal below). The
// whole view branches on this: which list/quota endpoints it fetches, which
// action buttons show, and how download/preview URLs are built. Toggling
// also resets the path so each disk starts at its root.
let netdiskMode = "netdisk";

// Keep independent browsing context for each disk so toggling does not lose
// the current folder/selection state of the other sides.
const modeState = {
  netdisk: { path: "", selected: new Set() },
  backup: { path: "", selected: new Set() },
  shareddisk: { path: "", selected: new Set() },
};

function currentModeState() {
  return modeState[netdiskMode];
}

// Mobile per-row action menus (see .row-menu in styles.css) toggle open via
// a JS-managed "open" class rather than native <details>/<summary>: a closed
// <details>'s non-summary content is force-hidden by the browser with an
// !important rule that no author CSS can override, which also defeats the
// "display: contents" trick used to render the very same markup as a flat
// icon row on wide screens. A tap on the toggle opens its menu (closing any
// other open one); a tap outside, or on one of the menu's own actions,
// closes it (like a bottom sheet, dismissed once an action is taken). Bound
// once at module load (not per render) so it never accumulates listeners.
document.addEventListener("click", (e) => {
  const toggle = e.target.closest(".row-menu-toggle");
  if (toggle) {
    const menu = toggle.closest(".row-menu");
    const wasOpen = menu.classList.contains("open");
    document.querySelectorAll(".row-menu.open").forEach((m) => m.classList.remove("open"));
    if (!wasOpen) menu.classList.add("open");
    return;
  }
  document.querySelectorAll(".row-menu.open").forEach((menu) => menu.classList.remove("open"));
});

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

function isSharedDiskMode() {
  return netdiskMode === "shareddisk";
}

// backupConfigured/sharedDiskConfigured reflect whether the caller's group
// even has a root path set for that disk (see /api/me). Neither disk has a
// meaningful "browse" experience without one — the backend just errors with
// "not configured" — so the UI hides the mode tab and copy/move destination
// entirely rather than offering a mode that only ever shows an error.
function backupConfigured() {
  return !!state.me?.backupConfigured;
}

function sharedDiskConfigured() {
  return !!state.me?.sharedDiskConfigured;
}

// isOwnSharedDiskRow reports whether a shared-disk listing row belongs to the
// caller's own subfolder (mirrors the backend's firstPathSegment check). Admins
// can manage every row, not just their own — see requireOwnSharedDiskPath.
function isOwnSharedDiskRow(path, ownFolder) {
  if (isAdmin()) return true;
  if (!ownFolder) return false;
  const first = (path || "").split("/")[0];
  return first === ownFolder;
}

// canMkdirInShared reports whether the caller may create a folder at the given
// shared-disk location: a regular user only may inside their own subfolder
// (root and other members' folders are read-only browse), an admin anywhere.
// Mirrors requireOwnSharedDiskPath, which rejects the root (first segment "")
// and any path whose first segment isn't the caller's own folder name.
function canMkdirInShared(path, ownFolder) {
  if (isAdmin()) return true;
  if (!ownFolder) return false;
  const segs = (path || "").split("/").filter(Boolean);
  return segs.length > 0 && segs[0] === ownFolder;
}

function backupUnavailableMessage(errOrMsg) {
  const msg = String(errOrMsg?.message || errOrMsg || "").trim();
  if (!msg) return t("netdisk.backupUnavailable");
  if (msg.toLowerCase().includes("not configured")) return msg;
  return t("netdisk.backupUnavailableMsg", { msg });
}

export async function renderNetdisk() {
  // A tab can go stale if its disk's path was unset (or never fetched yet)
  // after landing on it — fall back to the netdisk, which is always
  // available, rather than rendering a mode with no tab left to leave it by.
  if (netdiskMode === "backup" && !backupConfigured()) netdiskMode = "netdisk";
  if (netdiskMode === "shareddisk" && !sharedDiskConfigured()) netdiskMode = "netdisk";
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
    const listURL = isBackupMode()
      ? `/api/netdisk/backup/browse?path=${encodeURIComponent(state.netdisk.path || "")}`
      : isSharedDiskMode()
        ? `/api/shareddisk?path=${encodeURIComponent(state.netdisk.path || "")}`
        : `/api/netdisk?path=${encodeURIComponent(state.netdisk.path || "")}`;
    // Quota and external-link shares are netdisk-only concepts; the shared
    // disk has its own (simpler) quota endpoint, backup has neither.
    const quotaURL = isSharedDiskMode() ? "/api/shareddisk/quota" : "/api/netdisk/quota";
    const [list, quota, shares, adminShares] = await Promise.all([
      api(listURL),
      isBackupMode() ? Promise.resolve(null) : api(quotaURL).catch(() => null),
      isBackupMode() || isSharedDiskMode() ? Promise.resolve([]) : api("/api/netdisk/shares").catch(() => []),
      isBackupMode() || isSharedDiskMode() || !isAdmin() ? Promise.resolve([]) : api("/api/admin/netdisk/shares").catch(() => []),
    ]);
    const effectiveQuota = isBackupMode() ? (list?.quota || null) : quota;
    const backupWarning = isBackupMode() && list?.unavailable ? backupUnavailableMessage(list.message) : "";
    sig = JSON.stringify([netdiskMode, list.path, list.items, effectiveQuota, shares, adminShares, backupWarning, list.ownFolder]);
    state.netdisk = {
      path: list.path || "",
      items: list.items || [],
      quota: effectiveQuota,
      shares,
      adminShares,
      backupWarning,
      ownFolder: list.ownFolder || "",
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
  const shared = isSharedDiskMode();
  const ownFolder = state.netdisk.ownFolder || "";
  const rows = sortedItems(state.netdisk.items).map((f) => fileRow(f, mutable, backup, shared, ownFolder)).join("") ||
    `<tr class="empty-row"><td colspan="${mutable ? 6 : 5}">No files.</td></tr>`;
  const fileCount = state.netdisk.items.filter((f) => !f.dir).length;
  const folderCount = state.netdisk.items.length - fileCount;
  const quotaHtml = quotaBar(state.netdisk.quota);
  const selectionSize = [...state.netdisk.selected].length;
  // On the shared disk a regular user may only delete/move rows inside their
  // own subfolder; a selection that mixes in other members' rows disables the
  // batch delete/move buttons (copy stays available everywhere). Admins and
  // non-shared modes are unaffected.
  const allOwn = !shared || isAdmin() ||
    [...state.netdisk.selected].every((p) => isOwnSharedDiskRow(p, ownFolder));
  const modeOf = (name) => (shared ? "shareddisk" : backup ? "backup" : "netdisk") === name;

  $("#view").innerHTML =
    `<div class="stack netdisk-stack">` +
      `<div class="card netdisk-card" id="netdiskCard">` +
        `<div class="netdisk-toolbar">` +
          `<div class="netdisk-title">` +
            `<div class="netdisk-mode-toggle" role="tablist">` +
              `<button class="netdisk-mode-btn${modeOf("netdisk") ? " active" : ""}" data-mode="netdisk" role="tab" aria-selected="${modeOf("netdisk")}">${t("netdisk.modeNetdisk")}</button>` +
              (backupConfigured() ? `<button class="netdisk-mode-btn${modeOf("backup") ? " active" : ""}" data-mode="backup" role="tab" aria-selected="${modeOf("backup")}">${t("netdisk.modeBackup")}</button>` : "") +
              (sharedDiskConfigured() ? `<button class="netdisk-mode-btn${modeOf("shareddisk") ? " active" : ""}" data-mode="shareddisk" role="tab" aria-selected="${modeOf("shareddisk")}">${t("netdisk.modeSharedDisk")}</button>` : "") +
            `</div>` +
            `<span class="netdisk-count">${t("netdisk.counts", { folders: folderCount, files: fileCount })}</span>` +
          `</div>` +
          `<div class="head-tools netdisk-actions">` +
            `<button class="ghost" id="upDir">${t("netdisk.up")}</button>` +
            // New-folder is available on the netdisk and backup disk. On the
            // shared disk a regular user may only create inside their own
            // subfolder — the root and other members' folders are read-only
            // browse (requireOwnSharedDiskPath enforces this server-side too),
            // so the button is hidden there. Admins keep it everywhere.
            (shared && !canMkdirInShared(state.netdisk.path || "", ownFolder) ? "" :
              `<button class="ghost" id="mkdirBtn">${t("netdisk.newFolder")}</button>`) +
            // Upload and Folder-upload are netdisk-only: a backup disk is a
            // mirror target and the shared disk has no upload surface of its
            // own — files reach it via copy/move from the netdisk instead
            // (see openFolderPicker, which also works the other way: copying
            // or moving out of the shared disk onto the netdisk/backup disk).
            (backup || shared ? "" : `<label class="buttonlike btn-upload"><input id="uploadFiles" type="file" multiple> ${t("netdisk.upload")}</label>`) +
            (backup || shared ? "" : `<label class="buttonlike btn-upload"><input id="uploadFolder" type="file" webkitdirectory directory multiple> ${t("netdisk.folder")}</label>`) +
            (mutable
              ? (() => {
                  // mutDisabled: no selection, or (shared disk only) the
                  // selection mixes in other members' rows a regular user
                  // can't delete or move — copy stays enabled either way.
                  const mutDisabled = !selectionSize || !allOwn;
                  const mutTitle = !allOwn ? ` title="${t("netdisk.batchMixedForeign")}"` : "";
                  return `<button class="btn-delete" id="batchDelete" ${mutDisabled ? "disabled" : ""}${mutTitle}>${t("netdisk.deleteN", { n: selectionSize })}</button>` +
                    `<button class="ghost" id="batchCopy" ${selectionSize ? "" : "disabled"}>${t("netdisk.copyN", { n: selectionSize })}</button>` +
                    `<button class="ghost" id="batchMove" ${mutDisabled ? "disabled" : ""}${mutTitle}>${t("netdisk.moveN", { n: selectionSize })}</button>`;
                })() +
                // Share doesn't apply on the backup or shared disk. The
                // shared disk also has no zip-download surface of its own —
                // download a copy by copying it to the netdisk first.
                (backup || shared ? "" : `<button class="ghost" id="batchShare" ${selectionSize ? "" : "disabled"}>${t("netdisk.shareN", { n: selectionSize })}</button>`) +
                (shared ? "" : `<button class="btn-download" id="batchDownload" ${selectionSize ? "" : "disabled"}>${t("netdisk.downloadN", { n: selectionSize })}</button>`)
              : "") +
          `</div>` +
        `</div>` +
        (backup && state.netdisk.backupWarning
          ? `<div class="netdisk-backup-hint">${escapeHtml(state.netdisk.backupWarning)}</div>`
          : "") +
        (shared
          ? `<div class="netdisk-backup-hint">${t("netdisk.sharedDiskHint")}</div>`
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
      // External Links cards are netdisk-only — backup/shared-disk files can't be shared.
      (backup || shared ? "" :
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
  const shared = isSharedDiskMode();
  const base = shared ? "/api/shareddisk" : backup ? "/api/netdisk/backup" : "/api/netdisk";

  // --- Mode toggle: switch between the netdisk, backup and shared disks.
  // Switching resets path + selection so each disk opens at its own root.
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

  const mkdirBtn = $("#mkdirBtn");
  if (mkdirBtn) {
    mkdirBtn.onclick = async () => {
      const name = prompt(t("netdisk.folderNamePrompt"));
      if (!name) return;
      await api(`${base}/mkdir`, { method: "POST", body: JSON.stringify({ path: joinPath(state.netdisk.path, name) }) });
      toast(t("netdisk.folderCreated"), true);
      renderNetdisk();
    };
  }

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
  if (netdiskMode === "netdisk") {
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
    const batchCopyBtn = $("#batchCopy");
    if (batchCopyBtn) batchCopyBtn.onclick = () => batchCopyMove(false);
    const batchMoveBtn = $("#batchMove");
    if (batchMoveBtn) batchMoveBtn.onclick = () => batchCopyMove(true);
    const batchShareBtn = $("#batchShare");
    if (batchShareBtn) batchShareBtn.onclick = batchShare;
    const batchDownloadBtn = $("#batchDownload");
    if (batchDownloadBtn) batchDownloadBtn.onclick = batchDownload;
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

function fileRow(f, mutable, backup, shared, ownFolder) {
  // The shared disk has no download/preview endpoint of its own (retrieval
  // goes through copying it to the netdisk instead), so a shared-mode row
  // never links out: folders stay navigable, files are plain text.
  const href = shared ? "" : backup ? backupDownloadURL(f.path) : downloadURL(f.path);
  const checked = state.netdisk.selected.has(f.path) ? "checked" : "";
  const kind = shared ? null : previewKind(f.name);
  const name = f.dir
    ? `<button class="linklike netdisk-name-link" data-open="${escapeHtml(f.path)}" title="${escapeHtml(f.name)}">${escapeHtml(f.name)}</button>`
    : kind
      ? `<button class="linklike netdisk-name-link" data-view="${escapeHtml(f.path)}" data-backup="${backup ? "1" : ""}" title="${escapeHtml(f.name)}">${escapeHtml(f.name)}</button>`
      : shared
        ? `<span class="netdisk-name-link" title="${escapeHtml(f.name)}">${escapeHtml(f.name)}</span>`
        : `<a class="netdisk-name-link" href="${href}" title="${escapeHtml(f.name)}">${escapeHtml(f.name)}</a>`;
  // Only the caller's own subfolder (or, for an admin, any folder) is
  // editable on the shared disk — a row outside it can still be selected and
  // copied out to the netdisk/backup disk (a non-destructive read), but not
  // moved, renamed or deleted.
  const editable = shared ? isOwnSharedDiskRow(f.path, ownFolder) : true;
  const checkCell = mutable
    ? `<td class="chk-col"><input type="checkbox" class="netdisk-row-check" data-path="${escapeHtml(f.path)}" ${checked}></td>`
    : "";
  // On the backup disk we drop Share (no sharing from backup) and the
  // Backup-to-backup button (it's already there). On the shared disk there
  // is no download link and no share button (those stay netdisk-only); copy
  // is available on every row (copying someone else's file is harmless),
  // move/rename/delete only on rows inside the caller's own folder (or, for
  // an admin, any folder — requireOwnSharedDiskPath enforces this
  // server-side too). All actions live inside a .row-menu: on wide screens
  // it's rendered "flat" (CSS unwraps it via display:contents) so it looks
  // like the old inline icon row, but on phones the icon row has no space
  // for six buttons — the panel becomes a real dropdown behind a single
  // "more" trigger, like the file-row menu in mobile netdisk apps.
  let menuItems;
  if (!mutable) {
    menuItems = shared ? "" : `<a class="icon btn-download" href="${href}" title="${t("netdisk.download")}">⬇ <span>${t("netdisk.download")}</span></a>`;
  } else if (shared) {
    menuItems = `<button class="icon" title="${t("netdisk.copy")}" data-copy="${escapeHtml(f.path)}">⧉ <span>${t("netdisk.copy")}</span></button>` +
      (editable
        ? `<button class="icon" title="${t("netdisk.move")}" data-move="${escapeHtml(f.path)}">↗ <span>${t("netdisk.move")}</span></button>` +
          `<button class="icon" title="${t("netdisk.rename")}" data-ren="${escapeHtml(f.path)}" data-name="${escapeHtml(f.name)}">✎ <span>${t("netdisk.rename")}</span></button>` +
          `<button class="icon btn-delete" title="${t("common.delete")}" data-del="${escapeHtml(f.path)}" data-name="${escapeHtml(f.name)}">✕ <span>${t("common.delete")}</span></button>`
        : "");
  } else {
    menuItems = `<a class="icon btn-download" href="${href}" title="${t("netdisk.download")}">⬇ <span>${t("netdisk.download")}</span></a>` +
      `<button class="icon" title="${t("netdisk.rename")}" data-ren="${escapeHtml(f.path)}" data-name="${escapeHtml(f.name)}">✎ <span>${t("netdisk.rename")}</span></button>` +
      `<button class="icon" title="${t("netdisk.copy")}" data-copy="${escapeHtml(f.path)}">⧉ <span>${t("netdisk.copy")}</span></button>` +
      `<button class="icon" title="${t("netdisk.move")}" data-move="${escapeHtml(f.path)}">↗ <span>${t("netdisk.move")}</span></button>` +
      (backup ? "" : `<button class="icon" title="${t("netdisk.share")}" data-share="${escapeHtml(f.path)}" data-name="${escapeHtml(f.name)}">⤴ <span>${t("netdisk.share")}</span></button>`) +
      `<button class="icon btn-delete" title="${t("common.delete")}" data-del="${escapeHtml(f.path)}" data-name="${escapeHtml(f.name)}">✕ <span>${t("common.delete")}</span></button>`;
  }
  const actionCell = !menuItems ? "" :
    `<div class="row-menu">` +
      `<button type="button" class="icon row-menu-toggle" title="${t("netdisk.moreActions")}" aria-label="${t("netdisk.moreActions")}">⋮</button>` +
      `<div class="row-menu-panel">${menuItems}</div>` +
    `</div>`;
  const meta = `${f.dir ? "-" : fmtBytes(f.size)} · ${escapeHtml(fmtDate(f.modTime))}`;
  return `<tr>${checkCell}<td><div class="netdisk-file"><span class="netdisk-icon ${f.dir ? "folder" : "file"}" aria-hidden="true">${fileIcon(f.dir)}</span><div class="netdisk-file-text"><div class="primary-line">${name}</div><div class="netdisk-file-meta">${meta}</div></div></div></td><td class="netdisk-size">${f.dir ? "-" : fmtBytes(f.size)}</td><td class="netdisk-time" title="${escapeHtml(new Date(f.modTime).toLocaleString())}">${escapeHtml(fmtDate(f.modTime))}</td><td class="actions netdisk-row-actions">${actionCell}</td></tr>`;
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
  const endpoint = isSharedDiskMode() ? "/api/shareddisk/delete" : isBackupMode() ? "/api/netdisk/backup/delete" : "/api/netdisk/delete";
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
  const sourceDisk = isSharedDiskMode() ? "shareddisk" : isBackupMode() ? "backup" : "netdisk";
  // The shared disk has no same-disk copy/move of its own (see
  // sharedDiskRename for in-place renames within a folder) — when copying
  // or moving out of it, default the destination picker to the netdisk
  // instead of trying to target the disk it's already coming from.
  const initialTargetDisk = sourceDisk === "shareddisk" ? "netdisk" : sourceDisk;
  folderPicker = {
    move, paths,
    path: initialTargetDisk === sourceDisk ? (state.netdisk.path || "") : "",
    items: [], sourceDisk, targetDisk: initialTargetDisk, sharedDiskOwnFolder: "",
  };
  const action = move ? t("netdisk.move") : t("netdisk.copy");
  const diskLabel = initialTargetDisk === "backup" ? t("netdisk.backupDisk")
    : initialTargetDisk === "shareddisk" ? t("netdisk.modeSharedDisk")
    : t("netdisk.myNetdisk");
  const netdiskActive = initialTargetDisk === "netdisk" ? " active" : "";
  const backupActive = initialTargetDisk === "backup" ? " active" : "";
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
        (backupConfigured() ? `<button class="picker-tree-item picker-disk${backupActive}" data-picker-disk="backup" type="button">${t("netdisk.backupDisk")}</button>` : "") +
        // No self-target when the source is already the shared disk — there is
        // no same-disk copy/move endpoint for it. Otherwise it's a valid
        // destination: the picker browses the shared pool (fenced to the
        // caller's own subfolder for non-admins) so items can land in any
        // folder the caller may write to.
        (sourceDisk === "shareddisk" || !sharedDiskConfigured() ? "" :
          `<button class="picker-tree-item picker-disk" data-picker-disk="shareddisk" type="button">${t("netdisk.modeSharedDisk")}</button>`) +
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
      // Reset the shared-disk resolution so re-entering it (after browsing
      // elsewhere) re-anchors at the caller's own folder root rather than
      // resuming at whatever pool path was left behind.
      folderPicker.sharedDiskResolved = false;
      folderPicker.sharedDiskOwnFolder = "";
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
  // First entry into the shared disk as a destination: resolve the caller's
  // own folder once (the starting point they're allowed to write into), then
  // browse it like any other disk. Subsequent navigation falls through to the
  // shared-disk browse branch below.
  if (folderPicker.targetDisk === "shareddisk" && !folderPicker.sharedDiskResolved) {
    await loadPickerSharedDiskTarget();
    return;
  }
  listEl.innerHTML = `<div class="picker-empty">${t("netdisk.pickLoading")}</div>`;
  try {
    const url = folderPicker.targetDisk === "backup"
      ? `/api/netdisk/backup/browse?path=${encodeURIComponent(path)}`
      : folderPicker.targetDisk === "shareddisk"
        ? `/api/shareddisk?path=${encodeURIComponent(path)}`
        : `/api/netdisk?path=${encodeURIComponent(path)}`;
    const data = await api(url);
    folderPicker.items = (data.items || []).filter((f) => f.dir);
    renderFolderPicker();
  } catch (err) {
    if (listEl.isConnected) listEl.innerHTML = `<div class="picker-empty">${escapeHtml(err.message)}</div>`;
  }
}

// loadPickerSharedDiskTarget resolves the shared disk's browse starting point
// once, on first entry. A regular user is fenced inside their own subfolder:
// they only ever see that folder as the root and its descendants, never the
// other members' sibling folders (they can't write to those anyway —
// requireOwnSharedDiskPath rejects it server-side — so showing them would just
// be noise that ends in an error). An admin may write anywhere in the pool, so
// they start at the pool root and browse every member's folder.
async function loadPickerSharedDiskTarget() {
  const listEl = $("#pickerList");
  if (!listEl || !folderPicker) return;
  listEl.innerHTML = `<div class="picker-empty">${t("netdisk.pickLoading")}</div>`;
  try {
    const rootData = await api("/api/shareddisk?path=");
    folderPicker.sharedDiskOwnFolder = rootData.ownFolder || "";
    folderPicker.sharedDiskResolved = true;
    // Admins start at the pool root so they can target any member's folder.
    if (isAdmin()) {
      folderPicker.path = "";
      folderPicker.items = (rootData.items || []).filter((f) => f.dir);
      renderFolderPicker();
      return;
    }
    // A regular user is fenced inside their own subfolder: browse its contents
    // (not the pool root's sibling list) so they only see what they can write.
    const own = folderPicker.sharedDiskOwnFolder;
    folderPicker.path = own;
    const ownData = own ? await api(`/api/shareddisk?path=${encodeURIComponent(own)}`) : rootData;
    folderPicker.items = (ownData.items || []).filter((f) => f.dir);
    renderFolderPicker();
  } catch (err) {
    if (listEl.isConnected) listEl.innerHTML = `<div class="picker-empty">${escapeHtml(err.message)}</div>`;
  }
}

function renderFolderPicker() {
  const listEl = $("#pickerList");
  if (!folderPicker || !listEl) return; // modal closed mid-load
  const path = folderPicker.path;
  const shared = folderPicker.targetDisk === "shareddisk";
  $("#pickerPath").textContent = "/" + path;
  // On the shared disk a regular user is fenced inside their own subfolder:
  // "Up" is disabled once they're at its root (they can't write above it, and
  // requireOwnSharedDiskPath would reject the transfer anyway). An admin may
  // roam the whole pool, so their "Up" only disables at the pool root.
  const sharedRoot = shared && !isAdmin() ? folderPicker.sharedDiskOwnFolder : "";
  $("#pickerUp").disabled = shared ? path === sharedRoot : !path;
  $("#pickerMkdir").disabled = false;

  // A folder being moved cannot be its own destination: show it disabled.
  const sources = new Set(folderPicker.paths);
  const rows = (folderPicker.items || []).map((f) => {
    const blocked = folderPicker.move && sources.has(f.path);
    return `<div class="picker-row${blocked ? " disabled" : ""}"${blocked ? "" : ` data-open="${escapeHtml(f.path)}"`}>` +
      `<span class="picker-folder-icon">${fileIcon(true)}</span>` +
      `<span class="picker-folder-name" title="${escapeHtml(f.name)}">${escapeHtml(f.name)}</span>` +
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
  if (!folderPicker) return;
  const name = prompt(t("netdisk.newFolderNamePrompt"));
  if (!name || !folderPicker) return;
  try {
    const endpoint = folderPicker.targetDisk === "backup" ? "/api/netdisk/backup/mkdir"
      : folderPicker.targetDisk === "shareddisk" ? "/api/shareddisk/mkdir"
      : "/api/netdisk/mkdir";
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

// diskModeOf returns the current view's disk identifier in the "netdisk" |
// "backup" | "shareddisk" vocabulary the transfer/same-disk endpoints use.
function diskModeOf() {
  return isSharedDiskMode() ? "shareddisk" : isBackupMode() ? "backup" : "netdisk";
}

async function copyItems(items, targetDisk) {
  const sourceDisk = diskModeOf();
  // The shared disk has no same-disk copy endpoint of its own — the folder
  // picker never offers it as a target when it's also the source (see
  // openFolderPicker), so sameDisk is never true for sourceDisk "shareddisk".
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
  const sourceDisk = diskModeOf();
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
  const endpoint = isSharedDiskMode() ? "/api/shareddisk/rename" : isBackupMode() ? "/api/netdisk/backup/rename" : "/api/netdisk/rename";
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
// How many times a large file's *entire* chunked upload is automatically
// re-attempted after it fails (network drop, proxy hiccup, etc.) before giving
// up and surfacing the manual Retry button. This is cheap to do: the server
// keeps every chunk it already verified on disk (internal/server/chunkupload.go),
// so re-calling uploadLargeFile just resumes from the first missing chunk
// instead of re-uploading the whole file — a slow/flaky link shouldn't cost the
// user a from-scratch restart.
const LARGE_FILE_MAX_ATTEMPTS = 5;
const LARGE_FILE_RETRY_BASE_MS = 1000; // doubles each attempt: 1s, 2s, 4s, 8s

const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));

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
  // A failure mid-upload (dropped connection, slow-network hiccup) does not
  // immediately surface to the user: it re-drives uploadLargeFile up to
  // LARGE_FILE_MAX_ATTEMPTS times with backoff, and each attempt resumes from
  // the chunks the server already has rather than restarting at byte 0. Only
  // once every attempt is exhausted does the row fall back to a manual Retry.
  async function uploadOneLarge(entry, slot) {
    let lastLoaded = 0;
    let lastErr;
    for (let attempt = 0; attempt < LARGE_FILE_MAX_ATTEMPTS; attempt++) {
      try {
        const res = await uploadLargeFile(entry.file, entry.relPath, {
          base: "/api/netdisk", dir: path, csrfToken,
          onProgress: ({ loaded, total, speedBps }) => {
            const delta = Math.max(0, loaded - lastLoaded);
            lastLoaded = loaded;
            doneBytes += delta;
            overlay.updateActive(slot, {
              loaded, total,
              percent: total > 0 ? Math.min(100, Math.round((loaded / total) * 100)) : 0,
              speedBps,
            });
          },
        });
        // The server verified + assembled the file; settle as done. (res.crc32
        // is the whole-file digest computed during assembly.)
        void res;
        overlay.settleActive(slot, "done");
        ok++;
        return;
      } catch (err) {
        lastErr = err;
        if (attempt < LARGE_FILE_MAX_ATTEMPTS - 1) {
          await sleep(LARGE_FILE_RETRY_BASE_MS * 2 ** attempt);
        }
      }
    }
    failedFiles.push(entry);
    failed++;
    armRetry(slot, entry, lastErr?.message);
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
      return renderMarkdownInto(url, body);
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
