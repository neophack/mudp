// Users & Groups management: create users/groups, assign roles, group
// membership (Feishu approval flow), reset passwords, disable/delete accounts.

import { state, api, toast, refreshAll, renderView, isAdmin } from "../app.js";
import { showModal, closeModal } from "./ui.js";

const ROLES = [
  { value: "user", label: "User", hint: "Workspace containers, SSH/VS Code, GPU, quotas" },
  { value: "operator", label: "Operator", hint: "Full container/image/volume CRUD" },
  { value: "helpdesk", label: "Help Desk", hint: "View all + logs/exec, no mutations" },
  { value: "readonly", label: "Read-Only", hint: "View only" },
  { value: "admin", label: "Administrator", hint: "Full control + user management" },
];

export function renderUsers() {
  $("#view").innerHTML =
    `<div class="grid two">` +
      `<section class="stack">` +
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
            `<input name="portPrefix" type="number" min="100" max="655" placeholder="Port prefix, e.g. 100">` +
            `<div class="check-grid">${groupChecks(state.groups)}</div>` +
            `<button>Create User</button>` +
          `</form></div>` +
        `</div>` +
        `<div class="card"><div class="card-head"><h2>Group Netdisk Paths</h2></div>` +
          `<table class="data"><thead><tr><th>Group</th><th>Path</th><th class="actions">Actions</th></tr></thead>` +
          `<tbody>${state.groups.map(groupPathRow).join("") || `<tr class="empty-row"><td colspan="3">No groups yet.</td></tr>`}</tbody></table>` +
        `</div>` +
      `</section>` +
      `<div class="card">` +
        `<div class="card-head"><h2>Users</h2></div>` +
        `<table class="data">` +
          `<thead><tr><th>User</th><th>Role</th><th>Groups</th><th>Ports</th><th class="actions">Actions</th></tr></thead>` +
          `<tbody>${state.users.map(userRow).join("") || `<tr class="empty-row"><td colspan="5">No users yet.</td></tr>`}</tbody>` +
        `</table>` +
      `</div>` +
    `</div>`;

  $("#newGroup").onsubmit = async (e) => {
    e.preventDefault();
    try {
      await api("/api/groups", {
        method: "POST",
        body: JSON.stringify(Object.fromEntries(new FormData(e.target))),
      });
      await refreshAll();
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
    payload.portPrefix = Number(payload.portPrefix || 0);
    payload.groupIds = [...e.target.querySelectorAll("input[name=groupIds]:checked")].map((i) => Number(i.value));
    try {
      await api("/api/users", { method: "POST", body: JSON.stringify(payload) });
      await refreshAll();
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
  document.querySelectorAll("[data-delete-user]").forEach((btn) => {
    btn.onclick = () => deleteUser(Number(btn.dataset.deleteUser), btn.dataset.userName);
  });
  document.querySelectorAll("[data-group-path]").forEach((btn) => {
    btn.onclick = () => setGroupPath(Number(btn.dataset.groupPath), btn.dataset.groupName, btn.dataset.current || "");
  });
}

function userRow(user) {
  const isPending = (user.groups || []).length > 0 && (user.groups || []).every((g) => g === "pending");
  const roleBadge = roleBadgeFor(user.role, isPending);
  const disabled = user.disabled;
  return (
    `<tr class="${disabled ? "row-muted" : ""}">` +
      `<td><div class="primary-line">${escapeHtml(user.username)}${disabled ? ' <span class="badge badge-muted">disabled</span>' : ""}</div>` +
        `${user.feishuOpenId ? `<div class="secondary-line">飞书用户</div>` : ""}` +
        `<div class="secondary-line" style="margin-top:2px;">limit ${user.containerCap}</div></td>` +
      `<td>${roleBadge}</td>` +
      `<td><div class="secondary-line">${escapeHtml((user.groups || []).join(", ") || "None")}</div></td>` +
      `<td><div class="secondary-line">${user.portPrefix ? `${user.portPrefix * 100}-${user.portPrefix * 100 + 99}` : "Not assigned"}</div></td>` +
      `<td class="actions">` +
        `<button class="ghost" data-edit-groups="${user.id}" data-user-name="${escapeHtml(user.username)}">Groups</button>` +
        `<button class="ghost" data-edit-user="${user.id}">Edit</button>` +
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
  return `<tr><td><div class="primary-line">${escapeHtml(g.name)}</div></td><td><div class="secondary-line mono">${escapeHtml(g.netdiskPath || "Not configured")}</div></td><td class="actions"><button class="ghost" data-group-path="${g.id}" data-group-name="${escapeHtml(g.name)}" data-current="${escapeHtml(g.netdiskPath || "")}">Set Path</button></td></tr>`;
}

async function setGroupPath(groupId, name, current) {
  const path = prompt(`Netdisk root path for ${name}`, current || "");
  if (path === null) return;
  try {
    await api("/api/groups/netdisk", { method: "POST", body: JSON.stringify({ groupId, path }) });
    await refreshAll();
    renderView();
    toast("Netdisk path saved", true);
  } catch (err) {
    toast(err.message);
  }
}

function openUserGroups(userId, userName) {
  const user = state.users.find((u) => String(u.id) === String(userId)) || {};
  const checks = (state.groups || [])
    .map((g) => {
      const checked = (user.groups || []).includes(g.name) ? "checked" : "";
      return `<label class="check"><input type="checkbox" name="groupIds" value="${g.id}" ${checked}> ${escapeHtml(g.name)}${g.name === "pending" ? ' <span class="hint">(待审批)</span>' : ""}</label>`;
    })
    .join("");
  showModal({
    kind: "usergroups",
    title: `Edit groups — ${userName}`,
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
      await refreshAll();
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
    title: `Edit — ${user.username}`,
    body:
      `<form id="editUser" class="compact">` +
        `<label class="field-label">Role</label>` +
        roleSelect("role", currentRole) +
        `<label class="field-label">Container limit</label>` +
        `<input name="containerCap" type="number" min="1" value="${user.containerCap}">` +
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
    const payload = {
      id: Number(userId),
      role: fd.get("role"),
      containerCap: Number(fd.get("containerCap") || 10),
      portPrefix: Number(fd.get("portPrefix") || 0),
      password: fd.get("password") || "",
      disabled: !fd.has("enabled"),
    };
    try {
      await api("/api/users/update", { method: "POST", body: JSON.stringify(payload) });
      await refreshAll();
      renderView();
      closeModal();
      toast("User updated", true);
    } catch (err) {
      toast(err.message);
    }
  };
}

async function deleteUser(userId, userName) {
  if (userId === state.me.id) {
    toast("You cannot delete your own account.");
    return;
  }
  if (!confirm(`Delete user “${userName}”? This removes their group memberships and stacks.`)) return;
  try {
    await api("/api/users/delete", { method: "POST", body: JSON.stringify({ id: userId }) });
    await refreshAll();
    renderView();
    toast("User deleted", true);
  } catch (err) {
    toast(err.message);
  }
}

function $(selector) {
  return document.querySelector(selector);
}

function escapeHtml(v) {
  return String(v ?? "").replace(/[&<>"']/g, (m) => ({
    "&": "&amp;",
    "<": "&lt;",
    ">": "&gt;",
    '"': "&quot;",
    "'": "&#39;",
  }[m]));
}
