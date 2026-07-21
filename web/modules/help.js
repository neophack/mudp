import { isAdmin, canMutate } from "../app.js";

export function renderHelp() {
  const admin = isAdmin();
  const editable = canMutate();

  const userBasics = [
    "Open Netdisk to upload, preview, copy/move files, and download.",
    "Use Copy or Move and choose target disk in the left panel (Netdisk or Backup Disk).",
    "Use Containers to create/start/stop workloads, then open Terminal for in-container commands.",
    "Use Volumes and Networks for persistent data and service connectivity.",
    "Check Background jobs in the top bar to stop long-running tasks or clear completed ones.",
    "Use Notifications to read updates, delete one item, or clear all notifications.",
  ];

  const adminBasics = [
    "Users & Groups: create groups/users, assign roles, and maintain netdisk/backup paths.",
    "Disks: mount host disks, run DB backups, and configure daily netdisk backup schedule.",
    "Activity Log: review sensitive operations and troubleshooting timeline.",
    "Settings: configure registry mirrors and system integrations.",
  ];

  const quickTroubleshooting = [
    "Upload failed: verify netdisk quota and free disk space.",
    "Backup failed: verify group's backup path and available free space.",
    "Cannot share file: sharing is only supported on Netdisk, not Backup Disk.",
    "Container start failed: check image availability, port conflicts, and resource limits.",
  ];

  const tips = [
    editable
      ? "Your role can modify resources. Destructive actions still require confirmation dialogs."
      : "Your account is read-only for file/container mutations. You can still browse and inspect.",
    admin
      ? "As admin, review pending users regularly and keep group paths configured to avoid quota/path errors."
      : "If you need group/path/role changes, contact an administrator from Users & Groups management.",
  ];

  $("#view").innerHTML =
    `<div class="stack">` +
      `<div class="card"><div class="card-head"><h2>Getting Started</h2></div><div class="card-body">` +
        `<p class="hint" style="margin:0 0 10px">This page is a quick guide for both users and administrators to operate MUDP safely and efficiently.</p>` +
        `<div class="grid two">` +
          `<section class="stack">` +
            `<div class="card">` +
              `<div class="card-head"><h2>For All Users</h2></div>` +
              `<div class="card-body"><ol>${userBasics.map((x) => `<li>${escapeHtml(x)}</li>`).join("")}</ol></div>` +
            `</div>` +
            `<div class="card">` +
              `<div class="card-head"><h2>Tips For Your Account</h2></div>` +
              `<div class="card-body"><ul>${tips.map((x) => `<li>${escapeHtml(x)}</li>`).join("")}</ul></div>` +
            `</div>` +
          `</section>` +
          `<section class="stack">` +
            `<div class="card">` +
              `<div class="card-head"><h2>Troubleshooting</h2></div>` +
              `<div class="card-body"><ul>${quickTroubleshooting.map((x) => `<li>${escapeHtml(x)}</li>`).join("")}</ul></div>` +
            `</div>` +
            (admin
              ? `<div class="card">` +
                `<div class="card-head"><h2>Admin Quick Guide</h2></div>` +
                `<div class="card-body"><ol>${adminBasics.map((x) => `<li>${escapeHtml(x)}</li>`).join("")}</ol></div>` +
              `</div>`
              : `<div class="card"><div class="card-head"><h2>Need Admin Help?</h2></div><div class="card-body"><p class="hint" style="margin:0">If you need permission, path, or quota changes, ask an administrator to update Users &amp; Groups and Disks settings.</p></div></div>`
            ) +
          `</section>` +
        `</div>` +
      `</div></div>` +
    `</div>`;
}

function $(selector) {
  return document.querySelector(selector);
}

function escapeHtml(value) {
  return String(value || "")
    .replaceAll("&", "&amp;")
    .replaceAll("<", "&lt;")
    .replaceAll(">", "&gt;")
    .replaceAll('"', "&quot;")
    .replaceAll("'", "&#39;");
}
