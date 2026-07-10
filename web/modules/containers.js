// Container list (table view) and container actions (start/stop/restart/remove),
// plus routing of logs/terminal/details into their dedicated modules.

import { state, api, toast, refreshAll, renderView, escapeHtml } from "../app.js";
import { openLogs } from "./logs.js";
import { openTerminal } from "./terminal.js";
import { openDetails } from "./details.js";
import { openStats } from "./stats.js";
import { openFiles } from "./files.js";

export function renderContainers() {
  const q = state.search.trim().toLowerCase();
  const list = q
    ? state.containers.filter(
        (c) =>
          (c.name || c.fullName || "").toLowerCase().includes(q) ||
          (c.image || "").toLowerCase().includes(q)
      )
    : state.containers;

  const rows =
    list.map(containerRow).join("") ||
    `<tr class="empty-row"><td colspan="5">No containers. Click “+ New Container” to create one.</td></tr>`;

  $("#view").innerHTML =
    `<div class="card"><table class="data">` +
    `<thead><tr><th>Container</th><th>Status</th><th>Image</th><th>Resources</th><th class="actions">Actions</th></tr></thead>` +
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
}

// Placeholder so renderLogViewer (from logs.js) can be wired without a circular
// top-level import. The actual viewer is owned by logs.js.
function bindLogViewerHook() {}

function containerRow(c) {
  const running = c.state === "running";
  const name = c.name || c.fullName;
  const pending = (act) => state.pending.has(c.id + ":" + act);
  const statusBadge = running
    ? `<span class="badge badge-ok"><span class="dot"></span>${escapeHtml(c.status || "Up")}</span>`
    : `<span class="badge badge-muted"><span class="dot"></span>${escapeHtml(c.status || c.state || "Stopped")}</span>`;
  const ports = (c.ports || []).join(", ") || "—";
  // Docker on Linux binds to 0.0.0.0 so the backend URL contains 127.0.0.1.
  // Replace it (and localhost/0.0.0.0/::1) with the server hostname the browser
  // is already connected to, so links work on remote hosts.
  const fixUrl = (url) => url ? url.replace(/^(https?:\/\/)(127\.0\.0\.1|0\.0\.0\.0|::1|localhost)(:\d+)/, `$1${location.hostname}$3`) : url;
  // Coerce to a number defensively before toFixed: the backend usually sends
  // numbers, but a stray string/null would otherwise throw and blank the table.
  const num = (v) => (typeof v === "number" && isFinite(v) ? v : 0);
  const host = location.hostname;
  const conn = [];
  if (c.http8080Url) conn.push(`<a class="port-link" href="${escapeHtml(fixUrl(c.http8080Url))}" target="_blank" rel="noopener">8080 ↗</a>`);
  if (c.http80Url) conn.push(`<a class="port-link" href="${escapeHtml(fixUrl(c.http80Url))}" target="_blank" rel="noopener">80 ↗</a>`);
  const iconBtn = (act, glyph, title, cls = "icon", enabled = true) => {
    const isLoading = pending(act);
    const dis = !enabled || isLoading ? "disabled" : "";
    const loading = isLoading ? " is-loading" : "";
    return `<button class="${cls}${loading}" title="${title}" data-id="${escapeHtml(c.id)}" data-name="${escapeHtml(name)}" data-act="${act}" ${dis}>${glyph}</button>`;
  };
  return (
    `<tr>` +
      `<td><div class="primary-line">${escapeHtml(name)}</div><div class="secondary-line">${conn.length ? conn.join(" <span class='sep'>·</span> ") : escapeHtml(ports)}</div></td>` +
      `<td>${statusBadge}</td>` +
      `<td><div class="secondary-line">${escapeHtml(c.image || c.Image || "—")}</div></td>` +
      `<td><div class="secondary-line">${num(c.memoryMb).toFixed(0)} MB mem · ${num(c.diskMb).toFixed(0)} MB disk</div>` +
        `<div class="secondary-line">GPU: ${escapeHtml(c.gpu || "none")}</div></td>` +
      `<td class="actions">` +
        iconBtn("logs", "📄", "Logs") +
        iconBtn("start", "▶", "Start", "icon ok", !running) +
        iconBtn("stop", "■", "Stop", "icon warn", running) +
        iconBtn("restart", "⟳", "Restart", "icon", running) +
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
