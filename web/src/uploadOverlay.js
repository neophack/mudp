// Reactive controller for the floating upload-progress card. Same bounded-list
// semantics as the old DOM controller: only the files currently on the wire
// plus a ring of the most recently settled ones are rendered, so a huge
// multi-file upload never grows the DOM (or the state) without bound.
// UploadOverlay.vue renders whatever this module holds.

import { reactive } from "vue";
import { fmtBytes } from "@/lib/common.js";

export function fmtSpeed(bps) {
  return `${fmtBytes(Math.max(0, Math.round(bps || 0)))}/s`;
}

// Hard caps (kept tiny on purpose):
const MAX_ACTIVE = 100; // rows on the wire at once
const MAX_SETTLED = 20; // recently finished rows kept visible

// The single overlay instance; calling show again replaces it.
let overlayState = null;
let overlaySeq = 0;

export function showUploadOverlay() {
  overlayState = reactive({
    visible: true,
    label: "Uploading…",
    overall: { done: 0, failed: 0, total: 0, loaded: 0, bytesTotal: 0, speedBps: 0, etaSec: 0, percent: 0 },
    active: [], // { id, name, size, loaded, total, percent, speedBps }
    settled: [], // { id, name, size, status: "done"|"error", msg, retry }
    overflowDone: 0,
  });
  const s = overlayState;
  const findActive = (slot) => s.active.find((r) => r.id === slot);
  const findSettled = (slot) => s.settled.find((r) => r.id === slot);

  return {
    setLabel(text) { s.label = text; },

    addActive(meta) {
      if (s.active.length >= MAX_ACTIVE) return null;
      const row = reactive({
        id: ++overlaySeq,
        name: meta?.name ?? "file",
        size: meta?.size ?? 0,
        loaded: 0,
        total: meta?.size ?? 0,
        percent: 0,
        speedBps: 0,
      });
      s.active.unshift(row);
      return row.id;
    },

    updateActive(slot, p) {
      const row = findActive(slot);
      if (!row) return;
      row.loaded = p?.loaded ?? 0;
      row.total = p?.total ?? row.total;
      row.percent = p?.percent ?? 0;
      row.speedBps = p?.speedBps ?? 0;
    },

    markFailedWithRetry(slot, msg, onRetry) {
      let target = findActive(slot);
      if (target) {
        s.active = s.active.filter((r) => r.id !== slot);
        target = { id: target.id, name: target.name, size: target.size, status: "error", msg: msg || "Failed", retry: onRetry || null };
        s.settled.unshift(target);
      } else {
        target = findSettled(slot);
        if (target) {
          target.status = "error";
          target.msg = msg || "Failed";
          target.retry = onRetry || null;
        }
      }
      return !!target;
    },

    // Moves a settled (failed) row back into the active area, reusing the same
    // slot, so a retry animates the existing row instead of duplicating it.
    reactivate(slot, meta) {
      let row = findActive(slot);
      if (!row) {
        const settled = findSettled(slot);
        if (!settled) return false;
        s.settled = s.settled.filter((r) => r.id !== slot);
        row = reactive({ id: settled.id, name: meta?.name ?? settled.name, size: meta?.size ?? settled.size, loaded: 0, total: meta?.size ?? settled.size, percent: 0, speedBps: 0 });
        s.active.unshift(row);
      } else {
        row.name = meta?.name ?? row.name;
        if (meta?.size) row.size = meta.size;
        row.loaded = 0;
        row.percent = 0;
        row.speedBps = 0;
      }
      return true;
    },

    settleActive(slot, status, msg) {
      const row = findActive(slot);
      if (!row) return;
      s.active = s.active.filter((r) => r.id !== slot);
      s.settled.unshift({
        id: row.id,
        name: row.name,
        size: row.size,
        status: status === "error" ? "error" : "done",
        msg: status === "error" ? msg || "Failed" : "",
        retry: null,
      });
      // Keep the settled ring bounded: drop the oldest non-pinned row (a row
      // awaiting a user retry stays until acted on) and count the overflow.
      while (s.settled.length > MAX_SETTLED) {
        const idx = [...s.settled].reverse().findIndex((r) => !r.retry);
        if (idx === -1) break;
        s.settled.splice(s.settled.length - 1 - idx, 1);
        s.overflowDone++;
      }
    },

    updateOverall({ done, failed, total, loaded, bytesTotal, speedBps, etaSec, percent } = {}) {
      const o = s.overall;
      if (typeof percent === "number") o.percent = Math.min(100, Math.max(0, percent));
      o.done = done ?? o.done;
      o.failed = failed ?? o.failed;
      o.total = total ?? o.total;
      o.loaded = loaded ?? o.loaded;
      o.bytesTotal = bytesTotal ?? o.bytesTotal;
      o.speedBps = speedBps ?? o.speedBps;
      o.etaSec = etaSec ?? o.etaSec;
    },

    close() {
      s.visible = false;
      if (overlayState === s) overlayState = null;
    },
  };
}

export function getUploadOverlayState() {
  return overlayState;
}
