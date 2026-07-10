// Hardware monitoring: real-time host CPU, memory, temperature, and per-GPU
// utilisation/memory. Polls GET /api/hardware every few seconds while the tab is
// visible and tears down the timer when the user navigates away.
//
// In addition to live gauges, this view keeps an in-memory rolling history of
// each polled sample (reset on tab entry / page reload, same as htop/nvtop
// resetting their graphs on restart) so CPU/memory get an htop-style trend
// chart and GPUs/sensors get nvtop/lm-sensors-style trend sparklines.

import { api, escapeHtml } from "../app.js";

const POLL_MS = 5000;
// 120 samples @ 5s = 10 minutes of history, similar to htop's default window.
const HISTORY_LEN = 120;

// pollTimer holds the active interval id so it can be cleared when leaving the tab
// (otherwise polling continues in the background after navigating away).
let pollTimer = null;

// Rolling history buffers. Module-level so they survive the 5s re-renders of
// #hwRoot but reset on a fresh page load or when the tab is re-entered.
let history = null;

function freshHistory() {
  return { t: [], cpu: [], mem: [], gpu: new Map(), sensor: new Map() };
}

function pushSeries(arr, value) {
  arr.push(value);
  if (arr.length > HISTORY_LEN) arr.shift();
}

function pushHistory(snap) {
  const host = snap.host || {};
  const now = snap.updated ?? Date.now();
  pushSeries(history.t, now);
  pushSeries(history.cpu, Number(host.cpuPct) || 0);
  pushSeries(history.mem, Number(host.memPct) || 0);

  const seenGpu = new Set();
  for (const g of snap.gpus || []) {
    seenGpu.add(g.index);
    if (!history.gpu.has(g.index)) history.gpu.set(g.index, { util: [], temp: [] });
    const h = history.gpu.get(g.index);
    pushSeries(h.util, Number(g.utilPct) || 0);
    pushSeries(h.temp, Number(g.tempC) || 0);
  }
  for (const key of history.gpu.keys()) if (!seenGpu.has(key)) history.gpu.delete(key);

  const seenSensor = new Set();
  for (const t of host.temp || []) {
    seenSensor.add(t.name);
    if (!history.sensor.has(t.name)) history.sensor.set(t.name, []);
    pushSeries(history.sensor.get(t.name), Number(t.tempC) || 0);
  }
  for (const key of history.sensor.keys()) if (!seenSensor.has(key)) history.sensor.delete(key);
}

export function renderHardware() {
  history = freshHistory();
  // Render a skeleton immediately so the shell is responsive while the first fetch
  // resolves; renderHardwareData() replaces #hwRoot content with real numbers.
  // #hwTooltip is a sibling of #hwRoot (not inside it) so it survives the 5s
  // innerHTML replacement in refreshHardware() -> renderHardwareData().
  $("#view").innerHTML =
    `<div class="dash-stack" id="hwRoot"><div class="card"><div class="card-body"><p class="hint">Loading hardware snapshot…</p></div></div></div>` +
    `<div class="hw-tooltip" id="hwTooltip" hidden></div>`;
  bindHover();
  // Tear down any prior timer before starting a fresh one (covers tab re-entry).
  stopPolling();
  void refreshHardware();
  pollTimer = setInterval(refreshHardware, POLL_MS);
}

async function refreshHardware() {
  let snap;
  try {
    snap = await api("/api/hardware");
  } catch (err) {
    // Render the error but keep polling so the page recovers when the host comes back.
    const root = $("#hwRoot");
    if (root) root.innerHTML = `<div class="card"><div class="card-body"><div class="error-box">✗ ${escapeHtml(err.message)}</div></div></div>`;
    return;
  }
  pushHistory(snap);
  renderHardwareData(snap);
}

function renderHardwareData(snap) {
  const sys = snap.system || {};
  const host = snap.host || {};
  const gpus = snap.gpus || [];
  const root = $("#hwRoot");
  if (!root) return;
  root.innerHTML =
    `<div class="dash-tiles">` +
    tile("CPU", (host.cpuPct ?? 0).toFixed(1) + "%", `${sys.cpus ?? "?"} cores`, "🖥", host.cpuPct) +
    tile("Memory", (host.memPct ?? 0).toFixed(1) + "%", `${fmtMB(host.memUsedMb)} / ${fmtMB(host.memTotalMb)}`, "🧠", host.memPct) +
    tile("Load (1m)", (host.load1 ?? 0).toFixed(2), `CPUs: ${sys.cpus ?? "?"}`, "📈") +
    tile("GPU Temp", avgTemp(gpus), `${gpus.length} GPU${gpus.length === 1 ? "" : "s"}`, "🌡") +
    `</div>` +
    `<div class="hist-row">` +
    historyChart("CPU usage", history.cpu, history.t, { color: "var(--chart-cpu)" }) +
    historyChart("Memory usage", history.mem, history.t, { color: "var(--chart-mem)" }) +
    `</div>` +
    `<div class="dash-section-head"><h2>GPUs</h2><span class="hint">${gpus.length} detected</span></div>` +
    gpuGrid(gpus) +
    `<div class="dash-row-2">` +
    hostCard(sys, host) +
    tempCard(host.temp || []) +
    `</div>` +
    `<p class="hint">Updated ${new Date(snap.updated ?? Date.now()).toLocaleTimeString()} · refreshes every ${POLL_MS / 1000}s</p>`;
}

// tile renders a uniform stat tile with a big number, an optional meter bar
// (for 0-100 percentage metrics), and a sub label.
function tile(label, value, sub, icon, barPct) {
  return (
    `<div class="stat-tile">` +
      `<div class="stat-tile-head"><span class="stat-emoji">${icon}</span><span class="stat-label">${escapeHtml(label)}</span></div>` +
      `<div class="stat-value">${escapeHtml(value)}</div>` +
      (barPct != null ? loadBars(barPct) : "") +
      `<div class="stat-sub">${escapeHtml(sub)}</div>` +
    `</div>`
  );
}

// loadBars renders a thin meter bar for a 0-100 percentage value.
function loadBars(pct) {
  const p = Math.max(0, Math.min(100, Number(pct) || 0));
  return `<div class="bar"><div class="bar-fill" style="width:${p}%"></div></div>`;
}

// avgTemp returns the average GPU temperature across cards, or "—" if none report it.
function avgTemp(gpus) {
  const temps = gpus.map((g) => Number(g.tempC)).filter((t) => t > 0);
  if (!temps.length) return "—";
  return (temps.reduce((a, b) => a + b, 0) / temps.length).toFixed(0) + "°C";
}

// gpuGrid renders one card per GPU with utilisation/memory bars plus a small
// utilisation + temperature trend sparkline (nvtop-style).
function gpuGrid(gpus) {
  if (!gpus || !gpus.length) {
    return `<div class="card"><div class="card-body"><p class="hint">No NVIDIA GPUs detected (nvidia-smi unavailable).</p></div></div>`;
  }
  const cards = gpus
    .map((g) => {
      const h = history.gpu.get(g.index) || { util: [], temp: [] };
      return (
        `<div class="card gpu-card">` +
          `<div class="card-head"><h3>${escapeHtml(g.name || "GPU " + g.index)}</h3><span class="badge ${tempClass(g.tempC)}">${(g.tempC ?? 0).toFixed(0)}°C</span></div>` +
          `<div class="card-body">` +
            metricRow("GPU util", (g.utilPct ?? 0).toFixed(0) + "%", loadBars(g.utilPct)) +
            metricRow("Memory", `${fmtMB(g.memUsedMb)} / ${fmtMB(g.memTotalMb)}`, loadBars(g.memPct)) +
            (g.powerW > 0 ? metricRow("Power", g.powerW.toFixed(1) + " W", "") : "") +
            (g.memUtilPct > 0 ? metricRow("Mem controller", g.memUtilPct.toFixed(0) + "%", loadBars(g.memUtilPct)) : "") +
            `<div class="gpu-trends">` +
              trendRow("Util trend", h.util, "var(--chart-cpu)", 100) +
              trendRow("Temp trend", h.temp, trendColor(g.tempC), 100) +
            `</div>` +
          `</div>` +
        `</div>`
      );
    })
    .join("");
  return `<div class="gpu-grid">${cards}</div>`;
}

// trendColor maps a current temperature reading to a severity colour so the
// mini trend line reads at a glance, matching the badge severity elsewhere.
function trendColor(tempC) {
  const t = Number(tempC) || 0;
  if (t >= 85) return "var(--danger)";
  if (t >= 70) return "var(--warn)";
  return "var(--ok)";
}

// trendRow renders a small decorative sparkline (no hover) next to a label,
// used for GPU/sensor history where a full interactive chart would be noise.
function trendRow(label, values, color, max) {
  return (
    `<div class="trend-row">` +
      `<span class="trend-label">${escapeHtml(label)}</span>` +
      miniSparkline(values, { color, max }) +
    `</div>`
  );
}

// metricRow renders a labelled metric with an optional inline bar on the right.
function metricRow(label, value, bar) {
  return (
    `<div class="metric-row">` +
      `<div class="metric-label">${escapeHtml(label)}</div>` +
      `<div class="metric-value">${escapeHtml(value)}</div>` +
      (bar ? `<div class="metric-bar">${bar}</div>` : "") +
    `</div>`
  );
}

// tempClass picks a badge colour by temperature severity.
function tempClass(tempC) {
  const t = Number(tempC) || 0;
  if (t >= 85) return "badge-warn";
  if (t >= 70) return "badge-ok";
  return "badge-muted";
}

// hostCard shows the static host facts (OS, kernel, CPU count, total memory, Docker version).
function hostCard(sys, host) {
  const rows = [
    ["Host", sys.name],
    ["OS", `${sys.osType || ""} ${sys.osVersion || ""}`.trim()],
    ["Kernel", sys.kernel],
    ["Architecture", sys.arch],
    ["CPU cores", sys.cpus],
    ["Total memory", fmtMB(host.memTotalMb)],
    ["Docker", sys.dockerVersion],
    ["Storage driver", sys.storageDriver],
  ];
  return (
    `<div class="card"><div class="card-head"><h2>Host</h2></div><div class="card-body">` +
      rows
        .filter(([, v]) => v !== undefined && v !== null && v !== "")
        .map(([k, v]) => `<div class="kv"><span>${escapeHtml(k)}</span><strong>${escapeHtml(String(v))}</strong></div>`)
        .join("") +
    `</div></div>`
  );
}

// tempCard shows motherboard/CPU temperature sensors read from /sys/class/hwmon,
// each with a small trend sparkline (lm-sensors "watch" style).
function tempCard(temps) {
  if (!temps || !temps.length) return `<div class="card"><div class="card-head"><h2>Temperatures</h2></div><div class="card-body"><p class="hint">No temperature sensors available.</p></div></div>`;
  const rows = temps
    .map((t) => {
      const h = history.sensor.get(t.name) || [];
      return (
        `<div class="sensor-row">` +
          `<div class="kv"><span>${escapeHtml(t.name)}</span><strong class="${tempClass(t.tempC)}">${(t.tempC ?? 0).toFixed(1)}°C</strong></div>` +
          miniSparkline(h, { color: trendColor(t.tempC), max: null }) +
        `</div>`
      );
    })
    .join("");
  return `<div class="card"><div class="card-head"><h2>Temperatures</h2></div><div class="card-body">${rows}</div></div>`;
}

// fmtMB formats a megabyte figure with a unit suffix, rounding large values to GB.
function fmtMB(mb) {
  const v = Number(mb) || 0;
  if (v >= 1024) return (v / 1024).toFixed(1) + " GB";
  return v.toFixed(0) + " MB";
}

// ---------------- Charts ----------------
// Small inline-SVG line chart helpers. No charting dependency: everything here
// is a plain polyline/area path built from the rolling history buffers above.

// buildPath maps a value series onto a w×h SVG viewport and returns the line
// path, an area path closed to the baseline, and the raw plotted points.
function buildPath(values, w, h, max, min) {
  const n = values.length;
  if (n < 2) return { line: "", area: "", points: [] };
  const lo = min ?? 0;
  const hi = max ?? Math.max(...values, 1);
  const range = hi - lo || 1;
  const points = values.map((v, i) => {
    const x = (i / (n - 1)) * w;
    const y = h - Math.max(0, Math.min(1, (v - lo) / range)) * h;
    return [x, y];
  });
  const line = points.map((p, i) => `${i === 0 ? "M" : "L"}${p[0].toFixed(2)},${p[1].toFixed(2)}`).join(" ");
  const area = `${line} L${w.toFixed(2)},${h.toFixed(2)} L0,${h.toFixed(2)} Z`;
  return { line, area, points };
}

// historyChart renders a big interactive (hover) history chart used for the
// two headline metrics (CPU%, Memory%), both fixed to a 0-100 scale.
function historyChart(label, values, times, { color }) {
  const w = 600, h = 130;
  const { line, area, points } = buildPath(values, w, h, 100, 0);
  const last = values.length ? values[values.length - 1] : 0;
  const grid = [0, 0.5, 1]
    .map((f) => `<line x1="0" y1="${(h * f).toFixed(1)}" x2="${w}" y2="${(h * f).toFixed(1)}" class="hist-grid" />`)
    .join("");
  const dot = points.length
    ? `<circle cx="${points[points.length - 1][0].toFixed(2)}" cy="${points[points.length - 1][1].toFixed(2)}" r="4" fill="${color}" stroke="var(--workspace)" stroke-width="2" />`
    : "";
  const body = points.length
    ? `<svg viewBox="0 0 ${w} ${h}" preserveAspectRatio="none">` +
        grid +
        `<path d="${area}" fill="${color}" fill-opacity="0.1" stroke="none"></path>` +
        `<path d="${line}" fill="none" stroke="${color}" stroke-width="2" stroke-linejoin="round" stroke-linecap="round"></path>` +
        dot +
      `</svg>` +
      `<div class="hist-crosshair" hidden></div><div class="hist-dot" hidden></div>`
    : `<p class="hint">Collecting data…</p>`;
  const startAgo = times.length > 1 ? Math.round((times[times.length - 1] - times[0]) / 60000) : 0;
  return (
    `<div class="hist-card">` +
      `<div class="hist-head"><span class="hist-label">${escapeHtml(label)}</span><span class="hist-current" style="color:${color}">${last.toFixed(1)}%</span></div>` +
      `<div class="hist-chart" data-values="${values.join(",")}" data-times="${times.join(",")}" data-max="100" data-min="0" data-color="${color}" data-unit="%">${body}</div>` +
      (points.length ? `<div class="hist-axis"><span>${startAgo > 0 ? startAgo + " min ago" : "just now"}</span><span>now</span></div>` : "") +
    `</div>`
  );
}

// miniSparkline renders a small decorative trend line (no hover, no axis) for
// GPU/sensor cards. max=null autoscales to the series' own range.
function miniSparkline(values, { color = "var(--accent)", max = 100 } = {}) {
  const w = 140, h = 28;
  const { line } = buildPath(values, w, h, max, 0);
  if (!line) return `<svg class="mini-spark" viewBox="0 0 ${w} ${h}" preserveAspectRatio="none"></svg>`;
  return (
    `<svg class="mini-spark" viewBox="0 0 ${w} ${h}" preserveAspectRatio="none">` +
      `<path d="${line}" fill="none" stroke="${color}" stroke-width="2" stroke-linejoin="round" stroke-linecap="round"></path>` +
    `</svg>`
  );
}

// bindHover wires a single delegated mousemove/mouseleave handler on #hwRoot so
// hovering a .hist-chart shows a crosshair + tooltip. Delegation is required
// because refreshHardwareData() replaces #hwRoot's children every poll tick;
// the listener itself (bound once per tab entry) and the shared #hwTooltip
// node (a sibling of #hwRoot) both survive those replacements.
function bindHover() {
  const root = $("#hwRoot");
  const tooltip = $("#hwTooltip");
  if (!root || !tooltip) return;
  root.addEventListener("mousemove", (e) => {
    const chart = e.target.closest(".hist-chart");
    if (!chart) {
      tooltip.hidden = true;
      return;
    }
    const values = chart.dataset.values.split(",").map(Number);
    const times = chart.dataset.times.split(",").map(Number);
    if (values.length < 2) {
      tooltip.hidden = true;
      return;
    }
    const min = Number(chart.dataset.min) || 0;
    const max = Number(chart.dataset.max) || 100;
    const unit = chart.dataset.unit || "";
    const color = chart.dataset.color || "var(--accent)";
    const rect = chart.getBoundingClientRect();
    const frac = Math.max(0, Math.min(1, (e.clientX - rect.left) / rect.width));
    const idx = Math.round(frac * (values.length - 1));
    const val = values[idx];
    const x = (idx / (values.length - 1)) * rect.width;
    const y = rect.height - (Math.max(0, Math.min(1, (val - min) / (max - min || 1))) * rect.height);

    const crosshair = chart.querySelector(".hist-crosshair");
    const dot = chart.querySelector(".hist-dot");
    if (crosshair) {
      crosshair.hidden = false;
      crosshair.style.left = `${x}px`;
    }
    if (dot) {
      dot.hidden = false;
      dot.style.left = `${x}px`;
      dot.style.top = `${y}px`;
      dot.style.background = color;
    }

    tooltip.hidden = false;
    tooltip.innerHTML = `<strong>${val.toFixed(1)}${unit}</strong><span>${timeAgo(Date.now() - times[idx])}</span>`;
    const left = Math.min(e.clientX + 14, window.innerWidth - tooltip.offsetWidth - 8);
    const top = Math.max(e.clientY - 40, 8);
    tooltip.style.left = `${left}px`;
    tooltip.style.top = `${top}px`;
  });
  root.addEventListener("mouseleave", () => {
    tooltip.hidden = true;
  });
}

// timeAgo formats a millisecond age as a short relative label for the tooltip.
function timeAgo(ms) {
  if (ms < 15000) return "just now";
  const mins = Math.round(ms / 60000);
  if (mins < 1) return `${Math.round(ms / 1000)}s ago`;
  return `${mins}m ago`;
}

// stopPolling clears the hardware polling timer. Called when leaving the tab.
export function stopPolling() {
  if (pollTimer) {
    clearInterval(pollTimer);
    pollTimer = null;
  }
}

function $(selector) {
  return document.querySelector(selector);
}
