// Activity log (admin only): filterable list of recent management actions,
// with a CSV export button.

import { state, api, toast, displayNameForUsername } from "../app.js";

export function renderAudit() {
  const q = state.auditSearch || {};
  const rows = (state.audit || []).map(auditRow).join("") ||
    `<tr class="empty-row"><td colspan="4">No activity recorded.</td></tr>`;
  $("#view").innerHTML =
    `<div class="card">` +
      `<div class="card-head">` +
        `<h2>Activity Log</h2>` +
        `<div class="filters">` +
          `<input id="fActor" placeholder="Filter by actor…" value="${escapeHtml(q.actor || "")}">` +
          `<input id="fAction" placeholder="Filter by action…" value="${escapeHtml(q.action || "")}">` +
          `<button class="ghost" id="exportCsv">Export CSV</button>` +
        `</div>` +
      `</div>` +
      `<table class="data">` +
        `<thead><tr><th>Time</th><th>Actor</th><th>Action</th><th>Target</th></tr></thead>` +
        `<tbody>${rows}</tbody>` +
      `</table>` +
    `</div>`;
  $("#fActor").oninput = (e) => { state.auditSearch = { ...q, actor: e.target.value }; reloadAudit(); };
  $("#fAction").oninput = (e) => { state.auditSearch = { ...q, action: e.target.value }; reloadAudit(); };
  $("#exportCsv").onclick = exportCsv;
}

function auditRow(e) {
  return (
    `<tr>` +
      `<td><div class="secondary-line">${escapeHtml(fmtTime(e.createdAt))}</div></td>` +
      `<td><div class="primary-line">${escapeHtml(displayNameForUsername(e.actor))}</div></td>` +
      `<td><span class="badge badge-accent">${escapeHtml(e.action)}</span></td>` +
      `<td><span class="secondary-line mono">${escapeHtml(e.target)}</span></td>` +
    `</tr>`
  );
}

async function reloadAudit() {
  const q = state.auditSearch || {};
  const params = new URLSearchParams();
  if (q.actor) params.set("actor", q.actor);
  if (q.action) params.set("action", q.action);
  try {
    state.audit = await api("/api/admin/audit?" + params.toString());
    renderAudit();
  } catch (err) {
    toast(err.message);
  }
}

function exportCsv() {
  const rows = state.audit || [];
  const lines = ["time,actor,action,target"];
  for (const e of rows) {
    lines.push([e.createdAt, e.actor, e.action, e.target].map(csvCell).join(","));
  }
  const blob = new Blob([lines.join("\n")], { type: "text/csv" });
  const url = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = url;
  a.download = `mudp-audit-${new Date().toISOString().slice(0, 10)}.csv`;
  a.click();
  URL.revokeObjectURL(url);
}

function csvCell(v) {
  const s = String(v ?? "");
  return /[",\n]/.test(s) ? '"' + s.replace(/"/g, '""') + '"' : s;
}

function fmtTime(iso) {
  if (!iso) return "—";
  const t = new Date(iso);
  return isNaN(t) ? iso : t.toLocaleString();
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

function $(selector) {
  return document.querySelector(selector);
}
