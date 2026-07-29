import { state, api, toast, escapeHtml, t } from "../app.js";

export async function renderDisks() {
  // The refresh engine fetches /api/admin/disks into state.disks before calling
  // this; only fetch on first entry (no cached data yet) to avoid a redundant
  // request on every signature-driven repaint.
  if (!state.disks) {
    $("#view").innerHTML = `<div class="card"><div class="card-body"><p class="hint">${t("disks.loading")}</p></div></div>`;
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
        `<div class="card"><div class="card-head"><h2>${t("disks.mountDisk")}</h2></div><div class="card-body"><form id="mountForm" class="compact">` +
          `<input name="source" placeholder="${t("disks.sourcePlaceholder")}" value="${escapeHtml(state.diskMountConfig?.source || "")}">` +
          `<input name="target" placeholder="${t("disks.targetPlaceholder")}" value="${escapeHtml(state.diskMountConfig?.target || "")}">` +
          `<input name="fsType" placeholder="${t("disks.fsTypePlaceholder")}" value="${escapeHtml(state.diskMountConfig?.fsType || "")}">` +
          `<div class="head-tools" style="justify-content:flex-start">` +
            `<button type="button" class="ghost" id="saveMountConfig">${t("disks.saveConfig")}</button>` +
            `<button type="submit">${t("disks.mountNow")}</button>` +
          `</div>` +
          `<p class="hint" style="margin:0">${t("disks.mountHint")}</p>` +
        `</form></div></div>` +
        `<div class="card"><div class="card-head"><h2>${t("disks.backup")}</h2></div><div class="card-body"><form id="backupForm" class="compact">` +
          `<input name="targetDir" placeholder="${t("disks.backupDirPlaceholder")}" value="${escapeHtml(state.diskMountConfig?.backupTargetDir || "")}">` +
          `<button>${t("disks.backupDb")}</button>` +
          `<p class="hint" style="margin:0">${t("disks.backupHint")}</p>` +
        `</form></div></div>` +
      `</section>` +
      `<section class="stack disks-schedule-col">` +
        `<div class="card"><div class="card-head"><h2>${t("disks.netdiskBackupSchedule")}</h2></div><div class="card-body">` +
          `<p class="hint" style="margin:0 0 10px">${t("disks.scheduleHint")}</p>` +
          `<form id="backupScheduleForm" class="compact">` +
            `<label class="check"><input type="checkbox" id="bkSchedEnabled" ${state.backupSchedule?.enabled ? "checked" : ""}><span>${t("disks.enabled")}</span></label>` +
            `<div class="bk-sched-time">` +
              `<label>${t("disks.dailyAt")}</label>` +
              `<input id="bkSchedHour" type="number" min="0" max="23" value="${state.backupSchedule?.hour ?? 2}" title="${t("disks.hourTitle")}">` +
              `<span>:</span>` +
              `<input id="bkSchedMinute" type="number" min="0" max="59" value="${state.backupSchedule?.minute ?? 0}" title="${t("disks.minuteTitle")}">` +
            `</div>` +
            `<button>${t("disks.saveSchedule")}</button>` +
          `</form>` +
          `<p class="hint" id="bkSchedStatus" style="margin:10px 0 0"></p>` +
          `<button class="ghost" id="bkRunNow" style="margin-top:10px">${t("disks.backupAllNow")}</button>` +
        `</div></div>` +
      `</section>` +
      `<div class="card disks-table-card"><div class="card-head"><h2>${t("disks.disks")}</h2></div>` +
      `<table class="data"><thead><tr><th>${t("common.name")}</th><th>${t("users.colPath")}</th><th>${t("disks.colTotal")}</th><th>${t("disks.colFree")}</th><th>${t("disks.colUsed")}</th><th class="actions">${t("common.actions")}</th></tr></thead>` +
      `<tbody>${(state.disks || []).map(diskRow).join("") || `<tr class="empty-row"><td colspan="6">${t("disks.noDiskData")}</td></tr>`}</tbody></table></div>` +
    `</div>`;
  $("#mountForm").onsubmit = async (e) => {
    e.preventDefault();
    const payload = Object.fromEntries(new FormData(e.target));
    await api("/api/admin/disks/mount", { method: "POST", body: JSON.stringify(payload) });
    toast(t("disks.mountDone"), true);
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
      toast(t("disks.mountConfigSaved"), true);
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
    toast(t("disks.backupCreated", { path: out.path }), true);
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
      toast(t("disks.scheduleSaved"), true);
    } catch (err) {
      toast(err.message);
    }
  };
  $("#bkRunNow").onclick = async () => {
    try {
      const out = await api("/api/netdisk/backup/all", { method: "POST" });
      toast(t("disks.backupStarted", { n: out.started || 0 }), true);
    } catch (err) {
      toast(err.message);
    }
  };
  updateBackupScheduleStatus();
  document.querySelectorAll("[data-unmount]").forEach((btn) => {
    btn.onclick = async () => {
      if (!confirm(t("disks.unmountConfirm", { target: btn.dataset.unmount }))) return;
      await api("/api/admin/disks/unmount", { method: "POST", body: JSON.stringify({ target: btn.dataset.unmount }) });
      toast(t("disks.unmountDone"), true);
      state.disks = null;
      renderDisks();
    };
  });
}

function diskRow(d) {
  return `<tr><td><div class="primary-line">${escapeHtml(d.name || "-")}</div></td><td class="mono">${escapeHtml(d.path)}</td><td>${fmtBytes(d.totalBytes)}</td><td>${fmtBytes(d.freeBytes)}</td><td><div class="bar"><div class="bar-fill" style="width:${Math.max(0, Math.min(100, d.usedPct || 0)).toFixed(0)}%"></div></div></td><td class="actions"><button class="ghost" data-unmount="${escapeHtml(d.path)}">${t("disks.unmount")}</button></td></tr>`;
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
  parts.push(s.enabled ? t("disks.schedEnabled", { time }) : t("disks.schedDisabled"));
  if (s.lastRunAt) {
    const d = new Date(s.lastRunAt);
    if (!Number.isNaN(d.getTime())) parts.push(t("disks.lastRan", { when: d.toLocaleString() }));
  }
  el.textContent = parts.join(" · ");
}

function $(selector) {
  return document.querySelector(selector);
}
