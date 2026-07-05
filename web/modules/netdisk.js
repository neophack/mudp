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
  const rows = state.netdisk.items.map(fileRow).join("") || `<tr class="empty-row"><td colspan="5">No files.</td></tr>`;
  $("#view").innerHTML =
    `<div class="stack">` +
      `<div class="card"><div class="card-head"><h2>Files</h2><div class="head-tools">` +
        `<button class="ghost" id="upDir">Up</button>` +
        `<button class="ghost" id="mkdirBtn">New Folder</button>` +
        `<label class="buttonlike"><input id="uploadFiles" type="file" multiple> Upload</label>` +
      `</div></div>` +
      `<div class="card-body"><div class="kv"><span>Path</span><strong class="mono">/${escapeHtml(state.netdisk.path || "")}</strong></div>` +
      `<div class="kv"><span>Used</span><strong>${fmtBytes(state.netdisk.quota?.usedBytes || 0)}</strong></div></div>` +
      `<table class="data"><thead><tr><th>Name</th><th>Type</th><th>Size</th><th>Modified</th><th class="actions">Actions</th></tr></thead><tbody>${rows}</tbody></table></div>` +
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
    ? `<button class="linklike" data-open="${escapeHtml(f.path)}">${escapeHtml(f.name)}</button>`
    : `<a href="${href}">${escapeHtml(f.name)}</a>`;
  return `<tr><td><div class="primary-line">${name}</div></td><td>${f.dir ? "Folder" : "File"}</td><td>${f.dir ? "-" : fmtBytes(f.size)}</td><td>${escapeHtml(new Date(f.modTime).toLocaleString())}</td><td class="actions"><a class="ghost-link" href="${href}">Download</a><button class="ghost" data-ren="${escapeHtml(f.path)}" data-name="${escapeHtml(f.name)}">Rename</button><button class="ghost" data-share="${escapeHtml(f.path)}" data-name="${escapeHtml(f.name)}">Share</button><button class="icon danger" data-del="${escapeHtml(f.path)}" data-name="${escapeHtml(f.name)}">Del</button></td></tr>`;
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
