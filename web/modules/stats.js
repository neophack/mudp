// Live container stats panel: CPU%, memory, network I/O, block I/O, sampled
// over SSE. CSS-only sparklines — no chart dependency.

import { api, toast } from "../app.js";
import { showModalNoShell, closeModal } from "./ui.js";

export async function openStats(id, name) {
  showModalNoShell(
    "stats-modal",
    "wide",
    `<div class="modal-head"><h2>Stats: ${escapeHtml(name)}</h2>` +
      `<div class="modal-tools">` +
        `<select id="statsInterval">${[1, 2, 5].map((s) => `<option value="${s}">${s}s</option>`).join("")}</select>` +
        `<button class="ghost" data-close>Close</button>` +
      `</div></div>` +
      `<div class="modal-body"><div id="statsGrid" class="stats-grid"><p class="hint">Connecting…</p></div></div>`
  );
  const closeBtn = document.querySelector(".stats-modal [data-close]");
  if (closeBtn) closeBtn.onclick = closeModal;

  let interval = 2;
  const sel = document.querySelector("#statsInterval");
  if (sel) sel.onchange = () => { interval = Number(sel.value); reconnect(); };

  const grid = document.querySelector("#statsGrid");
  const history = { cpu: [], mem: [], netRx: [], netTx: [], gpu: [], gpuMem: [] };
  let es = null;
  let sawGPU = false;

  function renderStats(s) {
    if (s.error) {
      grid.innerHTML = `<div class="error-box">✗ ${escapeHtml(s.error)}</div>`;
      return;
    }
    push(history.cpu, s.cpuPct);
    push(history.mem, s.memMb);
    push(history.netRx, s.netRxKb);
    push(history.netTx, s.netTxKb);
    const hasGPU = (s.gpuPct || s.gpuMemMb || s.gpuMemLimitMb) !== undefined && (s.gpuMemLimitMb > 0 || s.gpuPct > 0);
    if (hasGPU) {
      sawGPU = true;
      push(history.gpu, s.gpuPct);
      push(history.gpuMem, s.gpuMemMb);
    }
    const cards =
      statCard("CPU", `${(s.cpuPct || 0).toFixed(1)}%`, spark(history.cpu, "%"), "var(--accent)") +
      statCard("Memory", `${(s.memMb || 0).toFixed(0)} MB`, `${(s.memPct || 0).toFixed(1)}% of ${(s.memLimitMb || 0).toFixed(0)} MB`, bar(s.memPct || 0)) +
      statCard("Network RX", `${(s.netRxKb || 0).toFixed(1)} KB`, spark(history.netRx, " KB"), "var(--ok)") +
      statCard("Network TX", `${(s.netTxKb || 0).toFixed(1)} KB`, spark(history.netTx, " KB"), "var(--warn)") +
      statCard("Block Read", `${(s.blockReadKb || 0).toFixed(1)} KB`, "disk read", "") +
      statCard("Block Write", `${(s.blockWriteKb || 0).toFixed(1)} KB`, "disk write", "") +
      statCard("Processes", String(s.pids ?? 0), "PIDs", "") +
      (sawGPU
        ? statCard("GPU", `${(s.gpuPct || 0).toFixed(1)}%`, spark(history.gpu, "%"), "var(--accent)") +
          statCard("GPU Memory", `${(s.gpuMemMb || 0).toFixed(0)} MB`, `${(s.gpuMemPct || 0).toFixed(1)}% of ${(s.gpuMemLimitMb || 0).toFixed(0)} MB`, bar(s.gpuMemPct || 0))
        : "");
    grid.innerHTML = cards;
  }

  function reconnect() {
    if (es) es.close();
    grid.innerHTML = `<p class="hint">Connecting…</p>`;
    history.cpu = []; history.mem = []; history.netRx = []; history.netTx = []; history.gpu = []; history.gpuMem = [];
    sawGPU = false;
    const url = `/api/containers/stats/stream?id=${encodeURIComponent(id)}&interval=${interval}`;
    es = new EventSource(url);
    es.addEventListener("sample", (ev) => {
      try { renderStats(JSON.parse(ev.data)); } catch {}
    });
    es.onerror = () => {
      grid.innerHTML += `<p class="hint">Stream ended.</p>`;
    };
  }
  reconnect();
}

function statCard(label, value, sub, accent) {
  return (
    `<div class="stat-card">` +
      `<div class="stat-card-head"><span>${escapeHtml(label)}</span></div>` +
      `<div class="stat-card-value">${escapeHtml(String(value))}</div>` +
      `<div class="stat-card-sub">${typeof sub === "string" ? sub : sub}</div>` +
    `</div>`
  );
}

// spark builds a tiny inline SVG sparkline of the last N samples.
function spark(series, unit) {
  if (!series || series.length < 2) return `<span class="hint">—</span>`;
  const w = 80, h = 24;
  const max = Math.max(...series, 1);
  const pts = series.map((v, i) => {
    const x = (i / (series.length - 1)) * w;
    const y = h - (v / max) * h;
    return `${x.toFixed(1)},${y.toFixed(1)}`;
  }).join(" ");
  return `<svg class="spark" viewBox="0 0 ${w} ${h}" preserveAspectRatio="none"><polyline points="${pts}" fill="none" stroke="currentColor" stroke-width="1.5"/></svg>`;
}

function bar(pct) {
  const p = Math.min(100, Math.max(0, pct));
  return `<div class="bar"><div class="bar-fill" style="width:${p.toFixed(0)}%"></div></div>`;
}

function push(arr, v) {
  arr.push(Number(v) || 0);
  if (arr.length > 30) arr.shift();
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
