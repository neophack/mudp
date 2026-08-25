// Standalone public share page. Baidu-Netdisk-style browsing + save-to-directory.

import { escapeHtml, fmtBytes, joinPath } from "/lib/common.js";
import { openYuvViewer } from "/lib/yuv.js";
import { openRawViewer } from "/lib/raw.js";
import { renderMarkdownInto } from "/lib/viewer.js";
// Shared with the console viewer: type dispatch + CSV/JSON/Office renderers
// (docx via mammoth, xlsx via SheetJS, both lazy-loaded from /vendor/).
import { previewKind, csvToTableHtml, prettyJsonOrNull, renderDocxInto, renderXlsxInto } from "/lib/preview.js";

const state = {
  token: "",
  path: "",
  items: [],
  selected: new Set(),
  share: null,
  me: null,
  pickerPath: "",
  pickerItems: [],
  password: "",
};

const $ = (s) => document.querySelector(s);

function readCSRFCookie() {
  const m = document.cookie.match(/(?:^|; )mudp_csrf=([^;]+)/);
  return m ? decodeURIComponent(m[1]) : "";
}

async function api(path, opts = {}) {
  const { headers, ...rest } = opts;
  const method = (opts.method || "GET").toUpperCase();
  const csrfToken = readCSRFCookie();
  const mergedHeaders = { "Content-Type": "application/json", ...(headers || {}) };
  if (csrfToken && method !== "GET" && method !== "HEAD") {
    mergedHeaders["X-CSRF-Token"] = csrfToken;
  }
  const res = await fetch(path, {
    credentials: "same-origin",
    headers: mergedHeaders,
    ...rest,
  });
  const data = await res.json().catch(() => ({}));
  if (!res.ok) {
    const err = new Error(data.error || res.statusText);
    err.status = res.status;
    throw err;
  }
  return data;
}

function displayName(user) {
  return user?.displayName || user?.username || "";
}

function getToken() {
  const meta = document.querySelector('meta[name="share-token"]');
  return meta?.content || location.pathname.split("/").pop();
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
      $("#userLabel").textContent = displayName(state.me);
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
    const data = await shareApi(`/api/netdisk/share/public?token=${encodeURIComponent(state.token)}&path=${encodeURIComponent(path)}`);
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

async function shareApi(path, opts = {}) {
  const headers = { "Content-Type": "application/json", ...(opts.headers || {}) };
  if (state.password) {
    headers["X-Share-Password"] = state.password;
  }
  const res = await fetch(path, {
    credentials: "same-origin",
    headers,
    ...opts,
  });
  const data = await res.json().catch(() => ({}));
  if (!res.ok) {
    if (res.status === 401 && data.needsPassword) {
      openPasswordPrompt();
    }
    const err = new Error(data.error || res.statusText);
    err.status = res.status;
    err.needsPassword = data.needsPassword;
    throw err;
  }
  return data;
}

function openPasswordPrompt() {
  $("#passwordBackdrop").hidden = false;
  $("#sharePasswordInput").focus();
}

function closePasswordPrompt() {
  $("#passwordBackdrop").hidden = true;
  $("#passwordError").textContent = "";
}

async function submitPassword() {
  const input = $("#sharePasswordInput");
  const password = input.value.trim();
  if (!password) return;
  state.password = password;
  closePasswordPrompt();
  await loadShare(state.path);
}

function renderShareMeta() {
  const s = state.share;
  if (!s) return;
  $("#shareName").textContent = s.name;
  // textContent already escapes; pre-escaping here double-rendered "A & B".
  $("#shareOwner").textContent = `Shared by: ${s.owner || "Unknown"}`;
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

// shareRootFor finds which shared entry `path` falls under, so the breadcrumb
// can be rendered relative to that entry instead of the owner's full netdisk
// path. Paths on the share object are relative to the owner's netdisk root and
// may sit several folders deep (e.g. "1/aa"); only "aa" was actually shared, so
// the ancestor segment ("1") must never be shown to a visitor.
function shareRootFor(path) {
  const paths = (state.share && state.share.paths) || [];
  let best = null;
  for (const p of paths) {
    if (path === p || path.startsWith(p + "/")) {
      if (!best || p.length > best.length) best = p;
    }
  }
  return best;
}

// The portion of `root` that is not the shared folder's own name — this is the
// owner-internal prefix that must be stripped before display.
function shareStripPrefix(root) {
  if (!root) return "";
  const last = root.split("/").pop();
  return root.slice(0, root.length - last.length);
}

function renderBreadcrumb(path) {
  const stripPrefix = shareStripPrefix(shareRootFor(path));
  const visible = path.startsWith(stripPrefix) ? path.slice(stripPrefix.length) : path;
  const parts = visible.split("/").filter(Boolean);
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
    const target = built; // snapshot this iteration's path; `built` keeps mutating after this
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
      btn.onclick = () => loadShare(stripPrefix + target);
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
    const kind = !f.dir ? previewKind(f.name) : null;
    const nameCell = f.dir
      ? `<button class="share-name linklike" data-open="${escapeHtml(f.path)}" title="${escapeHtml(f.name)}">${iconFor(f)}<span class="share-name-text">${escapeHtml(f.name)}</span></button>`
      : kind
        ? `<button class="share-name linklike" data-view="${escapeHtml(f.path)}" title="${escapeHtml(f.name)}">${iconFor(f)}<span class="share-name-text">${escapeHtml(f.name)}</span></button>`
        : `<span class="share-name" title="${escapeHtml(f.name)}">${iconFor(f)}<span class="share-name-text">${escapeHtml(f.name)}</span></span>`;
    return `<tr data-path="${escapeHtml(f.path)}">
      <td class="chk-cell"><input type="checkbox" class="chk share-select" data-path="${escapeHtml(f.path)}" ${checked}></td>
      <td>${nameCell}</td>
      <td class="share-size">${f.dir ? "-" : fmtBytes(f.size)}</td>
      <td class="share-time">${escapeHtml(new Date(f.modTime).toLocaleString())}</td>
      <td class="actions"><a class="ghost-link" href="${downloadURL(f.path)}">Download</a></td>
    </tr>`;
  }).join("");
  $("#shareBody").innerHTML = `<table class="data share-table"><thead><tr><th class="chk-col"><input type="checkbox" class="chk" id="selectAllBox"></th><th>Name</th><th>Size</th><th>Modified</th><th class="actions">Actions</th></tr></thead><tbody>${rows}</tbody></table>`;

  // Bind events
  document.querySelectorAll("[data-open]").forEach((btn) => {
    btn.onclick = () => loadShare(btn.dataset.open);
  });
  document.querySelectorAll("[data-view]").forEach((btn) => {
    btn.onclick = () => openViewer(btn.dataset.view);
  });
  document.querySelectorAll(".share-select").forEach((cb) => {
    cb.onchange = () => toggleSelection(cb.dataset.path, cb.checked);
  });
  const selectAllBox = $("#selectAllBox");
  if (selectAllBox) {
    selectAllBox.onchange = (e) => {
      state.items.forEach((f) => {
        if (e.target.checked) state.selected.add(f.path);
        else state.selected.delete(f.path);
      });
      syncRowCheckboxes();
      updateSelectionBar();
    };
  }
  syncSelectAllCheckboxes();
}

// syncRowCheckboxes updates each row checkbox's checked state in place, without
// rebuilding the table (a full re-render on every toggle caused the checkbox
// the user just clicked to be destroyed/recreated, producing a visible flash).
function syncRowCheckboxes() {
  document.querySelectorAll(".share-select").forEach((cb) => {
    cb.checked = state.selected.has(cb.dataset.path);
  });
}

function syncSelectAllCheckboxes() {
  const allSelected = state.items.length > 0 && state.items.every((f) => state.selected.has(f.path));
  const selectAllBox = $("#selectAllBox");
  if (selectAllBox) selectAllBox.checked = allSelected;
  const selectAllToolbar = $("#selectAll");
  if (selectAllToolbar) selectAllToolbar.checked = allSelected;
}

function toggleSelection(path, checked) {
  if (checked) state.selected.add(path);
  else state.selected.delete(path);
  syncSelectAllCheckboxes();
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
        <span class="picker-folder-name" title="${escapeHtml(f.name)}">${escapeHtml(f.name)}</span>
      </div>`;
    }).join("");
    $("#pickerList").innerHTML = rows;
    document.querySelectorAll("#pickerList .picker-row").forEach((row) => {
      row.onclick = () => loadPicker(row.dataset.open);
    });
  }

  $("#pickerHint").textContent = `Will save to: /${escapeHtml(path || "")}`;
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
  const to = state.pickerPath || "";
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
    syncRowCheckboxes();
    syncSelectAllCheckboxes();
    updateSelectionBar();
  };

  $("#saveSelectedBtn").onclick = openPicker;
  $("#downloadAllBtn").onclick = () => {
    if (!state.items.length) return;
    if (state.items.length === 1) {
      location.href = downloadURL(state.items[0].path);
      return;
    }
    // Multiple top-level items: download each in turn.
    state.items.forEach((f, i) => {
      setTimeout(() => {
        const a = document.createElement("a");
        a.href = downloadURL(f.path);
        a.download = "";
        a.click();
      }, i * 300);
    });
  };

  $("#submitPassword").onclick = submitPassword;
  $("#sharePasswordInput").onkeydown = (e) => {
    if (e.key === "Enter") submitPassword();
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

  $("#closeViewer").onclick = closeViewer;
  $("#viewerFullscreen").onclick = toggleViewerFullscreen;

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
  // Exiting native fullscreen (Esc / browser controls) must also drop the CSS
  // fullscreen state, or the viewer stays stuck on the white fullscreen layout.
  document.addEventListener("fullscreenchange", () => {
    const backdrop = $("#viewerBackdrop");
    if (!document.fullscreenElement && backdrop && !backdrop.hidden) {
      backdrop.classList.remove("viewer-fullscreen");
      const fsBtn = $("#viewerFullscreen");
      if (fsBtn) fsBtn.textContent = "⛶ Fullscreen";
    }
  });
}

function downloadURL(path) {
  const pw = state.password ? `&password=${encodeURIComponent(state.password)}` : "";
  return `/api/netdisk/share/download?token=${encodeURIComponent(state.token)}&path=${encodeURIComponent(path)}${pw}&ts=${Date.now()}`;
}

function rawURL(path) {
  const pw = state.password ? `&password=${encodeURIComponent(state.password)}` : "";
  return `/api/netdisk/share/raw?token=${encodeURIComponent(state.token)}&path=${encodeURIComponent(path)}${pw}&ts=${Date.now()}`;
}

// rasterURL is the server-side YUV/RAW decode endpoint for the public share
// page: the backend decodes one frame and returns a JPEG, so a share visitor
// previews the file without the owner's raw bytes leaving the server. Carries
// the same token/password as rawURL.
function rasterURL(path) {
  const pw = state.password ? `&password=${encodeURIComponent(state.password)}` : "";
  return `/api/netdisk/share/raster?token=${encodeURIComponent(state.token)}&path=${encodeURIComponent(path)}${pw}&ts=${Date.now()}`;
}

function closeTopModal() {
  // Viewer is topmost (it covers the others when open).
  if (!$("#viewerBackdrop").hidden) closeViewer();
  else if (!$("#pickerBackdrop").hidden) closePicker();
  else if (!$("#loginBackdrop").hidden) closeLogin();
  else if (!$("#passwordBackdrop").hidden) closePasswordPrompt();
}

// ---------- File preview ----------

function openViewer(path) {
  const name = path.split("/").pop() || path;
  const kind = previewKind(name);
  resetViewerState(); // clears any previous fullscreen / cached PDF
  $("#viewerTitle").textContent = name;
  // Hide the fullscreen button for types that can't use it (text/markdown/audio).
  const fsBtn = $("#viewerFullscreen");
  if (fsBtn) fsBtn.hidden = !["pdf", "video", "image", "yuv", "raw", "csv", "docx", "xlsx"].includes(kind);
  $("#viewerDownload").href = downloadURL(path);
  $("#viewerBackdrop").hidden = false;
  if (!kind) {
    // Unsupported type: show a friendly prompt with a direct download.
    $("#viewerBody").innerHTML =
      `<div class="viewer-unsupported">` +
        `<div class="viewer-unsupported-icon" aria-hidden="true">📄</div>` +
        `<p>Preview is not available for this file type yet.</p>` +
        `<p class="hint">You can download it to view locally.</p>` +
        `<a class="primary" href="${downloadURL(path)}" download>⬇ Download</a>` +
      `</div>`;
    return;
  }
  $("#viewerBody").innerHTML = `<div class="viewer-loading">Loading…</div>`;
  renderViewerContent(kind, name, rawURL(path), path).catch((err) => {
    const body = $("#viewerBody");
    if (body) body.innerHTML = `<div class="viewer-error">${escapeHtml(err.message || "Failed to load file")}</div>`;
  });
}

function closeViewer() {
  resetViewerState();
  $("#viewerBackdrop").hidden = true;
  // Release media resources so audio/video playback stops on close.
  $("#viewerBody").innerHTML = "";
}

// resetViewerState clears fullscreen and cached PDF/YUV state. Called on close
// and before each new preview so the previous file's resources don't leak.
function resetViewerState() {
  pdfRenderState = null;
  if (yuvController) {
    yuvController.destroy();
    yuvController = null;
  }
  if (rawController) {
    rawController.destroy();
    rawController = null;
  }
  $("#viewerBackdrop").classList.remove("viewer-fullscreen");
  const fsBtn = $("#viewerFullscreen");
  if (fsBtn) fsBtn.textContent = "⛶ Fullscreen";
  const media = $("#viewerBody").querySelector("video.viewer-media, audio.viewer-media");
  if (media) {
    try { media.pause(); } catch { /* best-effort */ }
  }
  if (document.fullscreenElement) {
    document.exitFullscreen?.().catch(() => {});
  }
}

function toggleViewerFullscreen() {
  const backdrop = $("#viewerBackdrop");
  if (!backdrop || backdrop.hidden) return;
  const isFs = backdrop.classList.toggle("viewer-fullscreen");
  const fsBtn = $("#viewerFullscreen");
  if (fsBtn) fsBtn.textContent = isFs ? "⛶ Exit fullscreen" : "⛶ Fullscreen";
  // For <video>/<img>, prefer the native Fullscreen API so controls stay usable.
  const media = backdrop.querySelector("video.viewer-media, img.viewer-media");
  if (media && media.requestFullscreen) {
    if (isFs) media.requestFullscreen?.().catch(() => {});
    else if (document.fullscreenElement) document.exitFullscreen?.().catch(() => {});
  }
  // Re-render PDF pages at the new container width for crisp output.
  if (pdfRenderState && backdrop.querySelector("#pdfPages")) {
    paintPdfPages($("#viewerBody"));
  }
  // Re-paint the YUV/RAW frame so the canvas adapts to the new viewport.
  if (yuvController) {
    yuvController.repaint();
  }
  if (rawController) {
    rawController.repaint();
  }
}

async function renderViewerContent(kind, name, url, path) {
  const body = $("#viewerBody");
  if (!body) return;
  switch (kind) {
    case "pdf":
      return renderPdf(url, body);
    case "markdown":
      return renderMarkdownInto(url, body);
    case "text":
      return renderText(url, body);
    case "json": {
      const text = await fetchText2MB(url);
      const pre = document.createElement("pre");
      pre.className = "viewer-text";
      // Invalid JSON falls back to the raw text view.
      pre.textContent = prettyJsonOrNull(text) ?? text;
      body.innerHTML = "";
      body.appendChild(pre);
      return;
    }
    case "csv": {
      const sep = name.toLowerCase().endsWith(".tsv") ? "\t" : ",";
      const text = await fetchText2MB(url);
      body.innerHTML = `<div class="viewer-csv"></div>`;
      // csvToTableHtml escapes every cell itself.
      body.querySelector(".viewer-csv").innerHTML = csvToTableHtml(text, sep);
      return;
    }
    case "docx":
    case "xlsx": {
      body.innerHTML = `<div class="viewer-md viewer-doc"></div>`;
      const el = body.firstElementChild;
      if (kind === "docx") await renderDocxInto(url, el);
      else await renderXlsxInto(url, el);
      return;
    }
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
      return renderYuv(url, rasterURL(path), body, name);
    case "raw":
      return renderRaw(url, rasterURL(path), body, name);
  }
}

// renderYuv builds the YUV viewer. The returned controller is stashed in
// yuvController so toggleViewerFullscreen can repaint the frame and
// resetViewerState can cancel its in-flight fetch on close. rasterUrl is the
// server-side decode endpoint; url is the byte-serving raw endpoint used only
// for the file-size probe.
function renderYuv(url, rasterUrl, body, name) {
  body.innerHTML = `<div class="viewer-loading">Loading YUV…</div>`;
  yuvController = openYuvViewer({ name, url, rasterUrl, bodyEl: body });
}

// renderRaw builds the RAW Bayer viewer. Mirrors renderYuv: the controller is
// stashed in rawController so toggleViewerFullscreen can repaint and
// resetViewerState can cancel its in-flight fetch on close.
function renderRaw(url, rasterUrl, body, name) {
  body.innerHTML = `<div class="viewer-loading">Loading RAW…</div>`;
  rawController = openRawViewer({ name, url, rasterUrl, bodyEl: body });
}

// pdfRenderState caches the loaded PDF document so a fullscreen toggle can
// re-render pages at the new width without re-downloading.
let pdfRenderState = null;

// yuvController holds the active YUV viewer's handle so a fullscreen toggle can
// repaint the frame and resetViewerState can cancel its in-flight fetch.
let yuvController = null;

// rawController mirrors yuvController for the RAW Bayer viewer.
let rawController = null;

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
// canvas is drawn at device-pixel resolution (CSS × devicePixelRatio) and then
// sized down via CSS, keeping text crisp on Retina/4K displays.
async function paintPdfPages(body) {
  const { pdf } = pdfRenderState || {};
  if (!pdf) return;
  body.innerHTML = `<div class="viewer-canvas-wrap" id="pdfPages"></div>`;
  const pages = document.getElementById("pdfPages");
  const maxWidth = Math.max(360, pages.clientWidth || 820);
  for (let i = 1; i <= pdf.numPages; i++) {
    if (!pages || !pages.isConnected) return; // viewer closed mid-render
    const page = await pdf.getPage(i);
    const baseViewport = page.getViewport({ scale: 1 });
    const scale = Math.max(1, maxWidth / baseViewport.width);
    const viewport = page.getViewport({ scale });
    const dpr = window.devicePixelRatio || 1;
    const canvas = document.createElement("canvas");
    canvas.className = "viewer-page";
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

async function renderText(url, body) {
  const text = await fetchText2MB(url);
  body.innerHTML = `<pre class="viewer-text"></pre>`;
  body.querySelector("pre").textContent = text;
}

// fetchText2MB fetches text-like content with the same size cap the console
// viewer enforces, so a huge log is never pulled into the DOM.
async function fetchText2MB(url) {
  const res = await fetch(url, { credentials: "same-origin" });
  const len = Number(res.headers.get("Content-Length") || 0);
  if (len > 2 * 1024 * 1024) {
    throw new Error("This file is larger than 2 MB. Please download it to view.");
  }
  return res.text();
}

init();
