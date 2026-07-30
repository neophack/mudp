// Shared multipart upload helper with real progress reporting. fetch() cannot
// surface upload progress (request streams are Chrome-only), so this wraps
// XMLHttpRequest, whose upload.onprogress fires with loaded/total byte counts.
// Also provides a small floating progress card attached to document.body so a
// background tab refresh (which re-renders #view) cannot wipe it mid-upload.

import { escapeHtml, fmtBytes } from "./common.js";

// uploadWithProgress POSTs formData to url and resolves with the parsed JSON
// body on 2xx; any other status rejects with the server's error message.
// onProgress receives { loaded, total, percent, speedBps } where speedBps is
// an exponentially-smoothed transfer rate derived between progress events.
export function uploadWithProgress(url, formData, { csrfToken = "", onProgress } = {}) {
  return new Promise((resolve, reject) => {
    const xhr = new XMLHttpRequest();
    xhr.open("POST", url);
    xhr.withCredentials = true;
    if (csrfToken) xhr.setRequestHeader("X-CSRF-Token", csrfToken);

    let lastTime = performance.now();
    let lastLoaded = 0;
    let speedBps = 0;
    xhr.upload.onprogress = (e) => {
      if (!e.lengthComputable || !onProgress) return;
      const now = performance.now();
      const dt = (now - lastTime) / 1000;
      if (dt > 0) {
        const inst = (e.loaded - lastLoaded) / dt;
        speedBps = speedBps > 0 ? speedBps * 0.7 + inst * 0.3 : inst;
        lastTime = now;
        lastLoaded = e.loaded;
      }
      onProgress({
        loaded: e.loaded,
        total: e.total,
        percent: e.total > 0 ? Math.min(100, Math.round((e.loaded / e.total) * 100)) : 0,
        speedBps,
      });
    };
    xhr.onload = () => {
      let data = {};
      try {
        data = JSON.parse(xhr.responseText || "{}");
      } catch {
        // Non-JSON error page (proxy/502); fall through to the status check.
      }
      if (xhr.status >= 200 && xhr.status < 300) resolve(data);
      else reject(new Error(data.error || `Upload failed (HTTP ${xhr.status})`));
    };
    xhr.onerror = () => reject(new Error("Upload failed: network error"));
    xhr.onabort = () => reject(new Error("Upload aborted"));
    xhr.send(formData);
  });
}

export function fmtSpeed(bps) {
  return `${fmtBytes(Math.max(0, Math.round(bps || 0)))}/s`;
}

// showUploadOverlay opens a floating progress card with an aggregate header and
// a BOUNDED per-file list. It never renders one row per uploaded file: only the
// handful of files currently on the wire plus a small ring buffer of the most
// recently finished ones. This is what keeps a multi-million-file folder upload
// from OOMing the page — the DOM node count is constant regardless of N.
//
// The earlier version eagerly built N rows via innerHTML + querySelectorAll,
// which at millions of files allocated a giant string and ~N DOM nodes and
// froze/crashed the tab. The bounded-window approach below mirrors how the
// Feishu/飞书 drive shows transfers: active files on top, a rolling summary,
// "… and N more done" for the overflow.
//
// Returns a controller:
//   addActive({name,size}) -> slot
//       claim a row for a file entering the wire. meta.name is the display
//       path, meta.size its byte length for the size label.
//   updateActive(slot, {loaded,total,percent,speedBps})
//       advance an active row's bar and status line.
//   settleActive(slot, "done"|"error", msg?)
//       retire an active row: it moves into the bounded "recently settled" area
//       and its slot is freed for reuse by the next file.
//   updateOverall({done,failed,total,loaded,bytesTotal,speedBps,etaSec,percent})
//       refresh the aggregate header (counts + overall bar + speed/ETA).
//   setLabel(text)           — replace the title text.
//   close()                  — remove the card.
// Only one overlay exists at a time; calling show again replaces it.
//
// Hard caps (kept tiny on purpose):
const MAX_ACTIVE = 16; // rows on the wire at once (≥ UPLOAD_BATCH_FILES so a
// batch never has to wait for a free slot)
const MAX_SETTLED = 24; // recently finished rows kept visible

export function showUploadOverlay() {
  hideUploadOverlay();
  const el = document.createElement("div");
  el.className = "upload-overlay";
  el.innerHTML =
    `<div class="upload-title">Uploading…</div>` +
    `<div class="bar upload-bar"><div class="bar-fill" style="width:0%"></div></div>` +
    `<div class="upload-meta">0 B / 0 B · 0 B/s</div>` +
    `<div class="upload-counts">0 / 0 · 0 done · 0 failed</div>` +
    `<div class="upload-file-list"></div>`;
  document.body.appendChild(el);

  const listEl = el.querySelector(".upload-file-list");
  const fill = el.querySelector(".upload-bar > .bar-fill");
  const meta = el.querySelector(".upload-meta");
  const counts = el.querySelector(".upload-counts");
  const title = el.querySelector(".upload-title");

  // DOM is partitioned into two fixed sub-containers so an active row never has
  // to be re-parented (which would restart its progress-bar transition):
  //   .upload-active   — files currently uploading (top, via CSS order 0)
  //   .upload-settled  — recently done/failed (bottom, via CSS order 2)
  // The overflow line ("… and N more done") sits between them and is the only
  // node that changes text as files stream past MAX_SETTLED.
  const activeWrap = document.createElement("div");
  activeWrap.className = "upload-active";
  const overflowLine = document.createElement("div");
  overflowLine.className = "upload-overflow";
  overflowLine.style.display = "none";
  const settledWrap = document.createElement("div");
  settledWrap.className = "upload-settled";
  listEl.append(activeWrap, overflowLine, settledWrap);

  const activeRows = new Map(); // slot -> row element
  let overflowDone = 0; // how many done rows were dropped off the settled list
  let freeSlots = []; // recycled row elements available for reuse

  function makeRow(name, size) {
    const row = document.createElement("div");
    row.className = "upload-file-row is-uploading";
    row.innerHTML =
      `<div class="upload-file-head">` +
        `<span class="upload-file-name" title="${escapeHtml(name)}">${escapeHtml(name)}</span>` +
        `<span class="upload-file-size">${fmtBytes(size || 0)}</span>` +
      `</div>` +
      `<div class="upload-file-bar"><div class="bar-fill" style="width:0%"></div></div>` +
      `<div class="upload-file-meta">` +
        `<span class="upload-file-status">Uploading…</span>` +
        // A per-row retry button, shown only on failed rows whose upload can be
        // re-attempted. Hidden by default; surfaced by markFailedWithRetry().
        `<button type="button" class="upload-file-retry" hidden>Retry</button>` +
      `</div>`;
    return row;
  }

  // refreshOverflow keeps the "… and N more done" line in sync with how many
  // completed rows were pushed out of the settled list.
  function refreshOverflow() {
    if (overflowDone > 0) {
      overflowLine.style.display = "";
      overflowLine.textContent = `… +${overflowDone} more`;
    } else {
      overflowLine.style.display = "none";
    }
  }

  // firstRecyclable returns the oldest settled row that is safe to drop for
  // reuse: anything without an armed retry button. Rows awaiting a user retry
  // are pinned so their button survives until acted upon.
  function firstRecyclable() {
    for (const child of settledWrap.children) {
      if (!child._retry) return child;
    }
    return null;
  }

  return {
    setLabel(text) { title.textContent = text; },

    addActive(meta) {
      const name = meta?.name ?? "file";
      const size = meta?.size ?? 0;
      // Reuse a recycled row when possible; otherwise build one — up to the
      // active cap. Past the cap (a caller over-subscribing the wire) the call
      // is a no-op so the DOM stays bounded; the caller should cap concurrent
      // files itself, this is just a hard backstop.
      let row = freeSlots.pop();
      if (!row) {
        if (activeRows.size >= MAX_ACTIVE) return null;
        row = makeRow(name, size);
      } else {
        row.querySelector(".upload-file-name").textContent = name;
        row.querySelector(".upload-file-name").title = name;
        row.querySelector(".upload-file-size").textContent = fmtBytes(size || 0);
        row.querySelector(".upload-file-bar > .bar-fill").style.width = "0%";
        row.querySelector(".upload-file-status").textContent = "Uploading…";
        row.className = "upload-file-row is-uploading";
        row._retry = null; // a recycled row loses any prior retry binding
        const rb = row.querySelector(".upload-file-retry");
        if (rb) { rb.hidden = true; rb.onclick = null; }
      }
      activeWrap.appendChild(row);
      // Keep newly-active rows at the top so the wire is always in view.
      listEl.scrollTop = 0;
      const slot = Symbol("slot");
      activeRows.set(slot, row);
      return slot;
    },

    updateActive(slot, p) {
      const row = activeRows.get(slot);
      if (!row) return;
      const pct = p?.percent ?? 0;
      row.querySelector(".upload-file-bar > .bar-fill").style.width = `${pct}%`;
      row.querySelector(".upload-file-status").textContent =
        `${fmtBytes(p?.loaded ?? 0)} · ${pct}% · ${fmtSpeed(p?.speedBps ?? 0)}`;
    },

    // markFailedWithRetry arms a Retry button on a row that failed and should be
    // re-uploadable. The row is pinned (not recycled) so the button stays alive
    // until clicked or the card closes. Clicking calls onRetry(), which the
    // caller uses to re-queue that file; it should then addActive() the file,
    // which recovers the row into the active area. Returns true if armed.
    markFailedWithRetry(slot, msg, onRetry) {
      let target = activeRows.get(slot);
      if (target) {
        // Was still in flight — retire it into the settled area first.
        activeRows.delete(slot);
        target.classList.remove("is-uploading", "is-done", "is-error");
        target.classList.add("is-error");
        settledWrap.appendChild(target);
      } else {
        // Find the pinned failed row by the slot we stamped on it.
        target = Array.from(settledWrap.children).find((r) => r._slot === slot);
      }
      if (!target) return false;
      target.classList.remove("is-uploading", "is-done");
      target.classList.add("is-error");
      const s = target.querySelector(".upload-file-status");
      if (s) s.textContent = msg || "Failed";
      const btn = target.querySelector(".upload-file-retry");
      if (btn) {
        btn.hidden = false;
        btn.onclick = (ev) => {
          ev.stopPropagation();
          // Re-arm as active-looking while the retry is in flight, then hand off.
          btn.hidden = true;
          if (typeof onRetry === "function") onRetry();
        };
      }
      target._slot = slot;
      target._retry = onRetry || null;
      return true;
    },

    // reactivate moves a settled (failed) row back into the active area and
    // resets it to "uploading", reusing the SAME slot. Used when a retry is in
    // flight so the user sees the bar animate again on the existing row instead
    // of a duplicate appearing. Safe on active rows too (no-op transition).
    reactivate(slot, meta) {
      let row = activeRows.get(slot);
      if (!row) {
        row = Array.from(settledWrap.children).find((r) => r._slot === slot);
        if (row) {
          activeRows.set(slot, row);
        }
      }
      if (!row) return false;
      const name = meta?.name ?? row.querySelector(".upload-file-name")?.textContent ?? "file";
      const size = meta?.size ?? 0;
      row.querySelector(".upload-file-name").textContent = name;
      row.querySelector(".upload-file-name").title = name;
      if (size) row.querySelector(".upload-file-size").textContent = fmtBytes(size);
      row.querySelector(".upload-file-bar > .bar-fill").style.width = "0%";
      row.querySelector(".upload-file-status").textContent = "Uploading…";
      const rb = row.querySelector(".upload-file-retry");
      if (rb) { rb.hidden = true; rb.onclick = null; }
      row._retry = null;
      row.classList.remove("is-uploading", "is-done", "is-error");
      row.classList.add("is-uploading");
      // Move to the active area and into view.
      activeWrap.appendChild(row);
      listEl.scrollTop = 0;
      return true;
    },

    settleActive(slot, status, msg) {
      const row = activeRows.get(slot);
      if (!row) return;
      activeRows.delete(slot);
      row.classList.remove("is-uploading", "is-done", "is-error");
      row.classList.add(status === "error" ? "is-error" : "is-done");
      const s = row.querySelector(".upload-file-status");
      if (status === "error") s.textContent = msg || "Failed";
      else s.textContent = "Done";
      row.querySelector(".upload-file-bar > .bar-fill").style.width = "100%";
      // A retryable failure is pinned (see settleActiveForRetry): its row is NOT
      // eligible for recycling so the Retry button stays clickable until the user
      // acts on it or the card closes. Plain done/error rows recycle as before.
      // Move the finished row into the settled ring. If the ring is full, retire
      // the oldest non-pinned row into the free pool (for reuse by the next
      // active row) and bump the overflow counter — never allocate a new node.
      while (settledWrap.childElementCount >= MAX_SETTLED) {
        const oldest = firstRecyclable();
        if (!oldest) break; // ring full of pinned retry rows: just append
        oldest.remove();
        oldest._retry = null;
        freeSlots.push(oldest);
        overflowDone++;
      }
      settledWrap.appendChild(row);
      refreshOverflow();
    },

    updateOverall({ done, failed, total, loaded, bytesTotal, speedBps, etaSec, percent } = {}) {
      if (typeof percent === "number") fill.style.width = `${Math.min(100, Math.max(0, percent))}%`;
      let text = `${fmtBytes(loaded ?? 0)}`;
      if (typeof bytesTotal === "number") text += ` / ${fmtBytes(bytesTotal)}`;
      text += ` · ${fmtSpeed(speedBps ?? 0)}`;
      if (typeof percent === "number") text += ` · ${Math.round(percent)}%`;
      if (etaSec && etaSec > 0 && (percent ?? 0) < 100) text += ` · ${fmtEta(etaSec)} left`;
      meta.textContent = text;
      // Counts line tolerates an unknown total ("—") for streamed uploads where
      // the folder is still being walked and N is not known up front.
      const totStr = typeof total === "number" ? total : "—";
      counts.textContent = `${done ?? 0} / ${totStr} · ${done ?? 0} done · ${failed ?? 0} failed`;
    },

    close() { el.remove(); },
  };
}

// fmtEta turns a remaining-seconds estimate into a short "1m 30s"-style label.
function fmtEta(sec) {
  if (!Number.isFinite(sec) || sec <= 0) return "";
  if (sec < 60) return `${Math.round(sec)}s`;
  const m = Math.floor(sec / 60);
  const s = Math.round(sec % 60);
  return s ? `${m}m ${s}s` : `${m}m`;
}

function hideUploadOverlay() {
  document.querySelectorAll(".upload-overlay").forEach((el) => el.remove());
}
