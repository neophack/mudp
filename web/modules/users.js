// Users & Groups management: create users/groups, assign roles, group
// membership (Feishu approval flow), reset passwords, disable/delete accounts.

import { state, api, toast, escapeHtml, fmtBytes, refreshSection, renderView, displayName, $ } from "../app.js";
import { showModal, closeModal } from "./ui.js";

const ROLES = [
  { value: "user", label: "User", hint: "Workspace containers, GPU, quotas" },
  { value: "operator", label: "Operator", hint: "Full container/image/volume CRUD" },
  { value: "helpdesk", label: "Help Desk", hint: "View all + logs/exec, no mutations" },
  { value: "readonly", label: "Read-Only", hint: "View only" },
  { value: "admin", label: "Administrator", hint: "Full control + user management" },
];

export async function renderUsers() {
  // Prefer the usage rows the refresh engine already fetched for this tick
  // (state.netdiskUsageRows) so we don't double-fetch on every background
  // refresh. Fall back to a one-off fetch on first entry, when no rows are
  // cached yet. Keep the previous usage map until fresh data arrives so
  // background refreshes don't flash "Loading…" every tick.
  const prevUsage = state.netdiskUsage || null;
  state.netdiskUsage = prevUsage;
  if (state.netdiskUsageRows) {
    state.netdiskUsage = mapUsageById(state.netdiskUsageRows);
  } else {
    api("/api/admin/netdisk/usage")
      .then((rows) => {
        state.netdiskUsageRows = rows || [];
        state.netdiskUsage = mapUsageById(state.netdiskUsageRows);
        paintNetdiskUsage();
      })
      .catch(() => { if (!prevUsage) state.netdiskUsage = {}; });
  }

  $("#view").innerHTML =
    `<div class="grid two users-layout">` +
      `<section class="stack users-settings-col">` +
        `<div class="card"><div class="card-head"><h2>New Group</h2></div>` +
          `<div class="card-body"><form id="newGroup" class="compact">` +
            `<input name="name" placeholder="Group name, e.g. research" required>` +
            `<button>Create Group</button>` +
          `</form></div>` +
        `</div>` +
        `<div class="card"><div class="card-head"><h2>New User</h2></div>` +
          `<div class="card-body"><form id="newUser" class="compact">` +
            `<input name="username" placeholder="Username" required>` +
            `<input name="password" type="password" placeholder="Password" required>` +
            roleSelect("role", "user") +
            `<input name="containerCap" type="number" min="1" value="10" placeholder="Container limit">` +
            `<input name="netdiskQuotaGB" type="number" min="0" step="0.1" value="0" placeholder="Netdisk quota (GB, 0 = unlimited)">` +
            `<div class="check-grid">${groupChecks(state.groups)}</div>` +
            `<button>Create User</button>` +
          `</form></div>` +
        `</div>` +
        `<div class="card"><div class="card-head"><h2>Group Netdisk Paths</h2></div>` +
          `<table class="data"><thead><tr><th>Group</th><th>Path</th><th class="actions">Actions</th></tr></thead>` +
          `<tbody>${state.groups.map(groupPathRow).join("") || `<tr class="empty-row"><td colspan="3">No groups yet.</td></tr>`}</tbody></table>` +
        `</div>` +
        `<div class="card"><div class="card-head"><h2>Group Backup Paths</h2></div>` +
          `<p class="hint" style="padding:0 16px;margin:0 0 8px">Slower mechanical disks where netdisk files are mirrored. Backup is not quota-bound.</p>` +
          `<table class="data"><thead><tr><th>Group</th><th>Backup Path</th><th class="actions">Actions</th></tr></thead>` +
          `<tbody>${state.groups.map(groupBackupRow).join("") || `<tr class="empty-row"><td colspan="3">No groups yet.</td></tr>`}</tbody></table>` +
        `</div>` +
        `<div class="card"><div class="card-head"><h2>Group Languages</h2></div>` +
          `<p class="hint" style="padding:0 16px;margin:0 0 8px">Default language for new members of each group.</p>` +
          `<table class="data"><thead><tr><th>Group</th><th>Language</th><th class="actions">Actions</th></tr></thead>` +
          `<tbody>${state.groups.map(groupLanguageRow).join("") || `<tr class="empty-row"><td colspan="3">No groups yet.</td></tr>`}</tbody></table>` +
        `</div>` +
      `</section>` +
      `<section class="stack users-main-col">` +
        `<div class="card">` +
          `<div class="card-head"><h2>Users</h2></div>` +
          `<table class="data">` +
            `<thead><tr><th>User</th><th>Role</th><th>Groups</th><th>Ports</th><th class="actions">Actions</th></tr></thead>` +
            `<tbody>${state.users.map(userRow).join("") || `<tr class="empty-row"><td colspan="5">No users yet.</td></tr>`}</tbody>` +
          `</table>` +
        `</div>` +
        `<div class="card"><div class="card-head"><h2>Netdisk Usage</h2><span class="hint" id="netdiskUsageTotal"></span></div>` +
          `<table class="data netdisk-usage-table"><thead><tr><th>User</th><th class="netdisk-used-col">Used</th><th>Quota</th><th class="netdisk-bar-col"></th></tr></thead>` +
          `<tbody id="netdiskUsageBody">${prevUsage ? usageRowsHtml(prevUsage) : `<tr class="empty-row"><td colspan="4">Loading…</td></tr>`}</tbody></table>` +
        `</div>` +
      `</section>` +
    `</div>`;

  $("#newGroup").onsubmit = async (e) => {
    e.preventDefault();
    try {
      await api("/api/groups", {
        method: "POST",
        body: JSON.stringify(Object.fromEntries(new FormData(e.target))),
      });
      await refreshSection("users", "groups");
      renderView();
      toast("Group created", true);
    } catch (err) {
      toast(err.message);
    }
  };
  $("#newUser").onsubmit = async (e) => {
    e.preventDefault();
    const fd = new FormData(e.target);
    const payload = Object.fromEntries(fd);
    payload.containerCap = Number(payload.containerCap || 10);
    payload.netdiskQuotaBytes = Math.round(Number(payload.netdiskQuotaGB || 0) * 1024 * 1024 * 1024);
    payload.groupIds = [...e.target.querySelectorAll("input[name=groupIds]:checked")].map((i) => Number(i.value));
    if (payload.groupIds.length === 0) {
      const defaultGroup = state.groups.find((g) => g.name === "users");
      if (defaultGroup) payload.groupIds = [defaultGroup.id];
    }
    try {
      await api("/api/users", { method: "POST", body: JSON.stringify(payload) });
      await refreshSection("users", "groups");
      renderView();
      toast("User created", true);
    } catch (err) {
      toast(err.message);
    }
  };
  document.querySelectorAll("[data-edit-groups]").forEach((btn) => {
    btn.onclick = () => openUserGroups(btn.dataset.editGroups, btn.dataset.userName);
  });
  document.querySelectorAll("[data-edit-user]").forEach((btn) => {
    btn.onclick = () => openUserEdit(btn.dataset.editUser);
  });
  document.querySelectorAll("[data-approve-user]").forEach((btn) => {
    btn.onclick = () => approveUser(Number(btn.dataset.approveUser), btn.dataset.userName);
  });
  document.querySelectorAll("[data-deactivate-user]").forEach((btn) => {
    btn.onclick = () => deactivateUser(Number(btn.dataset.deactivateUser), btn.dataset.userName);
  });
  document.querySelectorAll("[data-delete-user]").forEach((btn) => {
    btn.onclick = () => deleteUser(Number(btn.dataset.deleteUser), btn.dataset.userName);
  });
  document.querySelectorAll("[data-group-path]").forEach((btn) => {
    btn.onclick = () => setGroupPath(Number(btn.dataset.groupPath), btn.dataset.groupName, btn.dataset.current || "");
  });
  document.querySelectorAll("[data-group-backup]").forEach((btn) => {
    btn.onclick = () => setGroupBackupPath(Number(btn.dataset.groupBackup), btn.dataset.groupName, btn.dataset.current || "");
  });
  document.querySelectorAll("[data-group-lang]").forEach((btn) => {
    btn.onclick = () => setGroupLanguage(Number(btn.dataset.groupLang), btn.dataset.groupName, btn.dataset.current || "");
  });
}

function userRow(user) {
  const isPending = (user.groups || []).length > 0 && (user.groups || []).every((g) => g === "pending");
  const roleBadge = roleBadgeFor(user.role, isPending);
  const disabled = user.disabled;
  return (
    `<tr class="${disabled ? "row-muted" : ""}">` +
      `<td><div class="primary-line">${escapeHtml(displayName(user))}${disabled ? ' <span class="badge badge-muted">disabled</span>' : ""}</div>` +
        `${user.feishuOpenId ? `<div class="secondary-line">Feishu · ${escapeHtml(user.username)}</div>` : ""}` +
        `<div class="secondary-line" style="margin-top:2px;">limit ${user.containerCap} · netdisk ${formatQuota(user.netdiskQuotaBytes)}</div></td>` +
      `<td>${roleBadge}</td>` +
      `<td><div class="secondary-line">${escapeHtml((user.groups || []).join(", ") || "None")}</div></td>` +
      `<td><div class="secondary-line">${user.portPrefix ? `${user.portPrefix * 100}-${user.portPrefix * 100 + 99}` : "Not assigned"}</div></td>` +
      `<td class="actions">` +
        `<button class="ghost" data-edit-groups="${user.id}" data-user-name="${escapeHtml(user.username)}">Groups</button>` +
        (isPending ? `<button class="ok" data-approve-user="${user.id}" data-user-name="${escapeHtml(user.username)}">Approve</button>` : "") +
        `<button class="ghost" data-edit-user="${user.id}">Edit</button>` +
        `<button class="warn" data-deactivate-user="${user.id}" data-user-name="${escapeHtml(user.username)}"${user.id === state.me.id ? " disabled" : ""}>Deactivate</button>` +
        `<button class="icon danger" title="Delete" data-delete-user="${user.id}" data-user-name="${escapeHtml(user.username)}"${user.id === state.me.id ? " disabled" : ""}>✕</button>` +
      `</td>` +
    `</tr>`
  );
}

function roleBadgeFor(role, isPending) {
  if (role === "admin") return `<span class="badge badge-accent">admin</span>`;
  if (isPending) return `<span class="badge badge-warn">pending</span>`;
  const map = { operator: "badge-ok", helpdesk: "badge-muted", readonly: "badge-muted", user: "badge-muted" };
  return `<span class="badge ${map[role] || "badge-muted"}">${escapeHtml(role)}</span>`;
}

function roleSelect(name, current) {
  const opts = ROLES.map(
    (r) => `<option value="${r.value}" ${r.value === current ? "selected" : ""}>${escapeHtml(r.label)} — ${escapeHtml(r.hint)}</option>`
  ).join("");
  return `<select name="${name}">${opts}</select>`;
}

function groupChecks(groups) {
  return (groups || [])
    .map((g) => `<label class="check"><input type="checkbox" name="groupIds" value="${g.id}"> ${escapeHtml(g.name)}</label>`)
    .join("") || '<span class="hint">No groups yet.</span>';
}

function groupPathRow(g) {
  return `<tr><td><div class="primary-line">${escapeHtml(g.name)}</div></td><td class="path-cell"><div class="secondary-line mono">${escapeHtml(g.netdiskPath || "Not configured")}</div></td><td class="actions"><button class="ghost" data-group-path="${g.id}" data-group-name="${escapeHtml(g.name)}" data-current="${escapeHtml(g.netdiskPath || "")}">Set Path</button></td></tr>`;
}

async function setGroupPath(groupId, name, current) {
  const path = prompt(`Netdisk root path for ${name}`, current || "");
  if (path === null) return;
  try {
    await api("/api/groups/netdisk", { method: "POST", body: JSON.stringify({ groupId, path }) });
    await refreshSection("users", "groups");
    renderView();
    toast("Netdisk path saved", true);
  } catch (err) {
    toast(err.message);
  }
}

function groupBackupRow(g) {
  return `<tr><td><div class="primary-line">${escapeHtml(g.name)}</div></td><td class="path-cell"><div class="secondary-line mono">${escapeHtml(g.backupPath || "Not configured")}</div></td><td class="actions"><button class="ghost" data-group-backup="${g.id}" data-group-name="${escapeHtml(g.name)}" data-current="${escapeHtml(g.backupPath || "")}">Set Backup Path</button></td></tr>`;
}

function groupLanguageRow(g) {
  const langName = g.language === "zh_CN" ? "中文" : (g.language === "en_US" ? "English" : "Not set");
  return `<tr><td><div class="primary-line">${escapeHtml(g.name)}</div></td><td><div class="secondary-line">${escapeHtml(langName)}</div></td><td class="actions"><button class="ghost" data-group-lang="${g.id}" data-group-name="${escapeHtml(g.name)}" data-current="${escapeHtml(g.language || "")}">Set Language</button></td></tr>`;
}

async function setGroupBackupPath(groupId, name, current) {
  const path = prompt(`Backup disk root path for ${name} (mechanical disk recommended)`, current || "");
  if (path === null) return;
  try {
    await api("/api/groups/backup", { method: "POST", body: JSON.stringify({ groupId, path }) });
    await refreshSection("users", "groups");
    renderView();
    toast("Backup path saved", true);
  } catch (err) {
    toast(err.message);
  }
}

async function setGroupLanguage(groupId, name, current) {
  // Create a simple dialog to select language
  const languages = [
    { value: "zh_CN", label: "中文" },
    { value: "en_US", label: "English" },
  ];
  
  let html = `<div style="padding: 16px"><p style="margin-bottom: 12px">Select default language for group <strong>${escapeHtml(name)}</strong>:</p>`;
  languages.forEach((lang) => {
    const checked = current === lang.value ? "checked" : "";
    html += `<label class="check"><input type="radio" name="language" value="${lang.value}" ${checked}> ${lang.label}</label>`;
  });
  html += `</div>`;
  
  showModal({
    kind: "confirm",
    title: `Set Language for ${name}`,
    body: html,
    foot: `<button class="primary" id="saveGroupLang">Save</button><button class="ghost" data-close>Cancel</button>`,
  });

  document.getElementById("saveGroupLang").onclick = async () => {
    const selected = document.querySelector('input[name="language"]:checked');
    if (!selected) return;
    try {
      await api("/api/admin/group/language", {
        method: "POST",
        body: JSON.stringify({ groupId, language: selected.value }),
      });
      await refreshSection("users", "groups");
      renderView();
      closeModal();
      toast("Group language saved", true);
    } catch (err) {
      toast(err.message);
    }
  };
}

function openUserGroups(userId, userName) {
  const user = state.users.find((u) => String(u.id) === String(userId)) || {};
  const checks = (state.groups || [])
    .map((g) => {
      const checked = (user.groups || []).includes(g.name) ? "checked" : "";
      return `<label class="check"><input type="checkbox" name="groupIds" value="${g.id}" ${checked}> ${escapeHtml(g.name)}${g.name === "pending" ? ' <span class="hint">(pending approval)</span>' : ""}</label>`;
    })
    .join("");
  showModal({
    kind: "usergroups",
    title: `Edit groups — ${escapeHtml(displayName(user) || userName)}`,
    body: `<form id="groupForm" class="compact"><div class="check-grid">${checks || '<span class="hint">No groups.</span>'}</div></form>`,
    foot: `<button class="ghost" data-close>Cancel</button><button class="primary" id="saveGroups">Save</button>`,
  });
  $("#saveGroups").onclick = async () => {
    const groupIds = [...$("#groupForm").querySelectorAll("input[name=groupIds]:checked")].map((i) => Number(i.value));
    try {
      await api("/api/users/groups", {
        method: "POST",
        body: JSON.stringify({ userId: Number(userId), groupIds }),
      });
      await refreshSection("users", "groups");
      renderView();
      closeModal();
      toast("Groups updated", true);
    } catch (err) {
      toast(err.message);
    }
  };
}

function openUserEdit(userId) {
  const user = state.users.find((u) => String(u.id) === String(userId));
  if (!user) return;
  const currentRole = ROLES.find((r) => r.value === user.role) ? user.role : "user";
  const checked = user.disabled ? "" : "checked";
  showModal({
    kind: "useredit",
    title: `Edit — ${escapeHtml(displayName(user))}`,
    body:
      `<form id="editUser" class="compact">` +
        `<label class="field-label">Role</label>` +
        roleSelect("role", currentRole) +
        `<label class="field-label">Container limit</label>` +
        `<input name="containerCap" type="number" min="1" value="${user.containerCap}">` +
        `<label class="field-label">Netdisk quota (GB, 0 = unlimited)</label>` +
        `<input name="netdiskQuotaGB" type="number" min="0" step="0.1" value="${(user.netdiskQuotaBytes / 1024 / 1024 / 1024).toFixed(2)}">` +
        `<label class="field-label">Port prefix</label>` +
        `<input name="portPrefix" type="number" min="100" max="655" value="${user.portPrefix || ""}" placeholder="100 => 10000-10099">` +
        `<label class="field-label">New password <span class="hint">(leave blank to keep)</span></label>` +
        `<input name="password" type="password" placeholder="Reset password" autocomplete="new-password">` +
        `<label class="check"><input type="checkbox" name="enabled" ${checked}> Account enabled</label>` +
      `</form>`,
    foot: `<button class="ghost" data-close>Cancel</button><button class="primary" id="saveUser">Save</button>`,
  });
  $("#saveUser").onclick = async () => {
    const fd = new FormData($("#editUser"));
    const portPrefixRaw = fd.get("portPrefix").trim();
    const payload = {
      id: Number(userId),
      role: fd.get("role"),
      containerCap: Number(fd.get("containerCap") || 10),
      netdiskQuotaBytes: Math.round(Number(fd.get("netdiskQuotaGB") || 0) * 1024 * 1024 * 1024),
      portPrefix: portPrefixRaw === "" ? null : Number(portPrefixRaw),
      password: fd.get("password") || "",
      disabled: !fd.has("enabled"),
    };
    try {
      await api("/api/users/update", { method: "POST", body: JSON.stringify(payload) });
      await refreshSection("users", "groups");
      renderView();
      closeModal();
      toast("User updated", true);
    } catch (err) {
      toast(err.message);
    }
  };
}

async function approveUser(userId, userName) {
  if (!confirm(`Approve user “${userName}” and move them to the default users group?`)) return;
  try {
    await api("/api/users/approve", { method: "POST", body: JSON.stringify({ userId }) });
    await refreshSection("users", "groups");
    renderView();
    toast("User approved", true);
  } catch (err) {
    toast(err.message);
  }
}

async function deactivateUser(userId, userName) {
  if (userId === state.me.id) {
    toast("You cannot deactivate your own account.");
    return;
  }
  if (!confirm(`Deactivate user “${userName}”? They will be returned to the pending group and need to be re-approved before they can use the platform.`)) return;
  try {
    await api("/api/users/deactivate", { method: "POST", body: JSON.stringify({ id: userId }) });
    await refreshSection("users", "groups");
    renderView();
    toast("User deactivated", true);
  } catch (err) {
    toast(err.message);
  }
}

async function deleteUser(userId, userName) {
  if (userId === state.me.id) {
    toast("You cannot delete your own account.");
    return;
  }
  if (!confirm(`Delete user “${userName}”? This removes their group memberships and stacks permanently.`)) return;
  try {
    await api("/api/users/delete", { method: "POST", body: JSON.stringify({ id: userId }) });
    await refreshSection("users", "groups");
    renderView();
    toast("User deleted", true);
  } catch (err) {
    toast(err.message);
  }
}

function formatQuota(bytes) {
  if (!bytes) return "unlimited";
  const gb = bytes / 1024 / 1024 / 1024;
  if (gb >= 1) return `${gb.toFixed(1)} GB`;
  const mb = bytes / 1024 / 1024;
  if (mb >= 1) return `${mb.toFixed(1)} MB`;
  return `${bytes} B`;
}

// ---------- Netdisk usage (admin) ----------

// mapUsageById indexes the usage rows by user id for O(1) lookup while painting.
function mapUsageById(rows) {
  const m = {};
  for (const r of rows) m[r.id] = r;
  return m;
}

// paintNetdiskUsage fills the dedicated usage card once /api/admin/netdisk/usage
// resolves. Called after the initial render so the table can paint progressively.
function paintNetdiskUsage() {
  const body = $("#netdiskUsageBody");
  const totalEl = $("#netdiskUsageTotal");
  if (!body) return; // user navigated away before usage loaded
  const usage = state.netdiskUsage || {};
  body.innerHTML = usageRowsHtml(usage);
  if (totalEl) {
    const total = Object.values(usage).reduce((s, r) => s + (r.usedBytes || 0), 0);
    totalEl.textContent = `Total used: ${fmtBytes(total)}`;
  }
}

// usageRowsHtml builds the sorted <tr> list for a given usage map. Shared by the
// initial render (when usage is already cached) and the post-fetch repaint.
// Sort by used bytes (desc) so the heaviest consumers appear at the top.
function usageRowsHtml(usage) {
  const map = usage || {};
  const rows = [...state.users]
    .sort((a, b) => (map[b.id]?.usedBytes || 0) - (map[a.id]?.usedBytes || 0))
    .map((u) => usageRow(u, map[u.id]));
  return rows.join("") || `<tr class="empty-row"><td colspan="4">No users.</td></tr>`;
}

function usageRow(user, info) {
  const name = escapeHtml(displayName(user) || user.username);
  const used = info ? info.usedBytes || 0 : 0;
  const quota = info ? info.quotaBytes || 0 : user.netdiskQuotaBytes || 0;
  // A user may legitimately have no netdisk path yet (group not configured, or
  // the on-disk folder has never been created). Surface that instead of "0 B".
  let status = "";
  if (info && !info.configured) status = ` <span class="badge badge-muted">no path</span>`;
  else if (info && info.pathMissing) status = ` <span class="badge badge-muted">not created</span>`;
  const pct = quota > 0 ? Math.min(100, Math.round((used / quota) * 100)) : 0;
  const barCls = pct >= 90 ? "danger" : pct >= 70 ? "warn" : "";
  const quotaText = quota > 0 ? `${pct}% of ${fmtBytes(quota)}` : "unlimited";
  return (
    `<tr>` +
      `<td>${name}${status}</td>` +
      `<td class="netdisk-used-col">${fmtBytes(used)}</td>` +
      `<td>${escapeHtml(quotaText)}</td>` +
      `<td class="netdisk-bar-col">${quota > 0 ? `<span class="quota-bar ${barCls}"><span style="width:${pct}%"></span></span>` : ""}</td>` +
    `</tr>`
  );
}


