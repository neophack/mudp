import { state, api, toast, escapeHtml, isAdmin } from "../app.js";

export async function renderNetdisk() {
  $("#view").innerHTML = `<div class="card"><div class="card-body"><p class="hint">Loading files...</p></div></div>`;
  try {
    const [list, quota, shares, adminShares] = await Promise.all([
      api(`/api/netdisk?path=${encodeURIComponent(state.netdisk.path || "")}`),
      api("/api/netdisk/quota").catch(() => null),
      api("/api/netdisk/shares").catch(() => []),
      isAdmin() ? api("/api/admin/netdisk/shares").catch(() => []) : Promise.resolve([]),
    ]);
    state.netdisk = { path: list.path || "", items: list.items || [], quota, shares, adminShares };
  } catch (err) {
    $("#view").innerHTML = `<div class="card"><div class="card-body"><div class="error-box">${escapeHtml(err.message)}</div></div></div>`;
    return;
  }
  const rows = sortedItems(state.netdisk.items).map(fileRow).join("") || `<tr class="empty-row"><td colspan="5">No files.</td></tr>`;
  const fileCount = state.netdisk.items.filter((f) => !f.dir).length;
  const folderCount = state.netdisk.items.length - fileCount;
  $("#view").innerHTML =
    `<div class="stack netdisk-stack">` +
      `<div class="card netdisk-card"><div class="netdisk-toolbar">` +
        `<div class="netdisk-title"><h2>我的网盘</h2><span>${folderCount} folders, ${fileCount} files</span></div>` +
        `<div class="head-tools netdisk-actions">` +
          `<button class="ghost" id="upDir">Up</button>` +
          `<button class="ghost" id="mkdirBtn">New Folder</button>` +
          `<label class="buttonlike"><input id="uploadFiles" type="file" multiple> Upload</label>` +
        `</div>` +
      `</div>` +
      `<div class="netdisk-pathbar">` +
        `<div class="netdisk-crumbs">${breadcrumbs(state.netdisk.path)}</div>` +
        `<div class="netdisk-used">Used <strong>${fmtBytes(state.netdisk.quota?.usedBytes || 0)}</strong></div>` +
      `</div>` +
      `<table class="data netdisk-table"><thead><tr><th>Name</th><th>Size</th><th>Modified</th><th class="actions">Actions</th></tr></thead><tbody>${rows}</tbody></table></div>` +
      `<div class="card"><div class="card-head"><h2>External Links</h2><label class="check"><input type="checkbox" id="permanentShare"> Permanent new links</label></div>` +
      `<table class="data"><thead><tr><th>Name</th><th>Link</th><th>Expires</th><th class="actions">Actions</th></tr></thead><tbody>${shareRows(state.netdisk.shares || [])}</tbody></table></div>` +
      (isAdmin() ? `<div class="card"><div class="card-head"><h2>All External Links</h2><div class="head-tools"><label class="check compact-check" for="selectAllShares"><input type="checkbox" id="selectAllShares"><span>Select all</span></label><button class="danger" id="deleteSelectedShares">Delete Selected</button></div></div>` +
      `<table class="data"><thead><tr><th class="chk-col"></th><th>Owner</th><th>Name</th><th>Link</th><th>Expires</th></tr></thead><tbody>${adminShareRows(state.netdisk.adminShares || [])}</tbody></table></div>` : "") +
    `</div>`;
  $("#upDir").onclick = () => {
    const parts = (state.netdisk.path || "").split("/").filter(Boolean);
    parts.pop();
    state.netdisk.path = parts.join("/");
    renderNetdisk();
  };
  document.querySelectorAll("[data-crumb]").forEach((btn) => {
    btn.onclick = () => {
      state.netdisk.path = btn.dataset.crumb;
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
  $("#uploadFiles").onchange = async (e) => uploadFiles(e.target.files);
  document.querySelectorAll("[data-open]").forEach((btn) => {
    btn.onclick = () => {
      state.netdisk.path = btn.dataset.open;
      renderNetdisk();
    };
  });
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
  document.querySelectorAll("[data-share]").forEach((btn) => {
    btn.onclick = async () => createShare([btn.dataset.share], btn.dataset.name);
  });
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

function fileRow(f) {
  const href = downloadURL(f.path);
  const name = f.dir
    ? `<button class="linklike netdisk-name-link" data-open="${escapeHtml(f.path)}">${escapeHtml(f.name)}</button>`
    : `<a class="netdisk-name-link" href="${href}">${escapeHtml(f.name)}</a>`;
  return `<tr><td><div class="netdisk-file"><span class="netdisk-icon ${f.dir ? "folder" : "file"}" aria-hidden="true">${fileIcon(f.dir)}</span><div class="primary-line">${name}</div></div></td><td class="netdisk-size">${f.dir ? "-" : fmtBytes(f.size)}</td><td class="netdisk-time">${escapeHtml(new Date(f.modTime).toLocaleString())}</td><td class="actions netdisk-row-actions"><a class="ghost-link" href="${href}">Download</a><button class="ghost" data-ren="${escapeHtml(f.path)}" data-name="${escapeHtml(f.name)}">Rename</button><button class="ghost" data-share="${escapeHtml(f.path)}" data-name="${escapeHtml(f.name)}">Share</button><button class="icon danger" data-del="${escapeHtml(f.path)}" data-name="${escapeHtml(f.name)}">Del</button></td></tr>`;
}

function sortedItems(items) {
  return [...(items || [])].sort((a, b) => {
    if (a.dir !== b.dir) return a.dir ? -1 : 1;
    return String(a.name || "").localeCompare(String(b.name || ""), undefined, { numeric: true, sensitivity: "base" });
  });
}

function breadcrumbs(path) {
  const parts = (path || "").split("/").filter(Boolean);
  const crumbs = [`<button class="linklike" data-crumb="">全部文件</button>`];
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
    return `<tr class="${s.expired ? "row-muted" : ""}"><td><div class="primary-line">${escapeHtml(s.name)}</div><div class="secondary-line">${(s.paths || []).map(escapeHtml).join(", ")}</div></td><td><a href="${link}" target="_blank">${escapeHtml(link)}</a></td><td>${shareExpiry(s)}</td><td class="actions"><button class="ghost" data-copy-link="${escapeHtml(link)}">Copy</button><button class="icon danger" data-share-del="${escapeHtml(s.token)}">Del</button></td></tr>`;
  }).join("") || `<tr class="empty-row"><td colspan="4">No external links.</td></tr>`;
}

function adminShareRows(shares) {
  return (shares || []).map((s) => {
    const link = `${location.origin}/pan/${encodeURIComponent(s.token)}`;
    return `<tr class="${s.expired ? "row-muted" : ""}"><td><input type="checkbox" data-admin-share-token value="${escapeHtml(s.token)}"></td><td>${escapeHtml(s.owner || s.ownerId)}</td><td><div class="primary-line">${escapeHtml(s.name)}</div><div class="secondary-line">${(s.paths || []).map(escapeHtml).join(", ")}</div></td><td><a href="${link}" target="_blank">${escapeHtml(link)}</a></td><td>${shareExpiry(s)}</td></tr>`;
  }).join("") || `<tr class="empty-row"><td colspan="5">No external links.</td></tr>`;
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
  const out = await api("/api/netdisk/share", { method: "POST", body: JSON.stringify({ paths, name, permanent }) });
  const link = `${location.origin}${out.url}`;
  await navigator.clipboard?.writeText(link).catch(() => {});
  toast("External link created", true);
  renderNetdisk();
}

async function uploadFiles(files) {
  if (!files || !files.length) return;
  const fd = new FormData();
  fd.append("path", state.netdisk.path || "");
  [...files].forEach((f) => fd.append("files", f, f.name));
  const res = await fetch("/api/netdisk/upload", { method: "POST", credentials: "same-origin", body: fd });
  const data = await res.json().catch(() => ({}));
  if (!res.ok) {
    toast(data.error || "Upload failed");
    return;
  }
  toast(`Uploaded ${data.count || files.length} file(s)`, true);
  renderNetdisk();
}

function joinPath(a, b) {
  return [a, b].filter(Boolean).join("/");
}

function downloadURL(path) {
  return `/api/netdisk/download?path=${encodeURIComponent(path)}&ts=${Date.now()}`;
}

function fmtBytes(n) {
  if (!n) return "0 B";
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
  if (n < 1024 * 1024 * 1024) return `${(n / 1024 / 1024).toFixed(1)} MB`;
  return `${(n / 1024 / 1024 / 1024).toFixed(1)} GB`;
}

function $(selector) {
  return document.querySelector(selector);
}
