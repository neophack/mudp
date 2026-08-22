// Error monitor (admin): aggregated panics and 5xx responses, grouped by
// fingerprint the way Sentry groups issues. Recurrences bump a counter rather
// than adding rows; "resolve" deletes the group.

import { state, api, toast, escapeHtml, t } from "../app.js";
import { showModal } from "./ui.js";

export async function renderErrmon() {
  const tabAtEntry = state.tab;
  const data = await api("/api/admin/errors").catch(() => null);
  if (state.tab !== tabAtEntry) return;
  if (!data) {
    $("#view").innerHTML = `<div class="card"><div class="card-body"><p class="hint">${t("errors.loadFailed")}</p></div></div>`;
    return;
  }
  const stats = data.stats || {};
  const events = data.events || [];
  $("#view").innerHTML =
    `<div class="stack">` +
      `<div class="dash-tiles">` +
        tile(t("errors.statIssues"), stats.events ?? 0, "🧯") +
        tile(t("errors.statPanics"), stats.panics ?? 0, "💥") +
        tile(t("errors.statOccurrences"), stats.occurrences ?? 0, "🔁") +
      `</div>` +
      `<div class="card">` +
        `<div class="card-head"><h2>${t("errors.title")}</h2>` +
          `<div class="head-actions">` +
            `<a class="ghost" href="/api/admin/errors/export" download>${t("errors.export")}</a>` +
            `<button class="ghost" id="errorsClearBtn">${t("errors.clearAll")}</button>` +
          `</div>` +
        `</div>` +
        `<table class="data">` +
          `<thead><tr><th>${t("errors.colKind")}</th><th>${t("errors.colWhere")}</th><th>${t("errors.colMessage")}</th><th>${t("errors.colCount")}</th><th>${t("errors.colSeen")}</th><th class="actions">${t("common.actions")}</th></tr></thead>` +
          `<tbody>${events.map(eventRow).join("") || `<tr class="empty-row"><td colspan="6">${t("errors.noErrors")}</td></tr>`}</tbody>` +
        `</table>` +
      `</div>` +
    `</div>`;

  document.querySelectorAll("[data-error-details]").forEach((el) => {
    el.onclick = () => {
      const stack = el.getAttribute("data-error-details") || "";
      if (!stack) {
        toast(t("errors.noStack"));
        return;
      }
      showModal({
        kind: "error-stack",
        title: t("errors.viewStack"),
        body: `<pre class="mono stack-pre">${escapeHtml(stack)}</pre>`,
      });
    };
  });
  document.querySelectorAll("[data-error-resolve]").forEach((btn) => {
    btn.onclick = async () => {
      try {
        await api(`/api/admin/errors/${btn.dataset.errorResolve}`, { method: "DELETE" });
        toast(t("errors.resolved"), true);
        renderErrmon();
      } catch (err) {
        toast(err.message);
      }
    };
  });
  const clearBtn = $("#errorsClearBtn");
  if (clearBtn) {
    clearBtn.onclick = async () => {
      try {
        await api("/api/admin/errors/clear", { method: "POST" });
        toast(t("errors.cleared"), true);
        renderErrmon();
      } catch (err) {
        toast(err.message);
      }
    };
  }
}

function eventRow(e) {
  const isPanic = e.kind === "panic";
  const where = `${escapeHtml(e.method || "")} ${escapeHtml(e.path || "")}`.trim() || "—";
  const seen = `${t("errors.first")}: ${escapeHtml(e.firstSeen || "—")} · ${t("errors.last")}: ${escapeHtml(e.lastSeen || "—")}`;
  const message = `<div class="primary-line mono">${escapeHtml(truncate(e.message, 160))}</div>` +
    (e.stack ? `<button class="ghost" data-error-details="${escapeHtml(e.stack)}">${t("errors.viewStack")}</button>` : "");
  return (
    `<tr>` +
      `<td><span class="badge ${isPanic ? "badge-danger" : "badge-warn"}">${escapeHtml(e.kind)}</span></td>` +
      `<td class="mono">${where}</td>` +
      `<td>${message}</td>` +
      `<td>${e.count || 1}</td>` +
      `<td><div class="secondary-line">${seen}</div></td>` +
      `<td><button class="ghost" data-error-resolve="${e.id}">${t("errors.resolve")}</button></td>` +
    `</tr>`
  );
}

function tile(label, value, icon) {
  return (
    `<section class="card stat-tile">` +
      `<div class="stat-icon">${icon}</div>` +
      `<div class="stat-body">` +
        `<div class="stat-value">${escapeHtml(String(value))}</div>` +
        `<div class="stat-label">${escapeHtml(label)}</div>` +
      `</div>` +
    `</section>`
  );
}

function truncate(s, n) {
  s = s || "";
  return s.length > n ? s.slice(0, n) + "…" : s;
}

function $(selector) {
  return document.querySelector(selector);
}
