// Networks: list, create, delete, group sharing, and the per-network choice
// between Docker publishing and mudp port forwarding. mudp-managed networks are
// namespaced per user.

import { state, api, toast, refreshSection, renderView, canMutate, isAdmin, t } from "../app.js";
import { showModal, closeModal } from "./ui.js";
import { openNetworkDetail } from "./network_details.js";

export function renderNetworks() {
  const admin = isAdmin();
  const cols = admin ? 7 : 6;
  const rows = (state.networks || []).map(networkRow).join("") ||
    `<tr class="empty-row"><td colspan="${cols}">${t("networks.noNetworksCreate")}</td></tr>`;
  $("#view").innerHTML =
    `<div class="card">` +
      `<div class="card-head"><h2>${t("networks.title")}</h2>` +
        (admin ? `<button class="primary" id="newNetBtn">${t("networks.newNetwork")}</button>` : "") +
      `</div>` +
      (admin
        ? `<p class="hint" style="padding:0 16px;margin:0 0 8px">${t("networks.adminHint")}</p>`
        : "") +
      `<table class="data">` +
        `<thead><tr><th>${t("common.name")}</th><th>${t("common.driver")}</th><th>${t("networks.colSubnet")}</th><th>${t("common.containers")}</th><th>${t("common.owner")}</th>` +
          (admin ? `<th>${t("networks.colGroups")}</th>` : "") +
          `<th class="actions">${t("common.actions")}</th></tr></thead>` +
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
  document.querySelectorAll("[data-net-forward]").forEach((btn) => {
    btn.onclick = () => toggleForward(btn.dataset.netFullname, btn.dataset.netName, btn.dataset.netForward === "on");
  });
  document.querySelectorAll("[data-net-delete]").forEach((btn) => {
    btn.onclick = () => deleteNetwork(btn.dataset.netFullname, btn.dataset.netName);
  });
}

// networkBadge labels where a row came from: a Docker built-in, a network that
// already existed on the host, or one shared with the user through their group.
function networkBadge(n) {
  // "forward" says the ports of containers on this network are relayed by mudp
  // instead of published by Docker — set in Settings → Port forwarding, and
  // worth showing here because it changes what a port mapping does.
  const forward = n.forward ? ` <span class="badge badge-ok">${t("networks.badgeForward")}</span>` : "";
  // "internal" isolates the network from the host's external networks, so it is
  // worth flagging on the row: containers on it cannot reach the internet.
  const internal = n.internal ? ` <span class="badge badge-muted">${t("networks.badgeInternal")}</span>` : "";
  if (n.system) return ` <span class="badge badge-muted">${t("networks.badgeSystem")}</span>` + internal + forward;
  if (n.external) return ` <span class="badge badge-muted">${t("networks.badgeHost")}</span>` + internal + forward;
  if (n.shared) return ` <span class="badge badge-muted">${t("networks.badgeShared")}</span>` + internal + forward;
  return internal + forward;
}

// groupsCell describes who may use a network, for the admin-only Groups column.
// An empty grant list means different things by row: a network mudp hands out
// per-owner is simply unshared, while "bridge" — which every user could always
// join — stays open until an admin picks groups for it. host/none can't be
// joined at all, so there is nobody to name.
function groupsCell(n) {
  const groups = n.groups || [];
  if (groups.length) return groups.join(", ");
  if (n.system) return n.name === "bridge" ? t("networks.everyone") : "—";
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
  // Forwarding is only meaningful for a network a container can join and hold an
  // address on — the same set that can be shared.
  const forwardable = admin && n.attachable;
  const forwardTitle = n.forward
    ? t("networks.forwardTitleOn")
    : t("networks.forwardTitleOff");
  return (
    `<tr>` +
      `<td><div class="primary-line">${name}${networkBadge(n)}</div></td>` +
      `<td><div class="secondary-line">${escapeHtml(n.driver)}</div></td>` +
      `<td><div class="secondary-line mono">${escapeHtml(n.subnet || "—")}</div></td>` +
      `<td>${n.containers || 0}</td>` +
      `<td><div class="secondary-line">${escapeHtml(n.owner || t("networks.badgeSystem"))}</div></td>` +
      (admin
        ? `<td><div class="secondary-line">${escapeHtml(groupsCell(n))}</div></td>`
        : "") +
      `<td class="actions">` +
        `<button class="icon" title="${t("networks.details")}" data-net-inspect data-net-name="${name}" data-net-fullname="${full}">ℹ</button>` +
        (shareable ? `<button class="ghost" data-net-share data-net-name="${name}" data-net-fullname="${full}">${t("networks.groups")}</button>` : "") +
        (forwardable
          ? `<button class="ghost${n.forward ? " is-on" : ""}" title="${forwardTitle}" data-net-forward="${n.forward ? "off" : "on"}" data-net-name="${name}" data-net-fullname="${full}">${n.forward ? t("networks.forwarding") : t("networks.forward")}</button>`
          : "") +
        (canMutate() && n.canDelete ? `<button class="icon danger" title="${t("common.delete")}" data-net-delete data-net-name="${name}" data-net-fullname="${full}">✕</button>` : "") +
      `</td>` +
    `</tr>`
  );
}

// toggleForward switches one network between Docker publishing and mudp
// forwarding. It exists on this page because this is where an admin is standing
// when they find that a network's published ports answer nothing: on a host
// whose firewall is owned by something else (an OpenWrt router), Docker's
// publishing does not work, while the container's own address does.
//
// Turning it on applies to containers created from then on *and* to the ones
// already on the network, whose existing host ports are relayed instead.
async function toggleForward(fullName, name, forward) {
  if (forward && !confirm(t("networks.forwardConfirm", { name }))) return;
  try {
    const res = await api("/api/networks/forward", {
      method: "POST",
      body: JSON.stringify({ name: fullName, forward }),
    });
    // The Port forwarding page reads the same configuration, so drop its cache
    // rather than let it show the state from before this click.
    state.forwards = null;
    await refreshSection("networks");
    renderView();
    if (res.warning) {
      toast(t("networks.forwardSavedWarn", { warn: res.warning }));
    } else {
      toast(forward ? t("networks.forwardOn", { name }) : t("networks.forwardOff", { name }), true);
    }
  } catch (err) {
    toast(err.message);
  }
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
    ? t("networks.groupsHintSystem")
    : t("networks.groupsHint");
  showModal({
    kind: "netgroups",
    title: t("networks.groupsTitle", { name }),
    body:
      `<form id="netGroupForm" class="compact">` +
        `<p class="hint">${hint}</p>` +
        `<div class="check-grid">${checks || `<span class="hint">${t("networks.noGroupsYet")}</span>`}</div>` +
      `</form>`,
    foot: `<button class="ghost" data-close>${t("common.cancel")}</button><button class="primary" id="netGroupSave">${t("common.save")}</button>`,
  });
  $("#netGroupSave").onclick = async () => {
    const groupIds = [...$("#netGroupForm").querySelectorAll('input[name="groupIds"]:checked')].map((i) => Number(i.value));
    try {
      await api("/api/networks/access", { method: "POST", body: JSON.stringify({ name: fullName, groupIds }) });
      await refreshSection("networks");
      renderView();
      closeModal();
      toast(t("networks.accessUpdated"), true);
    } catch (err) {
      toast(err.message);
    }
  };
}

function openCreateNetwork() {
  showModal({
    kind: "network",
    title: t("networks.newTitle"),
    body:
      `<form id="netForm" class="compact">` +
        `<input name="name" placeholder="${t("networks.namePlaceholder")}" required>` +
        `<select name="driver"><option value="bridge">bridge</option><option value="overlay">overlay</option><option value="macvlan">macvlan</option></select>` +
        `<input name="subnet" placeholder="${t("networks.subnetPlaceholder")}">` +
        `<details class="advanced-block"><summary>${t("networks.advancedIpam")}</summary>` +
          `<input name="gateway" placeholder="${t("networks.gatewayPlaceholder")}">` +
          `<input name="ipRange" placeholder="${t("networks.ipRangePlaceholder")}">` +
          `<label class="check"><input type="checkbox" name="ipv6"> ${t("networks.enableIpv6")}</label>` +
          `<label class="check"><input type="checkbox" name="internal"> ${t("networks.internal")}</label>` +
        `</details>` +
      `</form>`,
    foot: `<button class="ghost" data-close>${t("common.cancel")}</button><button class="primary" id="netSubmit">${t("common.create")}</button>`,
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
      internal: form.querySelector('[name="internal"]').checked,
    };
    try {
      await api("/api/networks", { method: "POST", body: JSON.stringify(payload) });
      await refreshSection("networks");
      renderView();
      closeModal();
      toast(t("networks.created"), true);
    } catch (err) {
      toast(err.message);
    }
  };
}

async function deleteNetwork(fullName, name) {
  if (!confirm(t("networks.deleteConfirm", { name }))) return;
  try {
    await api("/api/networks/delete", { method: "POST", body: JSON.stringify({ name: fullName }) });
    await refreshSection("networks");
    renderView();
    toast(t("networks.deleted"), true);
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
