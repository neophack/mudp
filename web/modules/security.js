// Security monitor (admin only): where visitors log in from, on what devices,
// and whether anything looks like a VPN/proxy. Renders a Cloudflare-style gray
// world map (Canvas) of access locations plus a filterable event table and the
// monitor's configuration.

import { $, state, api, toast, t } from "../app.js";
import { escapeHtml, formatDate } from "../lib/common.js";
import { drawWorldMap } from "./worldmap.js";

// Local view state — the access log is paginated and filterable, so it is kept
// here rather than in the global app state.
const view = {
  stats: null,
  points: [],
  logs: [],
  filter: { event: "", ip: "", username: "", q: "", suspicious: false },
  page: 0,
  pageSize: 25,
  settings: null,
  tab: "overview", // overview | logs | settings | mcp
  // Tracks which sub-tabs have already fetched their data. Without this the
  // loaders' renderSecurity() repaint would re-trigger a fetch on every paint,
  // producing an infinite request loop that trips the rate limiter (429).
  loaded: {},
};

export function renderSecurity() {
  const admin = state.me && state.me.role === "admin";
  if (!admin) {
    $("#view").innerHTML = `<div class="card"><div class="card-body"><p>${t("security.adminOnly")}</p></div></div>`;
    return;
  }
  const sub = view.tab;
  let body = "";
  if (sub === "overview") body = renderOverview();
  else if (sub === "logs") body = renderLogs();
  else if (sub === "settings") body = renderSettings();
  else if (sub === "mcp") body = renderMcp();
  $("#view").innerHTML =
    `<div class="sec-tabs">` +
      secTabBtn("overview", "security.tabOverview") +
      secTabBtn("logs", "security.tabLogs") +
      secTabBtn("settings", "security.tabSettings") +
      secTabBtn("mcp", "mcp.tabMcp") +
    `</div>` +
    body;
  wireSubTabs();
  if (sub === "overview") wireOverview();
  else if (sub === "logs") wireLogs();
  else if (sub === "settings") wireSettings();
  else if (sub === "mcp") wireMcp();
  // Kick off the data fetch the first time a sub-tab is shown. Subsequent
  // repaints (e.g. after data arrives) pass through loadTab only on an actual
  // tab switch, never here, so the fetch doesn't loop.
  if (!view.loaded[sub]) loadTab(true);
}

function secTabBtn(id, key) {
  return `<button class="sec-tab${view.tab === id ? " active" : ""}" data-subtab="${id}">${t(key)}</button>`;
}

function wireSubTabs() {
  $$("#view .sec-tab").forEach((btn) => {
    // renderSecurity() already kicks off the first-time load for the new tab,
    // so no explicit loadTab call is needed here.
    btn.onclick = () => { view.tab = btn.dataset.subtab; renderSecurity(); };
  });
}

function loadTab(force) {
  if (view.tab === "overview") loadOverview(force);
  else if (view.tab === "logs") loadLogs(force);
  else if (view.tab === "settings") loadSettings(force);
  else if (view.tab === "mcp") loadMcp(force);
}

// ---------------- Overview (map + stats) ----------------

function renderOverview() {
  const s = view.stats || {};
  const topCountries = (s.topCountries || []).map(topRow).join("") || `<li class="muted">${t("security.none")}</li>`;
  const topIPs = (s.topIPs || []).map(topRow).join("") || `<li class="muted">${t("security.none")}</li>`;
  return (
    `<div class="sec-stat-row">` +
      statTile(t("security.totalVisits"), s.totalVisits ?? 0, "🌐") +
      statTile(t("security.successLogins"), s.loginSuccess ?? 0, "✓", "ok") +
      statTile(t("security.failedAttempts"), s.loginFailed ?? 0, "✗", s.loginFailed > 0 ? "warn" : "muted") +
      statTile(t("security.vpnProxy"), s.vpnProxy ?? 0, "🛡", s.vpnProxy > 0 ? "warn" : "muted") +
      statTile(t("security.suspicious"), s.suspicious ?? 0, "⚠", s.suspicious > 0 ? "warn" : "muted") +
      statTile(t("security.uniqueIPs"), s.uniqueIPs ?? 0, "🔢") +
    `</div>` +
    `<div class="card">` +
      `<div class="card-head"><h2>${t("security.accessMap")}</h2>` +
        `<span class="hint">${t("security.accessMapHint")}</span>` +
      `</div>` +
      `<div class="card-body">` +
        `<div class="sec-map-wrap"><canvas id="accessMap" width="980" height="480"></canvas></div>` +
        `<div id="mapTip" class="sec-map-tip"></div>` +
      `</div>` +
    `</div>` +
    `<div class="sec-cols">` +
      `<div class="card"><div class="card-head"><h2>${t("security.topCountries")}</h2></div><div class="card-body"><ul class="top-list">${topCountries}</ul></div></div>` +
      `<div class="card"><div class="card-head"><h2>${t("security.topIPs")}</h2></div><div class="card-body"><ul class="top-list">${topIPs}</ul></div></div>` +
    `</div>`
  );
}

function statTile(label, value, icon, tone) {
  return (
    `<section class="card stat-tile stat-${tone || ""}">` +
      `<div class="stat-icon">${icon}</div>` +
      `<div class="stat-body">` +
        `<div class="stat-value">${escapeHtml(String(value))}</div>` +
        `<div class="stat-label">${escapeHtml(label)}</div>` +
      `</div>` +
    `</section>`
  );
}

function topRow(c) {
  const flag = countryCodeFlag(c.label);
  return `<li><span class="flag">${flag}</span><span class="lbl">${escapeHtml(c.label)}</span><span class="cnt">${c.count}</span></li>`;
}

// Convert a 2-letter country code to an emoji flag. Falls back to a globe.
function countryCodeFlag(code) {
  if (!code || code.length !== 2) return "🌍";
  const A = 0x1f1e6;
  const base = "A".charCodeAt(0);
  const cc = code.toUpperCase();
  return String.fromCodePoint(A + cc.charCodeAt(0) - base, A + cc.charCodeAt(1) - base);
}

async function loadOverview(force) {
  if (!force && view.loaded.overview) return;
  view.loaded.overview = true;
  try {
    const [stats, points] = await Promise.all([
      api("/api/admin/access/stats"),
      api("/api/admin/access/geo?limit=500"),
    ]);
    view.stats = stats;
    view.points = points || [];
    renderSecurity();
    drawMap();
  } catch (err) {
    view.loaded.overview = false; // allow a retry on the next entry.
    toast(err.message);
  }
}

function wireOverview() {
  // The map is drawn after the canvas exists; loadOverview re-renders then
  // draws. If data was already loaded, draw immediately.
  if (view.points.length || view.stats) drawMap();
}

// ---------------- World map (Canvas) ----------------

// drawMap renders the shared world map with login-access points (brand blue).
// The basemap + projection + dot/tooltip mechanics live in worldmap.js; this
// page only supplies the points and the single-colour style + tooltip text.
function drawMap() {
  const canvas = $("#accessMap");
  const tip = $("#mapTip");
  drawWorldMap(canvas, tip, view.points || [], {
    pointLabel: (p) =>
      `<strong>${escapeHtml(p.label || p.city || t("security.unknown"))}</strong>` +
      `<br>${p.count} ${t("security.visits")}` +
      (p.country ? `<br>${escapeHtml(p.country)}` : ""),
  });
}

// ---------------- Logs table ----------------

function renderLogs() {
  const f = view.filter;
  const rows = (view.logs || []).map(logRow).join("") ||
    `<tr class="empty-row"><td colspan="8">${t("security.noLogs")}</td></tr>`;
  const total = view.logsTotal || 0;
  const from = total ? view.page * view.pageSize + 1 : 0;
  const to = Math.min(total, (view.page + 1) * view.pageSize);
  return (
    `<div class="card">` +
      `<div class="card-head">` +
        `<h2>${t("security.logsTitle")}</h2>` +
        `<div class="filters">` +
          `<select id="fEvent">` +
            `<option value="">${t("security.allEvents")}</option>` +
            `<option value="page_view"${f.event === "page_view" ? " selected" : ""}>${t("security.eventPageView")}</option>` +
            `<option value="login_success"${f.event === "login_success" ? " selected" : ""}>${t("security.eventLoginSuccess")}</option>` +
            `<option value="login_failed"${f.event === "login_failed" ? " selected" : ""}>${t("security.eventLoginFailed")}</option>` +
          `</select>` +
          `<input id="fIP" placeholder="${t("security.filterIP")}" value="${escapeHtml(f.ip)}">` +
          `<input id="fUser" placeholder="${t("security.filterUser")}" value="${escapeHtml(f.username)}">` +
          `<input id="fQ" placeholder="${t("security.search")}" value="${escapeHtml(f.q)}">` +
          `<label class="sec-check"><input type="checkbox" id="fSuspicious"${f.suspicious ? " checked" : ""}> ${t("security.suspiciousOnly")}</label>` +
          `<button class="ghost" id="exportCsv">${t("security.exportCsv")}</button>` +
        `</div>` +
      `</div>` +
      `<table class="data sec-table">` +
        `<thead><tr>` +
          `<th>${t("security.colTime")}</th>` +
          `<th>${t("security.colEvent")}</th>` +
          `<th>${t("security.colLocation")}</th>` +
          `<th>${t("security.colIP")}</th>` +
          `<th>${t("security.colDevice")}</th>` +
          `<th>${t("security.colUser")}</th>` +
          `<th>${t("security.colFlags")}</th>` +
        `</tr></thead>` +
        `<tbody>${rows}</tbody>` +
      `</table>` +
      `<div class="sec-pager">` +
        `<span class="hint">${total ? (from + "-" + to + " / " + total) : t("security.noLogs")}</span>` +
        `<button class="ghost" id="prevPage" ${view.page === 0 ? "disabled" : ""}>‹</button>` +
        `<button class="ghost" id="nextPage" ${to >= total ? "disabled" : ""}>›</button>` +
      `</div>` +
    `</div>`
  );
}

function logRow(e) {
  const flag = countryCodeFlag(e.countryCode);
  const loc = [e.city, e.country].filter(Boolean).join(", ") || t("security.unknown");
  const deviceBits = [e.browser, e.os, e.deviceType].filter(Boolean).join(" · ");
  const clientBits = [e.clientScreen, e.clientPlatform ? e.clientPlatform : null].filter(Boolean).join(" · ");
  const flags = flagBadges(e);
  const evBadge =
    e.event === "login_success"
      ? `<span class="badge badge-ok">${t("security.eventLoginSuccess")}</span>`
      : e.event === "login_failed"
      ? `<span class="badge badge-warn">${t("security.eventLoginFailed")}</span>`
      : `<span class="badge badge-muted">${t("security.eventPageView")}</span>`;
  return (
    `<tr>` +
      `<td><div class="secondary-line">${escapeHtml(fmtTime(e.createdAt))}</div></td>` +
      `<td>${evBadge}</td>` +
      `<td><span class="flag">${flag}</span> ${escapeHtml(loc)}${e.isp ? `<div class="secondary-line mono">${escapeHtml(e.isp)}</div>` : ""}</td>` +
      `<td><span class="mono">${escapeHtml(e.ip || "—")}</span>${e.publicIP ? `<div class="secondary-line mono">${escapeHtml(t("security.publicIpShort"))} ${escapeHtml(e.publicIP)}</div>` : ""}${e.clientTimezone ? `<div class="secondary-line">${escapeHtml(e.clientTimezone)}</div>` : ""}</td>` +
      `<td><div>${escapeHtml(deviceBits || "—")}</div>${clientBits ? `<div class="secondary-line">${escapeHtml(clientBits)}</div>` : ""}</td>` +
      `<td><div class="primary-line">${escapeHtml(e.username || "—")}</div>${e.failureReason ? `<div class="secondary-line">${escapeHtml(e.failureReason)}</div>` : ""}</td>` +
      `<td>${flags}</td>` +
    `</tr>`
  );
}

function flagBadges(e) {
  const out = [];
  if (e.isProxy || e.isHosting) {
    out.push(`<span class="badge badge-warn">${escapeHtml(e.proxyType || "vpn")}</span>`);
  }
  if (e.suspicious) {
    out.push(`<span class="badge badge-accent">${escapeHtml(e.suspicious)}</span>`);
  }
  return out.join(" ");
}

function wireLogs() {
  // `force` so filter/pager changes always re-fetch even though the tab has
  // already loaded once.
  const apply = () => { view.page = 0; loadLogs(true); };
  const onInput = (id, key, isCheck) => {
    const el = $("#" + id);
    if (!el) return;
    el.oninput = el.onchange = () => {
      view.filter[key] = isCheck ? el.checked : el.value;
      if (!isCheck) {
        // Debounce free-text typing.
        clearTimeout(view._t);
        view._t = setTimeout(apply, 300);
      } else {
        apply();
      }
    };
  };
  onInput("fEvent", "event", false);
  onInput("fIP", "ip", false);
  onInput("fUser", "username", false);
  onInput("fQ", "q", false);
  onInput("fSuspicious", "suspicious", true);
  const prev = $("#prevPage");
  if (prev) prev.onclick = () => { if (view.page > 0) { view.page--; loadLogs(true); } };
  const next = $("#nextPage");
  if (next) next.onclick = () => { view.page++; loadLogs(true); };
  const exp = $("#exportCsv");
  if (exp) exp.onclick = exportCsv;
}

async function loadLogs(force) {
  if (!force && view.loaded.logs) return;
  view.loaded.logs = true;
  const f = view.filter;
  const params = new URLSearchParams();
  if (f.event) params.set("event", f.event);
  if (f.ip) params.set("ip", f.ip);
  if (f.username) params.set("username", f.username);
  if (f.q) params.set("q", f.q);
  if (f.suspicious) params.set("suspicious", "1");
  params.set("limit", view.pageSize);
  params.set("offset", view.page * view.pageSize);
  try {
    const data = await api("/api/admin/access/logs?" + params.toString());
    // The handler returns a flat array; estimate "more" by a full page.
    view.logs = Array.isArray(data) ? data : [];
    view.logsTotal = view.page * view.pageSize + view.logs.length;
    renderSecurity();
  } catch (err) {
    view.loaded.logs = false; // allow a retry on the next entry.
    toast(err.message);
  }
}

function exportCsv() {
  const f = view.filter;
  const params = new URLSearchParams();
  if (f.event) params.set("event", f.event);
  if (f.ip) params.set("ip", f.ip);
  if (f.username) params.set("username", f.username);
  if (f.q) params.set("q", f.q);
  if (f.suspicious) params.set("suspicious", "1");
  // Server-side CSV export handles quoting; just trigger a download.
  const a = document.createElement("a");
  a.href = "/api/admin/access/export?" + params.toString();
  a.download = `mudp-access-${new Date().toISOString().slice(0, 10)}.csv`;
  document.body.appendChild(a);
  a.click();
  a.remove();
}

// ---------------- Settings ----------------

function renderSettings() {
  const s = view.settings || {};
  return (
    `<div class="card">` +
      `<div class="card-head"><h2>${t("security.settingsTitle")}</h2></div>` +
      `<div class="card-body sec-settings">` +
        toggleRow("secEnabled", "security.settingEnabled", "security.settingEnabledHint", s.enabled) +
        toggleRow("secGeo", "security.settingGeo", "security.settingGeoHint", s.geoipLookup) +
        toggleRow("secVpn", "security.settingVpn", "security.settingVpnHint", s.vpnDetect) +
        toggleRow("secClient", "security.settingClient", "security.settingClientHint", s.collectClient) +
        `<div class="sec-field">` +
          `<label>${t("security.settingRetention")}</label>` +
          `<div class="sec-retention">` +
            `<input type="number" id="secRetention" min="1" max="3650" value="${s.retentionDays ?? 90}">` +
            `<span class="hint">${t("security.days")}</span>` +
          `</div>` +
        `</div>` +
        `<div class="hint sec-note">${t("security.settingNote")}</div>` +

        // ----- IP-detection Worker (operator self-hosted Cloudflare Worker) -----
        // The Worker only exposes an unauthenticated /whoami (the visitor's
        // own IP+geo from request.cf), so configuring it is just the URL.
        `<div class="sec-subhead">${t("security.ipWorkerTitle")}</div>` +
        `<div class="sec-field">` +
          `<label for="secWorkerUrl">${t("security.ipWorkerUrl")}</label>` +
          `<div class="hint">${t("security.ipWorkerUrlHint")}</div>` +
          `<input type="text" id="secWorkerUrl" class="sec-input" ` +
            `value="${escapeHtml(s.ipWorkerUrl || "")}" ` +
            `placeholder="https://your-worker.workers.dev">` +
        `</div>` +

        // ----- One-click deployment aid -----
        // The Worker source is secret-free (no /lookup, no auth), so it's safe
        // to view/copy and paste into Cloudflare as-is.
        `<div class="sec-deploy">` +
          `<div class="sec-deploy-head">` +
            `<div class="hint">${t("security.ipWorkerDeployHint")}</div>` +
            `<button class="ghost" id="secWorkerView">${t("security.ipWorkerViewCode")}</button>` +
          `</div>` +
          `<pre class="sec-deploy-code" id="secWorkerCode" hidden></pre>` +
          `<div class="sec-deploy-actions" id="secWorkerCodeActions" hidden>` +
            `<button class="ghost" id="secWorkerCopyCode">${t("security.ipWorkerCopyCode")}</button>` +
          `</div>` +
        `</div>` +

        // ----- Deployment self-check -----
        // Two independent checks: config wiring + browser→Worker probe. There
        // is no server→Worker check anymore (the Worker has no /lookup).
        `<div class="sec-verify">` +
          `<div class="sec-verify-head">` +
            `<div class="sec-subhead">${t("security.ipWorkerVerifyTitle")}</div>` +
            `<button class="primary" id="secWorkerVerifyAll">${t("security.ipWorkerVerifyAll")}</button>` +
          `</div>` +
          verifyRow("vCfg", "security.ipWorkerVerifyCfg", "security.ipWorkerVerifyCfgHint") +
          verifyRow("vWho", "security.ipWorkerVerifyWho", "security.ipWorkerVerifyWhoHint") +
        `</div>` +

        `<button class="primary" id="secSave">${t("common.save")}</button>` +
      `</div>` +
    `</div>`
  );
}

function toggleRow(id, labelKey, hintKey, checked) {
  return (
    `<div class="sec-field">` +
      `<div>` +
        `<label for="${id}">${t(labelKey)}</label>` +
        `<div class="hint">${t(hintKey)}</div>` +
      `</div>` +
      `<label class="switch"><input type="checkbox" id="${id}"${checked ? " checked" : ""}><span class="slider"></span></label>` +
    `</div>`
  );
}

// verifyRow renders one self-check line: label + hint, a "run" button, and a
// result span that the run*() functions fill with ok/fail + detail.
function verifyRow(id, labelKey, hintKey) {
  return (
    `<div class="sec-verify-row" id="vr_${id}">` +
      `<div class="sec-verify-label">` +
        `<strong>${t(labelKey)}</strong>` +
        `<div class="hint">${t(hintKey)}</div>` +
      `</div>` +
      `<div class="sec-verify-side">` +
        `<span class="sec-verify-result hint" id="vres_${id}"></span>` +
        `<button class="ghost" id="vbtn_${id}">${t("security.ipWorkerVerifyRun")}</button>` +
      `</div>` +
    `</div>`
  );
}

function wireSettings() {
  const save = $("#secSave");
  if (save) save.onclick = saveSettings;
  const view = $("#secWorkerView");
  if (view) view.onclick = toggleWorkerSource;
  const copyCode = $("#secWorkerCopyCode");
  if (copyCode) copyCode.onclick = copyWorkerSource;
  // Deployment self-check buttons. Each maps an id suffix to its runner.
  const runners = {
    vCfg: runVerifyConfig,
    vWho: runVerifyWhoami,
  };
  for (const [id, fn] of Object.entries(runners)) {
    const btn = $("#vbtn_" + id);
    if (btn) btn.onclick = () => fn(id);
  }
  const all = $("#secWorkerVerifyAll");
  if (all) all.onclick = () => Promise.all(Object.keys(runners).map((id) => runners[id](id)));
}

// setVerifyResult paints a check's outcome: a green "✓" / red "✗", the detail
// message, and a running state while the request is in flight.
function setVerifyResult(id, state, detail) {
  const el = $("#vres_" + id);
  if (!el) return;
  el.className = "sec-verify-result " + state; // ok | fail | pending | ""
  const mark = state === "ok" ? "✓ " : state === "fail" ? "✗ " : state === "pending" ? "… " : "";
  el.textContent = mark + (detail || "");
}

// runVerifyConfig checks the public config endpoint: does mudp know about a
// configured, https Worker URL? This catches "forgot to save the URL" and
// "saved an http:// URL" before any network call.
async function runVerifyConfig(id) {
  setVerifyResult(id, "pending", t("security.ipWorkerVerifyRunning"));
  try {
    const data = await api("/api/security/ipworker");
    if (data && data.enabled && data.url) {
      setVerifyResult(id, "ok", data.url);
    } else {
      setVerifyResult(id, "fail", t("security.ipWorkerVerifyCfgFail"));
    }
  } catch (err) {
    setVerifyResult(id, "fail", err.message);
  }
}

// runVerifyWhoami exercises the browser→Worker path directly: the browser
// fetches the Worker's /whoami. This proves the Worker is deployed, reachable,
// CORS-permits the browser origin, and returns valid geo.
async function runVerifyWhoami(id) {
  setVerifyResult(id, "pending", t("security.ipWorkerVerifyRunning"));
  try {
    const cfg = await api("/api/security/ipworker");
    if (!cfg || !cfg.enabled || !cfg.url) {
      setVerifyResult(id, "fail", t("security.ipWorkerVerifyCfgFail"));
      return;
    }
    const res = await fetch(cfg.url + "/whoami", { credentials: "omit", signal: AbortSignal.timeout(8000) });
    if (!res.ok) {
      setVerifyResult(id, "fail", "HTTP " + res.status);
      return;
    }
    const data = await res.json();
    if (!data || data.status !== "success") {
      setVerifyResult(id, "fail", (data && data.message) || "no success");
      return;
    }
    setVerifyResult(id, "ok", `${data.ip || "?"} · ${data.city || "?"}, ${data.country || "?"}`);
  } catch (err) {
    setVerifyResult(id, "fail", err.message || String(err));
  }
}

// toggleWorkerSource fetches the embedded Worker source and shows/hides it. The
// source is secret-free (only an unauthenticated /whoami), so viewing/copying
// it is safe. Cached on the view to avoid re-fetching on every toggle.
async function toggleWorkerSource() {
  const pre = $("#secWorkerCode");
  const actions = $("#secWorkerCodeActions");
  if (!pre) return;
  if (!pre.hidden) {
    pre.hidden = true;
    if (actions) actions.hidden = true;
    return;
  }
  try {
    if (view.workerSource === undefined) {
      const res = await fetch("/api/admin/security/worker-source", { credentials: "same-origin" });
      view.workerSource = res.ok ? await res.text() : "";
    }
    pre.textContent = view.workerSource || "";
    pre.hidden = false;
    if (actions) actions.hidden = false;
  } catch (err) {
    toast(err.message || t("common.copyFailed"));
  }
}

async function copyWorkerSource() {
  const src = view.workerSource || "";
  if (!src) return;
  try {
    await navigator.clipboard.writeText(src);
    toast(t("security.ipWorkerCopied"), true);
  } catch (_) {
    toast(t("common.copyFailed"));
  }
}

async function loadSettings(force) {
  if (!force && view.loaded.settings) return;
  view.loaded.settings = true;
  try {
    view.settings = await api("/api/admin/security/settings");
    renderSecurity();
  } catch (err) {
    view.loaded.settings = false; // allow a retry on the next entry.
    toast(err.message);
  }
}

async function saveSettings() {
  const s = {
    enabled: $("#secEnabled")?.checked ?? true,
    geoipLookup: $("#secGeo")?.checked ?? true,
    vpnDetect: $("#secVpn")?.checked ?? true,
    collectClient: $("#secClient")?.checked ?? true,
    retentionDays: parseInt($("#secRetention")?.value, 10) || 90,
    // Worker config: just the URL (blank = disabled). The Worker has no
    // password/secret — it's an unauthenticated /whoami only.
    ipWorkerUrl: $("#secWorkerUrl")?.value.trim() || "",
  };
  try {
    await api("/api/admin/security/settings", { method: "POST", body: JSON.stringify(s) });
    view.settings = s;
    toast(t("security.saved"), true);
  } catch (err) {
    toast(err.message);
  }
}

// ---------------- MCP external-port attacks ----------------

// MCP attack data lives in state.mcp* (shared with the global state object),
// while its load gating rides on view.loaded.mcp so it reuses the same
// fetch-loop guard the other sub-tabs use.

function renderMcp() {
  // Until the first load resolves there is nothing to show but a placeholder;
  // loadMcp() will repaint once data arrives.
  if (!state.mcpAttacks) {
    return `<div class="card"><div class="card-body"><p class="hint">${t("mcp.loadingTokens")}</p></div></div>`;
  }

  const stats = state.mcpAttackStats || emptyStats();
  const q = state.mcpAttackSearch || {};
  const rows = (state.mcpAttacks || []).map(attackRow).join("") ||
    `<tr class="empty-row"><td colspan="10">${t("mcp.noAttacks")}</td></tr>`;

  const topCountries = (stats.topCountries || [])
    .map((c) => `<span class="chip">${escapeHtml(c.label)} <b>${c.count}</b></span>`)
    .join("");
  const topIPs = (stats.topIPs || [])
    .map((c) => `<span class="chip mono">${escapeHtml(c.label)} <b>${c.count}</b></span>`)
    .join("");

  return (
    `<div class="mcp-security-page">` +
      `<section class="card">` +
        `<div class="card-head">` +
          `<div>` +
            `<h2>${t("mcp.securityTitle")}</h2>` +
            `<p class="hint">${t("mcp.securityDesc")}</p>` +
          `</div>` +
        `</div>` +
        `<div class="stat-cards">` +
          statCard(t("mcp.totalAttacks"), stats.totalAttacks) +
          statCard(t("mcp.uniqueIPs"), stats.uniqueIPs) +
          statCard(t("mcp.last24h"), stats.last24h) +
        `</div>` +
        (topCountries || topIPs
          ? `<div class="mcp-top-buckets">` +
              (topCountries ? `<div><div class="secondary-line">${t("mcp.topCountries")}</div><div class="chip-row">${topCountries}</div></div>` : "") +
              (topIPs ? `<div><div class="secondary-line">${t("mcp.topIPs")}</div><div class="chip-row">${topIPs}</div></div>` : "") +
            `</div>`
          : "") +
      `</section>` +

      `<section class="card">` +
        `<div class="card-head">` +
          `<div><h2>${t("mcp.mapTitle")}</h2><p class="hint">${t("mcp.mapDesc")}</p></div>` +
          `<div class="mcp-map-legend">` +
            `<span class="mcp-legend-item"><i class="mcp-dot mcp-dot-green"></i>${t("mcp.legendAccess")}</span>` +
            `<span class="mcp-legend-item"><i class="mcp-dot mcp-dot-yellow"></i>${t("mcp.legendAttackLow")}</span>` +
            `<span class="mcp-legend-item"><i class="mcp-dot mcp-dot-red"></i>${t("mcp.legendAttackHigh")}</span>` +
          `</div>` +
        `</div>` +
        `<div class="mcp-map-wrap"><canvas id="mcpMap" width="980" height="480"></canvas></div>` +
        `<div id="mcpMapTip" class="mcp-map-tip"></div>` +
      `</section>` +

      `<section class="card">` +
        `<div class="card-head">` +
          `<div><h2>${t("mcp.attackList")}</h2><p class="hint">${t("mcp.geoFromCloudflare")}</p></div>` +
          `<div class="filters">` +
            `<input id="fAttackIP" placeholder="${t("mcp.filterIP")}" value="${escapeHtml(q.ip || "")}">` +
            `<input id="fAttackQ" placeholder="${t("mcp.filterKeyword")}" value="${escapeHtml(q.q || "")}">` +
            `<button class="ghost" id="exportAttacks">${t("mcp.exportAttacks")}</button>` +
          `</div>` +
        `</div>` +
        `<div class="mcp-table-wrap">` +
          `<table class="data">` +
            `<thead><tr>` +
              `<th>${t("mcp.colAttackTime")}</th><th>${t("mcp.colSource")}</th><th>${t("mcp.colIP")}</th><th>${t("mcp.colLocation")}</th><th>${t("mcp.colIsp")}</th><th>${t("mcp.colTimezone")}</th><th>${t("mcp.colDevice")}</th><th>${t("mcp.colReason")}</th><th>${t("mcp.colPath")}</th><th>${t("mcp.colUA")}</th>` +
            `</tr></thead>` +
            `<tbody>${rows}</tbody>` +
          `</table>` +
        `</div>` +
      `</section>` +
    `</div>`
  );
}

function statCard(label, value) {
  return `<div class="stat-card"><div class="stat-value">${escapeHtml(String(value ?? 0))}</div><div class="stat-label">${escapeHtml(label)}</div></div>`;
}

// drawMcpMap renders the shared world map with MCP access (green) and attack
// (yellow/red) dots. The basemap + projection + dot/tooltip mechanics live in
// worldmap.js; this page only supplies the points, the colouring rule, and the
// tooltip text — the parts that are specific to MCP.
async function drawMcpMap() {
  const canvas = $("#mcpMap");
  const tip = $("#mcpMapTip");
  const points = state.mcpMapPoints || [];
  await drawWorldMap(canvas, tip, points, {
    // colourFor maps kind+severity to [glow rgba, dot hex]. Access is green;
    // attacks are yellow (single/low) or red (repeat offender).
    colorFor: (p) => {
      if (p.kind === "access") return ["rgba(34, 197, 94, 0.7)", "#22c55e"];
      if (p.severity >= 2) return ["rgba(239, 68, 68, 0.75)", "#ef4444"];
      return ["rgba(245, 158, 11, 0.7)", "#f59e0b"];
    },
    pointLabel: (p) => {
      const kind = p.kind === "access" ? t("mcp.legendAccess") : t("mcp.legendAttack");
      return (
        `<strong>${escapeHtml(p.label || p.city || t("mcp.unknown"))}</strong>` +
        `<br>${escapeHtml(kind)} · ${p.count} ${t("mcp.events")}` +
        (p.country ? `<br>${escapeHtml(p.country)}` : "") +
        (p.ip ? `<br><span class="mono">${escapeHtml(p.ip)}</span>` : "")
      );
    },
  });
}

function wireMcp() {
  // Draw the map if data has already landed; loadMcp repaints then draws too.
  if (state.mcpAttacks) drawMcpMap();
  const ip = $("#fAttackIP");
  if (ip) ip.oninput = (e) => { state.mcpAttackSearch = { ...state.mcpAttackSearch, ip: e.target.value }; reloadAttacks(); };
  const q = $("#fAttackQ");
  if (q) q.oninput = (e) => { state.mcpAttackSearch = { ...state.mcpAttackSearch, q: e.target.value }; reloadAttacks(); };
  const exp = $("#exportAttacks");
  if (exp) exp.onclick = exportAttacks;
}

function emptyStats() {
  return { totalAttacks: 0, uniqueIPs: 0, last24h: 0, topCountries: [], topIPs: [], hourlyTrend: [] };
}

function attackRow(a) {
  // Location: flag + "city, region" on the first line, country on the second.
  // Falls back gracefully when only a country (or nothing) is known.
  const place = [a.city, a.region].filter(Boolean).join(", ");
  const location = a.countryCode || place || a.country
    ? `<span class="flag">${countryCodeFlag(a.countryCode)}</span> ` +
      `<span class="primary-line">${escapeHtml(place || a.country || "")}</span>` +
      (a.countryCode ? `<br><span class="secondary-line">${escapeHtml(a.countryCode)}</span>` : "")
    : `<span class="secondary-line">—</span>`;
  return (
    `<tr>` +
      `<td><div class="secondary-line">${escapeHtml(formatDate(a.createdAt))}</div></td>` +
      `<td>${sourceBadge(a.sourceKind)}</td>` +
      `<td><span class="mono">${escapeHtml(a.ip || "—")}</span></td>` +
      `<td>${location}</td>` +
      `<td><span class="secondary-line">${escapeHtml(a.isp || "—")}</span></td>` +
      `<td><span class="secondary-line">${escapeHtml(a.timezone || "—")}</span></td>` +
      `<td>${deviceBadge(a.device)}</td>` +
      `<td><span class="secondary-line">${escapeHtml(a.reason || "—")}</span></td>` +
      `<td><span class="secondary-line mono">${escapeHtml(a.path || "—")}</span></td>` +
      `<td><span class="secondary-line">${escapeHtml(uaSummary(a))}</span></td>` +
    `</tr>`
  );
}

// deviceBadge renders an icon + label for the client device class
// (desktop/mobile/tablet/bot), classified from CF-Device-Type or the UA.
// Unknown rows (pre-dating the field) render as a muted dash.
function deviceBadge(device) {
  switch (device) {
    case "desktop":
      return `<span class="badge">🖥 ${escapeHtml(t("mcp.deviceDesktop"))}</span>`;
    case "mobile":
      return `<span class="badge">📱 ${escapeHtml(t("mcp.deviceMobile"))}</span>`;
    case "tablet":
      return `<span class="badge">🔡 ${escapeHtml(t("mcp.deviceTablet"))}</span>`;
    case "bot":
      return `<span class="badge badge-warn">🤖 ${escapeHtml(t("mcp.deviceBot"))}</span>`;
    default:
      return `<span class="secondary-line">—</span>`;
  }
}

// sourceBadge renders the "外网/内网" tag for a request, derived from the
// visitor's IP (extranet = public, intranet = private/loopback). Unknown rows
// (pre-dating the field) render as a muted dash so the column still aligns.
function sourceBadge(kind) {
  if (kind === "extranet") {
    return `<span class="badge badge-accent">${t("mcp.sourceExtranet")}</span>`;
  }
  if (kind === "intranet") {
    return `<span class="badge badge-warn">${t("mcp.sourceIntranet")}</span>`;
  }
  return `<span class="secondary-line">—</span>`;
}

// uaSummary condenses the stored browser/os/UA into one readable label.
function uaSummary(a) {
  const parts = [a.browser, a.os].filter(Boolean);
  if (parts.length) return parts.join(" · ");
  return a.userAgent ? a.userAgent.slice(0, 60) : "—";
}

async function loadMcp(force) {
  if (!force && view.loaded.mcp) return;
  view.loaded.mcp = true;
  try {
    const [attacks, stats, points] = await Promise.all([
      api("/api/admin/mcp/attacks?limit=200"),
      api("/api/admin/mcp/attacks/stats"),
      api("/api/admin/mcp/map?limit=500"),
    ]);
    state.mcpAttacks = attacks || [];
    state.mcpAttackStats = stats || emptyStats();
    state.mcpMapPoints = points || [];
    renderSecurity();
    drawMcpMap();
  } catch {
    view.loaded.mcp = false; // allow a retry on the next entry.
    state.mcpAttacks = [];
    state.mcpAttackStats = emptyStats();
    state.mcpMapPoints = [];
  }
}

async function reloadAttacks() {
  const q = state.mcpAttackSearch || {};
  const params = new URLSearchParams({ limit: "200" });
  if (q.ip) params.set("ip", q.ip);
  if (q.q) params.set("q", q.q);
  try {
    state.mcpAttacks = (await api("/api/admin/mcp/attacks?" + params.toString())) || [];
    renderSecurity();
    drawMcpMap();
  } catch (err) {
    toast(err.message || t("mcp.loadFail"));
  }
}

function exportAttacks() {
  const rows = state.mcpAttacks || [];
  const lines = ["time,source,ip,country,country_code,region,city,isp,timezone,device,reason,path,browser,os,user_agent"];
  for (const a of rows) {
    lines.push(
      [a.createdAt, a.sourceKind, a.ip, a.country, a.countryCode, a.region, a.city, a.isp, a.timezone, a.device, a.reason, a.path, a.browser, a.os, a.userAgent]
        .map(csvCell)
        .join(","),
    );
  }
  const blob = new Blob([lines.join("\n")], { type: "text/csv" });
  const url = URL.createObjectURL(blob);
  const link = document.createElement("a");
  link.href = url;
  link.download = `mcp-attacks-${new Date().toISOString().slice(0, 10)}.csv`;
  link.click();
  URL.revokeObjectURL(url);
}

function csvCell(v) {
  const s = String(v ?? "");
  return /[",\n]/.test(s) ? '"' + s.replace(/"/g, '""') + '"' : s;
}

// ---------------- helpers ----------------

function fmtTime(iso) {
  if (!iso) return "—";
  const d = new Date(iso);
  return isNaN(d) ? iso : d.toLocaleString();
}

function $$(sel) { return Array.from(document.querySelectorAll(sel)); }
