// Container logs modal: tail selector, live follow (SSE), grep filter,
// line-wrap toggle, and download.

import { state, api, toast } from "../app.js";
import { showModalNoShell, closeModal, onModalClose } from "./ui.js";

export async function openLogs(id, title) {
  state.logViewer = {
    open: true,
    id,
    title,
    content: "Loading…",
    tail: state.logViewer.tail || 300,
    follow: false,
    grep: "",
    wrap: true,
  };
  renderLogViewer();
  await fetchLogs();
}

export async function fetchLogs() {
  if (!state.logViewer.open) return;
  try {
    const data = await api("/api/containers/action", {
      method: "POST",
      body: JSON.stringify({ id: state.logViewer.id, action: "logs", tail: state.logViewer.tail }),
    });
    state.logViewer.content = data.logs || "No logs yet";
    renderLogViewer();
  } catch (err) {
    toast(err.message);
  }
}

export function renderLogViewer() {
  document.querySelector(".modal-backdrop.logs-modal")?.remove();
  if (!state.logViewer.open) return;
  const lv = state.logViewer;
  const filtered = applyFilter(lv.content || "", lv.grep);
  showModalNoShell(
    "logs-modal",
    "wide",
    `<div class="modal-head"><h2>Logs: ${escapeHtml(lv.title)}</h2>` +
      `<div class="modal-tools">` +
        `<input id="logGrep" placeholder="Filter…" value="${escapeHtml(lv.grep || "")}" style="min-height:30px;width:140px;">` +
        `<select id="logTail">${[100, 300, 1000, 5000]
          .map((n) => `<option value="${n}" ${Number(lv.tail) === n ? "selected" : ""}>Last ${n}</option>`)
          .join("")}</select>` +
        `<label class="check" title="Live tail"><input type="checkbox" id="logFollow" ${lv.follow ? "checked" : ""}> Follow</label>` +
        `<label class="check" title="Wrap long lines"><input type="checkbox" id="logWrap" ${lv.wrap ? "checked" : ""}> Wrap</label>` +
        `<button class="ghost" id="downloadLogs" title="Download">⬇</button>` +
        `<button class="ghost" id="refreshLogs">Refresh</button>` +
        `<button class="ghost" data-close>Close</button>` +
      `</div></div>` +
      `<div class="modal-body"><pre class="log-output ${lv.wrap ? "" : "nowrap"}">${escapeHtml(filtered)}</pre></div>`,
    false
  );
  $("#refreshLogs").onclick = fetchLogs;
  $("#logTail").onchange = (e) => {
    lv.tail = Number(e.target.value);
    fetchLogs();
  };
  $("#logGrep").oninput = (e) => { lv.grep = e.target.value; renderLogViewer(); };
  $("#logFollow").onchange = (e) => {
    lv.follow = e.target.checked;
    if (lv.follow) startFollow(); else stopFollow();
  };
  $("#logWrap").onchange = (e) => { lv.wrap = e.target.checked; renderLogViewer(); };
  $("#downloadLogs").onclick = downloadLogs;
  const logClose = document.querySelector(".logs-modal [data-close]");
  if (logClose) {
    logClose.onclick = () => {
      stopFollow();
      closeModal();
    };
  }
  // Esc/backdrop-click bypass the Close button above, so the follow stream also
  // has to be torn down through the modal teardown registry. The backdrop is
  // recreated on every renderLogViewer() call, so this has to be re-registered
  // each time rather than once in openLogs.
  const backdrop = document.querySelector(".modal-backdrop.logs-modal");
  if (backdrop) onModalClose(backdrop, stopFollow);
  if (lv.follow) startFollow();
  const log = document.querySelector(".modal-body .log-output");
  if (log) log.scrollTop = log.scrollHeight;
}

let followES = null;

function startFollow() {
  stopFollow();
  const lv = state.logViewer;
  const url = `/api/containers/logs/stream?id=${encodeURIComponent(lv.id)}&tail=${lv.tail}`;
  followES = new EventSource(url);
  followES.addEventListener("log", (ev) => {
    try {
      const data = JSON.parse(ev.data);
      if (data.line) {
        lv.content = (lv.content === "Loading…" ? "" : lv.content) + data.line;
        const filtered = applyFilter(lv.content, lv.grep);
        const pre = document.querySelector(".logs-modal .log-output");
        if (pre) {
          pre.textContent = filtered;
          pre.scrollTop = pre.scrollHeight;
        }
      }
    } catch {}
  });
  followES.onerror = () => {
    // Stream ended (container stopped or disconnected). Leave existing logs.
  };
}

function stopFollow() {
  if (followES) {
    followES.close();
    followES = null;
  }
}

function applyFilter(text, grep) {
  if (!grep) return text;
  const needle = grep.toLowerCase();
  return text
    .split("\n")
    .filter((l) => l.toLowerCase().includes(needle))
    .join("\n");
}

function downloadLogs() {
  const lv = state.logViewer;
  const blob = new Blob([lv.content || ""], { type: "text/plain" });
  const url = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = url;
  a.download = `${lv.title}-logs.txt`;
  a.click();
  URL.revokeObjectURL(url);
}

function $(selector) {
  return document.querySelector(selector);
}

function escapeHtml(v) {
  return String(v ?? "").replace(/[&<>"']/g, (m) => ({
    "&": "&amp;",
    "<": "&lt;",
    ">": "&gt;",
    '"': "&quot;",
    "'": "&#39;",
  }[m]));
}
