// Interactive web terminal backed by xterm.js (bundled locally) and a
// WebSocket bridge to a Docker exec session. A real login shell (bash when
// available) runs inside the pty, so Tab completion, history (Up/Down), and
// Ctrl-R reverse search behave exactly like a local terminal.

import { state, toast } from "../app.js";
import { showModalNoShell, closeModal } from "./ui.js";

// One-line snippets inserted into the prompt via the toolbar. Picked to be
// safe, read-only, and useful when inspecting a container.
const COMMON_COMMANDS = [
  { label: "ls", cmd: "ls -la" },
  { label: "pwd", cmd: "pwd" },
  { label: "whoami", cmd: "whoami" },
  { label: "uname", cmd: "uname -a" },
  { label: "disk", cmd: "df -h" },
  { label: "mem", cmd: "free -h" },
  { label: "cpu", cmd: "nproc && cat /proc/loadavg" },
  { label: "gpu", cmd: "nvidia-smi 2>/dev/null || echo 'no nvidia-smi'" },
  { label: "procs", cmd: "ps aux --sort=-%cpu | head -n 15" },
  { label: "ports", cmd: "ss -tlnp 2>/dev/null || netstat -tlnp 2>/dev/null" },
  { label: "env", cmd: "env | sort" },
  { label: "date", cmd: "date" },
];

export function loadXterm() {
  if (window.Terminal && window.FitAddon && window.FitAddon.FitAddon) {
    return Promise.resolve();
  }
  return Promise.reject(new Error("Terminal library failed to load"));
}

export async function openTerminal(id, name) {
  showModalNoShell(
    "terminal-modal",
    "wide terminal-modal",
    `<div class="modal-head">` +
      `<div class="term-title">` +
        `<h2>🖥 Console</h2>` +
        `<div class="term-meta hint" id="termMeta">Loading container info…</div>` +
      `</div>` +
      `<div class="modal-tools term-toolbar">` +
        `<div class="cmd-menu">` +
          `<button class="ghost" id="termCmdBtn" title="Insert a command">⌘ Commands ▾</button>` +
          `<div class="cmd-dropdown" id="termCmdMenu" hidden>` +
            COMMON_COMMANDS.map((c) => `<button type="button" class="cmd-item" data-cmd="${escapeHtml(c.cmd)}"><code class="mono">${escapeHtml(c.cmd)}</code><span class="cmd-label">${escapeHtml(c.label)}</span></button>`).join("") +
          `</div>` +
        `</div>` +
        `<button class="ghost" id="termClear" title="Clear screen (Ctrl-L)">Clear</button>` +
        `<button class="ghost" id="termCopy" title="Copy selection">Copy</button>` +
        `<button class="ghost" id="termPaste" title="Paste (Ctrl-Shift-V)">Paste</button>` +
        `<button class="ghost" id="termFullscreen" title="Toggle fullscreen">⛶ Fullscreen</button>` +
        `<button class="ghost" data-close>Close</button>` +
      `</div>` +
    `</div>` +
    `<div class="modal-body">` +
      `<div id="termBox" class="term-box"></div>` +
      `<div class="term-statusbar">` +
        `<span class="term-stat" id="termConnStat"><span class="dot dot-muted"></span>Opening…</span>` +
        `<span class="term-stat term-keys">Tab complete · ↑/↓ history · Ctrl-R search · Ctrl-L clear</span>` +
        `<span class="term-stat term-size" id="termSize"></span>` +
      `</div>` +
    `</div>`,
    false
  );
  // Wire the close buttons ourselves (we passed bind=false above).
  document.querySelectorAll(".terminal-modal [data-close]").forEach((b) => (b.onclick = closeModal));

  // Fetch container metadata for the header (best-effort; never blocks the PTY).
  fetchContainerMeta(id);

  try {
    await loadXterm();
  } catch {
    setConnStat("error", "Failed to load terminal library");
    return;
  }
  const term = new window.Terminal({
    cursorBlink: true,
    fontSize: 14,
    lineHeight: 1.25,
    fontFamily: "'Cascadia Code', 'Fira Code', 'SFMono-Regular', Consolas, 'Liberation Mono', monospace",
    scrollback: 10000,
    convertEol: false,
    macOptionIsMeta: true,
    allowProposedApi: true,
    theme: TERM_THEME,
  });
  const fitAddon = new window.FitAddon.FitAddon();
  term.loadAddon(fitAddon);
  term.open($("#termBox"));
  fitAddon.fit();
  updateSize(term);
  state.terminal = { open: true, id, name, term, ws: null, fitAddon };

  const proto = location.protocol === "https:" ? "wss:" : "ws:";
  const url = `${proto}//${location.host}/api/containers/terminal?id=${encodeURIComponent(id)}&cols=${term.cols}&rows=${term.rows}`;
  const ws = new WebSocket(url);
  state.terminal.ws = ws;
  ws.binaryType = "arraybuffer";
  ws.onopen = () => {
    setConnStat("ok", "Connected");
    // The terminal may have been refitted between URL construction and the
    // WebSocket opening, so push the current dimensions to the server pty.
    sendResize();
  };
  ws.onmessage = (ev) => {
    term.write(typeof ev.data === "string" ? ev.data : new Uint8Array(ev.data));
  };
  ws.onclose = () => setConnStat("closed", "Disconnected — session closed");
  ws.onerror = () => setConnStat("error", "Connection error");
  term.onData((data) => {
    if (ws.readyState === WebSocket.OPEN) ws.send(JSON.stringify({ type: "stdin", data }));
  });

  // Keep the server-side pty in sync with the rendered terminal size. xterm.js
  // fires this after fitAddon.fit(), so resizes from window changes, fullscreen
  // toggles, and the initial layout settle all update the container pty.
  const sendResize = () => {
    const currentWs = state.terminal?.ws;
    if (currentWs && currentWs.readyState === WebSocket.OPEN) {
      currentWs.send(JSON.stringify({ type: "resize", cols: term.cols, rows: term.rows }));
    }
  };
  term.onResize(() => {
    updateSize(term);
    sendResize();
  });

  // Resize handling: window resize + container size changes (fullscreen toggle).
  const onResize = () => {
    try {
      fitAddon.fit();
    } catch {}
  };
  window.addEventListener("resize", onResize);
  let resizeObs = null;
  if (window.ResizeObserver) {
    resizeObs = new ResizeObserver(onResize);
    resizeObs.observe($("#termBox"));
  }
  state.terminal._onResize = onResize;
  state.terminal._resizeObs = resizeObs;

  // Toolbar wiring.
  bindToolbar(term, ws);
  // Re-fit once the modal's flex layout has settled (the first fit() runs
  // before the browser has finished sizing the flex container), then focus.
  requestAnimationFrame(() => {
    try { fitAddon.fit(); } catch {}
    setTimeout(() => { try { term.focus(); } catch {} }, 30);
  });
}

// TERM_THEME is a balanced dark palette (One Dark inspired) with distinct,
// high-contrast ANSI colours so ls/git/gcc/diff output reads well.
const TERM_THEME = {
  background: "#1a1b26",
  foreground: "#a9b1d6",
  cursor: "#c0caf5",
  cursorAccent: "#1a1b26",
  selectionBackground: "rgba(122,134,198,0.35)",
  black: "#32344a",
  red: "#f7768e",
  green: "#9ece6a",
  yellow: "#e0af68",
  blue: "#7aa2f7",
  magenta: "#bb9af7",
  cyan: "#7dcfff",
  white: "#a9b1d6",
  brightBlack: "#9699b8",
  brightRed: "#f7768e",
  brightGreen: "#9ece6a",
  brightYellow: "#e0af68",
  brightBlue: "#7aa2f7",
  brightMagenta: "#bb9af7",
  brightCyan: "#7dcfff",
  brightWhite: "#c0caf5",
};

// fetchContainerMeta fills the header with image/GPU details. Best-effort.
async function fetchContainerMeta(id) {
  try {
    const i = await fetch("/api/containers/inspect?id=" + encodeURIComponent(id), { credentials: "same-origin" }).then((r) => r.json());
    const parts = [];
    if (i.imageName || i.image) parts.push(`📦 ${i.imageName || i.image}`);
    if (i.gpu && i.gpu !== "none") parts.push(`🎴 GPU ${i.gpu}`);
    parts.push(`state: ${i.state}`);
    const meta = document.getElementById("termMeta");
    if (meta) meta.textContent = parts.join("  ·  ");
  } catch {
    // Header stays at "Loading…"; the PTY itself is unaffected.
  }
}

function setConnStat(kind, text) {
  const el = document.getElementById("termConnStat");
  if (!el) return;
  const dotClass = kind === "ok" ? "dot-ok" : kind === "error" || kind === "closed" ? "dot-danger" : "dot-muted";
  el.innerHTML = `<span class="dot ${dotClass}"></span>${escapeHtml(text)}`;
}

function updateSize(term) {
  const el = document.getElementById("termSize");
  if (el && term) el.textContent = `${term.cols}×${term.rows}`;
}

function bindToolbar(term, ws) {
  const send = (text) => {
    if (ws.readyState !== WebSocket.OPEN) return;
    ws.send(JSON.stringify({ type: "stdin", data: text }));
    try { term.focus(); } catch {}
  };

  // Commands dropdown.
  const cmdBtn = $("#termCmdBtn");
  const cmdMenu = $("#termCmdMenu");
  if (cmdBtn && cmdMenu) {
    cmdBtn.onclick = (e) => {
      e.stopPropagation();
      const isOpen = !cmdMenu.hidden;
      cmdMenu.hidden = isOpen;
    };
    cmdMenu.querySelectorAll("[data-cmd]").forEach((item) => {
      item.onclick = (e) => {
        e.stopPropagation();
        // Run the command immediately (append a newline so the shell executes it).
        send(item.dataset.cmd + "\r");
        cmdMenu.hidden = true;
      };
    });
  }
  // Clicking anywhere outside the menu closes it.
  document.addEventListener("click", closeMenus);
  state.terminal._menuCloser = closeMenus;

  const clearBtn = $("#termClear");
  if (clearBtn) clearBtn.onclick = () => { send("\f"); try { term.focus(); } catch {} };

  const copyBtn = $("#termCopy");
  if (copyBtn) copyBtn.onclick = async () => {
    const sel = term.getSelection();
    if (sel) {
      try { await navigator.clipboard.writeText(sel); toast("Selection copied", true); }
      catch { toast("Copy failed"); }
    } else {
      toast("Select text in the terminal first");
    }
  };

  const pasteBtn = $("#termPaste");
  if (pasteBtn) pasteBtn.onclick = async () => {
    try {
      const text = await navigator.clipboard.readText();
      if (text) send(text);
    } catch { toast("Clipboard read blocked by browser"); }
  };

  const fsBtn = $("#termFullscreen");
  if (fsBtn) fsBtn.onclick = () => toggleFullscreen(term);
}

function closeMenus() {
  const m = document.getElementById("termCmdMenu");
  if (m) m.hidden = true;
}

function toggleFullscreen(term) {
  const backdrop = document.querySelector(".terminal-modal");
  if (!backdrop) return;
  const isFs = backdrop.classList.toggle("term-fullscreen");
  const btn = document.getElementById("termFullscreen");
  if (btn) btn.textContent = isFs ? "⛶ Exit Fullscreen" : "⛶ Fullscreen";
  // Reflow the terminal to the new dimensions after the layout settles.
  requestAnimationFrame(() => {
    try { state.terminal?.fitAddon?.fit(); } catch {}
    try { term.focus(); } catch {}
  });
}

export function closeTerminal() {
  if (state.terminal._onResize) window.removeEventListener("resize", state.terminal._onResize);
  if (state.terminal._resizeObs) {
    try { state.terminal._resizeObs.disconnect(); } catch {}
  }
  if (state.terminal._menuCloser) {
    document.removeEventListener("click", state.terminal._menuCloser);
  }
  try {
    state.terminal.ws?.close();
  } catch {}
  try {
    state.terminal.term?.dispose();
  } catch {}
  state.terminal = { open: false, id: "", name: "", term: null, ws: null, fitAddon: null };
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

// Reference closeModal so tree-shaking/static analysis keeps the link; the
// backdrop click handler in ui.js is what actually closes terminals.
export { closeModal };
