// Port forwarding (admin): every host port mudp relays, who owns it, and the
// hand-added rules.
//
// Docker publishes a port by installing NAT rules; on a host where something
// else owns the firewall — an OpenWrt router appliance — that does not work and
// the published port answers nothing, even though the container is reachable at
// its own LAN address. mudp then carries the host port itself. This page is
// where that is set up and watched: which networks forward, what is currently
// listening, and any rule an admin added by hand for something the container
// labels do not cover.

import { state, api, toast, renderView, escapeHtml, isAdmin, t } from "../app.js";
import { showModal, closeModal } from "./ui.js";

export async function renderForwards() {
  if (!isAdmin()) {
    $("#view").innerHTML = `<div class="card"><div class="card-body"><p class="hint">${t("forwards.adminOnly")}</p></div></div>`;
    return;
  }
  // The refresh engine loads state.forwards before repainting; fetch only on
  // first entry so a signature-driven repaint costs no extra request.
  if (!state.forwards) {
    $("#view").innerHTML = `<div class="card"><div class="card-body"><p class="hint">${t("forwards.loading")}</p></div></div>`;
    try {
      state.forwards = await api("/api/admin/forwards");
    } catch (err) {
      $("#view").innerHTML = `<div class="card"><div class="card-body"><div class="error-box">${escapeHtml(err.message)}</div></div></div>`;
      return;
    }
  }
  const data = state.forwards || {};
  const rules = data.rules || [];
  const networks = data.networks || [];

  $("#view").innerHTML =
    `<div class="stack">` +
      (data.warning
        ? `<div class="card"><div class="card-body"><div class="error-box">${t("forwards.someNotRunning", { warn: escapeHtml(data.warning) })}</div></div></div>`
        : "") +
      `<div class="card">` +
        `<div class="card-head"><h2>${t("forwards.activeForwards")}</h2>` +
          `<button class="primary" id="addForwardBtn">${t("forwards.addForward")}</button>` +
        `</div>` +
        `<p class="hint" style="padding:0 16px;margin:0 0 8px">${t("forwards.hint")}</p>` +
        `<table class="data">` +
          `<thead><tr><th>${t("forwards.colHostPort")}</th><th>${t("common.user")}</th><th>${t("containers.colContainer")}</th><th>${t("forwards.colTarget")}</th><th>${t("forwards.colSource")}</th><th>${t("forwards.colConnections")}</th><th class="actions">${t("common.actions")}</th></tr></thead>` +
          `<tbody>${rules.map(ruleRow).join("") || `<tr class="empty-row"><td colspan="7">${t("forwards.noRules")}</td></tr>`}</tbody>` +
        `</table>` +
      `</div>` +
      `<div class="card">` +
        `<div class="card-head"><h2>${t("forwards.forwardingNetworks")}</h2>` +
          (networks.length ? `<span class="badge badge-ok">${t("forwards.nSelected", { n: networks.length })}</span>` : `<span class="badge">${t("forwards.none")}</span>`) +
        `</div>` +
        `<p class="hint" style="padding:0 16px;margin:0 0 8px">${t("forwards.netHint")}</p>` +
        `<div class="card-body"><form id="forwardNetForm" class="compact">` +
          (networkOptions(networks) || `<p class="hint">${t("forwards.noAttachable")}</p>`) +
          `<label class="field-label">${t("forwards.otherNames")}</label>` +
          `<textarea name="networksRaw" placeholder="e.g. openwrt-lan">${escapeHtml(unknownNetworks(networks).join("\n"))}</textarea>` +
          `<button>${t("forwards.saveNetworks")}</button>` +
        `</form></div>` +
      `</div>` +
    `</div>`;

  $("#addForwardBtn").onclick = () => openAddForward();
  document.querySelectorAll("[data-fwd-delete]").forEach((btn) => {
    btn.onclick = () => deleteForward(Number(btn.dataset.fwdDelete), btn.dataset.fwdLabel);
  });
  const form = $("#forwardNetForm");
  if (form) {
    form.onsubmit = async (e) => {
      e.preventDefault();
      const checked = [...form.querySelectorAll('input[name="forwardNet"]:checked')].map((i) => i.value);
      const extra = form.querySelector('[name="networksRaw"]').value || "";
      try {
        const res = await api("/api/admin/network/forward", {
          method: "POST",
          body: JSON.stringify({ networks: checked, networksRaw: extra }),
        });
        await reloadForwards();
        renderView();
        toast(res.warning ? t("forwards.savedWithWarn", { warn: res.warning }) : t("forwards.networksSaved"), !res.warning);
      } catch (err) {
        toast(err.message);
      }
    };
  }
}

// ruleRow renders one running listener. The user and container columns are what
// make a shared host readable: a bare port list says nothing about whose it is.
function ruleRow(r) {
  const manual = r.source === "manual";
  const target = `${r.targetIp || "?"}:${r.targetPort}`;
  const label = `${r.hostPort}/${r.proto}`;
  return (
    `<tr>` +
      `<td><div class="primary-line mono">${escapeHtml(label)}</div></td>` +
      `<td><div class="secondary-line">${escapeHtml(r.owner || "—")}</div></td>` +
      `<td><div class="secondary-line">${escapeHtml(r.name || "—")}</div>` +
        (r.note && r.note !== r.name ? `<div class="secondary-line hint">${escapeHtml(r.note)}</div>` : "") +
      `</td>` +
      `<td><div class="secondary-line mono">${escapeHtml(target)}</div></td>` +
      `<td><span class="badge ${manual ? "badge-warn" : "badge-muted"}">${manual ? t("forwards.manual") : t("forwards.container")}</span></td>` +
      `<td><div class="secondary-line">${t("forwards.connNow", { now: escapeHtml(String(r.active ?? 0)), total: escapeHtml(String(r.total ?? 0)) })}</div></td>` +
      `<td class="actions">` +
        (manual && r.manualId
          ? `<button class="icon danger" title="${t("common.delete")}" data-fwd-delete="${r.manualId}" data-fwd-label="${escapeHtml(label)}">✕</button>`
          : `<span class="hint">${t("forwards.fromContainer")}</span>`) +
      `</td>` +
    `</tr>`
  );
}

// networkOptions renders the checkbox list of attachable networks.
function networkOptions(selected) {
  return (state.networks || [])
    .filter((n) => n.attachable)
    .map((n) => {
      const value = n.fullName || n.name;
      const on = selected.some((s) => s === n.fullName || s === n.name);
      return (
        `<label class="check"><input type="checkbox" name="forwardNet" value="${escapeHtml(value)}" ${on ? "checked" : ""}> ` +
        `${escapeHtml(n.name)} <span class="hint">${escapeHtml(n.subnet || n.driver || "")}</span></label>`
      );
    })
    .join("");
}

// unknownNetworks are configured names that match no network on this host — a
// stack that has not been brought up, say. They go in the free-text field so
// saving the form does not silently drop them.
function unknownNetworks(selected) {
  const known = (state.networks || []).flatMap((n) => [n.fullName, n.name]);
  return selected.filter((s) => !known.includes(s));
}

// openAddForward asks for a host port and what to relay it to. The target is
// either a container — followed across restarts, because its address is
// resolved on every reconcile — or a fixed address for anything else.
function openAddForward() {
  const targets = (state.forwards?.targets || []).slice().sort((a, b) => (a.name || "").localeCompare(b.name || ""));
  const options = targets
    .map((t) => {
      const where = t.ip ? t.ip : `${t.state || "stopped"}, no address yet`;
      const ports = (t.ports || []).length ? ` — ${t.ports.join(", ")}` : "";
      return `<option value="${escapeHtml(t.id)}">${escapeHtml(t.name)} (${escapeHtml(t.owner || "—")}, ${escapeHtml(where)})${escapeHtml(ports)}</option>`;
    })
    .join("");
  showModal({
    kind: "forward",
    title: t("forwards.addTitle"),
    body:
      `<form id="forwardForm" class="compact">` +
        `<p class="hint">${t("forwards.addHint")}</p>` +
        `<div class="advanced-row">` +
          `<input name="hostPort" type="number" min="1" max="65535" placeholder="${t("forwards.hostPortPlaceholder")}" required>` +
          `<select name="proto"><option value="tcp">TCP</option><option value="udp">UDP</option></select>` +
        `</div>` +
        `<label class="field-label">${t("forwards.targetContainer")}</label>` +
        `<select name="containerId"><option value="">${t("forwards.fixedInstead")}</option>${options}</select>` +
        `<div class="advanced-row">` +
          `<input name="targetIp" placeholder="${t("forwards.targetIpPlaceholder")}">` +
          `<input name="targetPort" type="number" min="1" max="65535" placeholder="${t("forwards.targetPortPlaceholder")}" required>` +
        `</div>` +
        `<input name="note" placeholder="${t("forwards.notePlaceholder")}">` +
      `</form>`,
    foot: `<button class="ghost" data-close>${t("common.cancel")}</button><button class="primary" id="saveForward">${t("forwards.addBtn")}</button>`,
  });
  $("#saveForward").onclick = async () => {
    const fd = new FormData($("#forwardForm"));
    const payload = {
      hostPort: Number(fd.get("hostPort")) || 0,
      proto: fd.get("proto") || "tcp",
      containerId: fd.get("containerId") || "",
      targetIp: (fd.get("targetIp") || "").trim(),
      targetPort: Number(fd.get("targetPort")) || 0,
      note: (fd.get("note") || "").trim(),
    };
    try {
      const res = await api("/api/admin/forwards", { method: "POST", body: JSON.stringify(payload) });
      await reloadForwards();
      renderView();
      closeModal();
      toast(res.warning ? t("forwards.addedWithWarn", { warn: res.warning }) : t("forwards.forwardAdded"), !res.warning);
    } catch (err) {
      toast(err.message);
    }
  };
}

async function deleteForward(id, label) {
  if (!confirm(t("forwards.deleteConfirm", { label }))) return;
  try {
    await api("/api/admin/forwards/delete", { method: "POST", body: JSON.stringify({ id }) });
    await reloadForwards();
    renderView();
    toast(t("forwards.forwardDeleted"), true);
  } catch (err) {
    toast(err.message);
  }
}

// reloadForwards refreshes this page's data. It is also the refresh engine's
// loader for the tab, so a failed fetch keeps the last view rather than
// blanking a page an admin may be watching a live connection count on.
export async function reloadForwards() {
  if (!isAdmin()) {
    state.forwards = { rules: [], manual: [], networks: [], targets: [] };
    return state.forwards;
  }
  try {
    state.forwards = await api("/api/admin/forwards");
  } catch {
    state.forwards = state.forwards || { rules: [], manual: [], networks: [], targets: [] };
  }
  return state.forwards;
}

function $(selector) {
  return document.querySelector(selector);
}
