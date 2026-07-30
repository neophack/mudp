// Database housekeeping (admin): shows where the SQLite database's bytes are and
// lets an operator clear the log tables on demand.
//
// The automatic pruner already bounds those tables on a schedule; this page is
// for when an admin wants to see the breakdown and reclaim space now rather than
// wait. Only the tables in the store's allow-list can be cleared — user tables
// (users, groups, images, …) appear in the breakdown for visibility but carry no
// "clean" button, and the backend rejects them regardless.

import { state, api, toast, escapeHtml, fmtBytes, t } from "../app.js";
import { showModal, closeModal } from "./ui.js";

export async function renderDatabase() {
  // Fetch fresh on every entry: this page is about the current footprint, and a
  // stale cached number would mislead a decision to prune.
  $("#view").innerHTML = `<div class="card"><div class="card-body"><p class="hint">${t("database.loading")}</p></div></div>`;
  let data;
  try {
    data = await api("/api/admin/db/usage");
  } catch (err) {
    $("#view").innerHTML = `<div class="card"><div class="card-body"><div class="error-box">${escapeHtml(err.message)}</div></div></div>`;
    return;
  }
  state.dbUsage = data;

  const report = data.report || {};
  const tables = report.tables || [];
  const prunable = new Set(data.prunable || []);
  const maxBytes = Math.max(1, ...tables.map((tb) => tb.bytes || 0));

  $("#view").innerHTML =
    `<div class="stack">` +
      `<div class="db-stat-row">` +
        statCard(t("database.dbFile"), report.fileBytes) +
        statCard(t("database.walFile"), report.walBytes) +
        `<div class="card"><div class="card-body"><div class="stat"><span class="stat-label">${t("database.freePages")}</span><span class="stat-value">${escapeHtml(String(report.freePages ?? 0))}</span></div></div></div>` +
      `</div>` +
      `<div class="card">` +
        `<div class="card-head"><h2>${t("database.tables")}</h2>` +
          `<button class="ghost" id="dbRefresh">${t("action.refresh")}</button>` +
        `</div>` +
        (report.byteSizes
          ? `<p class="hint" style="padding:0 16px;margin:0 0 8px">${t("database.tableHint")}</p>`
          : `<p class="hint" style="padding:0 16px;margin:0 0 8px">${t("database.rowOnlyHint")}</p>`) +
        `<table class="data">` +
          `<thead><tr><th>${t("database.colTable")}</th><th>${t("database.colPurpose")}</th><th>${t("database.colSize")}</th><th>${t("database.colRows")}</th><th class="actions">${t("common.actions")}</th></tr></thead>` +
          `<tbody>${tables.map((tb) => tableRow(tb, prunable, maxBytes)).join("") || `<tr class="empty-row"><td colspan="5">${t("database.noTables")}</td></tr>`}</tbody>` +
        `</table>` +
      `</div>` +
    `</div>`;

  $("#dbRefresh").onclick = () => {
    state.dbUsage = null;
    renderDatabase();
  };
  document.querySelectorAll("[data-db-prune]").forEach((btn) => {
    btn.onclick = () => openPruneModal(btn.dataset.dbPrune);
  });
}

// statCard renders one top-of-page summary tile showing a byte figure.
function statCard(label, bytes) {
  return `<div class="card"><div class="card-body"><div class="stat"><span class="stat-label">${escapeHtml(label)}</span><span class="stat-value mono">${fmtBytes(bytes || 0)}</span></div></div></div>`;
}

// tableRow renders one table in the breakdown. Only log tables in the allow-list
// get a "clean" button; user tables are shown for visibility without one.
function tableRow(tb, prunable, maxBytes) {
  const pct = maxBytes > 0 ? Math.round(((tb.bytes || 0) / maxBytes) * 100) : 0;
  const size = tb.bytes > 0 ? fmtBytes(tb.bytes) : "—";
  const bar = tb.bytes > 0
    ? `<div class="bar"><div class="bar-fill" style="width:${pct}%"></div></div>`
    : "";
  const canClean = prunable.has(tb.name);
  // The description comes from the backend (one source of truth for the table
  // list). Unknown tables read back empty; show a dash so the cell is not blank.
  const purpose = escapeHtml(tb.description || "—");
  return (
    `<tr>` +
      `<td><div class="primary-line mono">${escapeHtml(tb.name)}</div></td>` +
      `<td><div class="secondary-line purpose-cell">${purpose}</div></td>` +
      `<td><div class="secondary-line mono">${escapeHtml(size)}</div>${bar}</td>` +
      `<td><div class="secondary-line">${escapeHtml(String(tb.rows ?? 0))}</div></td>` +
      `<td class="actions">` +
        (canClean
          ? `<button class="ghost" data-db-prune="${escapeHtml(tb.name)}">${t("database.clean")}</button>`
          : `<span class="hint">${t("database.protected")}</span>`) +
      `</td>` +
    `</tr>`
  );
}

// openPruneModal asks how far back to clear before deleting. olderThanDays <= 0
// means "everything"; a positive value clears only rows older than that.
function openPruneModal(table) {
  showModal({
    kind: "dbPrune",
    title: t("database.cleanTitle", { table }),
    body:
      `<form id="dbPruneForm" class="compact">` +
        `<p class="hint">${t("database.cleanHint", { table })}</p>` +
        `<label class="field-label">${t("database.cleanRange")}</label>` +
        `<select name="days">` +
          `<option value="7">${t("database.olderThan", { n: 7 })}</option>` +
          `<option value="30" selected>${t("database.olderThan", { n: 30 })}</option>` +
          `<option value="90">${t("database.olderThan", { n: 90 })}</option>` +
          `<option value="0">${t("database.allRows")}</option>` +
        `</select>` +
      `</form>`,
    foot: `<button class="ghost" data-close>${t("common.cancel")}</button><button class="danger" id="dbPruneConfirm">${t("database.cleanNow")}</button>`,
  });
  $("#dbPruneConfirm").onclick = async () => {
    const fd = new FormData($("#dbPruneForm"));
    const days = Number(fd.get("days"));
    try {
      const res = await api("/api/admin/db/prune", {
        method: "POST",
        body: JSON.stringify({ tables: [table], olderThanDays: days }),
      });
      const removed = res.deleted?.[table] ?? 0;
      toast(t("database.cleaned", { n: removed }), true);
      closeModal();
      state.dbUsage = null;
      renderDatabase();
    } catch (err) {
      toast(err.message);
    }
  };
}

function $(selector) {
  return document.querySelector(selector);
}
