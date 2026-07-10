// Stacks: deploy docker-compose projects via the host's `docker compose` CLI.
// Compose body + env live in the DB; deploys stream progress over SSE.

import { state, api, toast, refreshSection, renderView, canMutate } from "../app.js";
import { showModal, setModalBody, closeModal, readSSE } from "./ui.js";

const SAMPLE = `services:
  web:
    image: nginx:alpine
    ports:
      - "8080:80"
  db:
    image: postgres:16
    environment:
      POSTGRES_PASSWORD: \${DB_PASSWORD}
`;

export function renderStacks() {
  const rows = (state.stacks || []).map(stackRow).join("") ||
    `<tr class="empty-row"><td colspan="6">No stacks. Click “+ New Stack” to deploy a compose project.</td></tr>`;
  $("#view").innerHTML =
    `<div class="card">` +
      `<div class="card-head"><h2>Stacks</h2>` +
        (canMutate() ? `<button class="primary" id="newStackBtn">+ New Stack</button>` : "") +
      `</div>` +
      `<table class="data">` +
        `<thead><tr><th>Name</th><th>Services</th><th>Status</th><th>Owner</th><th>Updated</th><th class="actions">Actions</th></tr></thead>` +
        `<tbody>${rows}</tbody>` +
      `</table>` +
    `</div>`;
  const nb = $("#newStackBtn");
  if (nb) nb.onclick = () => openStackEditor(null);
  document.querySelectorAll("[data-stack-edit]").forEach((btn) => {
    btn.onclick = () => loadAndEdit(Number(btn.dataset.stackEdit));
  });
  document.querySelectorAll("[data-stack-up]").forEach((btn) => {
    btn.onclick = () => deployStack(Number(btn.dataset.stackUp));
  });
  document.querySelectorAll("[data-stack-down]").forEach((btn) => {
    btn.onclick = () => downStack(Number(btn.dataset.stackDown));
  });
  document.querySelectorAll("[data-stack-delete]").forEach((btn) => {
    btn.onclick = () => deleteStack(Number(btn.dataset.stackDelete), btn.dataset.stackName);
  });
}

function stackRow(s) {
  const allRunning = s.services > 0 && s.running === s.services;
  const someRunning = s.running > 0 && !allRunning;
  const status = allRunning
    ? `<span class="badge badge-ok"><span class="dot"></span>${s.running}/${s.services} up</span>`
    : someRunning
    ? `<span class="badge badge-warn"><span class="dot"></span>${s.running}/${s.services} up</span>`
    : s.services > 0
    ? `<span class="badge badge-muted"><span class="dot"></span>down</span>`
    : `<span class="badge badge-muted">—</span>`;
  return (
    `<tr>` +
      `<td><div class="primary-line">${escapeHtml(s.name)}</div><div class="secondary-line mono">${escapeHtml(s.projectName)}</div></td>` +
      `<td>${s.services || 0}</td>` +
      `<td>${status}</td>` +
      `<td><div class="secondary-line">${escapeHtml(s.owner || "—")}</div></td>` +
      `<td><div class="secondary-line">${escapeHtml(fmtTime(s.updatedAt))}</div></td>` +
      `<td class="actions">` +
        (canMutate() ? `<button class="icon ok" title="Deploy / Up" data-stack-up="${s.id}">▶</button>` : "") +
        (canMutate() ? `<button class="icon warn" title="Down" data-stack-down="${s.id}">■</button>` : "") +
        (canMutate() ? `<button class="icon" title="Edit" data-stack-edit="${s.id}">✎</button>` : "") +
        (canMutate() ? `<button class="icon danger" title="Delete" data-stack-delete="${s.id}" data-stack-name="${escapeHtml(s.name)}">✕</button>` : "") +
      `</td>` +
    `</tr>`
  );
}

function openStackEditor(stack) {
  const isNew = !stack;
  const body = stack?.composeYaml ?? SAMPLE;
  const env = stack ? envJSONToText(stack.envJson) : "DB_PASSWORD=change-me\n";
  showModal({
    kind: "stack",
    title: isNew ? "New Stack" : `Edit — ${stack.name}`,
    body:
      `<form id="stackForm" class="compact">` +
        `<input name="name" placeholder="Stack name, e.g. webapp" value="${escapeHtml(stack?.name || "")}" ${isNew ? "" : "disabled"}>` +
        `<label class="field-label">Environment variables (KEY=VALUE per line)</label>` +
        `<textarea name="env" class="mono" rows="4" spellcheck="false">${escapeHtml(env)}</textarea>` +
        `<label class="field-label">docker-compose.yml</label>` +
        `<textarea name="composeYaml" class="mono stack-editor" rows="14" spellcheck="false">${escapeHtml(body)}</textarea>` +
        `<p class="hint">\${VAR} references are substituted from the environment above. Images are pulled by the host's docker daemon.</p>` +
      `</form>`,
    foot: `<button class="ghost" data-close>Cancel</button><button class="primary" id="saveStack">Save</button>`,
  });
  $("#saveStack").onclick = async () => {
    const fd = new FormData($("#stackForm"));
    const payload = {
      composeYaml: fd.get("composeYaml"),
      env: fd.get("env"),
    };
    if (isNew) payload.name = fd.get("name");
    else payload.id = stack.id;
    try {
      const r = await api("/api/stacks", { method: "POST", body: JSON.stringify(payload) });
      await refreshSection("stacks");
      renderView();
      closeModal();
      toast(isNew ? "Stack saved" : "Stack updated", true);
      if (isNew && r.id) deployStack(r.id);
    } catch (err) {
      toast(err.message);
    }
  };
}

async function loadAndEdit(id) {
  try {
    const s = await api("/api/stacks/get?id=" + id);
    openStackEditor(s);
  } catch (err) {
    toast(err.message);
  }
}

async function deployStack(id) {
  showStackProgress("Deploying stack…", "");
  try {
    const res = await fetch("/api/stacks/up/stream", {
      method: "POST",
      credentials: "same-origin",
      headers: { "Content-Type": "application/json", Accept: "text/event-stream", "X-CSRF-Token": state.csrfToken },
      body: JSON.stringify({ id }),
    });
    if (!res.ok) {
      const data = await res.json().catch(() => ({}));
      toast(data.error || `Deploy failed (${res.status})`);
      closeModal();
      return;
    }
    await streamStack(res, "deployed");
  } catch (err) {
    toast(err.message);
  }
}

async function downStack(id) {
  if (!confirm("Tear down this stack? Containers will be stopped and removed (volumes kept).")) return;
  showStackProgress("Tearing down stack…", "");
  try {
    const res = await fetch("/api/stacks/down/stream", {
      method: "POST",
      credentials: "same-origin",
      headers: { "Content-Type": "application/json", Accept: "text/event-stream", "X-CSRF-Token": state.csrfToken },
      body: JSON.stringify({ id }),
    });
    if (!res.ok) {
      const data = await res.json().catch(() => ({}));
      toast(data.error || `Down failed (${res.status})`);
      closeModal();
      return;
    }
    await streamStack(res, "torn down");
  } catch (err) {
    toast(err.message);
  }
}

function showStackProgress(title, logs) {
  state.stackRun = { title, logs: logs || "", error: "" };
  renderStackProgress();
}

function renderStackProgress() {
  const sr = state.stackRun || { logs: "", error: "", title: "" };
  setModalBody(
    `<div class="step active"><span class="step-icon"><span class="spinner"></span></span><span class="step-label">${escapeHtml(sr.title)}</span></div>` +
    (sr.error ? `<div class="error-box">✗ ${escapeHtml(sr.error)}</div>` : "") +
    `<pre class="log-output">${escapeHtml(sr.logs || "")}</pre>` +
    `<div style="display:flex;gap:8px;justify-content:flex-end;"><button class="ghost" data-close>${sr.error ? "Close" : "Hide"}</button></div>`
  );
  const log = document.querySelector(".modal-body .log-output");
  if (log) log.scrollTop = log.scrollHeight;
}

async function streamStack(res, doneVerb) {
  await readSSE(res, (event, data) => {
    if (event === "progress") {
      state.stackRun.logs += (data.message || "") + "\n";
      renderStackProgress();
    } else if (event === "error") {
      state.stackRun.error = data.message || "failed";
      state.stackRun.logs += `[error] ${state.stackRun.error}\n`;
      renderStackProgress();
      toast(state.stackRun.error);
    } else if (event === "done" || event === "cancelled") {
      state.stackRun.logs += `[${event}] ${data.message || ""}\n`;
      renderStackProgress();
      toast(`Stack ${doneVerb}`, true);
      refreshSection("stacks", "containers").then(() => renderView());
      setTimeout(() => closeModal(), 800);
    }
  });
}

async function deleteStack(id, name) {
  if (!confirm(`Delete stack “${name}”? This removes the stored definition. Run Down first if you want to stop its containers.`)) return;
  try {
    await api("/api/stacks/delete", { method: "POST", body: JSON.stringify({ id }) });
    await refreshSection("stacks", "containers");
    renderView();
    toast("Stack deleted", true);
  } catch (err) {
    toast(err.message);
  }
}

function envJSONToText(json) {
  try {
    const obj = JSON.parse(json || "{}");
    return Object.entries(obj).map(([k, v]) => `${k}=${v}`).join("\n");
  } catch {
    return "";
  }
}

function fmtTime(iso) {
  if (!iso) return "—";
  const t = new Date(iso);
  return isNaN(t) ? iso : t.toLocaleString();
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
