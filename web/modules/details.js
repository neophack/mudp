// Container details (inspect) modal: image/network/ports/mounts/env snapshot,
// with editable restart policy and attached networks. Also exposes duplicate,
// commit-to-image (admin), and the raw Docker JSON inspect.

import { api, toast, state, canMutate, isAdmin, refreshSection, renderView } from "../app.js";
import { showModalNoShell, closeModal } from "./ui.js";

export async function openDetails(id, name) {
  showModalNoShell(
    "detail-modal",
    "wide",
    `<div class="modal-head"><h2>Details: ${escapeHtml(name)}</h2><button class="ghost" data-close>Close</button></div>` +
      `<div class="modal-body"><p class="hint">Loading…</p></div>`
  );
  let inner;
  let inspected = null;
  try {
    inspected = await api("/api/containers/inspect?id=" + encodeURIComponent(id));
    const i = inspected;
    inner =
      `<dl class="detail">` +
        detailRow("Name", escapeHtml(i.name)) +
        detailRow("ID", `<span class="mono">${escapeHtml(i.id)}</span>`) +
        detailRow(
          "State",
          `<span class="badge ${i.state === "running" ? "badge-ok" : "badge-muted"}"><span class="dot"></span>${escapeHtml(i.state)}</span>`
        ) +
        detailRow("Image", escapeHtml(i.imageName || i.image)) +
        detailRow("Created", escapeHtml(i.createdAt ? new Date(i.createdAt * 1000).toLocaleString() : "-")) +
        detailRow("GPU", escapeHtml(i.gpu || "none")) +
        detailRow("Run as", escapeHtml(i.user || "image default")) +
        detailRow("IP", escapeHtml(i.ipAddress || "-")) +
        detailRow("Entrypoint", `<span class="mono">${escapeHtml((i.entrypoint || []).join(" ") || "-")}</span>`) +
        detailRow("Command", `<span class="mono">${escapeHtml((i.cmd || []).join(" ") || "-")}</span>`) +
        detailRow(
          "Ports",
          (i.ports || [])
            .map((p) => escapeHtml(p.hostPort ? `${p.hostPort}:${p.privatePort}/${p.type}` : `${p.privatePort}/${p.type}`))
            .join(", ") || "-"
        ) +
        detailRow(
          "Mounts",
          (i.mounts || []).map((m) => escapeHtml(`${m.source} → ${m.target} (${m.type})`)).join(", ") || "-"
        ) +
        detailRow("Environment", `<span class="mono detail-env">${escapeHtml((i.env || []).join("\n") || "-")}</span>`) +
      `</dl>` +
      actionsCard(i) +
      settingsCard(i) +
      rawJsonCard();
  } catch (err) {
    inner = `<div class="error-box">✗ ${escapeHtml(err.message)}</div>`;
  }
  showModalNoShell(
    "detail-modal",
    "wide",
    `<div class="modal-head"><h2>Details: ${escapeHtml(name)}</h2><div class="modal-tools"><button class="ghost" data-close>Close</button></div></div>` +
      `<div class="modal-body">${inner}</div>`
  );
  bindDetailActions(id, inspected);
}

// actionsCard renders the duplicate / commit-to-image actions. Duplicate is
// available to any mutating role; commit is admin-only (matching image lifecycle).
function actionsCard(_i) {
  if (!canMutate()) return "";
  return (
    `<section class="card detail-settings">` +
      `<div class="card-head"><h2>Actions</h2></div>` +
      `<div class="card-body">` +
        `<div class="check-grid">` +
          `<button class="ghost" id="dupBtn" title="Clone this container's config into a new (stopped) container">⧉ Duplicate</button>` +
          (isAdmin() ? `<button class="ghost" id="commitBtn" title="Capture this container's filesystem into a new image">📦 Commit to image</button>` : "") +
        `</div>` +
      `</div>` +
    `</section>`
  );
}

// rawJsonCard is a collapsed <details> that lazily loads Docker's full inspect
// JSON when expanded, so the detail modal stays fast for the common case.
function rawJsonCard() {
  return (
    `<section class="card detail-settings">` +
      `<details id="rawJsonDetails">` +
        `<summary>Raw JSON inspect</summary>` +
        `<div id="rawJsonBody"><p class="hint">Expanding loads the full inspect document.</p></div>` +
      `</details>` +
    `</section>`
  );
}

// settingsCard renders the editable restart policy + networks block. Read-only
// roles see the current values without inputs.
function settingsCard(i) {
  const policy = (i.restartPolicy || "unless-stopped").toLowerCase();
  const currentNets = new Set((i.networks || []).map((n) => n.name));
  // Exclude system networks (host/none): the backend (validateNetworkAttachment)
  // rejects any network lacking the mudp-managed label, so surfacing them here
  // as checkable would only produce confusing "attach failed" errors on save.
  // "bridge" is the one exception — a safe pass-through the backend allows.
  const avail = (state.networks || []).filter((n) => !n.system || n.name === "bridge");
  const editable = canMutate();
  const netChecks = avail.length
    ? avail.map((n) => {
        const key = n.fullName || n.name;
        const checked = currentNets.has(n.name) || currentNets.has(key) ? "checked" : "";
        const disabled = editable ? "" : "disabled";
        return `<label class="check"><input type="checkbox" name="editNetworks" value="${escapeHtml(key)}" ${checked} ${disabled}> ${escapeHtml(n.name)}${n.system ? ' <span class="hint">(system)</span>' : ""}</label>`;
      }).join("")
    : `<p class="hint">No custom networks yet — create one from the Networks tab to attach this container.</p>`;
  const policySelect = editable
    ? `<select id="editRestart">` +
      `<option value="unless-stopped" ${policy === "unless-stopped" ? "selected" : ""}>Start on boot (unless-stopped)</option>` +
      `<option value="always" ${policy === "always" ? "selected" : ""}>Always restart (always)</option>` +
      `<option value="on-failure" ${policy === "on-failure" ? "selected" : ""}>Restart on failure (on-failure)</option>` +
      `<option value="no" ${policy === "no" ? "selected" : ""}>Do not auto-restart (no)</option>` +
      `</select>`
    : `<dd>${escapeHtml(i.restartPolicy || "-")}</dd>`;
  return (
    `<section class="card detail-settings">` +
      `<div class="card-head"><h2>Settings</h2>${editable ? `<button class="primary" id="saveSettings">Save</button>` : ""}</div>` +
      `<div class="card-body">` +
        `<div class="field-label">Restart policy</div>` +
        policySelect +
        `<div class="field-label" style="margin-top:12px;">Networks</div>` +
        `<div class="check-grid">${netChecks}</div>` +
        `<p class="hint" style="margin-top:8px;">Network changes apply immediately. The restart policy takes effect on the next start.</p>` +
      `</div>` +
    `</section>`
  );
}

function bindDetailActions(id, inspected) {
  const save = document.querySelector("#saveSettings");
  if (save) {
    save.onclick = async () => {
      const restartPolicy = document.querySelector("#editRestart")?.value;
      const networks = [...document.querySelectorAll('input[name="editNetworks"]:checked')].map((i) => i.value);
      const btn = save;
      const old = btn.textContent;
      btn.disabled = true;
      btn.textContent = "Saving…";
      try {
        await api("/api/containers/update", {
          method: "POST",
          body: JSON.stringify({ id, restartPolicy, networks }),
        });
        toast("Settings saved", true);
      } catch (err) {
        toast(err.message);
      } finally {
        btn.disabled = false;
        btn.textContent = old;
      }
    };
  }
  // Duplicate: prompt for a new name, then clone the container (stopped).
  const dup = document.querySelector("#dupBtn");
  if (dup) {
    dup.onclick = async () => {
      const base = inspected?.name ? String(inspected.name).replace(/-?copy.*$/i, "") : "container";
      const newName = prompt("Name for the duplicated container:", base + "-copy");
      if (!newName || !newName.trim()) return;
      dup.disabled = true;
      const old = dup.textContent;
      dup.textContent = "Duplicating…";
      try {
        await api("/api/containers/duplicate", {
          method: "POST",
          body: JSON.stringify({ id, name: newName.trim() }),
        });
        await refreshSection("containers");
        toast("Container duplicated", true);
        closeModal();
        renderView();
      } catch (err) {
        toast(err.message);
      } finally {
        dup.disabled = false;
        dup.textContent = old;
      }
    };
  }
  // Commit to image (admin only). Prompts for image name + optional tag.
  const commit = document.querySelector("#commitBtn");
  if (commit) {
    commit.onclick = async () => {
      const name = prompt("Image name to commit to:", inspected?.imageName || "my-image");
      if (!name || !name.trim()) return;
      const tag = prompt("Tag (default: latest):", "latest");
      try {
        const res = await api("/api/containers/commit", {
          method: "POST",
          body: JSON.stringify({ id, name: name.trim(), tag: (tag || "").trim(), comment: "" }),
        });
        toast(`Committed as ${res.name}`, true);
        await refreshSection("images");
      } catch (err) {
        toast(err.message);
      }
    };
  }
  // Raw JSON: lazy-load the full inspect document on first expand.
  const raw = document.querySelector("#rawJsonDetails");
  if (raw) {
    let loaded = false;
    raw.addEventListener("toggle", async () => {
      if (!raw.open || loaded) return;
      loaded = true;
      const body = document.querySelector("#rawJsonBody");
      if (body) body.innerHTML = `<p class="hint">Loading…</p>`;
      try {
        const res = await fetch("/api/containers/inspect/raw?id=" + encodeURIComponent(id), { credentials: "same-origin" });
        const text = await res.text();
        let pretty = text;
        try { pretty = JSON.stringify(JSON.parse(text), null, 2); } catch {}
        if (body) body.innerHTML = `<pre class="log-output raw-json">${escapeHtml(pretty)}</pre>`;
      } catch (err) {
        if (body) body.innerHTML = `<div class="error-box">✗ ${escapeHtml(err.message)}</div>`;
      }
    });
  }
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
