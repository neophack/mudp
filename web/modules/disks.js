import { state, api, toast, escapeHtml } from "../app.js";

export async function renderDisks() {
  // The refresh engine fetches /api/admin/disks into state.disks before calling
  // this; only fetch on first entry (no cached data yet) to avoid a redundant
  // request on every signature-driven repaint.
  if (!state.disks) {
    $("#view").innerHTML = `<div class="card"><div class="card-body"><p class="hint">Loading disks...</p></div></div>`;
    try {
      state.disks = await api("/api/admin/disks");
    } catch (err) {
      $("#view").innerHTML = `<div class="card"><div class="card-body"><div class="error-box">${escapeHtml(err.message)}</div></div></div>`;
      return;
    }
  }
  // Fetch the daily backup schedule once per entry (it's not part of the disks
  // section signature, so refresh it inline rather than via refreshSection).
  if (!state.backupSchedule) {
    try {
      state.backupSchedule = await api("/api/backup/schedule");
    } catch {
      state.backupSchedule = { enabled: false, hour: 2, minute: 0, lastRunAt: "" };
    }
  }
  if (!state.diskMountConfig) {
    try {
      state.diskMountConfig = await api("/api/admin/disks/config");
    } catch {
      state.diskMountConfig = { source: "", target: "", fsType: "", backupTargetDir: "" };
    }
  }
  $("#view").innerHTML =
    `<div class="grid two disks-layout">` +
      `<section class="stack disks-tools-col">` +
        `<div class="card"><div class="card-head"><h2>Mount Disk</h2></div><div class="card-body"><form id="mountForm" class="compact">` +
          `<input name="source" placeholder="Source device or share" value="${escapeHtml(state.diskMountConfig?.source || "")}">` +
          `<input name="target" placeholder="Target path" value="${escapeHtml(state.diskMountConfig?.target || "")}">` +
          `<input name="fsType" placeholder="Filesystem type (optional)" value="${escapeHtml(state.diskMountConfig?.fsType || "")}">` +
          `<div class="head-tools" style="justify-content:flex-start">` +
            `<button type="button" class="ghost" id="saveMountConfig">Save Config</button>` +
            `<button type="submit">Mount Now</button>` +
          `</div>` +
          `<p class="hint" style="margin:0">Source/Target/FSType are saved in the database and prefilled next time.</p>` +
        `</form></div></div>` +
        `<div class="card"><div class="card-head"><h2>Backup</h2></div><div class="card-body"><form id="backupForm" class="compact">` +
          `<input name="targetDir" placeholder="Mounted disk backup directory" value="${escapeHtml(state.diskMountConfig?.backupTargetDir || "")}">` +
          `<button>Backup Database</button>` +
          `<p class="hint" style="margin:0">Backup path is saved in DB. If empty, server falls back to saved config.</p>` +
        `</form></div></div>` +
      `</section>` +
      `<section class="stack disks-schedule-col">` +
        `<div class="card"><div class="card-head"><h2>Netdisk Backup Schedule</h2></div><div class="card-body">` +
          `<p class="hint" style="margin:0 0 10px">Back up every user's netdisk to their group's backup disk at a fixed daily time (e.g. 02:00). Runs server-side — survives browser tabs.</p>` +
          `<form id="backupScheduleForm" class="compact">` +
            `<label class="check"><input type="checkbox" id="bkSchedEnabled" ${state.backupSchedule?.enabled ? "checked" : ""}><span>Enabled</span></label>` +
            `<div class="bk-sched-time">` +
              `<label>Daily at</label>` +
              `<input id="bkSchedHour" type="number" min="0" max="23" value="${state.backupSchedule?.hour ?? 2}" title="Hour (0-23)">` +
              `<span>:</span>` +
              `<input id="bkSchedMinute" type="number" min="0" max="59" value="${state.backupSchedule?.minute ?? 0}" title="Minute (0-59)">` +
            `</div>` +
            `<button>Save Schedule</button>` +
          `</form>` +
          `<p class="hint" id="bkSchedStatus" style="margin:10px 0 0"></p>` +
          `<button class="ghost" id="bkRunNow" style="margin-top:10px">Back Up All Users Now</button>` +
        `</div></div>` +
      `</section>` +
      `<div class="card disks-table-card"><div class="card-head"><h2>Disks</h2></div>` +
      `<table class="data"><thead><tr><th>Name</th><th>Path</th><th>Total</th><th>Free</th><th>Used</th><th class="actions">Actions</th></tr></thead>` +
      `<tbody>${(state.disks || []).map(diskRow).join("") || `<tr class="empty-row"><td colspan="6">No disk data.</td></tr>`}</tbody></table></div>` +
    `</div>`;
  $("#mountForm").onsubmit = async (e) => {
    e.preventDefault();
    const payload = Object.fromEntries(new FormData(e.target));
    await api("/api/admin/disks/mount", { method: "POST", body: JSON.stringify(payload) });
    toast("Mount command completed", true);
    state.diskMountConfig = {
      ...(state.diskMountConfig || {}),
      source: payload.source || "",
      target: payload.target || "",
      fsType: payload.fsType || "",
    };
    // Force a fresh fetch so the just-mounted disk shows up immediately.
    state.disks = null;
    renderDisks();
  };
  $("#saveMountConfig").onclick = async () => {
    try {
      const fd = new FormData($("#mountForm"));
      const payload = {
        source: (fd.get("source") || "").toString(),
        target: (fd.get("target") || "").toString(),
        fsType: (fd.get("fsType") || "").toString(),
        backupTargetDir: state.diskMountConfig?.backupTargetDir || "",
      };
      await api("/api/admin/disks/config", { method: "POST", body: JSON.stringify(payload) });
      state.diskMountConfig = payload;
      toast("Mount config saved", true);
    } catch (err) {
      toast(err.message);
    }
  };
  $("#backupForm").onsubmit = async (e) => {
    e.preventDefault();
    const payload = Object.fromEntries(new FormData(e.target));
    const out = await api("/api/admin/backup", { method: "POST", body: JSON.stringify(payload) });
    state.diskMountConfig = {
      ...(state.diskMountConfig || {}),
      backupTargetDir: payload.targetDir || "",
    };
    await api("/api/admin/disks/config", {
      method: "POST",
      body: JSON.stringify({
        source: state.diskMountConfig?.source || "",
        target: state.diskMountConfig?.target || "",
        fsType: state.diskMountConfig?.fsType || "",
        backupTargetDir: state.diskMountConfig?.backupTargetDir || "",
      }),
    }).catch(() => {});
    toast(`Backup created: ${out.path}`, true);
  };
  $("#backupScheduleForm").onsubmit = async (e) => {
    e.preventDefault();
    const enabled = $("#bkSchedEnabled").checked;
    const hour = Math.max(0, Math.min(23, parseInt($("#bkSchedHour").value, 10) || 0));
    const minute = Math.max(0, Math.min(59, parseInt($("#bkSchedMinute").value, 10) || 0));
    try {
      await api("/api/backup/schedule", { method: "POST", body: JSON.stringify({ hour, minute, enabled }) });
      state.backupSchedule = { hour, minute, enabled, lastRunAt: state.backupSchedule?.lastRunAt || "" };
      updateBackupScheduleStatus();
      toast("Backup schedule saved", true);
    } catch (err) {
      toast(err.message);
    }
  };
  $("#bkRunNow").onclick = async () => {
    try {
      const out = await api("/api/netdisk/backup/all", { method: "POST" });
      toast(`Started backup for ${out.started || 0} user(s) — see Background jobs`, true);
    } catch (err) {
      toast(err.message);
    }
  };
  updateBackupScheduleStatus();
  document.querySelectorAll("[data-unmount]").forEach((btn) => {
    btn.onclick = async () => {
      if (!confirm(`Unmount ${btn.dataset.unmount}?`)) return;
      await api("/api/admin/disks/unmount", { method: "POST", body: JSON.stringify({ target: btn.dataset.unmount }) });
      toast("Unmount command completed", true);
      state.disks = null;
      renderDisks();
    };
  });
}

function diskRow(d) {
  return `<tr><td><div class="primary-line">${escapeHtml(d.name || "-")}</div></td><td class="mono">${escapeHtml(d.path)}</td><td>${fmtBytes(d.totalBytes)}</td><td>${fmtBytes(d.freeBytes)}</td><td><div class="bar"><div class="bar-fill" style="width:${Math.max(0, Math.min(100, d.usedPct || 0)).toFixed(0)}%"></div></div></td><td class="actions"><button class="ghost" data-unmount="${escapeHtml(d.path)}">Unmount</button></td></tr>`;
}

function fmtBytes(n) {
  if (!n) return "-";
  if (n < 1024 * 1024 * 1024) return `${(n / 1024 / 1024).toFixed(0)} MB`;
  return `${(n / 1024 / 1024 / 1024).toFixed(1)} GB`;
}

// updateBackupScheduleStatus writes the human-readable "next runs / last ran"
// line under the schedule form based on the cached state.backupSchedule.
function updateBackupScheduleStatus() {
  const el = $("#bkSchedStatus");
  if (!el) return;
  const s = state.backupSchedule;
  if (!s) {
    el.textContent = "";
    return;
  }
  const pad = (n) => String(n).padStart(2, "0");
  const time = `${pad(s.hour)}:${pad(s.minute)}`;
  const parts = [];
  parts.push(s.enabled ? `Enabled — runs daily at ${time}` : `Disabled (configure a time and enable to schedule)`);
  if (s.lastRunAt) {
    const d = new Date(s.lastRunAt);
    if (!Number.isNaN(d.getTime())) parts.push(`last ran ${d.toLocaleString()}`);
  }
  el.textContent = parts.join(" · ");
}

function $(selector) {
  return document.querySelector(selector);
}
