// Dual-pane file explorer: container filesystem (left) ↔ user netdisk (right).
// Lets the owner copy selected files/folders in either direction. Works on
// stopped containers because listing/download/upload use the Docker archive API
// (container writable layer), not exec.

import { api, toast, escapeHtml } from "../app.js";
import { showModalNoShell } from "./ui.js";

const $ = (sel, root = document) => root.querySelector(sel);
const $$ = (sel, root = document) => [...root.querySelectorAll(sel)];

// Per-explorer state, reset on each open.
let session = null;

export function openFiles(containerId, containerName) {
  session = {
    id: containerId,
    name: containerName,
    container: { path: "/", items: [], selected: new Set() },
    netdisk: { path: "", items: [], selected: new Set() },
  };
  showModalNoShell(
    "files-modal",
    "wide files-modal",
    `<div class="modal-head">
       <h2>Files · ${escapeHtml(containerName)}</h2>
       <button class="ghost" data-close>Close</button>
     </div>
     <div class="modal-body files-body">
       <div class="files-toolbar">
         <button class="primary" id="copyToNetdisk" title="Copy selected container files into the current netdisk folder">→ Copy to Netdisk</button>
         <button class="primary" id="copyToContainer" title="Copy selected netdisk files into the current container folder">← Copy to Container</button>
         <span class="hint" id="filesStatus"></span>
       </div>
       <div class="files-panes">
         <section class="files-pane">
           <div class="pane-head">
             <h3>Container</h3>
             <code class="mono pane-path" id="containerPath">/</code>
             <button class="ghost" id="containerUp">Up</button>
           </div>
           <div class="pane-scroll" id="containerList"><p class="hint">Loading…</p></div>
         </section>
         <section class="files-pane">
           <div class="pane-head">
             <h3>Netdisk</h3>
             <code class="mono pane-path" id="netdiskPath">/</code>
             <button class="ghost" id="netdiskUp">Up</button>
           </div>
           <div class="pane-scroll" id="netdiskList"><p class="hint">Loading…</p></div>
         </section>
       </div>
     </div>`,
    true
  );
  bindChrome();
  loadContainer();
  loadNetdisk();
}

function bindChrome() {
  $("#containerUp").onclick = () => goUp("container");
  $("#netdiskUp").onclick = () => goUp("netdisk");
  $("#copyToNetdisk").onclick = () => copy("to-netdisk");
  $("#copyToContainer").onclick = () => copy("to-container");
}

function setStatus(msg) {
  const el = $("#filesStatus");
  if (el) el.textContent = msg || "";
}

function sorted(items) {
  return items.slice().sort((a, b) =>
    a.dir === b.dir ? a.name.localeCompare(b.name) : a.dir ? -1 : 1
  );
}

// --- container pane ---------------------------------------------------------

async function loadContainer() {
  if (!session) return;
  const p = session.container;
  p.selected = new Set();
  setStatus("Loading container files…");
  try {
    const res = await api(`/api/containers/files/list?id=${encodeURIComponent(session.id)}&path=${encodeURIComponent(p.path)}`);
    p.path = res.path || p.path;
    p.items = res.items || [];
  } catch (err) {
    p.items = [];
    setStatus(err.message);
  }
  renderContainer();
  if (p.items.length) setStatus("");
}

function renderContainer() {
  if (!session) return;
  const p = session.container;
  $("#containerPath").textContent = p.path;
  const rows = sorted(p.items || []).map(containerRow).join("");
  $("#containerList").innerHTML = rows
    ? `<table class="data files-table"><tbody>${rows}</tbody></table>`
    : `<p class="hint">Empty folder.</p>`;
  $$("#containerList [data-open]").forEach((btn) => {
    btn.onclick = () => { session.container.path = btn.dataset.open; loadContainer(); };
  });
  $$("#containerList [data-pick]").forEach((box) => {
    box.onchange = () => togglePick(session.container.selected, box.dataset.pick, box.checked);
  });
  $$("#containerList [data-download]").forEach((btn) => {
    btn.onclick = () => window.open(
      `/api/containers/files/download?id=${encodeURIComponent(session.id)}&path=${encodeURIComponent(btn.dataset.download)}`,
      "_blank"
    );
  });
}

function containerRow(f) {
  const checked = session.container.selected.has(f.path) ? "checked" : "";
  const nameCell = f.dir
    ? `<button class="link" data-open="${escapeHtml(f.path)}">📁 ${escapeHtml(f.name)}</button>`
    : `<span>📄 ${escapeHtml(f.name)}</span>`;
  return (
    `<tr>` +
    `<td class="chk-col"><input type="checkbox" data-pick="${escapeHtml(f.path)}" ${checked}></td>` +
    `<td>${nameCell}</td>` +
    `<td>${f.dir ? "—" : fmtBytes(f.size)}</td>` +
    `<td>${escapeHtml(new Date(f.modTime).toLocaleString())}</td>` +
    `<td class="actions"><a class="ghost-link" data-download="${escapeHtml(f.path)}" title="Download">⬇</a></td>` +
    `</tr>`
  );
}

// --- netdisk pane -----------------------------------------------------------

async function loadNetdisk() {
  if (!session) return;
  const p = session.netdisk;
  p.selected = new Set();
  try {
    const res = await api(`/api/netdisk?path=${encodeURIComponent(p.path)}`);
    p.path = res.path || "";
    p.items = res.items || [];
  } catch (err) {
    p.items = [];
    setStatus(err.message);
  }
  renderNetdisk();
}

function renderNetdisk() {
  if (!session) return;
  const p = session.netdisk;
  $("#netdiskPath").textContent = "/" + (p.path || "");
  const rows = sorted(p.items || []).map(netdiskRow).join("");
  $("#netdiskList").innerHTML = rows
    ? `<table class="data files-table"><tbody>${rows}</tbody></table>`
    : `<p class="hint">Empty folder.</p>`;
  $$("#netdiskList [data-open]").forEach((btn) => {
    btn.onclick = () => { session.netdisk.path = btn.dataset.open; loadNetdisk(); };
  });
  $$("#netdiskList [data-pick]").forEach((box) => {
    box.onchange = () => togglePick(session.netdisk.selected, box.dataset.pick, box.checked);
  });
}

function netdiskRow(f) {
  const checked = session.netdisk.selected.has(f.path) ? "checked" : "";
  const nameCell = f.dir
    ? `<button class="link" data-open="${escapeHtml(f.path)}">📁 ${escapeHtml(f.name)}</button>`
    : `<span>📄 ${escapeHtml(f.name)}</span>`;
  return (
    `<tr>` +
    `<td class="chk-col"><input type="checkbox" data-pick="${escapeHtml(f.path)}" ${checked}></td>` +
    `<td>${nameCell}</td>` +
    `<td>${f.dir ? "—" : fmtBytes(f.size)}</td>` +
    `<td>${escapeHtml(new Date(f.modTime).toLocaleString())}</td>` +
    `<td></td>` +
    `</tr>`
  );
}

// --- shared helpers ---------------------------------------------------------

function togglePick(set, key, on) {
  if (on) set.add(key);
  else set.delete(key);
}

function goUp(side) {
  const p = session[side];
  const parts = (p.path || "").split("/").filter(Boolean);
  parts.pop();
  p.path = parts.join("/");
  if (side === "container") {
    p.path = "/" + (p.path || "");
    loadContainer();
  } else {
    loadNetdisk();
  }
}

// --- copy -------------------------------------------------------------------

async function copy(direction) {
  if (!session) return;
  const fromContainer = direction === "to-netdisk";
  const from = fromContainer ? session.container : session.netdisk;
  const destDir = fromContainer ? session.netdisk.path : session.container.path;
  const paths = [...from.selected];
  if (paths.length === 0) {
    toast("Select at least one file or folder first");
    return;
  }
  setStatus(fromContainer ? "Copying to netdisk…" : "Copying to container…");
  const btns = $$(".files-toolbar button");
  btns.forEach((b) => (b.disabled = true));
  try {
    const res = await api("/api/containers/files/copy", {
      method: "POST",
      body: JSON.stringify({ id: session.id, direction, paths, destDir }),
    });
    toast(`Copied ${res.copied ?? paths.length} item(s)`, true);
    from.selected = new Set();
    if (fromContainer) await loadNetdisk();
    else await loadContainer();
    setStatus("");
  } catch (err) {
    toast(err.message);
    setStatus(err.message);
  } finally {
    btns.forEach((b) => (b.disabled = false));
  }
}

function fmtBytes(n) {
  n = Number(n) || 0;
  if (n < 1024) return n + " B";
  if (n < 1024 * 1024) return (n / 1024).toFixed(1) + " KB";
  if (n < 1024 * 1024 * 1024) return (n / 1024 / 1024).toFixed(1) + " MB";
  return (n / 1024 / 1024 / 1024).toFixed(2) + " GB";
}
