// Dashboard: environment overview, resource counts, and the caller's workspace
// rollup. Rendered from a single /api/dashboard payload (no N+1 fetches).

import { state, escapeHtml, isAdmin, displayName, t } from "../app.js";

export function renderDashboard() {
  const d = state.dashboard;
  const admin = isAdmin();
  if (!d) {
    $("#view").innerHTML = `<div class="card"><div class="card-body"><p class="hint">${t("subtitle.dashboard")}</p></div></div>`;
    return;
  }
  const sys = d.system || {};
  const mine = d.mine || {};
  const healthy = !!sys.healthy;
  // Non-admins see only their own resource counts; admins see the whole
  // platform. A short label keeps the scope obvious.
  const scopeHint = admin
    ? ""
    : `<p class="hint dash-scope">${t("dash.scopeMine")}</p>`;

  $("#view").innerHTML =
    `<div class="dash-stack">` +
      scopeHint +
      // Row 1: four uniform stat tiles
      `<div class="dash-tiles">` +
        statTile(t("nav.containers"), sys.containers?.total ?? 0, t("dash.running", { n: sys.containers?.running ?? 0 }), "📦") +
        statTile(t("nav.images"), sys.images?.count ?? 0, admin ? fmtMB(sys.images?.sizeMb) : t("dash.inUse", { n: sys.images?.count ?? 0 }), "🖼") +
        statTile(t("nav.volumes"), sys.volumes?.count ?? 0, fmtMB(sys.volumes?.sizeMb), "💾") +
        statTile(t("nav.networks"), sys.networks ?? 0, admin ? t("dash.managedSystem") : t("dash.mineSystem"), "🌐") +
      `</div>` +
      // Row 2: environment (wide) + donut chart
      `<div class="dash-row-2">` +
        envCard(sys, healthy) +
        containersChart(sys.containers || {}) +
      `</div>` +
      // Row 3: my workspace + top users (admin) or my containers
      `<div class="dash-row-3">` +
        myWorkspaceCard(mine) +
        (admin ? topUsersCard(d.usage || []) : myContainersCard()) +
      `</div>` +
      // Row 4: recent activity (admin only)
      (admin ? `<div class="dash-row-full">${recentActivityCard(state.audit || [])}</div>` : "") +
    `</div>`;
}

// myContainersCard shows a non-admin their own containers as a compact list.
function myContainersCard() {
  const list = (state.containers || []).slice(0, 6);
  const rows = list.map((c) =>
    `<li><span class="badge ${c.state === "running" ? "badge-ok" : "badge-muted"}"><span class="dot"></span>${escapeHtml(c.state || "?")}</span>` +
    `<span class="primary-line">${escapeHtml(c.name || c.fullName)}</span>` +
    `<span class="secondary-line">${escapeHtml(c.image || "")}</span></li>`
  );
  return (
    `<section class="card">` +
      `<div class="card-head"><h2>${t("dash.myContainers")}</h2></div>` +
      `<div class="card-body"><ul class="audit-mini">${rows.join("") || `<li class="hint">${t("dash.noContainers")}</li>`}</ul></div>` +
    `</section>`
  );
}

function envCard(sys, healthy) {
  const rows = [
    [t("hardware.host"), escapeHtml(sys.name || "—")],
    ["OS", escapeHtml([sys.osType, sys.osVersion].filter(Boolean).join(" ") || "—")],
    ["Kernel", escapeHtml(sys.kernel || "—")],
    [t("dash.arch"), escapeHtml(sys.arch || "—")],
    ["CPUs", String(sys.cpus ?? "—")],
    [t("hardware.memory"), sys.memoryGb ? `${sys.memoryGb} GB` : "—"],
    ["Docker", escapeHtml(sys.dockerVersion || "—")],
    [t("hardware.colStorageDriver"), escapeHtml(sys.storageDriver || "—")],
    [t("dash.agent"), escapeHtml(sys.agentGoRuntime || "—") + ` · ${sys.agentCpu ?? "—"} CPU · ${fmtMB(sys.agentMemMb)} mem`],
  ];
  return (
    `<section class="card">` +
      `<div class="card-head"><h2>${t("dash.environment")}</h2>` +
        `<span class="badge ${healthy ? "badge-ok" : "badge-danger"}"><span class="dot"></span>${healthy ? t("dash.healthy") : t("dash.unreachable")}</span>` +
      `</div>` +
      `<div class="card-body"><dl class="detail">${rows.map((r) => `<dt>${r[0]}</dt><dd>${r[1]}</dd>`).join("")}</dl></div>` +
    `</section>`
  );
}

function statTile(label, value, sub, icon) {
  return (
    `<section class="card stat-tile">` +
      `<div class="stat-icon">${icon}</div>` +
      `<div class="stat-body">` +
        `<div class="stat-value">${escapeHtml(String(value))}</div>` +
        `<div class="stat-label">${escapeHtml(label)}</div>` +
        `<div class="stat-sub">${escapeHtml(sub)}</div>` +
      `</div>` +
    `</section>`
  );
}

// Containers-by-state donut. Pure CSS conic-gradient — no chart dependency.
function containersChart(c) {
  const total = c.total || 0;
  const running = c.running || 0;
  const stopped = c.stopped || 0;
  const paused = c.paused || 0;
  const unhealthy = c.unhealthy || 0;
  // Build conic-gradient slices. Order: running, paused, stopped.
  const pct = (n) => (total > 0 ? (n / total) * 100 : 0);
  let acc = 0;
  const slices = [];
  for (const [_label, n, color] of [
    [t("containers.filterRunning"), running, "var(--ok)"],
    [t("containers.filterPaused"), paused, "var(--warn)"],
    [t("containers.filterStopped"), stopped, "var(--muted)"],
  ]) {
    const span = pct(n);
    slices.push(`${color} ${acc}% ${acc + span}%`);
    acc += span;
  }
  const bg = total > 0 ? `conic-gradient(${slices.join(", ")})` : "var(--line-soft)";
  return (
    `<section class="card">` +
      `<div class="card-head"><h2>${t("dash.containers")}</h2></div>` +
      `<div class="card-body chart-row">` +
        `<div class="donut" style="background:${bg}"><div class="donut-hole"><strong>${total}</strong><span>${t("dash.total")}</span></div></div>` +
        `<ul class="legend">` +
          legendItem(t("containers.filterRunning"), running, "var(--ok)") +
          legendItem(t("containers.filterPaused"), paused, "var(--warn)") +
          legendItem(t("containers.filterStopped"), stopped, "var(--muted)") +
          (unhealthy > 0 ? legendItem("Unhealthy", unhealthy, "var(--danger)") : "") +
        `</ul>` +
      `</div>` +
    `</section>`
  );
}

function legendItem(label, n, color) {
  return `<li><span class="swatch" style="background:${color}"></span>${escapeHtml(label)} <strong>${n}</strong></li>`;
}

function myWorkspaceCard(mine) {
  const cap = mine.cap || 0;
  const used = mine.containers || 0;
  const pct = cap > 0 ? Math.min(100, Math.round((used / cap) * 100)) : 0;
  return (
    `<section class="card">` +
      `<div class="card-head"><h2>${t("dash.myWorkspace")}</h2></div>` +
      `<div class="card-body">` +
        `<div class="kv"><span>${t("nav.containers")}</span><strong>${used}${cap ? ` / ${cap}` : ""}</strong></div>` +
        `<div class="bar"><div class="bar-fill" style="width:${pct}%"></div></div>` +
        `<div class="kv-row">` +
          kv(t("containers.filterRunning"), mine.running ?? 0) +
          kv(t("hardware.memory"), fmtMB(mine.memoryMb)) +
          kv(t("common.disk"), fmtMB(mine.diskMb)) +
        `</div>` +
      `</div>` +
    `</section>`
  );
}

function kv(label, value) {
  return `<div class="kv"><span>${escapeHtml(label)}</span><strong>${escapeHtml(String(value))}</strong></div>`;
}

function topUsersCard(usage) {
  const top = [...(usage || [])]
    .sort((a, b) => (b.containers || 0) - (a.containers || 0))
    .slice(0, 6);
  return (
    `<section class="card">` +
      `<div class="card-head"><h2>${t("dash.topUsers")}</h2></div>` +
      `<table class="data"><thead><tr><th>${t("common.user")}</th><th>${t("common.containers")}</th><th>${t("hardware.memory")}</th><th>${t("common.disk")}</th><th>${t("common.gpu")}</th></tr></thead>` +
      `<tbody>${top.map(topRow).join("") || `<tr class="empty-row"><td colspan="5">${t("common.noData")}</td></tr>`}</tbody></table>` +
    `</section>`
  );
}

function topRow(u) {
  return (
    `<tr>` +
      `<td><div class="primary-line">${escapeHtml(displayName(u))}</div></td>` +
      `<td>${u.containers || 0}</td>` +
      `<td>${fmtMB(u.memoryMb)}</td>` +
      `<td>${fmtMB(u.diskMb)}</td>` +
      `<td><div class="secondary-line">${escapeHtml(u.gpu || "none")}</div></td>` +
    `</tr>`
  );
}

function recentActivityCard(audit) {
  const rows = (audit || []).slice(0, 8).map((e) =>
    `<li><span class="audit-actor">${escapeHtml(e.actor)}</span>` +
    `<span class="audit-act">${escapeHtml(e.action)}</span>` +
    `<span class="audit-target mono">${escapeHtml(e.target)}</span>` +
    `<span class="audit-time">${escapeHtml(relTime(e.createdAt))}</span></li>`
  );
  return (
    `<section class="card">` +
      `<div class="card-head"><h2>${t("dash.recentActivity")}</h2></div>` +
      `<div class="card-body"><ul class="audit-mini">${rows.join("") || `<li class="hint">${t("dash.noActivity")}</li>`}</ul></div>` +
    `</section>`
  );
}

function fmtMB(mb) {
  if (!mb || mb <= 0) return "0 MB";
  if (mb < 1024) return `${Math.round(mb)} MB`;
  return `${(mb / 1024).toFixed(1)} GB`;
}

function relTime(iso) {
  if (!iso) return "";
  const tt = new Date(iso).getTime();
  if (isNaN(tt)) return iso;
  const s = Math.round((Date.now() - tt) / 1000);
  if (s < 60) return t("dash.sAgo", { n: s });
  if (s < 3600) return t("dash.minAgo", { n: Math.round(s / 60) });
  if (s < 86400) return t("dash.hAgo", { n: Math.round(s / 3600) });
  return t("dash.dAgo", { n: Math.round(s / 86400) });
}

function $(selector) {
  return document.querySelector(selector);
}
