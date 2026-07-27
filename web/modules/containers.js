// Container list (table view) and container actions (start/stop/restart/remove),
// plus routing of logs/terminal/details into their dedicated modules.
//
// Beyond the classic single-row actions, this view also implements:
//   - state filtering (all / running / stopped / paused)
//   - multi-select with a batch action toolbar (start/stop/restart/remove/unpause)

import { state, api, toast, refreshAll, renderView, escapeHtml, canMutate, isAdmin, displayNameForUsername } from "../app.js";
import { openLogs } from "./logs.js";
import { openTerminal } from "./terminal.js";
import { openDetails } from "./details.js";
import { openStats } from "./stats.js";
import { openFiles } from "./files.js";

export function renderContainers() {
  const q = state.search.trim().toLowerCase();
  // Apply the state filter first, then the free-text search.
  const byState = applyStateFilter(state.containers || []);
  const list = q
    ? byState.filter(
        (c) =>
          (c.name || c.fullName || "").toLowerCase().includes(q) ||
          (c.image || "").toLowerCase().includes(q)
      )
    : byState;

  const rows =
    list.map((c) => containerRow(c)).join("") ||
    `<tr class="empty-row"><td colspan="${colSpan()}">No containers match. Adjust the filter or click “+ New Container”.</td></tr>`;

  const sel = state.containerSelection;
  const anySelected = sel.size > 0;
  const allChecked = list.length > 0 && list.every((c) => sel.has(c.id));

  $("#view").innerHTML =
    filterBar() +
    (anySelected ? batchToolbar(sel.size) : "") +
    `<div class="card"><table class="data">` +
    `<thead><tr>` +
      (canMutate() ? `<th class="chk-col"><input type="checkbox" id="selectAll" ${allChecked ? "checked" : ""}></th>` : "") +
      `<th>Container</th>` +
      (isAdmin() ? `<th>Owner</th>` : "") +
      `<th>Status</th><th>Image</th><th>Resources</th><th class="actions">Actions</th>` +
    `</tr></thead>` +
    `<tbody>${rows}</tbody></table></div>`;

  bindLogViewerHook();
  document.querySelectorAll("[data-act]").forEach((btn) => {
    btn.onclick = () => actionContainer(btn.dataset.id, btn.dataset.name, btn.dataset.act);
  });
  document.querySelectorAll("[data-copy]").forEach((btn) => {
    btn.onclick = async () => {
      try {
        await navigator.clipboard.writeText(btn.dataset.copy);
        toast("Copied", true);
        const old = btn.textContent;
        btn.textContent = "✓";
        btn.classList.add("copied");
        setTimeout(() => { btn.textContent = old; btn.classList.remove("copied"); }, 1200);
      } catch {
        toast("Copy failed — select the text manually");
      }
    };
  });
  // State filter pills.
  document.querySelectorAll("[data-filter]").forEach((btn) => {
    btn.onclick = () => { state.containerFilter = btn.dataset.filter; renderView(); };
  });
  // Batch toolbar actions.
  document.querySelectorAll("[data-batch]").forEach((btn) => {
    btn.onclick = () => runBatch(btn.dataset.batch);
  });
  // Row checkboxes.
  document.querySelectorAll("[data-cid]").forEach((box) => {
    box.onchange = () => { toggleSelect(box.dataset.cid, box.checked); renderView(); };
  });
  const selectAll = $("#selectAll");
  if (selectAll) {
    selectAll.onchange = () => {
      // Select/deselect only the currently filtered (visible) containers.
      const on = selectAll.checked;
      list.forEach((c) => toggleSelect(c.id, on));
      renderView();
    };
  }
}

// colSpan returns the table's column count for the empty-state row: the five
// base columns plus the optional checkbox column (mutating roles) and the
// optional owner column (admins).
function colSpan() {
  return 5 + (canMutate() ? 1 : 0) + (isAdmin() ? 1 : 0);
}

// applyStateFilter narrows the container list by the active state filter. The
// "stopped" bucket covers exited/dead/created states (matching the backend).
function applyStateFilter(containers) {
  const f = state.containerFilter || "all";
  if (f === "all") return containers;
  return containers.filter((c) => {
    const s = (c.state || "").toLowerCase();
    if (f === "running") return s === "running";
    if (f === "paused") return s === "paused";
    if (f === "stopped") return s !== "running" && s !== "paused";
    return true;
  });
}

// filterBar renders the all/running/stopped/paused pill row. Counts are computed
// from the full (unfiltered) list so they stay stable as the filter changes.
function filterBar() {
  const all = state.containers || [];
  const count = (pred) => all.filter(pred).length;
  const tabs = [
    { key: "all", label: "All", n: all.length },
    { key: "running", label: "Running", n: count((c) => c.state === "running") },
    { key: "stopped", label: "Stopped", n: count((c) => c.state !== "running" && c.state !== "paused") },
    { key: "paused", label: "Paused", n: count((c) => c.state === "paused") },
  ];
  return (
    `<div class="filter-bar">` +
      tabs
        .map(
          (t) =>
            `<button class="pill ${state.containerFilter === t.key ? "active" : ""}" data-filter="${t.key}">${escapeHtml(t.label)} <span class="pill-count">${t.n}</span></button>`
        )
        .join("") +
    `</div>`
  );
}

// batchToolbar appears above the table when one or more containers are selected.
// Mutating batch actions are hidden for read-only roles.
function batchToolbar(n) {
  if (!canMutate()) return `<div class="batch-bar"><span>${n} selected</span></div>`;
  return (
    `<div class="batch-bar">` +
      `<span class="batch-count">${n} selected</span>` +
      `<div class="batch-actions">` +
        `<button class="ghost" data-batch="start">▶ Start</button>` +
        `<button class="ghost" data-batch="stop">■ Stop</button>` +
        `<button class="ghost" data-batch="restart">⟳ Restart</button>` +
        `<button class="ghost" data-batch="unpause">⏵ Unpause</button>` +
        `<button class="ghost danger" data-batch="remove">✕ Remove</button>` +
      `</div>` +
    `</div>`
  );
}

function toggleSelect(id, on) {
  if (on) state.containerSelection.add(id);
  else state.containerSelection.delete(id);
}

// runBatch sends the selected IDs through the batch endpoint. "remove" confirms
// first. The selection is cleared after a successful batch.
function matchesAnyId(fullId, prefixes) {
  return prefixes.some((p) => fullId === p || (fullId && fullId.startsWith(p)));
}

async function runBatch(action) {
  const ids = [...state.containerSelection];
  if (ids.length === 0) return;
  if (action === "remove" && !confirm(`Remove ${ids.length} container(s)? This cannot be undone.`)) return;
  try {
    const res = await api("/api/containers/batch", {
      method: "POST",
      body: JSON.stringify({ ids, action }),
    });
    if (action === "remove") {
      state.containers = (state.containers || []).filter((c) => !matchesAnyId(c.id, ids));
    }
    await refreshAll();
    state.containerSelection.clear();
    renderView();
    const okN = (res.ok || []).length;
    const failN = (res.failed || []).length;
    if (failN === 0) toast(`${okN} container(s) ${action} OK`, true);
    else toast(`${okN} OK, ${failN} failed`);
  } catch (err) {
    toast(err.message);
  }
}

// Placeholder so renderLogViewer (from logs.js) can be wired without a circular
// top-level import. The actual viewer is owned by logs.js.
function bindLogViewerHook() {}

function containerRow(c) {
  const running = c.state === "running";
  const paused = c.state === "paused";
  const name = c.name || c.fullName;
  const pending = (act) => state.pending.has(c.id + ":" + act);
  const statusBadge = running
    ? `<span class="badge badge-ok"><span class="dot"></span>${escapeHtml(c.status || "Up")}</span>`
    : paused
    ? `<span class="badge badge-warn"><span class="dot"></span>${escapeHtml(c.status || "Paused")}</span>`
    : `<span class="badge badge-muted"><span class="dot"></span>${escapeHtml(c.status || c.state || "Stopped")}</span>`;
  const ports = (c.ports || []).join(", ") || "—";
  // Docker on Linux binds to 0.0.0.0 so the backend URL contains 127.0.0.1.
  // Replace it (and localhost/0.0.0.0/::1) with the server hostname the browser
  // is already connected to, so links work on remote hosts.
  const fixUrl = (url) => url ? url.replace(/^(https?:\/\/)(127\.0\.0\.1|0\.0\.0\.0|::1|localhost)(:\d+)/, `$1${location.hostname}$3`) : url;
  // Coerce to a number defensively before toFixed: the backend usually sends
  // numbers, but a stray string/null would otherwise throw and blank the table.
  const num = (v) => (typeof v === "number" && isFinite(v) ? v : 0);
  const conn = [];
  if (c.http8080Url) conn.push(`<a class="port-link" href="${escapeHtml(fixUrl(c.http8080Url))}" target="_blank" rel="noopener">8080 ↗</a>`);
  if (c.http80Url) conn.push(`<a class="port-link" href="${escapeHtml(fixUrl(c.http80Url))}" target="_blank" rel="noopener">80 ↗</a>`);
  // The quick links used to replace the port list, which hid every mapping on a
  // container publishing 80 or 8080 — including the ones mudp forwards. Show
  // both: the links are a shortcut, the mappings are the information.
  const portLine = [...conn, ports === "—" && conn.length ? "" : escapeHtml(ports)]
    .filter(Boolean)
    .join(" <span class='sep'>·</span> ");
  // A container whose ports mudp relays itself carries the forward label. Docker
  // reports nothing about those ports, so the row says where they come from.
  const forwarded = !!(c.labels && c.labels["mudp.forward.ports"]);
  const iconBtn = (act, glyph, title, cls = "icon", enabled = true) => {
    const isLoading = pending(act);
    const dis = !enabled || isLoading ? "disabled" : "";
    const loading = isLoading ? " is-loading" : "";
    return `<button class="${cls}${loading}" title="${title}" data-id="${escapeHtml(c.id)}" data-name="${escapeHtml(name)}" data-act="${act}" ${dis}>${glyph}</button>`;
  };
  const checked = state.containerSelection.has(c.id) ? "checked" : "";
  return (
    `<tr>` +
      (canMutate() ? `<td class="chk-col"><input type="checkbox" data-cid="${escapeHtml(c.id)}" ${checked}></td>` : "") +
      `<td><div class="primary-line">${escapeHtml(name)}${forwarded ? ` <span class="badge badge-muted" title="Host ports are relayed by mudp, not published by Docker">forward</span>` : ""}</div>` +
        `<div class="secondary-line">${portLine || "—"}</div></td>` +
      (isAdmin() ? `<td><div class="secondary-line">${escapeHtml(displayNameForUsername(c.owner) || "—")}</div></td>` : "") +
      `<td>${statusBadge}</td>` +
      `<td><div class="secondary-line">${escapeHtml(c.image || c.Image || "—")}</div></td>` +
      `<td><div class="secondary-line">${num(c.memoryMb).toFixed(0)} MB mem · ${num(c.diskMb).toFixed(0)} MB disk</div>` +
        `<div class="secondary-line">GPU: ${escapeHtml(c.gpu || "none")}</div></td>` +
      `<td class="actions">` +
        iconBtn("logs", "📄", "Logs") +
        iconBtn("start", "▶", "Start", "icon ok", !running) +
        iconBtn("stop", "■", "Stop", "icon warn", running || paused) +
        iconBtn("restart", "⟳", "Restart", "icon", running || paused) +
        (running ? iconBtn("pause", "⏸", "Pause") : "") +
        (paused ? iconBtn("unpause", "⏵", "Unpause", "icon ok") : "") +
        iconBtn("files", "📁", "Files") +
        (running ? iconBtn("terminal", "🖥", "Console") : "") +
        (running ? iconBtn("stats", "📊", "Stats") : "") +
        iconBtn("inspect", "ℹ", "Details") +
        iconBtn("remove", "✕", "Delete", "icon danger") +
      `</td>` +
    `</tr>`
  );
}

export async function actionContainer(id, name, action) {
  if (action === "logs") return openLogs(id, name);
  if (action === "terminal") return openTerminal(id, name);
  if (action === "inspect") return openDetails(id, name);
  if (action === "stats") return openStats(id, name);
  if (action === "files") return openFiles(id, name);
  if (action === "remove" && !confirm(`Delete container “${name}”? This cannot be undone.`)) return;
  const key = id + ":" + action;
  state.pending.add(key);
  renderView();
  try {
    await api("/api/containers/action", { method: "POST", body: JSON.stringify({ id, action }) });
    if (action === "remove") {
      state.containers = (state.containers || []).filter((c) => c.id !== id && !(c.id && c.id.startsWith(id)));
    }
    await refreshAll();
    renderView();
    toast("Done", true);
  } catch (err) {
    toast(err.message);
  } finally {
    state.pending.delete(key);
    renderView();
  }
}

function $(selector) {
  return document.querySelector(selector);
}
