import { state, api, toast, escapeHtml, fmtBytes, isAdmin, canMutate, displayNameForUsername } from "../app.js";
import { showModalNoShell, closeModal } from "./ui.js";
import { uploadWithProgress, showUploadOverlay } from "../lib/upload.js";

export async function renderNetdisk() {
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
      api(`/api/netdisk?path=${encodeURIComponent(state.netdisk.path || "")}`),
      api("/api/netdisk/quota").catch(() => null),
      api("/api/netdisk/shares").catch(() => []),
      isAdmin() ? api("/api/admin/netdisk/shares").catch(() => []) : Promise.resolve([]),
    ]);
    sig = JSON.stringify([list.path, list.items, quota, shares, adminShares]);
    state.netdisk = {
      path: list.path || "",
      items: list.items || [],
      quota,
      shares,
      adminShares,
      selected: state.netdisk?.selected || new Set(),
      sig,
    };
  } catch (err) {
    // A failed background refresh keeps the previous content; only the first
    // load surfaces the error.
    if (firstLoad) {
      $("#view").innerHTML = `<div class="card"><div class="card-body"><div class="error-box">${escapeHtml(err.message)}</div></div></div>`;
    }
    return;
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
  const rows = sortedItems(state.netdisk.items).map((f) => fileRow(f, mutable)).join("") ||
    `<tr class="empty-row"><td colspan="${mutable ? 6 : 5}">No files.</td></tr>`;
  const fileCount = state.netdisk.items.filter((f) => !f.dir).length;
  const folderCount = state.netdisk.items.length - fileCount;
  const quotaHtml = quotaBar(state.netdisk.quota);
  const selectionSize = [...state.netdisk.selected].length;

  $("#view").innerHTML =
    `<div class="stack netdisk-stack">` +
      `<div class="card netdisk-card" id="netdiskCard">` +
        `<div class="netdisk-toolbar">` +
          `<div class="netdisk-title"><h2>My Netdisk</h2><span>${folderCount} folders, ${fileCount} files</span></div>` +
          `<div class="head-tools netdisk-actions">` +
            `<button class="ghost" id="upDir">Up</button>` +
            `<button class="ghost" id="mkdirBtn">New Folder</button>` +
            `<label class="buttonlike"><input id="uploadFiles" type="file" multiple> Upload</label>` +
            `<label class="buttonlike"><input id="uploadFolder" type="file" webkitdirectory directory multiple> Folder</label>` +
            (mutable
              ? `<button class="ghost danger" id="batchDelete" ${selectionSize ? "" : "disabled"}>Delete (${selectionSize})</button>` +
                `<button class="ghost" id="batchCopy" ${selectionSize ? "" : "disabled"}>Copy (${selectionSize})</button>` +
                `<button class="ghost" id="batchMove" ${selectionSize ? "" : "disabled"}>Move (${selectionSize})</button>` +
                `<button class="ghost" id="batchShare" ${selectionSize ? "" : "disabled"}>Share (${selectionSize})</button>` +
                `<button class="ghost" id="batchDownload" ${selectionSize ? "" : "disabled"}>Download (${selectionSize})</button>`
              : "") +
          `</div>` +
        `</div>` +
        `<div class="netdisk-pathbar">` +
          `<div class="netdisk-crumbs">${breadcrumbs(state.netdisk.path)}</div>` +
          `<div class="netdisk-used">${quotaHtml}</div>` +
        `</div>` +
        `<table class="data netdisk-table"><thead><tr>` +
          (mutable ? `<th class="chk-col"><input type="checkbox" id="selectAllFiles"></th>` : "") +
          `<th>Name</th><th class="size-col">Size</th><th class="time-col">Modified</th><th class="actions">Actions</th>` +
        `</tr></thead><tbody>${rows}</tbody></table>` +
      `</div>` +
      `<div class="card"><div class="card-head"><h2>External Links</h2></div>` +
      `<table class="data netdisk-shares"><thead><tr><th class="share-name-col">Name</th><th class="share-link-col">Link</th><th class="share-expires-col">Expires</th><th class="share-access-col">Access</th><th class="actions">Actions</th></tr></thead><tbody>${shareRows(state.netdisk.shares || [])}</tbody></table></div>` +
      (isAdmin() ? `<div class="card"><div class="card-head"><h2>All External Links</h2><div class="head-tools"><label class="check compact-check" for="selectAllShares"><input type="checkbox" id="selectAllShares"><span>Select all</span></label><button class="danger" id="deleteSelectedShares">Delete Selected</button></div></div>` +
      `<table class="data netdisk-admin-shares"><thead><tr><th class="chk-col"></th><th class="share-owner-col">Owner</th><th class="share-name-col">Name</th><th class="share-link-col">Link</th><th class="share-expires-col">Expires</th><th class="share-access-col">Access</th></tr></thead><tbody>${adminShareRows(state.netdisk.adminShares || [])}</tbody></table></div>` : "") +
    `</div>`;

  bindNetdiskEvents(mutable);
}

function bindNetdiskEvents(mutable) {
  $("#upDir").onclick = () => {
    const parts = (state.netdisk.path || "").split("/").filter(Boolean);
    parts.pop();
    state.netdisk.path = parts.join("/");
    state.netdisk.selected = new Set();
    renderNetdisk();
  };

  document.querySelectorAll("[data-crumb]").forEach((btn) => {
    btn.onclick = () => {
      state.netdisk.path = btn.dataset.crumb;
      state.netdisk.selected = new Set();
      renderNetdisk();
    };
  });

  $("#mkdirBtn").onclick = async () => {
    const name = prompt("Folder name");
    if (!name) return;
    await api("/api/netdisk/mkdir", { method: "POST", body: JSON.stringify({ path: joinPath(state.netdisk.path, name) }) });
    toast("Folder created", true);
    renderNetdisk();
  };

  $("#uploadFiles").onchange = async (e) => {
    await uploadFiles(e.target.files, state.netdisk.path || "");
    e.target.value = "";
  };
  $("#uploadFolder").onchange = async (e) => {
    await uploadFiles(e.target.files, state.netdisk.path || "");
    e.target.value = "";
  };

  const card = $("#netdiskCard");
  card.ondragover = (e) => { e.preventDefault(); card.classList.add("drag-over"); };
  card.ondragleave = () => card.classList.remove("drag-over");
  card.ondrop = async (e) => {
    e.preventDefault();
    card.classList.remove("drag-over");
    if (!mutable) return toast("Read-only account cannot upload");
    const files = e.dataTransfer?.files;
    if (files?.length) await uploadFiles(files, state.netdisk.path || "");
  };

  document.querySelectorAll("[data-open]").forEach((btn) => {
    btn.onclick = () => {
      state.netdisk.path = btn.dataset.open;
      state.netdisk.selected = new Set();
      renderNetdisk();
    };
  });

  if (mutable) {
    document.querySelectorAll("[data-del]").forEach((btn) => {
      btn.onclick = async () => {
        if (!confirm(`Delete ${btn.dataset.name}?`)) return;
        await api("/api/netdisk/delete", { method: "POST", body: JSON.stringify({ paths: [btn.dataset.del] }) });
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
        state.netdisk.selected = new Set();
      }
      renderNetdisk();
    };
    $("#batchDelete").onclick = batchDelete;
    $("#batchCopy").onclick = () => batchCopyMove(false);
    $("#batchMove").onclick = () => batchCopyMove(true);
    $("#batchShare").onclick = batchShare;
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

function fileRow(f, mutable) {
  const href = downloadURL(f.path);
  const checked = state.netdisk.selected.has(f.path) ? "checked" : "";
  const name = f.dir
    ? `<button class="linklike netdisk-name-link" data-open="${escapeHtml(f.path)}">${escapeHtml(f.name)}</button>`
    : `<a class="netdisk-name-link" href="${href}">${escapeHtml(f.name)}</a>`;
  const checkCell = mutable
    ? `<td class="chk-col"><input type="checkbox" class="netdisk-row-check" data-path="${escapeHtml(f.path)}" ${checked}></td>`
    : "";
  const actionCell = mutable
    ? `<a class="icon" href="${href}" title="Download">⬇</a>` +
      `<button class="icon" title="Rename" data-ren="${escapeHtml(f.path)}" data-name="${escapeHtml(f.name)}">✎</button>` +
      `<button class="icon" title="Copy" data-copy="${escapeHtml(f.path)}">⧉</button>` +
      `<button class="icon" title="Move" data-move="${escapeHtml(f.path)}">↗</button>` +
      `<button class="icon" title="Share" data-share="${escapeHtml(f.path)}" data-name="${escapeHtml(f.name)}">⤴</button>` +
      `<button class="icon danger" title="Delete" data-del="${escapeHtml(f.path)}" data-name="${escapeHtml(f.name)}">✕</button>`
    : `<a class="icon" href="${href}" title="Download">⬇</a>`;
  return `<tr>${checkCell}<td><div class="netdisk-file"><span class="netdisk-icon ${f.dir ? "folder" : "file"}" aria-hidden="true">${fileIcon(f.dir)}</span><div class="primary-line">${name}</div></div></td><td class="netdisk-size">${f.dir ? "-" : fmtBytes(f.size)}</td><td class="netdisk-time" title="${escapeHtml(new Date(f.modTime).toLocaleString())}">${escapeHtml(fmtDate(f.modTime))}</td><td class="actions netdisk-row-actions">${actionCell}</td></tr>`;
}

function toggleFileSelection(path, checked) {
  if (checked) state.netdisk.selected.add(path);
  else state.netdisk.selected.delete(path);
  renderNetdisk();
}

async function batchDelete() {
  const paths = [...state.netdisk.selected];
  if (!paths.length) return;
  if (!confirm(`Delete ${paths.length} item(s)?`)) return;
  await api("/api/netdisk/delete", { method: "POST", body: JSON.stringify({ paths }) });
  state.netdisk.selected = new Set();
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
  folderPicker = { move, paths, path: state.netdisk.path || "", items: [] };
  const action = move ? "Move" : "Copy";
  showModalNoShell(
    "netdisk-picker",
    "wide picker-modal",
    `<div class="modal-head">` +
      `<div><h2>${action} ${paths.length} item(s)</h2><p class="hint" style="margin-top:4px">Choose a destination folder</p></div>` +
      `<button class="icon" data-close>✕</button>` +
    `</div>` +
    `<div class="picker-layout">` +
      `<aside class="picker-tree"><div class="picker-tree-item active">My Netdisk</div></aside>` +
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
    const data = await api(`/api/netdisk?path=${encodeURIComponent(path)}`);
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
    await api("/api/netdisk/mkdir", { method: "POST", body: JSON.stringify({ path: joinPath(folderPicker.path, name) }) });
    toast("Folder created", true);
    await loadPickerFolder(folderPicker.path);
  } catch (err) {
    toast(err.message);
  }
}

async function confirmFolderPicker() {
  if (!folderPicker) return;
  const { move, paths, path } = folderPicker;
  folderPicker = null;
  closeModal();
  const items = paths.map((p) => ({ from: p, to: path }));
  if (move) await moveItems(items);
  else await copyItems(items);
}

function parentDir(p) {
  return (p || "").split("/").filter(Boolean).slice(0, -1).join("/");
}

async function copyItems(items) {
  const data = await api("/api/netdisk/copy", {
    method: "POST",
    body: JSON.stringify({ items, move: false, policy: "rename" }),
  });
  state.netdisk.selected = new Set();
  const errors = (data.results || []).filter((r) => r.status === "error").length;
  toast(errors ? `Copied ${data.count || 0} item(s), ${errors} failed` : `Copied ${data.count || 0} item(s)`, !errors);
  renderNetdisk();
}

async function moveItems(items) {
  const data = await api("/api/netdisk/copy", {
    method: "POST",
    body: JSON.stringify({ items, move: true, policy: "rename" }),
  });
  state.netdisk.selected = new Set();
  const errors = (data.results || []).filter((r) => r.status === "error").length;
  toast(errors ? `Moved ${data.count || 0} item(s), ${errors} failed` : `Moved ${data.count || 0} item(s)`, !errors);
  renderNetdisk();
}

async function batchShare() {
  const paths = [...state.netdisk.selected];
  if (!paths.length) return;
  const name = paths.length === 1 ? paths[0].split("/").pop() : "Shared items";
  openShareModal(paths, name);
}

async function batchDownload() {
  const paths = [...state.netdisk.selected];
  if (!paths.length) return;
  if (paths.length === 1 && !state.netdisk.items.find((f) => f.path === paths[0])?.dir) {
    location.href = downloadURL(paths[0]);
    return;
  }
  // Multiple items: download each as a separate zip/file.
  paths.forEach((p, i) => {
    setTimeout(() => {
      const a = document.createElement("a");
      a.href = downloadURL(p);
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
  await api("/api/netdisk/rename", { method: "POST", body: JSON.stringify({ from: path, to }) });
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

async function uploadFiles(files, path) {
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
        csrfToken: state.csrfToken,
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
  renderNetdisk();
  // Let the user see the final per-file state before the card disappears.
  setTimeout(() => overlay.close(), failed ? 3000 : 1500);
  uploading = false;
}

function quotaBar(q) {
  if (!q) return `Used <strong>0 B</strong>`;
  const used = q.usedBytes || 0;
  if (q.totalBytes > 0) {
    const pct = Math.min(100, Math.round((used / q.totalBytes) * 100));
    return `Used <strong>${fmtBytes(used)}</strong> / ${fmtBytes(q.totalBytes)} <span class="quota-bar"><span style="width:${pct}%"></span></span> ${pct}%`;
  }
  if (q.diskFreeBytes != null) {
    return `Used <strong>${fmtBytes(used)}</strong> · Free ${fmtBytes(q.diskFreeBytes)}`;
  }
  return `Used <strong>${fmtBytes(used)}</strong>`;
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

function $(selector) {
  return document.querySelector(selector);
}
