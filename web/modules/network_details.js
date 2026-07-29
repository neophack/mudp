// Network detail modal: properties + attached containers, with attach/detach.
// Mirrors the container details.js modal pattern (showModalNoShell + id binding).

import { api, toast, state, canMutate, refreshSection, t } from "../app.js";
import { showModalNoShell } from "./ui.js";

// openNetworkDetail fetches the network detail payload and renders a modal with
// the network's properties and its connected containers. Mutating roles get an
// "attach container" picker and per-container detach buttons.
export async function openNetworkDetail(fullName, displayName) {
  showModalNoShell(
    "network-modal",
    "wide",
    `<div class="modal-head"><h2>${t("netdetail.title", { name: escapeHtml(displayName) })}</h2><button class="ghost" data-close>${t("common.close")}</button></div>` +
      `<div class="modal-body"><p class="hint">${t("common.loadingDots")}</p></div>`
  );
  let inner;
  let ok = false;
  try {
    const n = await api("/api/networks/inspect?name=" + encodeURIComponent(fullName));
    inner = propertiesCard(n) + containersCard(n) + attachCard(n);
    ok = true;
  } catch (err) {
    inner = `<div class="error-box">✗ ${escapeHtml(err.message)}</div>`;
  }
  showModalNoShell(
    "network-modal",
    "wide",
    `<div class="modal-head"><h2>${t("netdetail.title", { name: escapeHtml(displayName) })}</h2><div class="modal-tools"><button class="ghost" data-close>${t("common.close")}</button></div></div>` +
      `<div class="modal-body">${inner}</div>`
  );
  if (ok) bindActions(fullName);
}

// propertiesCard renders the read-only network attributes.
function propertiesCard(n) {
  return (
    `<section class="card detail-settings">` +
      `<div class="card-head"><h2>${t("netdetail.properties")}</h2></div>` +
      `<div class="card-body"><dl class="detail">` +
        detailRow(t("common.name"), escapeHtml(n.name)) +
        detailRow(t("netdetail.fullName"), `<span class="mono">${escapeHtml(n.fullName)}</span>`) +
        detailRow(t("common.driver"), escapeHtml(n.driver || "—")) +
        detailRow(t("netdetail.subnet"), `<span class="mono">${escapeHtml(n.subnet || "auto")}</span>`) +
        detailRow(t("netdetail.gateway"), `<span class="mono">${escapeHtml(n.gateway || "—")}</span>`) +
        (n.ipRange ? detailRow(t("netdetail.ipRange"), `<span class="mono">${escapeHtml(n.ipRange)}</span>`) : "") +
        detailRow(t("netdetail.ipv6"), n.ipv6 ? t("netdetail.enabled") : t("netdetail.disabled")) +
        detailRow(t("networks.badgeInternal"), n.internal ? t("netdetail.internalOn") : t("netdetail.disabled")) +
        // The inspect payload returns containers as an array of endpoints (see
        // NetworkDetail), so count the array rather than treating it as a number.
        detailRow(t("netdetail.connectedContainers"), t("netdetail.attached", { n: (n.containers || []).length })) +
      `</dl></div>` +
    `</section>`
  );
}

// containersCard lists the containers currently attached to this network, each
// with a detach button (mutating roles only).
function containersCard(n) {
  const attached = n.containers || [];
  const mutable = canMutate();
  const rows = attached.length
    ? attached
        .map(
          (c) =>
            `<tr>` +
              `<td><div class="primary-line">${escapeHtml(c.name)}</div><div class="secondary-line mono">${escapeHtml(c.id.slice(0, 12))}</div></td>` +
              `<td><span class="mono">${escapeHtml(c.ipv4 || "—")}</span></td>` +
              `<td><span class="mono">${escapeHtml(c.ipv6 || "—")}</span></td>` +
              `<td>${mutable ? `<button class="icon warn" title="${t("netdetail.detach")}" data-detach="${escapeHtml(c.id)}">■</button>` : "—"}</td>` +
            `</tr>`
        )
        .join("")
    : `<tr class="empty-row"><td colspan="4">${t("netdetail.noContainers")}</td></tr>`;
  return (
    `<section class="card detail-settings">` +
      `<div class="card-head"><h2>${t("netdetail.connectedContainers")}</h2></div>` +
      `<table class="data"><thead><tr><th>${t("containers.colContainer")}</th><th>${t("netdetail.colIpv4")}</th><th>${t("netdetail.colIpv6")}</th><th class="actions">${t("common.actions")}</th></tr></thead>` +
      `<tbody>${rows}</tbody></table>` +
    `</section>`
  );
}

// attachCard renders a picker of the user's running containers not yet on this
// network, plus an "Attach" button. Hidden for read-only roles.
function attachCard(n) {
  if (!canMutate()) return "";
  const attachedIDs = new Set((n.containers || []).map((c) => c.id));
  // Offer the user's own running containers that aren't already attached.
  const candidates = (state.containers || [])
    .filter((c) => c.state === "running" && !attachedIDs.has(c.id));
  if (candidates.length === 0) return "";
  const opts = candidates
    .map((c) => `<option value="${escapeHtml(c.id)}">${escapeHtml(c.name || c.fullName)}</option>`)
    .join("");
  return (
    `<section class="card detail-settings">` +
      `<div class="card-head"><h2>${t("netdetail.attachTitle")}</h2></div>` +
      `<div class="card-body">` +
        `<div class="attach-row">` +
          `<select id="attachSelect">${opts}</select>` +
          `<button class="primary" id="attachBtn">${t("netdetail.attach")}</button>` +
        `</div>` +
      `</div>` +
    `</section>`
  );
}

// bindActions wires the attach/detach buttons after the modal body is rendered.
function bindActions(fullName) {
  const attach = document.querySelector("#attachBtn");
  if (attach) {
    attach.onclick = async () => {
      const sel = document.querySelector("#attachSelect");
      const containerId = sel ? sel.value : "";
      if (!containerId) return;
      attach.disabled = true;
      const old = attach.textContent;
      attach.textContent = t("netdetail.attaching");
      try {
        await api("/api/networks/connect", {
          method: "POST",
          body: JSON.stringify({ name: fullName, containerId }),
        });
        toast(t("netdetail.attachedOk"), true);
        await refreshSection("containers");
        // Re-open the modal to reflect the new attached container.
        openNetworkDetail(fullName, displayNameFromState(fullName));
      } catch (err) {
        toast(err.message);
      } finally {
        attach.disabled = false;
        attach.textContent = old;
      }
    };
  }
  document.querySelectorAll("[data-detach]").forEach((btn) => {
    btn.onclick = async () => {
      const containerId = btn.dataset.detach;
      if (!confirm(t("netdetail.detachConfirm"))) return;
      btn.disabled = true;
      try {
        await api("/api/networks/disconnect", {
          method: "POST",
          body: JSON.stringify({ name: fullName, containerId }),
        });
        toast(t("netdetail.detachedOk"), true);
        await refreshSection("containers");
        openNetworkDetail(fullName, displayNameFromState(fullName));
      } catch (err) {
        toast(err.message);
        btn.disabled = false;
      }
    };
  });
}

// displayNameFromState recovers the display name of a network from app state,
// used when re-rendering the detail modal after an attach/detach.
function displayNameFromState(fullName) {
  const n = (state.networks || []).find((x) => (x.fullName || x.name) === fullName);
  return n ? (n.name || fullName) : fullName;
}

function detailRow(label, value) {
  return `<dt>${label}</dt><dd>${value}</dd>`;
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
