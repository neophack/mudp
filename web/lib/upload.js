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

// showUploadOverlay opens a floating progress card listing every file with
// its own row (name · size · progress · speed) plus an aggregate header.
// `entries` is a FileList/Array being uploaded, or a list of { file, relPath }
// objects (relPath carries the in-folder path for folder uploads). Returns a
// controller:
//   setLabel(text)                    — replace the header label
//   setStatus(index, status, msg?)    — "uploading" | "done" | "error"
//   updateFile(index, p)              — per-file progress {loaded,total,percent,speedBps}
//   updateOverall({percent,speedBps,etaSec}) — aggregate header metrics
//   close()                           — remove the card
// Only one overlay exists at a time; calling show again replaces it.
// Row ordering inside the file list, which is a flex column: files currently on
// the wire float to the top, the queue keeps its pick order below them, and
// settled files sink to the bottom. This uses the CSS order property rather
// than moving DOM nodes so a bar in flight never restarts its width transition.
const ROW_ORDER = { uploading: 0, pending: 1, done: 2, error: 2 };

export function showUploadOverlay(entries) {
  const list = [...(entries || [])];
  hideUploadOverlay();
  const el = document.createElement("div");
  el.className = "upload-overlay";
  const rows = list.map((entry, i) => {
    const f = entry?.file ?? entry; // accept both {file,relPath} and raw File
    const name = entry?.relPath || entry?.webkitRelativePath || f.name;
    return (
      `<div class="upload-file-row" data-idx="${i}" style="order:${ROW_ORDER.pending}">` +
        `<div class="upload-file-head">` +
          `<span class="upload-file-name" title="${escapeHtml(name)}">${escapeHtml(name)}</span>` +
          `<span class="upload-file-size">${fmtBytes(f.size)}</span>` +
        `</div>` +
        `<div class="upload-file-bar"><div class="bar-fill" style="width:0%"></div></div>` +
        `<div class="upload-file-meta"><span class="upload-file-status">Pending</span></div>` +
      `</div>`
    );
  }).join("");
  el.innerHTML =
    `<div class="upload-title">Uploading ${list.length} file(s)…</div>` +
    `<div class="bar upload-bar"><div class="bar-fill" style="width:0%"></div></div>` +
    `<div class="upload-meta">0 B / 0 B · 0 B/s</div>` +
    `<div class="upload-file-list">${rows}</div>`;
  document.body.appendChild(el);

  const rowEls = el.querySelectorAll(".upload-file-row");
  const listEl = el.querySelector(".upload-file-list");
  const fill = el.querySelector(".upload-bar > .bar-fill");
  const meta = el.querySelector(".upload-meta");
  const title = el.querySelector(".upload-title");

  return {
    setLabel(text) { title.textContent = text; },
    setStatus(index, status, msg) {
      const row = rowEls[index];
      if (!row) return;
      // setStatus("uploading") repeats on every progress tick; only react to an
      // actual transition so the list is not yanked back to the top while the
      // user is scrolling through a long queue.
      const wasUploading = row.classList.contains("is-uploading");
      row.classList.remove("is-uploading", "is-done", "is-error");
      row.classList.add(`is-${status}`);
      row.style.order = ROW_ORDER[status] ?? ROW_ORDER.pending;
      if (status === "uploading" && !wasUploading) listEl.scrollTop = 0;
      const s = row.querySelector(".upload-file-status");
      if (status === "done") s.textContent = "Done";
      else if (status === "error") s.textContent = msg || "Failed";
      else s.textContent = "Uploading…";
    },
    updateFile(index, p) {
      const row = rowEls[index];
      if (!row) return;
      row.querySelector(".upload-file-bar > .bar-fill").style.width = `${p.percent}%`;
      row.querySelector(".upload-file-status").textContent =
        `${fmtBytes(p.loaded)} · ${p.percent}% · ${fmtSpeed(p.speedBps)}`;
    },
    updateOverall({ percent, loaded, total, speedBps, etaSec }) {
      fill.style.width = `${percent}%`;
      let text = `${fmtBytes(loaded)} / ${fmtBytes(total)} · ${fmtSpeed(speedBps)} · ${percent}%`;
      if (etaSec && etaSec > 0 && percent < 100) text += ` · ${fmtEta(etaSec)} left`;
      meta.textContent = text;
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
