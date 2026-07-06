// Networks: list, create, delete. mudp-managed networks are namespaced per user.

import { state, api, toast, refreshSection, renderView, canMutate, isAdmin } from "../app.js";
import { showModal, closeModal } from "./ui.js";

export function renderNetworks() {
  const rows = (state.networks || []).map(networkRow).join("") ||
    `<tr class="empty-row"><td colspan="6">No networks. Click “+ New Network” to create one.</td></tr>`;
  $("#view").innerHTML =
    `<div class="card">` +
      `<div class="card-head"><h2>Networks</h2>` +
        (isAdmin() ? `<button class="primary" id="newNetBtn">+ New Network</button>` : "") +
      `</div>` +
      `<table class="data">` +
        `<thead><tr><th>Name</th><th>Driver</th><th>Subnet</th><th>Containers</th><th>Owner</th><th class="actions">Actions</th></tr></thead>` +
        `<tbody>${rows}</tbody>` +
      `</table>` +
    `</div>`;
  const nb = $("#newNetBtn");
  if (nb) nb.onclick = openCreateNetwork;
  document.querySelectorAll("[data-net-delete]").forEach((btn) => {
    btn.onclick = () => deleteNetwork(btn.dataset.netFullname, btn.dataset.netName);
  });
}

function networkRow(n) {
  const sys = !!n.system;
  const nameCell = sys
    ? `<div class="primary-line">${escapeHtml(n.name)} <span class="badge badge-muted">system</span></div>`
    : `<div class="primary-line">${escapeHtml(n.name)}</div>`;
  return (
    `<tr>` +
      `<td>${nameCell}</td>` +
      `<td><div class="secondary-line">${escapeHtml(n.driver)}</div></td>` +
      `<td><div class="secondary-line mono">${escapeHtml(n.subnet || "—")}</div></td>` +
      `<td>${n.containers || 0}</td>` +
      `<td><div class="secondary-line">${escapeHtml(n.owner || "system")}</div></td>` +
      `<td class="actions">${canMutate() && !sys ? `<button class="icon danger" title="Delete" data-net-name="${escapeHtml(n.name)}" data-net-fullname="${escapeHtml(n.fullName || n.name)}">✕</button>` : "—"}</td>` +
    `</tr>`
  );
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
      `</form>`,
    foot: `<button class="ghost" data-close>Cancel</button><button class="primary" id="netSubmit">Create</button>`,
  });
  $("#netSubmit").onclick = async () => {
    const fd = new FormData($("#netForm"));
    const payload = {
      name: fd.get("name"),
      driver: fd.get("driver") || "bridge",
      subnet: fd.get("subnet") || "",
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
