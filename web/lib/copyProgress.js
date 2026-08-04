// Floating bottom-left progress overlay for netdisk copy/move/transfer/
// restore operations. Purely a DOM/state controller -- no networking (that
// stays with the caller, which already has `api` from app.js), mirroring how
// upload.js separates uploadWithProgress (networking) from the DOM-only
// showUploadOverlay.
//
// Two callers drive the same overlay, and only one owns it at a time:
//   - netdisk.js's copyItems/moveItems, for an operation started in this tab:
//     calls beginLocalCopy() immediately when the request is fired, then
//     feeds it live counts from its own fast /api/tasks?all=1 poll, and
//     end()s it once its fetch settles.
//   - jobs.js's regular /api/tasks poll, for an operation this tab didn't
//     start -- most notably one still running after the page was refreshed
//     mid-copy: calls syncCopyOverlayFromTasks() on every tick.
// A locally-owned overlay always wins over the passive poll; the poll only
// drives the overlay when no local operation currently owns it, and hands
// back control (closes it) once the task it was tracking disappears.
//
// The server reports raw Done/Total counts plus a `unit` ("bytes" or
// "items", see ActiveTask.Unit in tasks.go) -- all KB/MB/GB formatting
// happens here client-side via fmtBytes, never on the server.

import { fmtBytes } from "./common.js";

let overlayEl = null;
let source = null; // "local" | "poll" | null
let closeTimer = null;
let currentTaskId = null; // server task id backing the overlay, once known
let dismissedTaskId = null; // task id the user manually closed; stays hidden until this task ends

const KIND_TITLE = {
  "netdisk.copy": "Copying…",
  "netdisk.move": "Moving…",
  "netdisk.transfer": "Transferring…",
  "netdisk.restore": "Restoring…",
};

export const COPY_MOVE_TASK_KINDS = new Set(Object.keys(KIND_TITLE));

function ensureOverlay(kind) {
  clearTimeout(closeTimer);
  if (!overlayEl) {
    overlayEl = document.createElement("div");
    overlayEl.className = "copy-overlay";
    overlayEl.innerHTML =
      `<div class="copy-head">` +
        `<div class="copy-title"></div>` +
        `<button type="button" class="copy-close" aria-label="Close" title="Close">&times;</button>` +
      `</div>` +
      `<div class="bar copy-bar"><div class="bar-fill" style="width:0%"></div></div>` +
      `<div class="copy-meta"></div>`;
    // Stack above an already-open upload overlay rather than covering it.
    const uploadEl = document.querySelector(".upload-overlay");
    if (uploadEl) {
      overlayEl.style.bottom = `${20 + uploadEl.getBoundingClientRect().height + 12}px`;
    }
    overlayEl.querySelector(".copy-close").onclick = () => {
      dismissedTaskId = currentTaskId;
      removeOverlay();
      source = null;
    };
    document.body.appendChild(overlayEl);
  }
  overlayEl.querySelector(".copy-title").textContent = KIND_TITLE[kind] || "Working…";
}

function updateOverlay({ done = 0, total = 0, message = "", unit = "bytes" } = {}) {
  if (!overlayEl) return;
  const pct = total > 0 ? Math.min(100, Math.round((done / total) * 100)) : 0;
  overlayEl.querySelector(".copy-bar > .bar-fill").style.width = `${pct}%`;
  const fmt = unit === "items" ? String : fmtBytes;
  const totStr = total > 0 ? fmt(total) : "…";
  const msg = message ? ` · ${message}` : "";
  overlayEl.querySelector(".copy-meta").textContent = `${fmt(done)} / ${totStr} · ${pct}%${msg}`;
}

function removeOverlay() {
  if (overlayEl) {
    overlayEl.remove();
    overlayEl = null;
  }
}

// beginLocalCopy shows the overlay immediately for an operation this tab just
// started, before the server has reported anything about it yet. total is
// the item count known client-side (the real byte total, if any, arrives via
// the caller's own poll and update() call); unit guesses "items" for a
// same-disk move (always item-counted server-side, see ActiveTask.Unit) and
// "bytes" otherwise, self-correcting once the first poll tick lands.
// Returns { update(done, total, message, unit), end() } -- the caller drives
// update() from its own poll and calls end() once its fetch settles.
export function beginLocalCopy(kind, total) {
  source = "local";
  currentTaskId = null;
  dismissedTaskId = null; // a freshly started operation always shows, even if a prior one was dismissed
  let lastUnit = kind === "netdisk.move" ? "items" : "bytes";
  let lastTotal = total;
  ensureOverlay(kind);
  updateOverlay({ done: 0, total, unit: lastUnit });
  return {
    update(done, totalNow, message, unit, id) {
      if (source !== "local") return; // superseded by another local op; ignore stale ticks
      if (unit) lastUnit = unit;
      if (id) currentTaskId = id;
      lastTotal = totalNow || lastTotal;
      updateOverlay({ done, total: lastTotal, message, unit: lastUnit });
    },
    end() {
      if (source !== "local") return;
      updateOverlay({ done: lastTotal, total: lastTotal, unit: lastUnit });
      // Brief pause so the user sees the bar reach 100% before it vanishes.
      closeTimer = setTimeout(() => {
        if (source === "local") {
          removeOverlay();
          source = null;
          currentTaskId = null;
        }
      }, 500);
    },
  };
}

// syncCopyOverlayFromTasks drives the overlay from the passive /api/tasks
// poll (jobs.js) for operations this tab didn't itself start. Pass the
// currently-active tasks for this user already filtered to
// COPY_MOVE_TASK_KINDS; called on every poll tick regardless of whether any
// such tasks are present.
export function syncCopyOverlayFromTasks(tasks) {
  if (source === "local") return; // an in-tab operation owns the display
  if (!tasks || !tasks.length) {
    if (source === "poll") {
      removeOverlay();
      source = null;
    }
    dismissedTaskId = null; // nothing active -- next task to appear is a new one, show it
    return;
  }
  const t = tasks.slice().sort((a, b) => Date.parse(b.startedAt) - Date.parse(a.startedAt))[0];
  if (t.id === dismissedTaskId) return; // user closed this exact task's overlay; it's still visible in Jobs
  currentTaskId = t.id;
  source = "poll";
  ensureOverlay(t.kind);
  updateOverlay({ done: t.done || 0, total: t.total || 0, message: t.message, unit: t.unit || "bytes" });
}
