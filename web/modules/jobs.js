// Global background-jobs tracker. Long-running SSE operations (image pull/build,
// container create, stack up/down) register themselves here so users can see all
// active work in one place and cancel it manually from the header dropdown.

import { state } from "../app.js";
import { showModal, setModalBody } from "./ui.js";

const KIND_LABEL = {
  "image.pull": "Pull image",
  "image.build": "Build image",
  "image.import": "Import image",
  "stack.up": "Stack deploy",
  "stack.down": "Stack down",
  "container.create": "Create container",
};

const KIND_ICON = {
  "image.pull": `<path d="M21 12a9 9 0 1 1-6.219-8.56"/><path d="M21 3v9h-9"/>`,
  "image.build": `<path d="m2 22 8-14 4 8 5-9 3 7"/><circle cx="12" cy="4" r="2"/>`,
  "image.import": `<path d="M12 15V3m0 12-4-4m4 4 4-4"/><path d="M2 17l.621 2.485A2 2 0 0 0 4.561 21h14.878a2 2 0 0 0 1.94-1.515L22 17"/>`,
  "stack.up": `<path d="M12.83 2.18a2 2 0 0 0-1.66 0L2.6 6.08a1 1 0 0 0 0 1.83l8.58 3.91a2 2 0 0 0 1.66 0l8.58-3.9a1 1 0 0 0 0-1.83z"/><path d="M2 12a1 1 0 0 0 .58.91l8.6 3.91a2 2 0 0 0 1.65 0l8.58-3.9A1 1 0 0 0 22 12"/><path d="M2 17a1 1 0 0 0 .58.91l8.6 3.91a2 2 0 0 0 1.65 0l8.58-3.9A1 1 0 0 0 22 17"/>`,
  "stack.down": `<path d="M12.83 2.18a2 2 0 0 0-1.66 0L2.6 6.08a1 1 0 0 0 0 1.83l8.58 3.91a2 2 0 0 0 1.66 0l8.58-3.9a1 1 0 0 0 0-1.83z"/><path d="M2 12a1 1 0 0 0 .58.91l8.6 3.91a2 2 0 0 0 1.65 0l8.58-3.9A1 1 0 0 0 22 12"/><path d="M2 17a1 1 0 0 0 .58.91l8.6 3.91a2 2 0 0 0 1.65 0l8.58-3.9A1 1 0 0 0 22 17"/>`,
  "container.create": `<path d="M21 8a2 2 0 0 0-1-1.73l-7-4a2 2 0 0 0-2 0l-7 4A2 2 0 0 0 3 8v8a2 2 0 0 0 1 1.73l7 4a2 2 0 0 0 2 0l7-4A2 2 0 0 0 21 16Z"/><path d="m3.3 7 8.7 5 8.7-5"/>`,
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
  state.jobs = (state.jobs || []).filter((j) => j.active);
  updateBadge();
  refreshJobsModal();
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
  document.querySelectorAll("[data-job-cancel]").forEach((btn) => {
    btn.onclick = () => {
      const id = btn.dataset.jobCancel;
      cancelJob(id);
    };
  });
  document.querySelectorAll("[data-job-remove]").forEach((btn) => {
    btn.onclick = () => {
      const id = btn.dataset.jobRemove;
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
  const action = job.active
    ? `<button class="icon danger" title="Cancel job" data-job-cancel="${escapeHtml(job.id)}">✕</button>`
    : `<button class="icon" title="Remove from list" data-job-remove="${escapeHtml(job.id)}">🗑</button>`;
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

