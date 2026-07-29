// Per-user resource usage table (admin only).

import { state, api, isAdmin, displayName, displayNameForUsername, t } from "../app.js";

export async function renderUsage() {
  const tabAtEntry = state.tab;
  const [history, processes, usage] = await Promise.all([
    api("/api/resources/history").catch(() => []),
    isAdmin() ? api("/api/admin/processes").catch(() => []) : Promise.resolve([]),
    isAdmin() ? api("/api/admin/usage").catch(() => state.usage || []) : Promise.resolve(null),
  ]);
  // The user switched tabs mid-fetch: drop the result instead of painting
  // usage over the newly active view.
  if (state.tab !== tabAtEntry) return;
  if (isAdmin() && usage) {
    state.usage = usage;
  }
  const rows = (isAdmin()
    ? state.usage
    : [{
        username: state.me?.username || "me",
        displayName: state.me?.displayName || "",
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
      `<div class="card-head"><h2>${t("usage.resourceUsage")}</h2></div>` +
      `<table class="data">` +
        `<thead><tr><th>${t("common.user")}</th><th>${t("usage.colContainers")}</th><th>${t("usage.colMemory")}</th><th>${t("usage.colDisk")}</th><th>${t("usage.colGpu")}</th><th>${t("usage.colGpuUsage")}</th><th>${t("usage.colGpuMemory")}</th></tr></thead>` +
        `<tbody>${rows
          .map(
            (u) =>
              `<tr>` +
              `<td><div class="primary-line">${escapeHtml(displayName(u))}</div></td>` +
              `<td>${u.containers}</td>` +
              `<td>${(u.memoryMb || 0).toFixed(0)} MB</td>` +
              `<td>${(u.diskMb || 0).toFixed(0)} MB</td>` +
              `<td><div class="secondary-line">${escapeHtml(u.gpu || "none")}</div></td>` +
              `<td>${gpuUsage(u)}</td>` +
              `<td>${gpuMemory(u)}</td>` +
              `</tr>`
          )
          .join("") || `<tr class="empty-row"><td colspan="7">${t("usage.noUsage")}</td></tr>`}</tbody>` +
      `</table>` +
    `</div>` +
    `<div class="card"><div class="card-head"><h2>${t("usage.last24h")}</h2></div><div class="card-body">` +
      `<div class="stats-grid">${trendCards(history).join("") || `<p class="hint">${t("usage.noHistory")}</p>`}</div>` +
    `</div></div>` +
    (isAdmin() ? `<div class="card"><div class="card-head"><h2>${t("usage.topProcesses")}</h2></div>` +
      `<table class="data"><thead><tr><th>${t("common.user")}</th><th>${t("usage.colContainer")}</th><th>${t("usage.colPid")}</th><th>${t("usage.colCpu")}</th><th>${t("usage.colCommand")}</th></tr></thead>` +
      `<tbody>${(processes || []).map(processRow).join("") || `<tr class="empty-row"><td colspan="5">${t("usage.noProcess")}</td></tr>`}</tbody></table>` +
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
    // Samples are per-container, but each snapshot round shares one timestamp.
    // Aggregate per timestamp so the trend reflects the user's total footprint
    // (matching the live "Resource Usage" table, which sums across containers),
    // not just the largest single container reading.
    const byTs = new Map();
    for (const x of list) {
      const ts = x.createdAt || "";
      const agg = byTs.get(ts) || { cpu: 0, mem: 0, gpu: 0 };
      agg.cpu += x.cpuPct || 0;
      agg.mem += x.memMb || 0;
      // GPU % is already a per-user average in the snapshot, so don't double-sum.
      agg.gpu = Math.max(agg.gpu, x.gpuPct || 0);
      byTs.set(ts, agg);
    }
    const series = [...byTs.values()];
    const cpu = series.map((x) => x.cpu);
    const mem = series.map((x) => x.mem);
    const gpu = series.map((x) => x.gpu);
    const maxCpu = Math.max(...cpu, 0).toFixed(1);
    const maxMem = Math.max(...mem, 0).toFixed(0);
    const maxGpu = Math.max(...gpu, 0).toFixed(1);
    return `<div class="stat-card"><div class="stat-card-head">${escapeHtml(displayNameForUsername(user))}</div><div class="stat-card-value">${maxCpu}% ${t("hardware.cpu")}</div><div class="stat-card-sub">${t("usage.peakGpu", { mem: maxMem, gpu: maxGpu })}</div>${spark(cpu)}</div>`;
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
  return `<tr><td>${escapeHtml(displayNameForUsername(p.user || ""))}</td><td>${escapeHtml(p.container || "")}</td><td class="mono">${escapeHtml(p.pid || "")}</td><td>${(p.cpuPct || 0).toFixed(1)}%</td><td><div class="secondary-line mono">${escapeHtml(p.command || "")}</div></td></tr>`;
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
