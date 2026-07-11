import { state, api, toast, escapeHtml, fmtBytes, isAdmin, canMutate, displayNameForUsername } from "../app.js";

export async function renderNetdisk() {
  $("#view").innerHTML = `<div class="card"><div class="card-body"><p class="hint">Loading files...</p></div></div>`;
  try {
    const [list, quota, shares, adminShares] = await Promise.all([
      api(`/api/netdisk?path=${encodeURIComponent(state.netdisk.path || "")}`),
      api("/api/netdisk/quota").catch(() => null),
      api("/api/netdisk/shares").catch(() => []),
      isAdmin() ? api("/api/admin/netdisk/shares").catch(() => []) : Promise.resolve([]),
    ]);
    state.netdisk = {
      path: list.path || "",
      items: list.items || [],
      quota,
      shares,
      adminShares,
      selected: state.netdisk?.selected || new Set(),
    };
  } catch (err) {
    $("#view").innerHTML = `<div class="card"><div class="card-body"><div class="error-box">${escapeHtml(err.message)}</div></div></div>`;
    return;
  }

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
          `<th>Name</th><th>Size</th><th>Modified</th><th class="actions">Actions</th>` +
        `</tr></thead><tbody>${rows}</tbody></table>` +
      `</div>` +
      `<div class="card"><div class="card-head"><h2>External Links</h2></div>` +
      `<table class="data"><thead><tr><th>Name</th><th>Link</th><th>Expires</th><th>Access</th><th class="actions">Actions</th></tr></thead><tbody>${shareRows(state.netdisk.shares || [])}</tbody></table></div>` +
      (isAdmin() ? `<div class="card"><div class="card-head"><h2>All External Links</h2><div class="head-tools"><label class="check compact-check" for="selectAllShares"><input type="checkbox" id="selectAllShares"><span>Select all</span></label><button class="danger" id="deleteSelectedShares">Delete Selected</button></div></div>` +
      `<table class="data"><thead><tr><th class="chk-col"></th><th>Owner</th><th>Name</th><th>Link</th><th>Expires</th><th>Access</th></tr></thead><tbody>${adminShareRows(state.netdisk.adminShares || [])}</tbody></table></div>` : "") +
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
      btn.onclick = async () => copyItems([{ from: btn.dataset.copy, to: state.netdisk.path }]);
    });
    document.querySelectorAll("[data-move]").forEach((btn) => {
      btn.onclick = async () => {
        const dest = prompt("Move to folder", state.netdisk.path || "");
        if (dest == null) return;
        await moveItems([{ from: btn.dataset.move, to: dest }]);
      };
    });
    document.querySelectorAll("[data-share]").forEach((btn) => {
      btn.onclick = async () => createShare([btn.dataset.share], btn.dataset.name);
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
      await navigator.clipboard.writeText(btn.dataset.copyLink);
      toast("Link copied", true);
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
    ? `<a class="ghost-link" href="${href}">Download</a>` +
      `<button class="ghost" data-ren="${escapeHtml(f.path)}" data-name="${escapeHtml(f.name)}">Rename</button>` +
      `<button class="ghost" data-copy="${escapeHtml(f.path)}">Copy</button>` +
      `<button class="ghost" data-move="${escapeHtml(f.path)}">Move</button>` +
      `<button class="ghost" data-share="${escapeHtml(f.path)}" data-name="${escapeHtml(f.name)}">Share</button>` +
      `<button class="icon danger" data-del="${escapeHtml(f.path)}" data-name="${escapeHtml(f.name)}">Del</button>`
    : `<a class="ghost-link" href="${href}">Download</a>`;
  return `<tr>${checkCell}<td><div class="netdisk-file"><span class="netdisk-icon ${f.dir ? "folder" : "file"}" aria-hidden="true">${fileIcon(f.dir)}</span><div class="primary-line">${name}</div></div></td><td class="netdisk-size">${f.dir ? "-" : fmtBytes(f.size)}</td><td class="netdisk-time">${escapeHtml(new Date(f.modTime).toLocaleString())}</td><td class="actions netdisk-row-actions">${actionCell}</td></tr>`;
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

async function batchCopyMove(move) {
  const paths = [...state.netdisk.selected];
  if (!paths.length) return;
  const dest = prompt(move ? "Move selected items to folder" : "Copy selected items to folder", state.netdisk.path || "");
  if (dest == null) return;
  const items = paths.map((p) => ({ from: p, to: dest }));
  if (move) await moveItems(items);
  else await copyItems(items);
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
  const name = prompt("Share name", paths.length === 1 ? paths[0].split("/").pop() : "Shared items");
  if (!name) return;
  await createShare(paths, name);
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
    const access = s.hasPassword ? "Password" : "Public";
    return `<tr class="${s.expired ? "row-muted" : ""}"><td><div class="primary-line">${escapeHtml(s.name)}</div><div class="secondary-line">${(s.paths || []).map(escapeHtml).join(", ")}</div></td><td><a href="${link}" target="_blank">${escapeHtml(link)}</a></td><td>${shareExpiry(s)}</td><td>${escapeHtml(access)}</td><td class="actions"><button class="ghost" data-copy-link="${escapeHtml(link)}">Copy</button><button class="icon danger" data-share-del="${escapeHtml(s.token)}">Del</button></td></tr>`;
  }).join("") || `<tr class="empty-row"><td colspan="5">No external links.</td></tr>`;
}

function adminShareRows(shares) {
  return (shares || []).map((s) => {
    const link = `${location.origin}/pan/${encodeURIComponent(s.token)}`;
    const access = s.hasPassword ? "Password" : "Public";
    return `<tr class="${s.expired ? "row-muted" : ""}"><td><input type="checkbox" data-admin-share-token value="${escapeHtml(s.token)}"></td><td>${escapeHtml(displayNameForUsername(s.owner) || s.ownerId)}</td><td><div class="primary-line">${escapeHtml(s.name)}</div><div class="secondary-line">${(s.paths || []).map(escapeHtml).join(", ")}</div></td><td><a href="${link}" target="_blank">${escapeHtml(link)}</a></td><td>${shareExpiry(s)}</td><td>${escapeHtml(access)}</td></tr>`;
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

async function createShare(paths, name) {
  const permanent = !!$("#permanentShare")?.checked;
  const days = prompt("Link expires in days (0 = permanent)", permanent ? "0" : "7");
  if (days == null) return;
  const expiresDays = parseInt(days, 10) || 0;
  const password = prompt("Optional password (leave empty for public link)", "");
  if (password == null) return;
  const body = { paths, name };
  if (expiresDays > 0) {
    body.expiresDays = expiresDays;
  } else {
    body.permanent = true;
  }
  if (password) body.password = password;
  const out = await api("/api/netdisk/share", { method: "POST", body: JSON.stringify(body) });
  const link = `${location.origin}${out.url}`;
  await navigator.clipboard?.writeText(link).catch(() => {});
  toast("External link created", true);
  renderNetdisk();
}

async function uploadFiles(files, path) {
  if (!files || !files.length) return;
  if (!canMutate()) return toast("Read-only account cannot upload");
  const fd = new FormData();
  fd.append("path", path);
  [...files].forEach((f) => {
    // webkitdirectory supplies webkitRelativePath; fall back to name.
    const name = f.webkitRelativePath || f.name;
    fd.append("files", f, name);
  });
  const headers = state.csrfToken ? { "X-CSRF-Token": state.csrfToken } : undefined;
  const res = await fetch("/api/netdisk/upload", { method: "POST", credentials: "same-origin", headers, body: fd });
  const data = await res.json().catch(() => ({}));
  if (!res.ok) {
    toast(data.error || "Upload failed");
    return;
  }
  toast(`Uploaded ${data.count || files.length} file(s)`, true);
  renderNetdisk();
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

function downloadURL(path) {
  return `/api/netdisk/download?path=${encodeURIComponent(path)}&ts=${Date.now()}`;
}

function $(selector) {
  return document.querySelector(selector);
}
