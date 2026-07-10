// Standalone public share page. Baidu-Netdisk-style browsing + save-to-directory.

const state = {
  token: "",
  path: "",
  items: [],
  selected: new Set(),
  share: null,
  me: null,
  pickerPath: "",
  pickerItems: [],
};

const $ = (s) => document.querySelector(s);

async function api(path, opts = {}) {
  const res = await fetch(path, {
    credentials: "same-origin",
    headers: { "Content-Type": "application/json", ...(opts.headers || {}) },
    ...opts,
  });
  const data = await res.json().catch(() => ({}));
  if (!res.ok) {
    const err = new Error(data.error || res.statusText);
    err.status = res.status;
    throw err;
  }
  return data;
}

function fmtBytes(n) {
  if (!n) return "0 B";
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
  if (n < 1024 * 1024 * 1024) return `${(n / 1024 / 1024).toFixed(1)} MB`;
  return `${(n / 1024 / 1024 / 1024).toFixed(1)} GB`;
}

function escapeHtml(v) {
  return String(v ?? "").replace(/[&<>"']/g, (m) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[m]));
}

function getToken() {
  const meta = document.querySelector('meta[name="share-token"]');
  return meta?.content || window.__SHARE_TOKEN__ || location.pathname.split("/").pop();
}

function joinPath(a, b) {
  return [a, b].filter((x) => x != null && x !== "").join("/");
}

const FOLDER_SVG = '<svg viewBox="0 0 24 24"><path d="M3 6.8C3 5.8 3.8 5 4.8 5h5.1l2 2.2h7.3c1 0 1.8.8 1.8 1.8v1H3V6.8Z"/><path d="M3 9h18l-1.2 8.2c-.1 1-1 1.8-2 1.8H6.2c-1 0-1.8-.7-2-1.8L3 9Z"/></svg>';
const FILE_SVG = '<svg viewBox="0 0 24 24"><path d="M6 3.5h8.4L19 8.1v12.1c0 1-.8 1.8-1.8 1.8H6.8c-1 0-1.8-.8-1.8-1.8V5.3c0-1 .8-1.8 1.8-1.8Z"/><path d="M14 3.8V8h4.2"/><path d="M8.5 12h7M8.5 15h7M8.5 18h4.5"/></svg>';

function iconFor(item) {
  const cls = item.dir ? "share-ico folder" : "share-ico file";
  const svg = item.dir ? FOLDER_SVG : FILE_SVG;
  return `<span class="${cls}" aria-hidden="true">${svg}</span>`;
}

// ---------- Auth ----------

async function loadMe() {
  try {
    state.me = await api("/api/me");
    if (state.me && state.me.authenticated !== false) {
      $("#loginBtn").hidden = true;
      $("#userLabel").textContent = state.me.username;
      $("#userLabel").hidden = false;
      $("#saveSelectedBtn").disabled = state.selected.size === 0;
    } else {
      state.me = null;
      $("#loginBtn").hidden = false;
      $("#userLabel").hidden = true;
      $("#saveSelectedBtn").disabled = true;
    }
  } catch {
    state.me = null;
    $("#loginBtn").hidden = false;
    $("#userLabel").hidden = true;
    $("#saveSelectedBtn").disabled = true;
  }
}

// ---------- Share listing ----------

async function loadShare(path = "") {
  state.path = path;
  $("#shareBody").innerHTML = `<div class="share-loading">Loading file list…</div>`;
  try {
    const data = await api(`/api/netdisk/share/public?token=${encodeURIComponent(state.token)}&path=${encodeURIComponent(path)}`);
    state.share = data.share;
    state.items = data.items || [];
    renderShareMeta();
    renderBreadcrumb(path);
    renderItems();
  } catch (e) {
    $("#shareBody").innerHTML = `<div class="share-error">${escapeHtml(e.message)}</div>`;
    $("#shareName").textContent = "Load failed";
  }
}

function renderShareMeta() {
  const s = state.share;
  if (!s) return;
  $("#shareName").textContent = s.name;
  $("#shareOwner").textContent = `Shared by: ${escapeHtml(s.owner || "Unknown")}`;
  $("#shareExpiry").innerHTML = s.permanent
    ? `<span class="badge badge-accent">Permanent</span>`
    : s.expired
      ? `<span class="badge badge-danger">Expired</span>`
      : `Expires: ${escapeHtml(s.expiresAt ? new Date(s.expiresAt).toLocaleString() : "7 days")}`;
  const total = state.items.reduce((sum, f) => sum + (f.size || 0), 0);
  const dirs = state.items.filter((f) => f.dir).length;
  const files = state.items.length - dirs;
  $("#shareStats").textContent = `Total ${state.items.length} items · ${fmtBytes(total)}${dirs ? ` · ${dirs} folder(s)` : ""}${files ? ` · ${files} file(s)` : ""}`;
}

function renderBreadcrumb(path) {
  const parts = path.split("/").filter(Boolean);
  const home = document.createElement("button");
  home.className = "linklike";
  home.textContent = "All Files";
  home.onclick = () => loadShare("");
  const nav = $("#breadcrumb");
  nav.innerHTML = "";
  nav.appendChild(home);
  let built = "";
  parts.forEach((part, i) => {
    built = joinPath(built, part);
    const isLast = i === parts.length - 1;
    const sep = document.createElement("span");
    sep.className = "sep";
    sep.textContent = "/";
    nav.appendChild(sep);
    if (isLast) {
      const span = document.createElement("span");
      span.textContent = escapeHtml(part);
      nav.appendChild(span);
    } else {
      const btn = document.createElement("button");
      btn.className = "linklike";
      btn.textContent = escapeHtml(part);
      btn.onclick = () => loadShare(built);
      nav.appendChild(btn);
    }
  });
}

function renderItems() {
  if (!state.items.length) {
    $("#shareBody").innerHTML = "";
    $("#shareEmpty").hidden = false;
    return;
  }
  $("#shareEmpty").hidden = true;
  const rows = state.items.map((f) => {
    const checked = state.selected.has(f.path) ? "checked" : "";
    const nameCell = f.dir
      ? `<button class="share-name linklike" data-open="${escapeHtml(f.path)}">${iconFor(f)}<span class="share-name-text">${escapeHtml(f.name)}</span></button>`
      : `<span class="share-name">${iconFor(f)}<span class="share-name-text">${escapeHtml(f.name)}</span></span>`;
    return `<tr data-path="${escapeHtml(f.path)}">
      <td class="chk-cell"><input type="checkbox" class="chk share-select" data-path="${escapeHtml(f.path)}" ${checked}></td>
      <td>${nameCell}</td>
      <td class="share-size">${f.dir ? "-" : fmtBytes(f.size)}</td>
      <td class="share-time">${escapeHtml(new Date(f.modTime).toLocaleString())}</td>
      <td class="actions"><a class="ghost-link" href="/api/netdisk/share/download?token=${encodeURIComponent(state.token)}&path=${encodeURIComponent(f.path)}&ts=${Date.now()}">Download</a></td>
    </tr>`;
  }).join("");
  $("#shareBody").innerHTML = `<table class="data share-table"><thead><tr><th class="chk-col"><input type="checkbox" class="chk" id="selectAllBox"></th><th>Name</th><th>Size</th><th>Modified</th><th class="actions">Actions</th></tr></thead><tbody>${rows}</tbody></table>`;

  // Bind events
  document.querySelectorAll("[data-open]").forEach((btn) => {
    btn.onclick = () => loadShare(btn.dataset.open);
  });
  document.querySelectorAll(".share-select").forEach((cb) => {
    cb.onchange = () => toggleSelection(cb.dataset.path, cb.checked);
  });
  const allSelected = state.items.length > 0 && state.items.every((f) => state.selected.has(f.path));
  const selectAllBox = $("#selectAllBox");
  if (selectAllBox) {
    selectAllBox.checked = allSelected;
    selectAllBox.onchange = (e) => {
      state.items.forEach((f) => {
        if (e.target.checked) state.selected.add(f.path);
        else state.selected.delete(f.path);
      });
      renderItems();
      updateSelectionBar();
    };
  }
  const selectAllToolbar = $("#selectAll");
  if (selectAllToolbar) selectAllToolbar.checked = allSelected;
}

function toggleSelection(path, checked) {
  if (checked) state.selected.add(path);
  else state.selected.delete(path);
  renderItems();
  updateSelectionBar();
}

function updateSelectionBar() {
  const count = state.selected.size;
  const btn = $("#saveSelectedBtn");
  btn.disabled = !state.me || count === 0;
  btn.textContent = count ? `Save to Netdisk (${count} selected)` : "Save to Netdisk";
}

// ---------- Directory picker ----------

function openPicker() {
  if (!state.me) {
    $("#loginBackdrop").hidden = false;
    return;
  }
  if (state.selected.size === 0) {
    toast("Please select at least one item");
    return;
  }
  state.pickerPath = "";
  $("#pickerBackdrop").hidden = false;
  loadPicker("");
}

async function loadPicker(path) {
  state.pickerPath = path;
  try {
    const data = await api(`/api/netdisk?path=${encodeURIComponent(path)}`);
    state.pickerItems = (data.items || []).filter((f) => f.dir);
    renderPicker();
  } catch (e) {
    $("#pickerList").innerHTML = `<div class="share-error">${escapeHtml(e.message)}</div>`;
  }
}

function renderPicker() {
  const path = state.pickerPath;
  $("#pickerPath").textContent = "/" + (path || "");
  $("#pickerUp").disabled = !path;

  // Tree sidebar: quick roots (could be expanded later; keep it simple for now).
  $("#pickerTree").innerHTML = `<div class="picker-tree-item active">My Netdisk</div>`;

  // Main list
  if (!state.pickerItems.length) {
    $("#pickerList").innerHTML = `<div class="share-empty">This folder is empty</div>`;
  } else {
    const rows = state.pickerItems.map((f) => {
      return `<div class="picker-row" data-open="${escapeHtml(f.path)}">
        <span class="picker-folder-icon">${FOLDER_SVG}</span>
        <span class="picker-folder-name">${escapeHtml(f.name)}</span>
      </div>`;
    }).join("");
    $("#pickerList").innerHTML = rows;
    document.querySelectorAll("#pickerList .picker-row").forEach((row) => {
      row.onclick = () => loadPicker(row.dataset.open);
    });
  }

  const defaultName = state.share?.name || "From Share";
  $("#pickerHint").textContent = `Will save to: /${escapeHtml(path || "")}/${escapeHtml(defaultName)}`;
}

async function pickerMkdir() {
  const name = prompt("New folder name");
  if (!name) return;
  const newPath = joinPath(state.pickerPath, name);
  try {
    await api("/api/netdisk/mkdir", { method: "POST", body: JSON.stringify({ path: newPath }) });
    toast("Folder created", true);
    await loadPicker(state.pickerPath);
  } catch (e) {
    toast(e.message);
  }
}

async function confirmSave() {
  const defaultName = state.share?.name || "From Share";
  const to = joinPath(state.pickerPath, defaultName);
  const paths = [...state.selected];
  const btn = $("#confirmPicker");
  btn.disabled = true;
  btn.classList.add("is-loading");
  try {
    const data = await api("/api/netdisk/share/save", {
      method: "POST",
      body: JSON.stringify({ token: state.token, paths, to, policy: "rename" }),
    });
    const saved = data.count || 0;
    const errors = (data.results || []).filter((r) => r.status === "error").length;
    if (errors) {
      toast(`Save complete: ${saved} succeeded, ${errors} failed`);
    } else {
      toast(`Successfully saved ${saved} items to netdisk`, true);
    }
    closePicker();
  } catch (e) {
    toast(e.message);
  } finally {
    btn.disabled = false;
    btn.classList.remove("is-loading");
  }
}

function closePicker() {
  $("#pickerBackdrop").hidden = true;
}

// ---------- Login prompt ----------

function openLogin() {
  $("#loginBackdrop").hidden = false;
}

function closeLogin() {
  $("#loginBackdrop").hidden = true;
}

function goLogin() {
  location.href = "/?redirect=" + encodeURIComponent(location.pathname + location.search);
}

// ---------- Toast ----------

function toast(msg, ok = false) {
  const el = document.createElement("div");
  el.className = `toast ${ok ? "ok" : ""}`;
  el.textContent = msg;
  $("#toastContainer").appendChild(el);
  setTimeout(() => el.remove(), 3400);
}

// ---------- Init ----------

async function init() {
  state.token = getToken();
  if (!state.token) {
    $("#shareBody").innerHTML = `<div class="share-error">Invalid share link</div>`;
    return;
  }
  await loadMe();
  await loadShare();

  bindEvents();
}

function bindEvents() {
  $("#selectAll").onchange = (e) => {
    state.items.forEach((f) => {
      if (e.target.checked) state.selected.add(f.path);
      else state.selected.delete(f.path);
    });
    renderItems();
    updateSelectionBar();
  };

  $("#saveSelectedBtn").onclick = openPicker;
  $("#downloadAllBtn").onclick = () => {
    if (!state.items.length) return;
    if (state.items.length === 1) {
      location.href = `/api/netdisk/share/download?token=${encodeURIComponent(state.token)}&path=${encodeURIComponent(state.items[0].path)}&ts=${Date.now()}`;
      return;
    }
    // Multiple top-level items: download each in turn.
    state.items.forEach((f, i) => {
      setTimeout(() => {
        const a = document.createElement("a");
        a.href = `/api/netdisk/share/download?token=${encodeURIComponent(state.token)}&path=${encodeURIComponent(f.path)}&ts=${Date.now()}`;
        a.download = "";
        a.click();
      }, i * 300);
    });
  };

  $("#loginBtn").onclick = openLogin;
  $("#closeLogin").onclick = closeLogin;
  $("#goLogin").onclick = goLogin;

  $("#closePicker").onclick = closePicker;
  $("#cancelPicker").onclick = closePicker;
  $("#confirmPicker").onclick = confirmSave;
  $("#pickerUp").onclick = () => {
    const parts = state.pickerPath.split("/").filter(Boolean);
    parts.pop();
    loadPicker(parts.join("/"));
  };
  $("#pickerMkdir").onclick = pickerMkdir;

  // Click on the backdrop ring (not the modal contents) closes the top modal.
  let pressOnBackdrop = false;
  document.addEventListener("mousedown", (e) => {
    pressOnBackdrop = !!(e.target.classList && e.target.classList.contains("modal-backdrop"));
  });
  document.addEventListener("click", (e) => {
    const endedOnBackdrop = !!(e.target.classList && e.target.classList.contains("modal-backdrop"));
    if (pressOnBackdrop && endedOnBackdrop) closeTopModal();
    pressOnBackdrop = false;
  });
  // Esc closes the top modal.
  document.addEventListener("keydown", (e) => {
    if (e.key === "Escape") closeTopModal();
  });
}

function closeTopModal() {
  // Picker takes precedence over the login prompt when both are shown.
  if (!$("#pickerBackdrop").hidden) closePicker();
  else if (!$("#loginBackdrop").hidden) closeLogin();
}

init();
