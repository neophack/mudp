import { state, api, toast, escapeHtml } from "../app.js";

export async function renderDisks() {
  $("#view").innerHTML = `<div class="card"><div class="card-body"><p class="hint">Loading disks...</p></div></div>`;
  try {
    state.disks = await api("/api/admin/disks");
  } catch (err) {
    $("#view").innerHTML = `<div class="card"><div class="card-body"><div class="error-box">${escapeHtml(err.message)}</div></div></div>`;
    return;
  }
  $("#view").innerHTML =
    `<div class="grid two">` +
      `<section class="stack">` +
        `<div class="card"><div class="card-head"><h2>Mount Disk</h2></div><div class="card-body"><form id="mountForm" class="compact">` +
          `<input name="source" placeholder="Source device or share">` +
          `<input name="target" placeholder="Target path">` +
          `<input name="fsType" placeholder="Filesystem type (optional)">` +
          `<button>Mount</button>` +
        `</form></div></div>` +
        `<div class="card"><div class="card-head"><h2>Backup</h2></div><div class="card-body"><form id="backupForm" class="compact">` +
          `<input name="targetDir" placeholder="Mounted disk backup directory">` +
          `<button>Backup Database</button>` +
        `</form></div></div>` +
      `</section>` +
      `<div class="card"><div class="card-head"><h2>Disks</h2></div>` +
      `<table class="data"><thead><tr><th>Name</th><th>Path</th><th>Total</th><th>Free</th><th>Used</th><th class="actions">Actions</th></tr></thead>` +
      `<tbody>${(state.disks || []).map(diskRow).join("") || `<tr class="empty-row"><td colspan="6">No disk data.</td></tr>`}</tbody></table></div>` +
    `</div>`;
  $("#mountForm").onsubmit = async (e) => {
    e.preventDefault();
    const payload = Object.fromEntries(new FormData(e.target));
    await api("/api/admin/disks/mount", { method: "POST", body: JSON.stringify(payload) });
    toast("Mount command completed", true);
    renderDisks();
  };
  $("#backupForm").onsubmit = async (e) => {
    e.preventDefault();
    const payload = Object.fromEntries(new FormData(e.target));
    const out = await api("/api/admin/backup", { method: "POST", body: JSON.stringify(payload) });
    toast(`Backup created: ${out.path}`, true);
  };
  document.querySelectorAll("[data-unmount]").forEach((btn) => {
    btn.onclick = async () => {
      if (!confirm(`Unmount ${btn.dataset.unmount}?`)) return;
      await api("/api/admin/disks/unmount", { method: "POST", body: JSON.stringify({ target: btn.dataset.unmount }) });
      toast("Unmount command completed", true);
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

function $(selector) {
  return document.querySelector(selector);
}
