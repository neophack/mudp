// Users & Groups management: create users/groups, assign roles, group
// membership (Feishu approval flow), reset passwords, disable/delete accounts.

import { state, api, toast, escapeHtml, fmtBytes, refreshSection, renderView, displayName, displayNameForUsername, $, t } from "../app.js";
import { showModal, closeModal } from "./ui.js";

function ROLES() {
  return [
    { value: "user", label: t("users.roleUser"), hint: t("users.roleUserHint") },
    { value: "operator", label: t("users.roleOperator"), hint: t("users.roleOperatorHint") },
    { value: "helpdesk", label: t("users.roleHelpdesk"), hint: t("users.roleHelpdeskHint") },
    { value: "readonly", label: t("users.roleReadonly"), hint: t("users.roleReadonlyHint") },
    { value: "admin", label: t("users.roleAdmin"), hint: t("users.roleAdminHint") },
  ];
}

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
  // Same fetch-if-missing pattern for the long-running-task card: the 15s
  // background refresh (usersLoader) keeps it warm afterwards, but the first
  // time the tab is opened nothing has fetched it yet.
  if (!state.adminTasks) {
    api("/api/admin/tasks")
      .then((rows) => {
        state.adminTasks = rows || [];
        paintAdminTasks();
      })
      .catch(() => { if (!state.adminTasks) state.adminTasks = []; });
  }

  $("#view").innerHTML =
    `<div class="grid two users-layout">` +
      `<section class="stack users-settings-col">` +
        `<div class="card"><div class="card-head"><h2>${t("users.newGroup")}</h2></div>` +
          `<div class="card-body"><form id="newGroup" class="compact">` +
            `<input name="name" placeholder="${t("users.groupNamePlaceholder")}" required>` +
            `<button>${t("users.createGroup")}</button>` +
          `</form></div>` +
        `</div>` +
        `<div class="card"><div class="card-head"><h2>${t("users.newUser")}</h2></div>` +
          `<div class="card-body"><form id="newUser" class="compact">` +
            `<input name="username" placeholder="${t("users.usernamePlaceholder")}" required>` +
            `<input name="password" type="password" placeholder="${t("users.passwordPlaceholder")}" required>` +
            roleSelect("role", "user") +
            `<input name="containerCap" type="number" min="1" value="10" placeholder="${t("users.containerLimitPlaceholder")}">` +
            `<input name="netdiskQuotaGB" type="number" min="0" step="0.1" value="0" placeholder="${t("users.netdiskQuotaPlaceholder")}">` +
            `<div class="check-grid">${groupChecks(state.groups)}</div>` +
            `<button>${t("users.createUser")}</button>` +
          `</form></div>` +
        `</div>` +
        `<div class="card"><div class="card-head"><h2>${t("users.groupPaths")}</h2></div>` +
          `<table class="data"><thead><tr><th>${t("users.colGroup")}</th><th>${t("users.colPath")}</th><th class="actions">${t("common.actions")}</th></tr></thead>` +
          `<tbody>${state.groups.map(groupPathRow).join("") || `<tr class="empty-row"><td colspan="3">${t("users.noGroups")}</td></tr>`}</tbody></table>` +
        `</div>` +
        `<div class="card"><div class="card-head"><h2>${t("users.groupBackupPaths")}</h2></div>` +
          `<p class="hint" style="padding:0 16px;margin:0 0 8px">${t("users.backupHint")}</p>` +
          `<table class="data"><thead><tr><th>${t("users.colGroup")}</th><th>${t("users.colBackupPath")}</th><th class="actions">${t("common.actions")}</th></tr></thead>` +
          `<tbody>${state.groups.map(groupBackupRow).join("") || `<tr class="empty-row"><td colspan="3">${t("users.noGroups")}</td></tr>`}</tbody></table>` +
        `</div>` +
        `<div class="card"><div class="card-head"><h2>${t("users.groupSharedDiskPaths")}</h2></div>` +
          `<p class="hint" style="padding:0 16px;margin:0 0 8px">${t("users.sharedDiskHint")}</p>` +
          `<table class="data"><thead><tr><th>${t("users.colGroup")}</th><th>${t("users.colSharedDiskPath")}</th><th class="actions">${t("common.actions")}</th></tr></thead>` +
          `<tbody>${state.groups.map(groupSharedDiskRow).join("") || `<tr class="empty-row"><td colspan="3">${t("users.noGroups")}</td></tr>`}</tbody></table>` +
        `</div>` +
        `<div class="card"><div class="card-head"><h2>${t("users.groupLanguages")}</h2></div>` +
          `<p class="hint" style="padding:0 16px;margin:0 0 8px">${t("users.groupLangHint")}</p>` +
          `<table class="data"><thead><tr><th>${t("users.colGroup")}</th><th>${t("users.colLanguage")}</th><th class="actions">${t("common.actions")}</th></tr></thead>` +
          `<tbody>${state.groups.map(groupLanguageRow).join("") || `<tr class="empty-row"><td colspan="3">${t("users.noGroups")}</td></tr>`}</tbody></table>` +
        `</div>` +
      `</section>` +
      `<section class="stack users-main-col">` +
        `<div class="card">` +
          `<div class="card-head"><h2>${t("users.users")}</h2></div>` +
          `<table class="data">` +
            `<thead><tr><th>${t("common.user")}</th><th>${t("users.colRole")}</th><th>${t("users.colGroups")}</th><th>${t("users.colPorts")}</th><th class="actions">${t("common.actions")}</th></tr></thead>` +
            `<tbody>${state.users.map(userRow).join("") || `<tr class="empty-row"><td colspan="5">${t("users.noUsers")}</td></tr>`}</tbody>` +
          `</table>` +
        `</div>` +
        `<div class="card"><div class="card-head"><h2>${t("users.netdiskUsage")}</h2><span class="hint" id="netdiskUsageTotal"></span></div>` +
          `<table class="data netdisk-usage-table"><thead><tr><th>${t("common.user")}</th><th class="netdisk-used-col">${t("netdisk.usedCol")}</th><th>${t("users.colQuota")}</th><th class="netdisk-bar-col"></th></tr></thead>` +
          `<tbody id="netdiskUsageBody">${prevUsage ? usageRowsHtml(prevUsage) : `<tr class="empty-row"><td colspan="4">${t("users.loadingDots")}</td></tr>`}</tbody></table>` +
        `</div>` +
        `<div class="card"><div class="card-head"><h2>${t("users.longTasks")}</h2></div>` +
          `<p class="hint" style="padding:0 16px;margin:0 0 8px">${t("users.longTasksHint")}</p>` +
          `<div id="adminTasksBody">${adminTasksHtml(state.adminTasks)}</div>` +
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
      toast(t("users.groupCreated"), true);
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
      toast(t("users.userCreated"), true);
    } catch (err) {
      if (err.message === "user capacity full") {
        toast(t("users.capacityFull"));
      } else {
        toast(err.message);
      }
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
  document.querySelectorAll("[data-group-shareddisk]").forEach((btn) => {
    btn.onclick = () => setGroupSharedDiskPath(Number(btn.dataset.groupShareddisk), btn.dataset.groupName, btn.dataset.current || "");
  });
}

function userRow(user) {
  const isPending = (user.groups || []).length > 0 && (user.groups || []).every((g) => g === "pending");
  const roleBadge = roleBadgeFor(user.role, isPending);
  const disabled = user.disabled;
  return (
    `<tr class="${disabled ? "row-muted" : ""}">` +
      `<td><div class="primary-line">${escapeHtml(displayName(user))}${disabled ? ` <span class="badge badge-muted">${t("users.roleDisabled")}</span>` : ""}</div>` +
        `${user.feishuOpenId ? `<div class="secondary-line">${t("users.feishuLine", { name: escapeHtml(user.username) })}</div>` : ""}` +
        `<div class="secondary-line" style="margin-top:2px;">${t("users.limitLine", { cap: user.containerCap, quota: formatQuota(user.netdiskQuotaBytes) })}</div></td>` +
      `<td>${roleBadge}</td>` +
      `<td><div class="secondary-line">${escapeHtml((user.groups || []).join(", ") || t("users.groupsNone"))}</div></td>` +
      `<td><div class="secondary-line">${user.portPrefix ? `${user.portPrefix * 100}-${user.portPrefix * 100 + 99}` : t("users.portsNotAssigned")}</div></td>` +
      `<td class="actions">` +
        `<button class="ghost" data-edit-groups="${user.id}" data-user-name="${escapeHtml(user.username)}">${t("networks.groups")}</button>` +
        (isPending ? `<button class="ok" data-approve-user="${user.id}" data-user-name="${escapeHtml(user.username)}">${t("users.approve")}</button>` : "") +
        `<button class="ghost" data-edit-user="${user.id}">${t("common.edit")}</button>` +
        `<button class="warn" data-deactivate-user="${user.id}" data-user-name="${escapeHtml(user.username)}"${user.id === state.me.id ? " disabled" : ""}>${t("users.deactivate")}</button>` +
        `<button class="icon danger" title="${t("common.delete")}" data-delete-user="${user.id}" data-user-name="${escapeHtml(user.username)}"${user.id === state.me.id ? " disabled" : ""}>✕</button>` +
      `</td>` +
    `</tr>`
  );
}

function roleBadgeFor(role, isPending) {
  if (role === "admin") return `<span class="badge badge-accent">${t("users.roleAdminBadge")}</span>`;
  if (isPending) return `<span class="badge badge-warn">${t("users.rolePending")}</span>`;
  const map = { operator: "badge-ok", helpdesk: "badge-muted", readonly: "badge-muted", user: "badge-muted" };
  return `<span class="badge ${map[role] || "badge-muted"}">${escapeHtml(role)}</span>`;
}

function roleSelect(name, current) {
  const opts = ROLES().map(
    (r) => `<option value="${r.value}" ${r.value === current ? "selected" : ""}>${escapeHtml(r.label)} — ${escapeHtml(r.hint)}</option>`
  ).join("");
  return `<select name="${name}">${opts}</select>`;
}

function groupChecks(groups) {
  return (groups || [])
    .map((g) => `<label class="check"><input type="checkbox" name="groupIds" value="${g.id}"> ${escapeHtml(g.name)}</label>`)
    .join("") || `<span class="hint">${t("networks.noGroupsYet")}</span>`;
}

function groupPathRow(g) {
  return `<tr><td><div class="primary-line">${escapeHtml(g.name)}</div></td><td class="path-cell"><div class="secondary-line mono">${escapeHtml(g.netdiskPath || t("users.notConfigured"))}</div></td><td class="actions"><button class="ghost" data-group-path="${g.id}" data-group-name="${escapeHtml(g.name)}" data-current="${escapeHtml(g.netdiskPath || "")}">${t("users.setPath")}</button></td></tr>`;
}

async function setGroupPath(groupId, name, current) {
  const path = prompt(t("users.netdiskPathPrompt", { name }), current || "");
  if (path === null) return;
  try {
    await api("/api/groups/netdisk", { method: "POST", body: JSON.stringify({ groupId, path }) });
    await refreshSection("users", "groups");
    renderView();
    toast(t("users.netdiskPathSaved"), true);
  } catch (err) {
    toast(err.message);
  }
}

function groupBackupRow(g) {
  return `<tr><td><div class="primary-line">${escapeHtml(g.name)}</div></td><td class="path-cell"><div class="secondary-line mono">${escapeHtml(g.backupPath || t("users.notConfigured"))}</div></td><td class="actions"><button class="ghost" data-group-backup="${g.id}" data-group-name="${escapeHtml(g.name)}" data-current="${escapeHtml(g.backupPath || "")}">${t("users.setBackupPath")}</button></td></tr>`;
}

function groupSharedDiskRow(g) {
  return `<tr><td><div class="primary-line">${escapeHtml(g.name)}</div></td><td class="path-cell"><div class="secondary-line mono">${escapeHtml(g.sharedDiskPath || t("users.notConfigured"))}</div></td><td class="actions"><button class="ghost" data-group-shareddisk="${g.id}" data-group-name="${escapeHtml(g.name)}" data-current="${escapeHtml(g.sharedDiskPath || "")}">${t("users.setSharedDiskPath")}</button></td></tr>`;
}

async function setGroupSharedDiskPath(groupId, name, current) {
  const path = prompt(t("users.sharedDiskPathPrompt", { name }), current || "");
  if (path === null) return;
  try {
    await api("/api/groups/shareddisk", { method: "POST", body: JSON.stringify({ groupId, path }) });
    await refreshSection("users", "groups");
    renderView();
    toast(t("users.sharedDiskPathSaved"), true);
  } catch (err) {
    toast(err.message);
  }
}

function groupLanguageRow(g) {
  const langName = g.language === "zh_CN" ? "中文" : (g.language === "en_US" ? "English" : t("users.notSet"));
  return `<tr><td><div class="primary-line">${escapeHtml(g.name)}</div></td><td><div class="secondary-line">${escapeHtml(langName)}</div></td><td class="actions"><button class="ghost" data-group-lang="${g.id}" data-group-name="${escapeHtml(g.name)}" data-current="${escapeHtml(g.language || "")}">${t("users.setLanguage")}</button></td></tr>`;
}

async function setGroupBackupPath(groupId, name, current) {
  const path = prompt(t("users.backupPathPrompt", { name }), current || "");
  if (path === null) return;
  try {
    await api("/api/groups/backup", { method: "POST", body: JSON.stringify({ groupId, path }) });
    await refreshSection("users", "groups");
    renderView();
    toast(t("users.backupPathSaved"), true);
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
  
  let html = `<div style="padding: 16px"><p style="margin-bottom: 12px">${t("users.groupLangPrompt", { name: escapeHtml(name) })}</p>`;
  languages.forEach((lang) => {
    const checked = current === lang.value ? "checked" : "";
    html += `<label class="check"><input type="radio" name="language" value="${lang.value}" ${checked}> ${lang.label}</label>`;
  });
  html += `</div>`;

  showModal({
    kind: "confirm",
    title: t("users.groupLangTitle", { name }),
    body: html,
    foot: `<button class="primary" id="saveGroupLang">${t("common.save")}</button><button class="ghost" data-close>${t("common.cancel")}</button>`,
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
      toast(t("users.groupLangSaved"), true);
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
      return `<label class="check"><input type="checkbox" name="groupIds" value="${g.id}" ${checked}> ${escapeHtml(g.name)}${g.name === "pending" ? ` <span class="hint">${t("users.pendingApproval")}</span>` : ""}</label>`;
    })
    .join("");
  showModal({
    kind: "usergroups",
    title: t("users.editGroupsTitle", { name: escapeHtml(displayName(user) || userName) }),
    body: `<form id="groupForm" class="compact"><div class="check-grid">${checks || `<span class="hint">${t("users.noGroupsShort")}</span>`}</div></form>`,
    foot: `<button class="ghost" data-close>${t("common.cancel")}</button><button class="primary" id="saveGroups">${t("common.save")}</button>`,
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
      toast(t("users.groupsUpdated"), true);
    } catch (err) {
      toast(err.message);
    }
  };
}

function openUserEdit(userId) {
  const user = state.users.find((u) => String(u.id) === String(userId));
  if (!user) return;
  const currentRole = ROLES().find((r) => r.value === user.role) ? user.role : "user";
  const checked = user.disabled ? "" : "checked";
  showModal({
    kind: "useredit",
    title: t("users.editTitle", { name: escapeHtml(displayName(user)) }),
    body:
      `<form id="editUser" class="compact">` +
        `<label class="field-label">${t("users.colRole")}</label>` +
        roleSelect("role", currentRole) +
        `<label class="field-label">${t("users.containerLimit")}</label>` +
        `<input name="containerCap" type="number" min="1" value="${user.containerCap}">` +
        `<label class="field-label">${t("users.netdiskQuota")}</label>` +
        `<input name="netdiskQuotaGB" type="number" min="0" step="0.1" value="${(user.netdiskQuotaBytes / 1024 / 1024 / 1024).toFixed(2)}">` +
        `<label class="field-label">${t("users.portPrefix")}</label>` +
        `<input name="portPrefix" type="number" min="100" max="655" value="${user.portPrefix || ""}" placeholder="${t("users.portPrefixPlaceholder")}">` +
        `<label class="field-label">${t("users.newPassword")} <span class="hint">${t("users.newPasswordHint")}</span></label>` +
        `<input name="password" type="password" placeholder="${t("users.resetPassword")}" autocomplete="new-password">` +
        `<label class="check"><input type="checkbox" name="enabled" ${checked}> ${t("users.accountEnabled")}</label>` +
      `</form>`,
    foot: `<button class="ghost" data-close>${t("common.cancel")}</button><button class="primary" id="saveUser">${t("common.save")}</button>`,
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
      toast(t("users.userUpdated"), true);
    } catch (err) {
      toast(err.message);
    }
  };
}

async function approveUser(userId, userName) {
  if (!confirm(t("users.approveConfirm", { userName }))) return;
  try {
    await api("/api/users/approve", { method: "POST", body: JSON.stringify({ userId }) });
    await refreshSection("users", "groups");
    renderView();
    toast(t("users.userApproved"), true);
  } catch (err) {
    toast(err.message);
  }
}

async function deactivateUser(userId, userName) {
  if (userId === state.me.id) {
    toast(t("users.cannotDeactivateSelf"));
    return;
  }
  if (!confirm(t("users.deactivateConfirm", { userName }))) return;
  try {
    await api("/api/users/deactivate", { method: "POST", body: JSON.stringify({ id: userId }) });
    await refreshSection("users", "groups");
    renderView();
    toast(t("users.userDeactivated"), true);
  } catch (err) {
    toast(err.message);
  }
}

async function deleteUser(userId, userName) {
  if (userId === state.me.id) {
    toast(t("users.cannotDeleteSelf"));
    return;
  }
  if (!confirm(t("users.deleteConfirm", { userName }))) return;
  try {
    await api("/api/users/delete", { method: "POST", body: JSON.stringify({ id: userId }) });
    await refreshSection("users", "groups");
    renderView();
    toast(t("users.userDeleted"), true);
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
    totalEl.textContent = t("users.totalUsed", { size: fmtBytes(total) });
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
  return rows.join("") || `<tr class="empty-row"><td colspan="4">${t("users.noUsers")}</td></tr>`;
}

function usageRow(user, info) {
  const name = escapeHtml(displayName(user) || user.username);
  const used = info ? info.usedBytes || 0 : 0;
  const quota = info ? info.quotaBytes || 0 : user.netdiskQuotaBytes || 0;
  // A user may legitimately have no netdisk path yet (group not configured, or
  // the on-disk folder has never been created). Surface that instead of "0 B".
  let status = "";
  if (info && !info.configured) status = ` <span class="badge badge-muted">${t("users.noPath")}</span>`;
  else if (info && info.pathMissing) status = ` <span class="badge badge-muted">${t("users.notCreated")}</span>`;
  const pct = quota > 0 ? Math.min(100, Math.round((used / quota) * 100)) : 0;
  const barCls = pct >= 90 ? "danger" : pct >= 70 ? "warn" : "";
  const quotaText = quota > 0 ? `${pct}% / ${fmtBytes(quota)}` : t("users.unlimited");
  return (
    `<tr>` +
      `<td>${name}${status}</td>` +
      `<td class="netdisk-used-col">${fmtBytes(used)}</td>` +
      `<td>${escapeHtml(quotaText)}</td>` +
      `<td class="netdisk-bar-col">${quota > 0 ? `<span class="quota-bar ${barCls}"><span style="width:${pct}%"></span></span>` : ""}</td>` +
    `</tr>`
  );
}

// ---------- Long-running tasks (admin) ----------
//
// Surfaces every in-flight bulk netdisk copy/move/transfer/restore/upload
// (chunked or not) plus running backup jobs, across ALL users — not just the
// viewing admin's own — so an admin can confirm nothing is mid-flight before
// restarting or updating the app. Backed by GET /api/admin/tasks; refreshed
// on the normal "users" tab poll cadence via usersLoader in refresh.js.

const TASK_KIND_LABEL_KEY = {
  "netdisk.copy": "task.netdiskCopy",
  "netdisk.move": "task.netdiskMove",
  "netdisk.upload": "task.netdiskUpload",
  "netdisk.upload.chunked": "task.netdiskUploadChunked",
  "netdisk.transfer": "task.netdiskTransfer",
  "netdisk.restore": "task.netdiskRestore",
  "backup.run": "task.backupRun",
};

// paintAdminTasks fills the dedicated task card once /api/admin/tasks
// resolves. Called after the initial render so the card can paint
// progressively, same as paintNetdiskUsage.
function paintAdminTasks() {
  const body = $("#adminTasksBody");
  if (!body) return; // user navigated away before the fetch landed
  body.innerHTML = adminTasksHtml(state.adminTasks);
}

function adminTasksHtml(tasks) {
  if (!tasks || !tasks.length) {
    return `<div class="empty-state">${t("users.longTasksNone")}</div>`;
  }
  const rows = [...tasks]
    .sort((a, b) => new Date(a.startedAt) - new Date(b.startedAt))
    .map(taskRow)
    .join("");
  return (
    `<table class="data">` +
      `<thead><tr><th>${t("users.longTasksUser")}</th><th>${t("users.longTasksTask")}</th><th>${t("users.longTasksProgress")}</th><th>${t("users.longTasksElapsed")}</th></tr></thead>` +
      `<tbody>${rows}</tbody>` +
    `</table>`
  );
}

function taskRow(task) {
  const label = t(TASK_KIND_LABEL_KEY[task.kind] || "") || task.kind;
  const owner = displayNameForUsername(task.ownerName) || task.ownerName || "—";
  const elapsed = formatTaskElapsed(Date.now() - Date.parse(task.startedAt));
  const hasProgress = typeof task.progress === "number" && task.progress >= 0;
  const progressHtml = hasProgress
    ? `<div class="job-progress"><div class="job-progress-fill" style="width:${Math.min(100, task.progress)}%"></div></div><span class="hint">${task.progress}%</span>`
    : `<span class="hint">${escapeHtml(task.message || "…")}</span>`;
  return (
    `<tr>` +
      `<td>${escapeHtml(owner)}</td>` +
      `<td><div class="primary-line">${escapeHtml(label)}</div><div class="secondary-line">${escapeHtml(task.name || "")}</div></td>` +
      `<td>${progressHtml}</td>` +
      `<td>${escapeHtml(elapsed)}</td>` +
    `</tr>`
  );
}

function formatTaskElapsed(ms) {
  const totalSeconds = Math.max(0, Math.floor(ms / 1000));
  if (totalSeconds < 60) return `${totalSeconds}s`;
  const minutes = Math.floor(totalSeconds / 60);
  const seconds = totalSeconds % 60;
  if (minutes < 60) return `${minutes}m ${seconds}s`;
  const hours = Math.floor(minutes / 60);
  const mins = minutes % 60;
  return `${hours}h ${mins}m`;
}


