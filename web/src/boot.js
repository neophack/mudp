// One-time boot sequence, run by the router guard before the first navigation
// resolves. Mirrors the old app.js load(): setup check → session check → i18n
// → data load → background pollers. Returns the landing route name.

import { api, readCSRFCookie } from "@/api";
import { store, refreshAll, fetchNotifications, applySiteName } from "@/store";
import { initI18n } from "@/lib/i18n.js";
import { applyElementLocale } from "@/i18n";
import { startBackupJobsPolling, startTaskPolling } from "@/jobs";
import { startAutoRefresh } from "@/refresh";

async function establishSession() {
  const me = await api("/api/me").catch(() => null);
  if (!me || me.authenticated === false) {
    store.me = null;
    store.csrfToken = "";
    return "login";
  }
  store.csrfToken = me.csrfToken || readCSRFCookie() || "";
  store.me = me;
  store.feishu = (await api("/api/feishu/config").catch(() => ({ enabled: false }))).enabled;
  // Initialize i18n with user language preference, group language, and system default.
  initI18n(me.language, me.defaultLanguage, me.groupLanguage);
  applyElementLocale();
  fetchNotifications().catch(() => {});
  if (me.pending) return "pending";
  return "app";
}

export async function boot() {
  // Resolve the language from localStorage first so the setup and login pages
  // — which render before any session exists — are already translated;
  // establishSession() re-runs it once the account's own preference is known.
  initI18n();
  applyElementLocale();
  const setup = await api("/api/setup/status").catch(() => ({ setupNeeded: false }));
  applySiteName(setup.siteName);
  if (setup.setupNeeded) {
    store.setupNeeded = true;
    return "setup";
  }
  const dest = await establishSession();
  if (dest !== "app") return dest;
  // Data loads must never gate navigation: a stalled section endpoint (e.g. a
  // wedged Docker daemon) would otherwise keep the user on the login screen.
  // Views render their empty states and fill in as responses land.
  refreshAll().catch(() => {});
  // Best-effort GPU count probe so the create modal renders the right GPU options.
  // Failure (non-GPU host, offline) leaves gpuCount at 0 and the modal falls
  // back to none/all.
  api("/api/hardware/gpus").then((r) => { store.gpuCount = r.count || 0; }).catch(() => {});
  // Begin background auto-refresh of the active route's data.
  startAutoRefresh();
  // Server-side backup jobs survive the browser tab; poll them so the
  // background-jobs badge reflects in-progress backups started elsewhere.
  startBackupJobsPolling();
  // Bulk file tasks (netdisk copy/move/transfer/restore/upload) that have
  // been running long enough to matter.
  startTaskPolling();
  return "app";
}

// afterLogin finishes the session bootstrap for an in-page login (the boot()
// path above was skipped because the user had no session at load time).
export async function afterLogin(user, csrfToken) {
  store.me = user;
  store.csrfToken = csrfToken || "";
  if (user.pending) return "pending";
  refreshAll().catch(() => {});
  api("/api/hardware/gpus").then((r) => { store.gpuCount = r.count || 0; }).catch(() => {});
  startAutoRefresh();
  startBackupJobsPolling();
  startTaskPolling();
  return "app";
}
