// Networks: list, create, delete. mudp-managed networks are namespaced per user.

import { state, api, toast, refreshSection, renderView, canMutate, isAdmin } from "../app.js";
import { showModal, closeModal } from "./ui.js";
import { openNetworkDetail } from "./network_details.js";

export function renderNetworks() {
  const admin = isAdmin();
  const cols = admin ? 7 : 6;
  const rows = (state.networks || []).map(networkRow).join("") ||
    `<tr class="empty-row"><td colspan="${cols}">No networks. Click “+ New Network” to create one.</td></tr>`;
  $("#view").innerHTML =
    `<div class="card">` +
      `<div class="card-head"><h2>Networks</h2>` +
        (admin ? `<button class="primary" id="newNetBtn">+ New Network</button>` : "") +
      `</div>` +
      (admin
        ? `<p class="hint" style="padding:0 16px;margin:0 0 8px">Networks already on this host are listed too. Use “Groups” to choose which user groups can see and attach to one — including Docker's “bridge”, which stays open to everyone until you pick groups for it. Only the network's creator can delete it.</p>`
        : "") +
      `<table class="data">` +
        `<thead><tr><th>Name</th><th>Driver</th><th>Subnet</th><th>Containers</th><th>Owner</th>` +
          (admin ? `<th>Groups</th>` : "") +
          `<th class="actions">Actions</th></tr></thead>` +
        `<tbody>${rows}</tbody>` +
      `</table>` +
    `</div>`;
  const nb = $("#newNetBtn");
  if (nb) nb.onclick = openCreateNetwork;
  document.querySelectorAll("[data-net-inspect]").forEach((btn) => {
    btn.onclick = () => openNetworkDetail(btn.dataset.netFullname, btn.dataset.netName);
  });
  document.querySelectorAll("[data-net-share]").forEach((btn) => {
    btn.onclick = () => openShareNetwork(btn.dataset.netFullname, btn.dataset.netName);
  });
  document.querySelectorAll("[data-net-delete]").forEach((btn) => {
    btn.onclick = () => deleteNetwork(btn.dataset.netFullname, btn.dataset.netName);
  });
}

// networkBadge labels where a row came from: a Docker built-in, a network that
// already existed on the host, or one shared with the user through their group.
function networkBadge(n) {
  if (n.system) return ` <span class="badge badge-muted">system</span>`;
  if (n.external) return ` <span class="badge badge-muted">host</span>`;
  if (n.shared) return ` <span class="badge badge-muted">shared</span>`;
  return "";
}

// groupsCell describes who may use a network, for the admin-only Groups column.
// An empty grant list means different things by row: a network mudp hands out
// per-owner is simply unshared, while "bridge" — which every user could always
// join — stays open until an admin picks groups for it. host/none can't be
// joined at all, so there is nobody to name.
function groupsCell(n) {
  const groups = n.groups || [];
  if (groups.length) return groups.join(", ");
  if (n.system) return n.name === "bridge" ? "everyone" : "—";
  return "—";
}

function networkRow(n) {
  const admin = isAdmin();
  const full = escapeHtml(n.fullName || n.name);
  const name = escapeHtml(n.name);
  // Built-in networks belong to Docker and are never deletable. "bridge" is
  // still shareable: restricting it to groups is the only way to keep users off
  // the default network. host/none can't be joined, so sharing them is moot.
  const shareable = admin && (!n.system || n.name === "bridge");
  return (
    `<tr>` +
      `<td><div class="primary-line">${name}${networkBadge(n)}</div></td>` +
      `<td><div class="secondary-line">${escapeHtml(n.driver)}</div></td>` +
      `<td><div class="secondary-line mono">${escapeHtml(n.subnet || "—")}</div></td>` +
      `<td>${n.containers || 0}</td>` +
      `<td><div class="secondary-line">${escapeHtml(n.owner || "system")}</div></td>` +
      (admin
        ? `<td><div class="secondary-line">${escapeHtml(groupsCell(n))}</div></td>`
        : "") +
      `<td class="actions">` +
        `<button class="icon" title="Details" data-net-inspect data-net-name="${name}" data-net-fullname="${full}">ℹ</button>` +
        (shareable ? `<button class="ghost" data-net-share data-net-name="${name}" data-net-fullname="${full}">Groups</button>` : "") +
        (canMutate() && n.canDelete ? `<button class="icon danger" title="Delete" data-net-delete data-net-name="${name}" data-net-fullname="${full}">✕</button>` : "") +
      `</td>` +
    `</tr>`
  );
}

// openShareNetwork lets an admin pick the user groups that may see and use a
// network. Sharing never transfers ownership, so the network stays deletable
// only by whoever created it.
function openShareNetwork(fullName, name) {
  const net = (state.networks || []).find((n) => (n.fullName || n.name) === fullName) || {};
  const current = new Set(net.groups || []);
  const checks = (state.groups || [])
    .map((g) => `<label class="check"><input type="checkbox" name="groupIds" value="${g.id}" ${current.has(g.name) ? "checked" : ""}> ${escapeHtml(g.name)}</label>`)
    .join("");
  // Docker's "bridge" starts out open to everyone, so for that row the dialog
  // is about taking access away, not granting it.
  const hint = net.system
    ? `Every user can attach to <b>bridge</b> while no group is selected. Select groups to restrict it to their members — admins keep access either way.`
    : `Members of the selected groups can see this network and attach their containers to it.`;
  showModal({
    kind: "netgroups",
    title: `Groups — ${name}`,
    body:
      `<form id="netGroupForm" class="compact">` +
        `<p class="hint">${hint}</p>` +
        `<div class="check-grid">${checks || '<span class="hint">No groups yet.</span>'}</div>` +
      `</form>`,
    foot: `<button class="ghost" data-close>Cancel</button><button class="primary" id="netGroupSave">Save</button>`,
  });
  $("#netGroupSave").onclick = async () => {
    const groupIds = [...$("#netGroupForm").querySelectorAll('input[name="groupIds"]:checked')].map((i) => Number(i.value));
    try {
      await api("/api/networks/access", { method: "POST", body: JSON.stringify({ name: fullName, groupIds }) });
      await refreshSection("networks");
      renderView();
      closeModal();
      toast("Network access updated", true);
    } catch (err) {
      toast(err.message);
    }
  };
}

function openCreateNetwork() {
  showModal({
    kind: "network",
    title: "New Network",
    body:
      `<form id="netForm" class="compact">` +
        `<input name="name" placeholder="Network name, e.g. frontend" required>` +
        `<select name="driver"><option value="bridge">bridge</option><option value="overlay">overlay</option><option value="macvlan">macvlan</option></select>` +
        `<input name="subnet" placeholder="Subnet (optional), e.g. 172.20.0.0/16">` +
        `<details class="advanced-block"><summary>Advanced IPAM (optional)</summary>` +
          `<input name="gateway" placeholder="Gateway, e.g. 172.20.0.1">` +
          `<input name="ipRange" placeholder="IP range, e.g. 172.20.0.0/24">` +
          `<label class="check"><input type="checkbox" name="ipv6"> Enable IPv6</label>` +
        `</details>` +
      `</form>`,
    foot: `<button class="ghost" data-close>Cancel</button><button class="primary" id="netSubmit">Create</button>`,
  });
  $("#netSubmit").onclick = async () => {
    const form = $("#netForm");
    const fd = new FormData(form);
    const payload = {
      name: fd.get("name"),
      driver: fd.get("driver") || "bridge",
      subnet: fd.get("subnet") || "",
      gateway: fd.get("gateway") || "",
      ipRange: fd.get("ipRange") || "",
      ipv6: form.querySelector('[name="ipv6"]').checked,
    };
    try {
      await api("/api/networks", { method: "POST", body: JSON.stringify(payload) });
      await refreshSection("networks");
      renderView();
      closeModal();
      toast("Network created", true);
    } catch (err) {
      toast(err.message);
    }
  };
}

async function deleteNetwork(fullName, name) {
  if (!confirm(`Delete network “${name}”?`)) return;
  try {
    await api("/api/networks/delete", { method: "POST", body: JSON.stringify({ name: fullName }) });
    await refreshSection("networks");
    renderView();
    toast("Network deleted", true);
  } catch (err) {
    toast(err.message);
  }
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

function $(selector) {
  return document.querySelector(selector);
}
