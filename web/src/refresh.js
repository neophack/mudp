// Centralized auto-refresh: polls the active route's data on a per-route
// interval. Because views render reactively from the store, "re-render only
// when data changed" is handled by Vue itself — this loop only refreshes the
// data. Stays invisible:
//  - recursive setTimeout (never setInterval), so requests never overlap;
//  - the whole tick is wrapped in try/catch, so a transient error can never
//    kill the loop;
//  - ticks are skipped while logged out, while the page is hidden, while an
//    Element dialog/drawer is open, or while the user is typing in the view.

import { store, refreshSection, fetchNotifications } from "@/store";

// ROUTE_REFRESH maps a route name to its poll interval and the sections to
// refresh. Views not listed either poll themselves (netdisk, hardware,
// processes, security) or are static forms (settings). usage/mcp keep
// view-local data and register a hook below instead of a store section.
const ROUTE_REFRESH = {
  containers: { ms: 5000, sections: ["containers"] },
  dashboard: { ms: 8000, sections: ["dashboard"] },
  images: { ms: 10000, sections: ["images"] },
  volumes: { ms: 10000, sections: ["volumes"] },
  networks: { ms: 10000, sections: ["networks"] },
  stacks: { ms: 10000, sections: ["stacks"] },
  users: { ms: 15000, sections: ["users", "groups"] },
  audit: { ms: 15000, sections: ["audit"] },
  disks: { ms: 15000, sections: ["disks"] },
  forwards: { ms: 5000, sections: ["forwards"] },
  usage: { ms: 10000 },
  mcp: { ms: 15000 },
};

// Views whose data is view-local (not a store section) register a refresh
// hook for their route; runTick drives it at the route's interval.
const routeHooks = new Map();
export function registerRouteRefresh(name, fn) {
  routeHooks.set(name, fn);
}
export function unregisterRouteRefresh(name) {
  routeHooks.delete(name);
}

const FALLBACK_MS = 10000;
let timer = null;
let inFlight = false;

// dialogOpen reports whether an Element overlay (dialog/drawer/message-box)
// is currently on screen — background refreshes must not run behind one.
function overlayOpen() {
  return !!(document.querySelector(".v-modal") || document.querySelector(".el-message-box__wrapper"));
}

function userTyping() {
  const active = document.activeElement;
  return !!active && !!active.closest(".app-main") && active.matches("input, textarea, select");
}

export function tickBlocked() {
  if (!store.me) return true;
  if (document.hidden) return true;
  if (overlayOpen()) return true;
  return userTyping();
}

// startAutoRefresh begins the polling loop. Idempotent: repeated calls (e.g.
// on re-login) never create a second loop or a second listener.
export function startAutoRefresh() {
  if (timer !== null) return;
  document.addEventListener("visibilitychange", () => {
    if (!document.hidden) refreshActiveRoute();
  });
  timer = setTimeout(tick, intervalMs());
}

// refreshActiveRoute runs one quiet refresh of the current route's data.
// Called when the page becomes visible again, so the view the user lands on
// is fresh without waiting for the next interval.
export function refreshActiveRoute() {
  return runTick().catch(() => {});
}

async function tick() {
  await runTick().catch(() => {});
  timer = setTimeout(tick, intervalMs());
}

async function runTick() {
  if (inFlight || tickBlocked()) return;
  const name = store.route || "";
  const policy = ROUTE_REFRESH[name];
  inFlight = true;
  try {
    if (policy) {
      // Skip the audit poll while the operator has filters active: an
      // unfiltered refresh would silently replace what the filters describe.
      if (policy.sections && !(name === "audit" && store.auditSearch && (store.auditSearch.actor || store.auditSearch.action))) {
        await refreshSection(...policy.sections);
      }
      const hook = routeHooks.get(name);
      if (hook) await hook();
    }
    // The bell badge refreshes on every tick of every route (even ones
    // without a data policy, like hardware) so new approvals/notices show up
    // live.
    await fetchNotifications();
  } finally {
    inFlight = false;
  }
}

function intervalMs() {
  return ROUTE_REFRESH[store.route]?.ms || FALLBACK_MS;
}
