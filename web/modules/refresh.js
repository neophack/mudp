// Centralized auto-refresh: polls the active tab's data on a per-tab interval
// and re-renders the view only when the data actually changed. Replaces the
// manual Refresh button for background freshness. Designed to stay invisible:
//  - recursive setTimeout (never setInterval), so requests never overlap;
//  - the whole tick is wrapped in try/catch, so a transient error can never
//    kill the loop;
//  - ticks are skipped while logged out, while the page is hidden, while a
//    modal is open, or while the user is editing a control inside #view;
//  - state-backed views re-render only when the fetched data signature
//    changed, so scroll position, selections and focus survive quiet ticks.

import { state, api, refreshSection, renderView } from "../app.js";
import { fetchNotifications, updateNotificationBadge } from "./notifications.js";
import { renderUsage } from "./usage.js";
import { renderNetdisk } from "./netdisk.js";
import { refreshMCPTokens } from "./mcp.js";
import { reloadForwards } from "./forwards.js";

// sectionLoader polls one or more global-state sections and returns a
// signature string used for change detection.
function sectionLoader(...keys) {
  return async () => {
    await refreshSection(...keys);
    return JSON.stringify(keys.map((k) => state[k]));
  };
}

// auditLoader skips polling while the operator has filters active: filtered
// results live in state.audit, and an unfiltered refresh would silently
// replace what the filter inputs describe.
async function auditLoader() {
  const q = state.auditSearch || {};
  if (q.actor || q.action) return lastSigs.audit;
  await refreshSection("audit");
  return JSON.stringify(state.audit);
}

// usersLoader refreshes the user/group lists, the admin netdisk-usage report,
// and the admin long-running-task list in parallel, then folds all of it into
// the change-detection signature. Folding usage/tasks in is what makes those
// cards update live from another session's activity — without it, only
// users/groups changes would trigger a re-render.
async function usersLoader() {
  const isAdmin = state.me && state.me.role === "admin";
  const usagePromise = isAdmin ? api("/api/admin/netdisk/usage").catch(() => null) : Promise.resolve(null);
  const tasksPromise = isAdmin ? api("/api/admin/tasks").catch(() => null) : Promise.resolve(null);
  // refreshSection writes users/groups into state in parallel with the other
  // fetches; we don't need its return value, just the state mutation.
  await Promise.all([refreshSection("users", "groups"), usagePromise, tasksPromise]);
  const usage = await usagePromise;
  const tasks = await tasksPromise;
  // Stash the fresh usage rows so renderUsers() can paint them without a
  // duplicate fetch on this tick. renderUsers re-indexes by id at render time.
  if (usage) state.netdiskUsageRows = usage;
  if (tasks) state.adminTasks = tasks;
  return JSON.stringify([state.users, state.groups, usage, tasks]);
}

// TAB_REFRESH maps a tab to its poll interval and data loader. A loader
// returning null means "the render function fetches and decides itself"
// (self-fetching views, marked selfFetch). hardware polls itself on its own
// timer; scripts is a static config form and is intentionally excluded.
const TAB_REFRESH = {
  containers: { ms: 5000, load: sectionLoader("containers") },
  dashboard: { ms: 8000, load: sectionLoader("dashboard") },
  images: { ms: 10000, load: sectionLoader("images") },
  volumes: { ms: 10000, load: sectionLoader("volumes") },
  networks: { ms: 10000, load: sectionLoader("networks") },
  stacks: { ms: 10000, load: sectionLoader("stacks") },
  usage: { ms: 10000, selfFetch: true, load: async () => { await renderUsage(); return null; } },
  users: { ms: 15000, load: usersLoader },
  audit: { ms: 15000, load: auditLoader },
  netdisk: { ms: 15000, selfFetch: true, load: async () => { await renderNetdisk(); return null; } },
  // MCP: refresh the token list into state, then only re-render when it changed.
  // This keeps the inline create-token form from being clobbered every tick.
  mcp: { ms: 15000, load: async () => { await refreshMCPTokens(); return JSON.stringify([state.mcpTokens, state.mcpRemote]); } },
  disks: { ms: 15000, load: sectionLoader("disks") },
  // Forwards carry live connection counts and follow containers across
  // restarts, so they are polled on the container cadence rather than the
  // slower config one.
  forwards: { ms: 5000, load: async () => JSON.stringify(await reloadForwards()) },
};

const FALLBACK_MS = 10000;
// lastSigs holds the most recently rendered data signature per tab; a tick
// re-renders only when the signature changed since the last render.
const lastSigs = {};
let timer = null;
let inFlight = false;

// tickBlocked reports whether a background refresh must not run right now.
// Exported for unit tests.
export function tickBlocked() {
  if (!state.me) return true;
  if (document.hidden) return true;
  if (document.querySelector(".modal-backdrop")) return true;
  const view = document.querySelector("#view");
  const active = document.activeElement;
  if (view && active && view.contains(active) && active.matches("input, textarea, select")) return true;
  return false;
}

// startAutoRefresh begins the polling loop. Idempotent: repeated calls (e.g.
// on re-login) never create a second loop or a second listener.
export function startAutoRefresh() {
  if (timer !== null) return;
  document.addEventListener("visibilitychange", () => {
    if (!document.hidden) refreshActiveTab();
  });
  timer = setTimeout(tick, intervalMs());
}

// refreshActiveTab runs one quiet refresh of the current tab. Called when
// switching tabs and when the page becomes visible again, so the view the
// user lands on is fresh without waiting for the next interval. Callers may
// ignore the returned promise (fire-and-forget); tests await it.
// includeSelfFetch=false skips self-fetching views (netdisk/usage): their
// renderView() already fetched on entry, so a tick would duplicate the fetch.
export function refreshActiveTab({ includeSelfFetch = true } = {}) {
  if (!includeSelfFetch && TAB_REFRESH[state.tab]?.selfFetch) return Promise.resolve();
  return runTick().catch(() => {});
}

async function tick() {
  await runTick().catch(() => {});
  timer = setTimeout(tick, intervalMs());
}

async function runTick() {
  if (inFlight || tickBlocked()) return;
  const tab = state.tab;
  const policy = TAB_REFRESH[tab];
  inFlight = true;
  try {
    if (policy) {
      const sig = await policy.load();
      // The user navigated away or started editing while the request was in
      // flight: drop the re-render rather than clobbering what is on screen.
      if (state.tab === tab && !tickBlocked() && sig !== null && sig !== lastSigs[tab]) {
        lastSigs[tab] = sig;
        renderView();
      }
    }
    // The bell badge refreshes on every tick of every tab (even ones without
    // a data policy, like hardware) so new approvals/notices show up live.
    await fetchNotifications();
    updateNotificationBadge();
  } finally {
    inFlight = false;
  }
}

function intervalMs() {
  return TAB_REFRESH[state.tab]?.ms || FALLBACK_MS;
}
