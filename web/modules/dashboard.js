// Dashboard: environment overview, resource counts, and the caller's workspace
// rollup. Rendered from a single /api/dashboard payload (no N+1 fetches).

import { state, api, escapeHtml, isAdmin, displayName, t } from "../app.js";
import { detectClientIP, readIPCache, isCacheFresh } from "../lib/publicip.js";

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
// parallel (WebRTC/STUN + the configured IP-detection Worker) via detectClientIP.
// The result (plus a detection timestamp) is cached in localStorage and rendered
// immediately so the dashboard never flashes "detecting" on every poll; the cache
// is refreshed asynchronously in the background every 3 minutes and the UI
// updates silently when it completes. Each step re-checks that its element is
// still in the DOM so the 8s dashboard poller (which may rebuild #view) can't
// clobber a stale result.
async function probeClientIP() {
  const ipEl = document.getElementById("dash-client-ip");
  const locEl = document.getElementById("dash-client-loc");
  const lanEl = document.getElementById("dash-client-lan");
  if (!ipEl) return;

  let result;
  try {
    result = await detectClientIP(4000);
  } catch (_) {
    result = { public: [], lan: [], geo: null, background: Promise.resolve(null) };
  }
  if (!document.body.contains(ipEl)) return; // dashboard re-rendered under us

  renderClientIP(ipEl, lanEl, locEl, result);

  // If detectClientIP is refreshing the cache in the background, update the UI
  // silently when that probe finishes — without resetting the text to "detecting".
  if (result.background) {
    result.background
      .then((fresh) => {
        if (fresh && document.body.contains(ipEl)) {
          renderClientIP(ipEl, lanEl, locEl, fresh);
        }
      })
      .catch(() => {});
  }
}

// renderClientIP writes public IP, LAN IP, and (optionally) location into the
// "My Workspace" card. It keeps the existing value when the result is empty so
// a stale refresh doesn't erase a previously cached value.
function renderClientIP(ipEl, lanEl, locEl, result) {
  const pub = result.public || [];
  const lan = result.lan || [];
  const geo = result.geo || null;

  if (pub.length) {
    ipEl.textContent = pub.join(", ");
    ipEl.classList.remove("hint");
  } else if (!ipEl.textContent || ipEl.textContent === t("dash.detecting")) {
    ipEl.textContent = t("dash.detectFailed");
    ipEl.classList.add("hint");
  }

  if (lanEl) {
    if (lan.length) {
      lanEl.textContent = lan.join(", ");
      lanEl.classList.remove("hint");
    } else if (!lanEl.textContent || lanEl.textContent === t("dash.detecting")) {
      lanEl.textContent = "—";
      lanEl.classList.add("hint");
    }
  }

  // Location chip. When the Worker answered /whoami it already carried geo (in
  // the same shape /api/geo returns), so we render it directly and skip a
  // second round-trip. Otherwise fall back to the server's cached GeoIP proxy
  // (ip-api.com is HTTP-only → the browser can't reach it on HTTPS pages).
  if (locEl) {
    if (geo) {
      if (document.body.contains(locEl)) renderClientLocation(locEl, geo);
    } else if (pub.length) {
      api(`/api/geo?ip=${encodeURIComponent(pub[0])}`)
        .then((g) => {
          if (document.body.contains(locEl)) renderClientLocation(locEl, g);
        })
        .catch(() => {
          // Leave the placeholder; location is a nice-to-have, not essential.
        });
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
// Badge color/label mirrors containerRow() in containers.js so a container's
// state reads the same way everywhere in the app.
function myContainersCard() {
  const list = (state.containers || []).slice(0, 6);
  const rows = list.map((c) => {
    const running = c.state === "running";
    const paused = c.state === "paused";
    const badgeClass = running ? "badge-ok" : paused ? "badge-warn" : "badge-muted";
    const label = running ? t("containers.up") : paused ? t("containers.paused") : t("containers.stopped");
    return (
      `<li><span class="badge ${badgeClass}"><span class="dot"></span>${escapeHtml(label)}</span>` +
      `<span class="primary-line">${escapeHtml(c.name || c.fullName)}</span>` +
      `<span class="secondary-line">${escapeHtml(c.image || "")}</span></li>`
    );
  });
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

// isAllowedAvatarSrc mirrors the server's CSP img-src directive (see
// internal/middleware/secheaders.go: "img-src 'self' data: blob:") so we only
// ever point an <img> at a URL the browser will actually load.
function isAllowedAvatarSrc(url) {
  if (!url) return false;
  if (url.startsWith("data:") || url.startsWith("blob:")) return true;
  try {
    return new URL(url, window.location.href).origin === window.location.origin;
  } catch {
    return false;
  }
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
  // Feishu avatar URLs point at Feishu's own CDN, which the app's CSP
  // (img-src 'self' data: blob:) blocks — the request never succeeds, it
  // just logs a violation and leaves a broken image on every render. Fall
  // back to the placeholder instead of ever attempting a cross-origin src.
  const avatarHtml = isAllowedAvatarSrc(avatar)
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
  const cached = readIPCache();
  const hasCache = cached && isCacheFresh(cached);
  const ipInit = hasCache && cached.public?.length
    ? escapeHtml(cached.public.join(", "))
    : escapeHtml(t("dash.detecting"));
  const lanInit = hasCache && cached.lan?.length
    ? escapeHtml(cached.lan.join(", "))
    : escapeHtml(t("dash.detecting"));
  const ipClass = hasCache && cached.public?.length ? "mono" : "mono hint";
  const lanClass = hasCache && cached.lan?.length ? "mono" : "mono hint";
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
        // server's public IP shown in the Environment card. The cached value is
        // rendered immediately so the dashboard never flashes "detecting" on
        // every poll.
        `<div class="client-ip-block">` +
          `<div class="kv"><span>${t("dash.yourIp")}</span>` +
            `<span class="client-ip-main">` +
              `<strong id="dash-client-ip" class="${ipClass}">${ipInit}</strong>` +
              `<span id="dash-client-loc" class="hint"></span>` +
            `</span>` +
          `</div>` +
          `<div class="kv"><span>${t("dash.yourLan")}</span><strong id="dash-client-lan" class="${lanClass}">${lanInit}</strong></div>` +
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
