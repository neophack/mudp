// fnOS-style system update UI: a dedicated update window (version transition,
// release notes, manual downloads) and — once the admin commits — a locked
// full-screen overlay whose progress ring walks through download → install →
// restart, then reloads the page onto the new version. The upgrade itself
// runs server-side (internal/server/selfupgrade.go); this module only starts
// it and paints its progress via UpgradeDialog.vue / UpgradeOverlay.vue.

import { reactive } from "vue";
import { api } from "@/api";
import { tt } from "@/i18n";
import { fmtBytes } from "@/lib/common.js";

export const upgradeState = reactive({
  // Update window: { visible, res } where res is a /api/update/check payload.
  dialog: { visible: false, res: null },
  // Full-screen upgrade overlay: null when idle, else progress fields.
  overlay: null,
});

export function isUpgrading() {
  return !!upgradeState.overlay;
}

// openUpgrade opens the update window. `check` is the dashboard's
// already-fetched /api/update/check payload when available; without one it
// fetches the server-cached answer itself.
export async function openUpgrade(check) {
  const res = check || (await api("/api/update/check").catch(() => null));
  if (!res) {
    import("element-plus").then(({ ElMessage }) => ElMessage.error(tt("dash.checkFailed")));
    return;
  }
  upgradeState.dialog = { visible: true, res };
}

export function closeUpgrade() {
  upgradeState.dialog = { visible: false, res: null };
}

// startUpgrade closes the update window, locks the screen behind a full-screen
// overlay, starts the server-side self-upgrade and polls it to completion.
export function startUpgrade(tag) {
  if (isUpgrading()) return;
  closeUpgrade();
  upgradeState.overlay = {
    phaseKey: "upgrade.preparing",
    detail: "",
    pct: null, // null → indeterminate spinner ring
    done: false, // "ok" | "err"
    failed: false,
    message: "",
  };

  // The old process exits mid-upgrade, so expect a stretch of failed fetches:
  // connection refused simply means "restarting".
  const deadline = Date.now() + 4 * 60 * 1000;
  const stop = (message) => {
    clearInterval(poll);
    failUpgrade(message);
  };
  const poll = setInterval(pollTick, 1000);

  api("/api/admin/upgrade", { method: "POST", body: JSON.stringify({ tag }) })
    .catch(async (err) => {
      // A second click may race an in-flight upgrade (409): adopt it instead
      // of failing — the poll below re-attaches to its progress.
      const st = await api("/api/admin/upgrade").catch(() => null);
      if (!st || (st.phase !== "running:download" && st.phase !== "running:restarting")) {
        stop(err.message);
      }
    });

  async function pollTick() {
    const o = upgradeState.overlay;
    if (!o) {
      clearInterval(poll);
      return;
    }
    try {
      const st = await api("/api/admin/upgrade");
      if (st.phase === "error") {
        stop(st.message ? `${tt("upgrade.failed")}: ${st.message}` : tt("upgrade.failed"));
        return;
      }
      if (st.phase === "running:download") {
        const read = st.read || 0;
        const total = st.total || 0;
        const pct = total > 0 ? (read / total) * 100 : null;
        const detail = total > 0 ? `${fmtBytes(read)} / ${fmtBytes(total)}` : fmtBytes(read);
        o.phaseKey = "upgrade.downloading";
        o.detail = detail;
        o.pct = pct;
      } else if (st.phase === "running:restarting") {
        o.phaseKey = "upgrade.installing";
        o.detail = "";
        o.pct = null;
      }
    } catch {
      o.phaseKey = "upgrade.restarting";
      o.detail = "";
      o.pct = null;
    }
    try {
      const me = await api("/api/me");
      if (me && me.version === tag) {
        clearInterval(poll);
        finishUpgrade();
      }
    } catch { /* still restarting */ }
    if (Date.now() > deadline) {
      stop(tt("upgrade.timeout"));
    }
  }
}

function finishUpgrade() {
  const o = upgradeState.overlay;
  if (!o) return;
  o.phaseKey = "upgrade.success";
  o.detail = "";
  o.pct = null;
  o.done = "ok";
  import("element-plus").then(({ ElMessage }) => ElMessage.success(tt("upgrade.success")));
  setTimeout(() => location.reload(), 1200);
}

function failUpgrade(message) {
  const o = upgradeState.overlay;
  if (!o) return;
  o.phaseKey = message;
  o.detail = tt("upgrade.rolledBack");
  o.pct = null;
  o.done = "err";
}

export function closeUpgradeOverlay() {
  upgradeState.overlay = null;
}
