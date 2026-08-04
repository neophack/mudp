// Global background-jobs tracker. Three kinds of jobs live here side by side:
//   - client-side SSE jobs (image pull/build, container create, stack up/down)
//     identified by ids like "job_…" and cancelled by aborting their fetch;
//   - server-side backup jobs (id prefix "backup_") that survive the browser
//     tab — polled from /api/backup/jobs and cancelled via /api/backup/jobs/cancel.
//   - server-side bulk file tasks (id prefix "task_": netdisk copy/move/
//     transfer/restore/upload, chunked or not) — polled from /api/tasks. These
//     only appear once they've run longer than the server's visibility
//     threshold (a few seconds), so routine fast copies never flash a row,
//     and they are NOT cancellable (no cancel endpoint exists for them): once
//     a poll stops reporting one, it's assumed finished and flipped to "done".

import { state, api } from "../app.js";
import { showModal, setModalBody } from "./ui.js";
import { syncCopyOverlayFromTasks, COPY_MOVE_TASK_KINDS } from "../lib/copyProgress.js";

const KIND_LABEL = {
  "image.pull": "Pull image",
  "image.build": "Build image",
  "image.import": "Import image",
  "stack.up": "Stack deploy",
  "stack.down": "Stack down",
  "container.create": "Create container",
  "backup.run": "Netdisk backup",
  "netdisk.copy": "Netdisk copy",
  "netdisk.move": "Netdisk move",
  "netdisk.upload": "Netdisk upload",
  "netdisk.upload.chunked": "Netdisk large-file upload",
  "netdisk.transfer": "Netdisk ⇄ backup transfer",
  "netdisk.restore": "Restore from backup",
};

const KIND_ICON = {
  "image.pull": `<path d="M21 12a9 9 0 1 1-6.219-8.56"/><path d="M21 3v9h-9"/>`,
  "image.build": `<path d="m2 22 8-14 4 8 5-9 3 7"/><circle cx="12" cy="4" r="2"/>`,
  "image.import": `<path d="M12 15V3m0 12-4-4m4 4 4-4"/><path d="M2 17l.621 2.485A2 2 0 0 0 4.561 21h14.878a2 2 0 0 0 1.94-1.515L22 17"/>`,
  "stack.up": `<path d="M12.83 2.18a2 2 0 0 0-1.66 0L2.6 6.08a1 1 0 0 0 0 1.83l8.58 3.91a2 2 0 0 0 1.66 0l8.58-3.9a1 1 0 0 0 0-1.83z"/><path d="M2 12a1 1 0 0 0 .58.91l8.6 3.91a2 2 0 0 0 1.65 0l8.58-3.9A1 1 0 0 0 22 12"/><path d="M2 17a1 1 0 0 0 .58.91l8.6 3.91a2 2 0 0 0 1.65 0l8.58-3.9A1 1 0 0 0 22 17"/>`,
  "stack.down": `<path d="M12.83 2.18a2 2 0 0 0-1.66 0L2.6 6.08a1 1 0 0 0 0 1.83l8.58 3.91a2 2 0 0 0 1.66 0l8.58-3.9a1 1 0 0 0 0-1.83z"/><path d="M2 12a1 1 0 0 0 .58.91l8.6 3.91a2 2 0 0 0 1.65 0l8.58-3.9A1 1 0 0 0 22 12"/><path d="M2 17a1 1 0 0 0 .58.91l8.6 3.91a2 2 0 0 0 1.65 0l8.58-3.9A1 1 0 0 0 22 17"/>`,
  "container.create": `<path d="M21 8a2 2 0 0 0-1-1.73l-7-4a2 2 0 0 0-2 0l-7 4A2 2 0 0 0 3 8v8a2 2 0 0 0 1 1.73l7 4a2 2 0 0 0 2 0l7-4A2 2 0 0 0 21 16Z"/><path d="m3.3 7 8.7 5 8.7-5"/>`,
  "backup.run": `<path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4"/><polyline points="7 10 12 15 17 10"/><line x1="12" y1="15" x2="12" y2="3"/>`,
  "netdisk.copy": `<rect x="9" y="9" width="13" height="13" rx="2"/><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/>`,
  "netdisk.move": `<path d="M5 12h14"/><path d="m12 5 7 7-7 7"/>`,
  "netdisk.upload": `<path d="M12 15V3m0 12-4-4m4 4 4-4"/><path d="M2 17l.621 2.485A2 2 0 0 0 4.561 21h14.878a2 2 0 0 0 1.94-1.515L22 17"/>`,
  "netdisk.upload.chunked": `<path d="M12 15V3m0 12-4-4m4 4 4-4"/><path d="M2 17l.621 2.485A2 2 0 0 0 4.561 21h14.878a2 2 0 0 0 1.94-1.515L22 17"/>`,
  "netdisk.transfer": `<path d="M17 3v18"/><path d="m21 7-4-4-4 4"/><path d="m13 17 4 4 4-4"/>`,
  "netdisk.restore": `<path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4"/><polyline points="7 10 12 15 17 10"/><line x1="12" y1="15" x2="12" y2="3"/>`,
};

function generateId() {
  return `job_${Date.now()}_${Math.random().toString(36).slice(2, 8)}`;
}

export function activeJobCount() {
  return (state.jobs || []).filter((j) => j.active).length;
}

function updateBadge() {
  const badge = document.querySelector(".jobs-button .jobs-badge");
  const btn = document.getElementById("jobsButton");
  const count = activeJobCount();
  if (badge) {
    badge.textContent = count > 99 ? "99+" : String(count);
    badge.style.display = count > 0 ? "flex" : "none";
  }
  if (btn) {
    btn.title = count > 0 ? `${count} background job${count === 1 ? "" : "s"}` : "Background jobs";
  }
}

export function registerJob({ kind, name }) {
  const controller = new AbortController();
  const id = generateId();
  const job = {
    id,
    kind,
    name: name || kind,
    status: "running",
    startedAt: Date.now(),
    logs: "",
    message: "Running…",
    controller,
    active: true,
  };
  state.jobs = state.jobs || [];
  state.jobs.push(job);
  updateBadge();
  refreshJobsModal();

  const setStatus = (status, message) => {
    job.status = status;
    if (message !== undefined) job.message = message;
    job.active = status === "running";
    updateBadge();
    refreshJobsModal();
  };

  return {
    id,
    signal: controller.signal,
    log(message) {
      const line = String(message || "");
      job.logs += line + "\n";
      job.message = line;
      refreshJobsModal();
    },
    setStatus,
    done(message) {
      setStatus("done", message || "Completed");
    },
    error(message) {
      setStatus("error", message || "Failed");
    },
    cancel() {
      controller.abort();
      setStatus("cancelled", "Cancelled");
    },
  };
}

export function cancelJob(id) {
  const job = (state.jobs || []).find((j) => j.id === id);
  if (!job) return;
  if (!job.active) return;
  // Bulk file tasks (netdisk copy/move/transfer/restore/upload) have no
  // cancel endpoint -- they're visibility-only. jobRow() already hides the
  // Stop button for these; this is just a defensive backstop.
  if (job.cancellable === false) return;
  // Server-side backup jobs are cancelled through their own endpoint; the next
  // poll tick will flip the status to "cancelled". Client-side SSE jobs are
  // cancelled by aborting the fetch stream.
  if (job.server) {
    api("/api/backup/jobs/cancel", { method: "POST", body: JSON.stringify({ id }) })
      .then(() => { /* status flips on next poll */ })
      .catch(() => { /* best-effort */ });
    return;
  }
  try {
    job.controller.abort();
  } catch {
    /* ignore */
  }
  job.status = "cancelled";
  job.message = "Cancelled by user";
  job.active = false;
  updateBadge();
  refreshJobsModal();
}

export function clearCompletedJobs() {
  // Keep running jobs (both client and server-side) so a clear mid-backup
  // doesn't drop a still-active server job from view.
  state.jobs = (state.jobs || []).filter((j) => j.active);
  updateBadge();
  refreshJobsModal();
}

// ---------- Server-side backup job sync ----------
//
// Backup jobs live on the server (they survive the browser tab). We poll
// /api/backup/jobs every few seconds and merge the server's view into state.jobs
// so the existing panel/render code shows them with no extra plumbing. Server
// jobs are tagged server:true and use the server's id verbatim (prefixed
// "backup_"), which is what cancelJob keys off to pick the cancel endpoint.

let backupPollTimer = null;

export function startBackupJobsPolling() {
  // One poller for the whole app lifetime; idempotent so repeated calls no-op.
  if (backupPollTimer) return;
  const tick = () => {
    api("/api/backup/jobs")
      .then((jobs) => mergeBackupJobs(jobs || []))
      .catch(() => { /* best-effort; keep the previous snapshot */ });
  };
  tick();
  backupPollTimer = setInterval(tick, 3000);
}

function mergeBackupJobs(serverJobs) {
  if (!Array.isArray(serverJobs)) return;
  // Replace the server-side slice of state.jobs with the fresh snapshot, keep
  // all client-side (SSE) jobs untouched.
  const clientJobs = (state.jobs || []).filter((j) => !j.server);
  const mapped = serverJobs.map((j) => mapServerJob(j));
  // Preserve the local "removed" intent: a server job the user dismissed via
  // 🗑 is tracked in dismissedBackupIds so it doesn't reappear next poll.
  const kept = mapped.filter((j) => !dismissedBackupIds.has(j.id));
  state.jobs = [...clientJobs, ...kept];
  updateBadge();
  refreshJobsModal();
}

const dismissedBackupIds = new Set();

function mapServerJob(j) {
  // Server jobs are "active" while running; everything else (done/error/
  // cancelled) is inactive so the badge counts only live work.
  const active = j.status === "running";
  // Compose a human message that includes progress % and byte counts when present.
  let message = j.message || "";
  if (active && j.total > 0) {
    const pct = j.progress || 0;
    message = `${message} (${pct}% · ${fmtBytes(j.done)}/${fmtBytes(j.total)})`;
  }
  return {
    id: j.id,
    kind: j.kind || "backup.run",
    name: j.name || "Backup",
    status: j.status,
    startedAt: j.startedAt ? Date.parse(j.startedAt) : Date.now(),
    message,
    active,
    server: true,
    progress: j.progress || 0,
    done: j.done || 0,
    total: j.total || 0,
  };
}

// ---------- Server-side bulk file task sync ----------
//
// Mirrors the backup-job sync above, but for the netdisk copy/move/transfer/
// restore/upload tasks tracked server-side in tasks.go. Two differences from
// backup jobs: /api/tasks only reports a task once it's been running past the
// server's visibility threshold (so routine fast copies never flash a row),
// and there is no terminal status -- a task simply stops being reported once
// it finishes, so one active in the last poll but missing from this one is
// assumed done. Not cancellable (no cancel endpoint exists for these), so
// jobRow() never renders a Stop button for them.

let taskPollTimer = null;

export function startTaskPolling() {
  // One poller for the whole app lifetime; idempotent so repeated calls no-op.
  if (taskPollTimer) return;
  const tick = () => {
    api("/api/tasks")
      .then((tasks) => mergeUserTasks(tasks || []))
      .catch(() => { /* best-effort; keep the previous snapshot */ });
  };
  tick();
  taskPollTimer = setInterval(tick, 3000);
}

function mergeUserTasks(serverTasks) {
  if (!Array.isArray(serverTasks)) return;
  // Drives the bottom-left copy/move progress overlay for operations this
  // tab didn't itself start -- most notably one still running after the page
  // was refreshed mid-copy, since a page load re-enters this poll loop but
  // loses whatever local promise was tracking the in-flight request.
  syncCopyOverlayFromTasks(serverTasks.filter((t) => COPY_MOVE_TASK_KINDS.has(t.kind)));
  const seen = new Set(serverTasks.map((t) => t.id));
  const kept = [];
  for (const j of state.jobs || []) {
    if (!j.taskSource || !j.active) {
      kept.push(j); // not ours, or already finished -- leave as-is
      continue;
    }
    if (!seen.has(j.id)) {
      // Was running as of the last poll, no longer reported: the server has
      // no terminal status for these, so this is the best signal available
      // that it finished.
      kept.push({ ...j, status: "done", message: "Completed", active: false });
    }
    // else: still active and still reported -- dropped here, replaced below
    // by the fresh entry built from this poll's data.
  }
  const mapped = serverTasks.map(mapServerTask);
  state.jobs = [...kept, ...mapped];
  updateBadge();
  refreshJobsModal();
}

function mapServerTask(t) {
  let message = t.message || "";
  const hasProgress = typeof t.progress === "number" && t.progress >= 0 && t.total > 0;
  if (hasProgress) {
    // A same-disk move (and, rarely, a copy whose size scan hit its own time
    // budget -- see pathSizeBefore in tasks.go) counts items, not bytes; the
    // server says which via t.unit so this never mislabels a plain "3/5" as
    // a byte size.
    const fmt = t.unit === "items" ? String : fmtBytes;
    message = `${message} (${t.progress}% · ${fmt(t.done)}/${fmt(t.total)})`.trim();
  }
  return {
    id: t.id,
    kind: t.kind,
    name: t.name || t.kind,
    status: "running",
    startedAt: t.startedAt ? Date.parse(t.startedAt) : Date.now(),
    message,
    active: true,
    taskSource: true,
    cancellable: false,
    progress: hasProgress ? t.progress : undefined,
    done: t.done || 0,
    total: t.total || 0,
  };
}

function fmtBytes(n) {
  if (!n) return "0 B";
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(0)} KB`;
  if (n < 1024 * 1024 * 1024) return `${(n / 1024 / 1024).toFixed(1)} MB`;
  return `${(n / 1024 / 1024 / 1024).toFixed(2)} GB`;
}

export function renderJobsButton() {
  const count = activeJobCount();
  return (
    `<button class="icon jobs-button" id="jobsButton" title="${count > 0 ? count + " background job" + (count === 1 ? "" : "s") : "Background jobs"}">` +
      `<svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">` +
        `<path d="M12 2v4"/><path d="m5 5 2.8 2.8"/><path d="m19 5-2.8 2.8"/><path d="M12 8a4 4 0 0 0-4 4v6h8v-6a4 4 0 0 0-4-4Z"/><path d="M8 22h8"/>` +
      `</svg>` +
      (count > 0 ? `<span class="jobs-badge">${count > 99 ? "99+" : count}</span>` : "") +
    `</button>`
  );
}

export function openJobsModal() {
  showModal({
    kind: "jobs",
    title: "Background jobs",
    body: renderJobsBody(),
    foot:
      `<button class="ghost" data-close>Close</button>` +
      (state.jobs?.some((j) => !j.active) ? `<button class="ghost" id="clearCompletedJobs">Clear completed</button>` : ""),
  });
  bindJobsModal();
}

function refreshJobsModal() {
  const backdrop = document.querySelector(".modal-backdrop.jobs-modal");
  if (!backdrop) return;
  setModalBody(renderJobsBody());
  const foot = backdrop.querySelector(".modal-foot");
  if (foot) {
    foot.innerHTML =
      `<button class="ghost" data-close>Close</button>` +
      (state.jobs?.some((j) => !j.active) ? `<button class="ghost" id="clearCompletedJobs">Clear completed</button>` : "");
  }
  bindJobsModal();
}

function bindJobsModal() {
  document.querySelectorAll("[data-job-stop]").forEach((btn) => {
    btn.onclick = () => {
      const id = btn.dataset.jobStop;
      cancelJob(id);
    };
  });
  document.querySelectorAll("[data-job-remove]").forEach((btn) => {
    btn.onclick = () => {
      const id = btn.dataset.jobRemove;
      // Server jobs would otherwise reappear on the next poll tick; remember the
      // dismissal so mergeBackupJobs filters them out.
      const job = (state.jobs || []).find((j) => j.id === id);
      if (job && job.server) dismissedBackupIds.add(id);
      state.jobs = (state.jobs || []).filter((j) => j.id !== id);
      updateBadge();
      refreshJobsModal();
    };
  });
  const clearBtn = document.getElementById("clearCompletedJobs");
  if (clearBtn) clearBtn.onclick = clearCompletedJobs;
}

function renderJobsBody() {
  const jobs = state.jobs || [];
  if (!jobs.length) {
    return `<div class="empty-state">No background jobs yet.</div>`;
  }
  return (
    `<div class="job-list">` +
      jobs
        .slice()
        .sort((a, b) => b.startedAt - a.startedAt)
        .map((job) => jobRow(job))
        .join("") +
    `</div>`
  );
}

function jobRow(job) {
  const label = KIND_LABEL[job.kind] || job.kind;
  const icon = KIND_ICON[job.kind] || KIND_ICON["container.create"];
  const elapsed = formatElapsed(Date.now() - job.startedAt);
  const statusClass =
    job.status === "running"
      ? "badge-warn"
      : job.status === "done"
      ? "badge-ok"
      : job.status === "error"
      ? "badge-danger"
      : "badge-muted";
  const statusText = job.status[0].toUpperCase() + job.status.slice(1);
  // Bulk file tasks have no cancel endpoint (visibility-only): show neither a
  // Stop button nor a false promise of one while they're running.
  const action = !job.active
    ? `<button class="icon" title="Remove from list" data-job-remove="${escapeHtml(job.id)}">🗑</button>`
    : job.cancellable === false
    ? ``
    : `<button class="warn" title="Stop job" data-job-stop="${escapeHtml(job.id)}">Stop</button>`;
  // Backup jobs report a 0..100 progress; render a thin bar under the message
  // so the user can watch a long backup advance without opening the row.
  const progressHtml =
    job.active && typeof job.progress === "number" && job.total > 0
      ? `<div class="job-progress"><div class="job-progress-fill" style="width:${Math.min(100, job.progress)}%"></div></div>`
      : ``;
  return (
    `<div class="job-item ${job.active ? "job-active" : "job-inactive"}" data-job-id="${escapeHtml(job.id)}">` +
      `<div class="job-icon"><svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">${icon}</svg></div>` +
      `<div class="job-body">` +
        `<div class="job-headline">` +
          `<span class="job-name" title="${escapeHtml(job.name)}">${escapeHtml(job.name)}</span>` +
          `<span class="badge ${statusClass}">${escapeHtml(statusText)}</span>` +
        `</div>` +
        `<div class="job-meta">` +
          `<span class="job-kind">${escapeHtml(label)}</span>` +
          `<span class="job-time">${escapeHtml(elapsed)}</span>` +
        `</div>` +
        `<div class="job-message hint">${escapeHtml(truncate(job.message || "", 140))}</div>` +
        progressHtml +
      `</div>` +
      `<div class="job-actions">${action}</div>` +
    `</div>`
  );
}

function formatElapsed(ms) {
  const totalSeconds = Math.floor(ms / 1000);
  if (totalSeconds < 60) return `${totalSeconds}s`;
  const minutes = Math.floor(totalSeconds / 60);
  const seconds = totalSeconds % 60;
  if (minutes < 60) return `${minutes}m ${seconds}s`;
  const hours = Math.floor(minutes / 60);
  const mins = minutes % 60;
  return `${hours}h ${mins}m`;
}

function truncate(text, max) {
  if (!text) return "—";
  if (text.length <= max) return text;
  return text.slice(0, max - 1) + "…";
}

function escapeHtml(v) {
  return String(v ?? "").replace(/[&<>"']/g, (m) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[m]));
}

