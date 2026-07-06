// Per-user resource usage table (admin only).

import { state, api, isAdmin } from "../app.js";

export async function renderUsage() {
  const [history, processes, usage] = await Promise.all([
    api("/api/resources/history").catch(() => []),
    isAdmin() ? api("/api/admin/processes").catch(() => []) : Promise.resolve([]),
    isAdmin() ? api("/api/admin/usage").catch(() => state.usage || []) : Promise.resolve(null),
  ]);
  if (isAdmin() && usage) {
    state.usage = usage;
  }
  const rows = (isAdmin()
    ? state.usage
    : [{
        username: state.me?.username || "me",
        containers: (state.containers || []).length,
        memoryMb: (state.containers || []).reduce((sum, c) => sum + (c.memoryMb || 0), 0),
        diskMb: (state.containers || []).reduce((sum, c) => sum + (c.diskMb || 0), 0),
        gpu: [...new Set((state.containers || []).map((c) => c.gpu).filter((g) => g && g !== "none"))].join(", "),
        gpuPct: avg((state.containers || []).map((c) => c.gpuPct || 0).filter(Boolean)),
        gpuMemMb: (state.containers || []).reduce((sum, c) => sum + (c.gpuMemMb || 0), 0),
        gpuMemTotalMb: (state.containers || []).reduce((sum, c) => sum + (c.gpuMemTotalMb || 0), 0),
      }]) || [];
  $("#view").innerHTML =
    `<div class="stack">` +
    `<div class="card">` +
      `<div class="card-head"><h2>Resource Usage</h2></div>` +
      `<table class="data">` +
        `<thead><tr><th>User</th><th>Containers</th><th>Memory</th><th>Disk</th><th>GPU</th><th>GPU Usage</th><th>GPU Memory</th></tr></thead>` +
        `<tbody>${rows
          .map(
            (u) =>
              `<tr>` +
              `<td><div class="primary-line">${escapeHtml(u.username)}</div></td>` +
              `<td>${u.containers}</td>` +
              `<td>${(u.memoryMb || 0).toFixed(0)} MB</td>` +
              `<td>${(u.diskMb || 0).toFixed(0)} MB</td>` +
              `<td><div class="secondary-line">${escapeHtml(u.gpu || "none")}</div></td>` +
              `<td>${gpuUsage(u)}</td>` +
              `<td>${gpuMemory(u)}</td>` +
              `</tr>`
          )
          .join("") || `<tr class="empty-row"><td colspan="7">No usage data.</td></tr>`}</tbody>` +
      `</table>` +
    `</div>` +
    `<div class="card"><div class="card-head"><h2>Last 24 Hours</h2></div><div class="card-body">` +
      `<div class="stats-grid">${trendCards(history).join("") || `<p class="hint">No history yet.</p>`}</div>` +
    `</div></div>` +
    (isAdmin() ? `<div class="card"><div class="card-head"><h2>Top Processes</h2></div>` +
      `<table class="data"><thead><tr><th>User</th><th>Container</th><th>PID</th><th>CPU</th><th>Command</th></tr></thead>` +
      `<tbody>${(processes || []).map(processRow).join("") || `<tr class="empty-row"><td colspan="5">No process data.</td></tr>`}</tbody></table>` +
    `</div>` : "") +
    `</div>`;
}

function trendCards(history) {
  const byUser = new Map();
  for (const s of history || []) {
    const key = s.username || "unknown";
    const list = byUser.get(key) || [];
    list.push(s);
    byUser.set(key, list);
  }
  return [...byUser.entries()].slice(0, 12).map(([user, list]) => {
    const cpu = list.map((x) => x.cpuPct || 0);
    const mem = list.map((x) => x.memMb || 0);
    const maxCpu = Math.max(...cpu, 0).toFixed(1);
    const maxMem = Math.max(...mem, 0).toFixed(0);
    const gpu = list.map((x) => x.gpuPct || 0);
    const maxGpu = Math.max(...gpu, 0).toFixed(1);
    return `<div class="stat-card"><div class="stat-card-head">${escapeHtml(user)}</div><div class="stat-card-value">${maxCpu}% CPU</div><div class="stat-card-sub">${maxMem} MB peak memory · ${maxGpu}% peak GPU</div>${spark(cpu)}</div>`;
  });
}

function gpuUsage(u) {
  if (!u.gpu || u.gpu === "none") return `<span class="muted-value">n/a</span>`;
  return `${(u.gpuPct || 0).toFixed(1)}%`;
}

function gpuMemory(u) {
  if (!u.gpu || u.gpu === "none") return `<span class="muted-value">n/a</span>`;
  const used = u.gpuMemMb || 0;
  const total = u.gpuMemTotalMb || 0;
  if (!total) return `<span class="muted-value">n/a</span>`;
  return `<div>${used.toFixed(0)} / ${total.toFixed(0)} MB</div><div class="secondary-line">${((u.gpuMemPct || (used / total * 100)) || 0).toFixed(1)}%</div>`;
}

function avg(values) {
  if (!values.length) return 0;
  return values.reduce((sum, v) => sum + v, 0) / values.length;
}

function processRow(p) {
  return `<tr><td>${escapeHtml(p.user || "")}</td><td>${escapeHtml(p.container || "")}</td><td class="mono">${escapeHtml(p.pid || "")}</td><td>${(p.cpuPct || 0).toFixed(1)}%</td><td><div class="secondary-line mono">${escapeHtml(p.command || "")}</div></td></tr>`;
}

function spark(series) {
  if (!series || series.length < 2) return "";
  const w = 160, h = 34, max = Math.max(...series, 1);
  const pts = series.map((v, i) => `${((i / (series.length - 1)) * w).toFixed(1)},${(h - (v / max) * h).toFixed(1)}`).join(" ");
  return `<svg class="spark wide" viewBox="0 0 ${w} ${h}" preserveAspectRatio="none"><polyline points="${pts}" fill="none" stroke="currentColor" stroke-width="1.5"/></svg>`;
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
