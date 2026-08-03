// Dashboard: environment overview, resource counts, and the caller's workspace
// rollup. Rendered from a single /api/dashboard payload (no N+1 fetches).

import { state, api, escapeHtml, isAdmin, displayName, t } from "../app.js";
import { detectClientIP } from "../lib/publicip.js";

export function renderDashboard() {
  const d = state.dashboard;
  const admin = isAdmin();
  if (!d) {
    $("#view").innerHTML = `<div class="card"><div class="card-body"><p class="hint">${t("subtitle.dashboard")}</p></div></div>`;
    return;
  }
  const sys = d.system || {};
  const mine = d.mine || {};
  const user = d.user || {};
  const feishuUser = !!(user.feishuOpenId && user.feishuOpenId !== "");
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
      // Row 2.5: Feishu identity card (only for Feishu-SSO users)
      (feishuUser ? `<div class="dash-row-feishu">${feishuCard(user)}</div>` : "") +
      // Row 3: my workspace + top users (admin) or my containers
      `<div class="dash-row-3">` +
        myWorkspaceCard(mine) +
        (admin ? topUsersCard(d.usage || []) : myContainersCard()) +
      `</div>` +
      // Row 4: recent activity (admin only)
      (admin ? `<div class="dash-row-full">${recentActivityCard(state.audit || [])}</div>` : "") +
    `</div>`;
  // Kick off the client-side browser-IP probe after the DOM is painted. It
  // resolves into the #dash-client-ip span in "My Workspace" without touching
  // the rest of the dashboard, so a refresh mid-probe just restarts it.
  probeClientIP();
}

// probeClientIP fills the visitor's own IP, its location, and the visitor's
// LAN address in the "My Workspace" card. Detection runs several methods in
// parallel (WebRTC/STUN + HTTPS "what is my IP" services) via detectClientIP,
// then — once a public IP is known — asks /api/geo for the city/country to show
// next to it. Each step re-checks that its element is still in the DOM so the
// 8s dashboard poller (which may rebuild #view) can't clobber a stale result.
async function probeClientIP() {
  const ipEl = document.getElementById("dash-client-ip");
  const locEl = document.getElementById("dash-client-loc");
  const lanEl = document.getElementById("dash-client-lan");
  if (!ipEl) return;

  let pub = [];
  let lan = [];
  let geo = null;
  try {
    ({ public: pub, lan, geo } = await detectClientIP(4000));
  } catch (_) {
    // pub/lan stay empty — rendered as "unavailable" below.
  }
  if (!document.body.contains(ipEl)) return; // dashboard re-rendered under us

  ipEl.textContent = pub.length ? pub.join(", ") : t("dash.detectFailed");
  ipEl.classList.toggle("hint", pub.length === 0);

  if (lanEl) {
    lanEl.textContent = lan.length ? lan.join(", ") : "—";
    lanEl.classList.toggle("hint", lan.length === 0);
  }

  // Location chip. When the Worker answered /whoami it already carried geo (in
  // the same shape /api/geo returns), so we render it directly and skip a
  // second round-trip. Otherwise fall back to the server's cached GeoIP proxy
  // (ip-api.com is HTTP-only → the browser can't reach it on HTTPS pages).
  if (locEl) {
    if (geo) {
      if (document.body.contains(locEl)) renderClientLocation(locEl, geo);
    } else if (pub.length) {
      try {
        const g = await api(`/api/geo?ip=${encodeURIComponent(pub[0])}`);
        if (document.body.contains(locEl)) renderClientLocation(locEl, g);
      } catch (_) {
        // Leave the placeholder; location is a nice-to-have, not essential.
      }
    }
  }
}

// renderClientLocation fills the location chip from a /api/geo response:
// "🇯🇵 Tokyo, Japan" style, plus an ISP/proxy hint when present. Empty geo
// (disabled or unresolved) clears the chip so we don't show a bare flag.
function renderClientLocation(el, geo) {
  if (!geo || (!geo.city && !geo.country)) {
    el.textContent = "";
    el.className = "hint";
    return;
  }
  const flag = countryCodeFlag(geo.countryCode);
  const place = [geo.city, geo.country].filter(Boolean).join(", ") || "—";
  const parts = [`${flag} ${escapeHtml(place)}`];
  if (geo.isp) parts.push(`<span class="hint">· ${escapeHtml(geo.isp)}</span>`);
  if (geo.proxy || geo.hosting) {
    parts.push(`<span class="badge badge-warn">${escapeHtml(t("dash.vpnProxy"))}</span>`);
  }
  el.innerHTML = parts.join(" ");
  el.className = "client-loc";
}

// countryCodeFlag turns a 2-letter country code into its regional-indicator
// flag emoji. Mirrors the helper in security.js; duplicated here to keep the
// dashboard module dependency-free (importing security.js would pull the whole
// security page in).
function countryCodeFlag(code) {
  if (!code || code.length !== 2) return "🌍";
  const A = 0x1f1e6;
  const base = "A".charCodeAt(0);
  const cc = code.toUpperCase();
  return String.fromCodePoint(A + cc.charCodeAt(0) - base, A + cc.charCodeAt(1) - base);
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
    [t("dash.lanIp"), escapeHtml((sys.lanIps || []).join(", ") || "—")],
    // Public IP is the server's own egress address, probed outbound by the
    // agent (sys.publicIp). Distinct from the visitor's browser IP, which is
    // detected client-side and shown in the "My Workspace" card.
    [t("dash.publicIp"), escapeHtml(sys.publicIp || "—")],
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

function feishuCard(u) {
  const avatar = u.feishuAvatar || "";
  const name = escapeHtml(displayName(u) || u.username || "—");
  const rows = [
    [t("dash.feishuOpenId"), escapeHtml(u.feishuOpenId || "—")],
    [t("dash.feishuEnterpriseEmail"), escapeHtml(u.feishuEnterpriseEmail || u.feishuEmail || "—")],
    [t("dash.feishuEmail"), escapeHtml(u.feishuEmail || "—")],
    [t("dash.feishuMobile"), escapeHtml(u.feishuMobile || "—")],
    [t("dash.feishuTenant"), escapeHtml(u.feishuTenantName || u.feishuTenantKey || "—")],
    [t("dash.feishuDepartment"), escapeHtml(u.feishuDepartment || "—")],
    [t("dash.feishuLastLogin"), escapeHtml(u.lastLoginAt || "—")],
  ];
  const avatarHtml = avatar
    ? `<img src="${escapeHtml(avatar)}" alt="" class="feishu-avatar" loading="lazy">`
    : `<div class="feishu-avatar feishu-avatar-placeholder">🧑‍💼</div>`;
  return (
    `<section class="card feishu-card">` +
      `<div class="card-head">` +
        `<h2>${t("dash.feishuProfile")}</h2>` +
        `<span class="badge badge-ok"><span class="dot"></span>${t("dash.feishuLoginMethod")}</span>` +
      `</div>` +
      `<div class="card-body feishu-card-body">` +
        `<div class="feishu-header">` +
          `${avatarHtml}` +
          `<div class="feishu-title">` +
            `<div class="feishu-name">${name}</div>` +
            `<div class="feishu-handle">${escapeHtml(u.username || "—")}</div>` +
          `</div>` +
        `</div>` +
        `<dl class="detail feishu-detail">${rows.map((r) => `<dt>${r[0]}</dt><dd>${r[1]}</dd>`).join("")}</dl>` +
      `</div>` +
    `</section>`
  );
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
        // Your IP: the visitor's own browser IP + its location, detected
        // client-side via WebRTC/STUN and HTTPS services after render, then
        // geo-located through /api/geo. Stable ids let the async probe update
        // these cells without re-rendering the card. Distinct from the
        // server's public IP shown in the Environment card.
        `<div class="client-ip-block">` +
          `<div class="kv"><span>${t("dash.yourIp")}</span>` +
            `<span class="client-ip-main">` +
              `<strong id="dash-client-ip" class="mono hint">${escapeHtml(t("dash.detecting"))}</strong>` +
              `<span id="dash-client-loc" class="hint"></span>` +
            `</span>` +
          `</div>` +
          `<div class="kv"><span>${t("dash.yourLan")}</span><strong id="dash-client-lan" class="mono hint">${escapeHtml(t("dash.detecting"))}</strong></div>` +
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
