// Processes: every process across the caller's running containers, plus the
// exit-watches the server polls (10s). When a watched process ends the owner
// gets an in-app notification and, when configured, a Feishu message.

import { state, api, toast, escapeHtml, isAdmin, displayNameForUsername, t } from "../app.js";

const POLL_MS = 5000;

let pollTimer = null;

export async function renderProcesses() {
  stopPolling();
  const tabAtEntry = state.tab;
  const data = await api("/api/processes").catch(() => null);
  if (state.tab !== tabAtEntry) return;
  if (!data) {
    $("#view").innerHTML = `<div class="card"><div class="card-body"><p class="hint">${t("processes.loadFailed")}</p></div></div>`;
    return;
  }
  paint(data.processes || [], data.watches || []);
  pollTimer = setInterval(async () => {
    if (state.tab !== "processes") return stopPolling();
    const fresh = await api("/api/processes").catch(() => null);
    if (fresh && state.tab === "processes") paint(fresh.processes || [], fresh.watches || []);
  }, POLL_MS);
}

export function stopPolling() {
  if (pollTimer) clearInterval(pollTimer);
  pollTimer = null;
}

function paint(processes, watches) {
  const admin = isAdmin();
  const watchedKeys = new Set(watches.map((w) => `${w.containerId}:${w.pid}`));
  const watchRows = watches.map((w) =>
    `<tr>` +
      `<td>${escapeHtml(w.containerName || w.containerId.slice(0, 12))}</td>` +
      `<td class="mono">${escapeHtml(String(w.pid))}</td>` +
      `<td><div class="secondary-line mono">${escapeHtml(w.command || "")}</div></td>` +
      `<td class="mono">${escapeHtml(w.createdAt || "")}</td>` +
      `<td><button class="ghost" data-unwatch="${w.id}">${t("processes.unwatch")}</button></td>` +
    `</tr>`
  ).join("");
  const rows = processes.map((p) => {
    const key = `${p.containerId}:${p.pid}`;
    const watched = watchedKeys.has(key);
    return (
      `<tr>` +
        (admin ? `<td>${escapeHtml(displayNameForUsername(p.user || ""))}</td>` : "") +
        `<td>${escapeHtml(p.container || "")}</td>` +
        `<td class="mono">${escapeHtml(String(p.pid))}</td>` +
        `<td>${(p.cpuPct || 0).toFixed(1)}%</td>` +
        `<td>${fmtMem(p.memMb)}</td>` +
        `<td><div class="secondary-line mono">${escapeHtml(p.command || "")}</div></td>` +
        `<td>${watched
          ? `<span class="badge badge-ok"><span class="dot"></span>${t("processes.watching")}</span>`
          : `<button class="ghost" data-watch-container="${escapeHtml(p.containerId)}" data-watch-pid="${escapeHtml(String(p.pid))}">${t("processes.watch")}</button>`}</td>` +
      `</tr>`
    );
  }).join("");
  $("#view").innerHTML =
    `<div class="stack">` +
      `<div class="card">` +
        `<div class="card-head"><h2>${t("processes.watchedTitle")}</h2>` +
          `<span class="card-head-sub">${t("processes.watchedSub")}</span></div>` +
        `<table class="data">` +
          `<thead><tr><th>${t("processes.colContainer")}</th><th>${t("processes.colPid")}</th><th>${t("processes.colCommand")}</th><th>${t("processes.colSince")}</th><th class="actions">${t("common.actions")}</th></tr></thead>` +
          `<tbody>${watchRows || `<tr class="empty-row"><td colspan="5">${t("processes.noWatches")}</td></tr>`}</tbody>` +
        `</table>` +
      `</div>` +
      `<div class="card">` +
        `<div class="card-head"><h2>${t("processes.title")}</h2>` +
          `<span class="card-head-sub">${t("processes.sub")}</span></div>` +
        `<table class="data">` +
          `<thead><tr>${admin ? `<th>${t("common.user")}</th>` : ""}<th>${t("processes.colContainer")}</th><th>${t("processes.colPid")}</th><th>${t("processes.colCpu")}</th><th>${t("processes.colMem")}</th><th>${t("processes.colCommand")}</th><th class="actions">${t("common.actions")}</th></tr></thead>` +
          `<tbody>${rows || `<tr class="empty-row"><td colspan="${admin ? 7 : 6}">${t("processes.noProcesses")}</td></tr>`}</tbody>` +
        `</table>` +
      `</div>` +
    `</div>`;

  document.querySelectorAll("[data-unwatch]").forEach((btn) => {
    btn.onclick = async () => {
      try {
        await api("/api/containers/processes/unwatch", { method: "POST", body: JSON.stringify({ id: Number(btn.dataset.unwatch) }) });
        toast(t("processes.unwatched"), true);
        renderProcesses();
      } catch (err) {
        toast(err.message);
      }
    };
  });
  document.querySelectorAll("[data-watch-container]").forEach((btn) => {
    btn.onclick = async () => {
      try {
        await api("/api/containers/processes/watch", {
          method: "POST",
          body: JSON.stringify({ containerId: btn.dataset.watchContainer, pid: btn.dataset.watchPid }),
        });
        toast(t("processes.watched"), true);
        renderProcesses();
      } catch (err) {
        toast(err.message);
      }
    };
  });
}

function fmtMem(mb) {
  if (!mb || mb <= 0) return "0 MB";
  if (mb < 1024) return `${Math.round(mb)} MB`;
  return `${(mb / 1024).toFixed(1)} GB`;
}

function $(selector) {
  return document.querySelector(selector);
}
