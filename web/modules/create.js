// New-container modal and its SSE-driven progress panel.

import { state, toast, refreshAll, renderView } from "../app.js";
import { showModal, setModalBody, closeModal, readSSE } from "./ui.js";

const STAGE_ORDER = ["image", "bootstrap", "create", "copy", "start", "ssh", "vscode", "done"];
const STAGE_LABEL = {
  image: "Inspect image",
  bootstrap: "Generate bootstrap scripts",
  create: "Create container",
  copy: "Inject bootstrap files",
  start: "Start container",
  ssh: "Bring up SSH",
  vscode: "Bring up VS Code Server",
  done: "Complete",
};

export function openCreateModal() {
  state.create = { active: false, steps: [], logs: "", error: "" };
  const imageOptions = state.images
    .map((img) => `<option value="${escapeHtml(img.name)}">${escapeHtml(img.name)}</option>`)
    .join("");
  // System networks (bridge/host/none) are shown read-only on the Networks view
  // but cannot be attached to — validateNetworkAttachment rejects anything
  // lacking the mudp-managed label, so never expose them as checkable options.
  const myNetworks = (state.networks || [])
    .filter((n) => !n.system)
    .map((n) => `<label class="check"><input type="checkbox" name="networks" value="${escapeHtml(n.fullName || n.name)}"> ${escapeHtml(n.name)}</label>`)
    .join("");
  const myVolumes = (state.volumes || []).map((v) => v.name);
  const prefix = Number(state.me?.portPrefix || 0);
  const portHint = prefix > 0 ? `Assigned host ports: ${prefix * 100}-${prefix * 100 + 99}` : "Ask an admin to assign a port prefix before publishing ports.";
  showModal({
    kind: "create",
    title: "New Container",
    body:
      `<form id="newContainer" class="compact">` +
        `<input name="name" placeholder="Container name, e.g. dev01" required>` +
        `<select name="image" required><option value="">Select image</option>${imageOptions}</select>` +
        `<select name="gpus">` +
          `<option value="none">No GPU</option>` +
          `<option value="all">All GPUs</option>` +
          `<option value="0">GPU 0</option>` +
          `<option value="1">GPU 1</option>` +
        `</select>` +
        `<textarea name="env" placeholder="Environment variables, one KEY=VALUE per line"></textarea>` +
        `<textarea name="ports" placeholder="Port mappings, one host:container per line\n${escapeHtml(portHint)}"></textarea>` +
        `<textarea name="mounts" placeholder="Managed volume mounts, one volume-name:target[:ro] per line${myVolumes.length ? '\nAvailable volumes: ' + escapeHtml(myVolumes.join(', ')) : ''}"></textarea>` +
        (myNetworks ? `<label class="field-label">Networks</label><div class="check-grid">${myNetworks}</div>` : "") +
        `<label class="field-label">Restart policy</label>` +
        `<select name="restartPolicy">` +
          `<option value="unless-stopped" selected>Start on boot (unless-stopped)</option>` +
          `<option value="always">Always restart (always)</option>` +
          `<option value="on-failure">Restart on failure (on-failure)</option>` +
          `<option value="no">Do not auto-restart (no)</option>` +
        `</select>` +
        `<label class="check"><input type="checkbox" name="ssh" checked> Enable SSH port</label>` +
        `<label class="check"><input type="checkbox" name="vscode" checked> Enable VS Code Web port</label>` +
        `<label class="check"><input type="checkbox" name="forward8080"> Forward container port 8080</label>` +
        `<label class="check"><input type="checkbox" name="forward80"> Forward container port 80</label>` +
        `<label class="check"><input type="checkbox" name="mountNetdisk" checked> Mount netdisk at /netdisk</label>` +
        `<input name="accessPassword" type="password" minlength="6" placeholder="Connection password (for SSH / VS Code)">` +
      `</form>`,
    foot: `<button class="ghost" data-close>Cancel</button><button class="primary" id="createSubmit">Create and Start</button>`,
  });
  $("#createSubmit").onclick = async () => {
    const fd = new FormData($("#newContainer"));
    const payload = Object.fromEntries(fd);
    payload.env = String(payload.env || "")
      .split(/\n+/)
      .map((s) => s.trim())
      .filter(Boolean);
    payload.ssh = fd.has("ssh");
    payload.vscode = fd.has("vscode");
    payload.forward8080 = fd.has("forward8080");
    payload.forward80 = fd.has("forward80");
    payload.mountNetdisk = fd.has("mountNetdisk");
    payload.networks = [...$("#newContainer").querySelectorAll('input[name="networks"]:checked')].map((i) => i.value);
    payload.restartPolicy = fd.get("restartPolicy") || "unless-stopped";
    await streamCreate(payload);
  };
}

export function renderCreateProgress() {
  if (state.modal.kind !== "create") return;
  const steps = state.create.steps
    .map((s) => {
      const label = STAGE_LABEL[s.stage] || s.stage;
      let icon = "○";
      let cls = "";
      if (s.state === "active") {
        icon = `<span class="spinner"></span>`;
        cls = "active";
      } else if (s.state === "done") {
        icon = "✓";
        cls = "done";
      } else if (s.state === "error") {
        icon = "✗";
        cls = "error";
      }
      return `<li class="step ${cls}"><span class="step-icon">${icon}</span><span class="step-label">${escapeHtml(label)}</span><span class="step-msg">${escapeHtml(s.message || "")}</span></li>`;
    })
    .join("");
  setModalBody(
    `<ol class="steps">${steps}</ol>` +
      (state.create.error ? `<div class="error-box">✗ ${escapeHtml(state.create.error)}</div>` : ``) +
      `<pre class="log-output create-log">${escapeHtml(state.create.logs || "")}</pre>` +
      `<div style="display:flex;gap:8px;justify-content:flex-end;">` +
        (state.create.error ? `<button class="primary" id="createRetry">Retry</button>` : ``) +
        `<button class="ghost" data-close>${state.create.error ? "Close" : "Hide"}</button>` +
      `</div>`
  );
  const retry = $("#createRetry");
  if (retry) retry.onclick = openCreateModal;
  const log = document.querySelector(".modal-body .create-log");
  if (log) log.scrollTop = log.scrollHeight;
}

function markStage(stage, message, st) {
  let existing = state.create.steps.find((s) => s.stage === stage);
  if (!existing) state.create.steps.push({ stage, message, state: st });
  else {
    existing.message = message;
    existing.state = st;
  }
  state.create.steps.sort((a, b) => {
    const ai = STAGE_ORDER.indexOf(a.stage);
    const bi = STAGE_ORDER.indexOf(b.stage);
    return (ai < 0 ? 99 : ai) - (bi < 0 ? 99 : bi);
  });
}

export async function streamCreate(payload) {
  state.create = { active: true, steps: [], logs: "", error: "" };
  renderCreateProgress();
  try {
    const res = await fetch("/api/containers/create/stream", {
      method: "POST",
      credentials: "same-origin",
      headers: { "Content-Type": "application/json", Accept: "text/event-stream" },
      body: JSON.stringify(payload),
    });
    if (!res.ok) {
      const data = await res.json().catch(() => ({}));
      state.create.error = data.error || `Request failed (${res.status})`;
      state.create.active = false;
      renderCreateProgress();
      toast(state.create.error);
      return;
    }
    await readSSE(res, (event, data) => {
      if (event === "progress") {
        const stage = data.stage || "info";
        const message = data.message || "";
        if (stage === "done") {
          state.create.steps.forEach((s) => {
            if (s.state === "active") s.state = "done";
          });
          markStage(stage, message, "done");
        } else {
          state.create.steps.forEach((s) => {
            if (s.state === "active") s.state = "done";
          });
          markStage(stage, message, "active");
        }
        state.create.logs += `[${stage}] ${message}\n`;
        renderCreateProgress();
      } else if (event === "error") {
        state.create.steps.forEach((s) => {
          if (s.state === "active") s.state = "error";
        });
        state.create.error = data.message || "Creation failed";
        state.create.logs += `[error] ${state.create.error}\n`;
        renderCreateProgress();
        toast(state.create.error);
      } else if (event === "done") {
        state.create.active = false;
        renderCreateProgress();
        toast("Container created", true);
        refreshAll().then(() => renderView());
        setTimeout(() => closeModal(), 700);
      }
    });
  } catch (err) {
    state.create.error = err.message;
    state.create.active = false;
    renderCreateProgress();
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
