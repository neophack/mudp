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

import { reactive } from "vue";
import { api } from "@/api";
import { store } from "@/store";
import { syncCopyOverlayFromTasks, COPY_MOVE_TASK_KINDS } from "@/overlays";

// Job kind -> i18n key. Resolved when the panel renders so the labels follow
// the interface language like everything else.
export const KIND_LABEL = {
  "image.pull": "jobs.kindPull",
  "image.build": "jobs.kindBuild",
  "image.import": "jobs.kindImport",
  "stack.up": "jobs.kindStackUp",
  "stack.down": "jobs.kindStackDown",
  "container.create": "jobs.kindContainerCreate",
  "backup.run": "jobs.kindBackup",
  "netdisk.copy": "task.netdiskCopy",
  "netdisk.move": "task.netdiskMove",
  "netdisk.upload": "task.netdiskUpload",
  "netdisk.upload.chunked": "task.netdiskUploadChunked",
  "netdisk.transfer": "task.netdiskTransfer",
  "netdisk.restore": "task.netdiskRestore",
};

export function activeJobCount() {
  return (store.jobs || []).filter((j) => j.active).length;
}

function generateId() {
  return `job_${Date.now()}_${Math.random().toString(36).slice(2, 8)}`;
}

// registerJob tracks a client-side streaming operation. Returns a handle the
// caller feeds log/status updates through; cancel() aborts the fetch stream.
export function registerJob({ kind, name }) {
  const controller = new AbortController();
  const id = generateId();
  const job = reactive({
    id,
    kind,
    name: name || kind,
    status: "running",
    startedAt: Date.now(),
    logs: "",
    message: "Running…",
    controller,
    active: true,
  });
  store.jobs = store.jobs || [];
  store.jobs.push(job);

  const setStatus = (status, message) => {
    job.status = status;
    if (message !== undefined) job.message = message;
    job.active = status === "running";
  };

  return {
    id,
    signal: controller.signal,
    log(message) {
      const line = String(message || "");
      job.logs += line + "\n";
      job.message = line;
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
  const job = (store.jobs || []).find((j) => j.id === id);
  if (!job) return;
  if (!job.active) return;
  // Bulk file tasks (netdisk copy/move/transfer/restore/upload) have no
  // cancel endpoint -- they're visibility-only. This is a defensive backstop.
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
}

export function removeJob(id) {
  const job = (store.jobs || []).find((j) => j.id === id);
  // Server jobs would otherwise reappear on the next poll tick; remember the
  // dismissal so mergeBackupJobs filters them out.
  if (job && job.server) dismissedBackupIds.add(id);
  store.jobs = (store.jobs || []).filter((j) => j.id !== id);
}

export function clearCompletedJobs() {
  // Keep running jobs (both client and server-side) so a clear mid-backup
  // doesn't drop a still-active server job from view.
  store.jobs = (store.jobs || []).filter((j) => j.active);
}

// ---------- Server-side backup job sync ----------
//
// Backup jobs live on the server (they survive the browser tab). We poll
// /api/backup/jobs every few seconds and merge the server's view into
// store.jobs so the panel shows them with no extra plumbing. Server jobs are
// tagged server:true and use the server's id verbatim (prefixed "backup_"),
// which is what cancelJob keys off to pick the cancel endpoint.

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

const dismissedBackupIds = new Set();

function mergeBackupJobs(serverJobs) {
  if (!Array.isArray(serverJobs)) return;
  // Replace the server-side slice of store.jobs with the fresh snapshot, keep
  // all client-side (SSE) jobs untouched.
  const clientJobs = (store.jobs || []).filter((j) => !j.server);
  const mapped = serverJobs.map(mapServerJob);
  // Preserve the local "removed" intent: a server job the user dismissed is
  // tracked in dismissedBackupIds so it doesn't reappear next poll.
  const kept = mapped.filter((j) => !dismissedBackupIds.has(j.id));
  store.jobs = [...clientJobs, ...kept];
}

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
// restore/upload tasks tracked server-side. Two differences: /api/tasks only
// reports a task once it's been running past the server's visibility
// threshold (so routine fast copies never flash a row), and there is no
// terminal status -- a task simply stops being reported once it finishes, so
// one active in the last poll but missing from this one is assumed done. Not
// cancellable (no cancel endpoint exists for these).

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
  // was refreshed mid-copy.
  syncCopyOverlayFromTasks(serverTasks.filter((t) => COPY_MOVE_TASK_KINDS.has(t.kind)));
  const seen = new Set(serverTasks.map((t) => t.id));
  const kept = [];
  for (const j of store.jobs || []) {
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
  store.jobs = [...kept, ...mapped];
}

function mapServerTask(t) {
  let message = t.message || "";
  const hasProgress = typeof t.progress === "number" && t.progress >= 0 && t.total > 0;
  if (hasProgress) {
    // A same-disk move (and, rarely, a copy whose size scan hit its own time
    // budget) counts items, not bytes; the server says which via t.unit so
    // this never mislabels a plain "3/5" as a byte size.
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
