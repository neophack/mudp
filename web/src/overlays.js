// Reactive controller for the floating bottom-left copy/move/transfer/restore
// progress overlay. Mirrors the semantics of the old DOM-only lib controller:
// a locally-owned overlay (an operation started in this tab) always wins over
// the passive /api/tasks poll, which only drives the overlay for operations
// this tab didn't start (e.g. one still running after a mid-copy page
// refresh). CopyOverlay.vue renders whatever this module holds.

import { reactive } from "vue";
import { tt } from "@/i18n";

// Task kind -> i18n key, the same vocabulary the Jobs panel and the admin
// long-running-task table use, so the overlay follows the interface language.
const KIND_TITLE = {
  "netdisk.copy": "task.netdiskCopy",
  "netdisk.move": "task.netdiskMove",
  "netdisk.transfer": "task.netdiskTransfer",
  "netdisk.restore": "task.netdiskRestore",
};

export const COPY_MOVE_TASK_KINDS = new Set(Object.keys(KIND_TITLE));

export const copyOverlay = reactive({
  visible: false,
  title: "",
  done: 0,
  total: 0,
  message: "",
  unit: "bytes",
});

let source = null; // "local" | "poll" | null
let closeTimer = null;
let currentTaskId = null; // server task id backing the overlay, once known
let dismissedTaskId = null; // task id the user manually closed; hidden until it ends

function show(kind, { done = 0, total = 0, message = "", unit = "bytes" } = {}) {
  clearTimeout(closeTimer);
  copyOverlay.title = tt(KIND_TITLE[kind] || "common.loadingDots");
  copyOverlay.done = done;
  copyOverlay.total = total;
  copyOverlay.message = message || "";
  copyOverlay.unit = unit || "bytes";
  copyOverlay.visible = true;
}

// beginLocalCopy shows the overlay immediately for an operation this tab just
// started, before the server has reported anything about it yet. Returns
// { update(done, total, message, unit, id), end() } — the caller drives
// update() from its own poll and calls end() once its fetch settles.
export function beginLocalCopy(kind, total) {
  source = "local";
  currentTaskId = null;
  dismissedTaskId = null; // a freshly started operation always shows, even if a prior one was dismissed
  let lastUnit = kind === "netdisk.move" ? "items" : "bytes";
  let lastTotal = total;
  show(kind, { done: 0, total, unit: lastUnit });
  return {
    update(done, totalNow, message, unit, id) {
      if (source !== "local") return; // superseded by another local op; ignore stale ticks
      if (unit) lastUnit = unit;
      if (id) currentTaskId = id;
      lastTotal = totalNow || lastTotal;
      copyOverlay.done = done;
      copyOverlay.total = lastTotal;
      copyOverlay.message = message || "";
      copyOverlay.unit = lastUnit;
    },
    end() {
      if (source !== "local") return;
      copyOverlay.done = lastTotal;
      copyOverlay.total = lastTotal;
      copyOverlay.message = "";
      // Brief pause so the user sees the bar reach 100% before it vanishes.
      closeTimer = setTimeout(() => {
        if (source === "local") {
          copyOverlay.visible = false;
          source = null;
          currentTaskId = null;
        }
      }, 500);
    },
  };
}

// syncCopyOverlayFromTasks drives the overlay from the passive /api/tasks
// poll for operations this tab didn't itself start. Pass the currently-active
// tasks for this user already filtered to COPY_MOVE_TASK_KINDS; called on
// every poll tick regardless of whether any such tasks are present.
export function syncCopyOverlayFromTasks(tasks) {
  if (source === "local") return; // an in-tab operation owns the display
  if (!tasks || !tasks.length) {
    if (source === "poll") {
      copyOverlay.visible = false;
      source = null;
    }
    dismissedTaskId = null; // nothing active — next task to appear is a new one, show it
    return;
  }
  const t = tasks.slice().sort((a, b) => Date.parse(b.startedAt) - Date.parse(a.startedAt))[0];
  if (t.id === dismissedTaskId) return; // user closed this exact task's overlay; it's still visible in Jobs
  currentTaskId = t.id;
  source = "poll";
  show(t.kind, { done: t.done || 0, total: t.total || 0, message: t.message, unit: t.unit || "bytes" });
}

// dismissCopyOverlay hides the overlay for the task currently driving it; the
// task keeps running and stays visible in the Jobs panel.
export function dismissCopyOverlay() {
  dismissedTaskId = currentTaskId;
  copyOverlay.visible = false;
  source = null;
}
