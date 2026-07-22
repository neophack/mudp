// New-container modal and its SSE-driven progress panel.

import { state, toast, refreshAll, renderView, readCSRFCookie } from "../app.js";
import { showModal, setModalBody, closeModal, readSSE } from "./ui.js";
import { registerJob } from "./jobs.js";

const STAGE_ORDER = ["image", "create", "start", "refresh", "done"];

// lastPayload holds the most recent create request so the Retry button in the
// progress panel can resubmit it instead of wiping the form (which would also
// stack a second modal backdrop — see openCreateModal's showModal call).
let lastPayload = null;
const STAGE_LABEL = {
  image: "Inspect image",
  create: "Create container",
  start: "Start container",
  refresh: "Refresh list",
  done: "Complete",
};

export function openCreateModal() {
  state.create = { active: false, steps: [], logs: "", error: "" };
  const imageOptions = state.images
    .map((img) => {
      const hasPreset = img.preset && (img.preset.gpus || (img.preset.ports && img.preset.ports.length) || img.preset.description);
      return `<option value="${escapeHtml(img.name)}">${escapeHtml(img.name)}${hasPreset ? " ⚙" : ""}</option>`;
    })
    .join("");
  // Networks the user can attach to. mudp-mesh is the WRT gateway's LAN and
  // is the default gateway network — it's surfaced as a checkbox that defaults
  // to checked, so a fresh container is isolated behind the router unless the
  // user explicitly opts out (and typically picks another network). Docker
  // built-ins like host/none are filtered out (validateNetworkAttachment rejects
  // them); bridge is kept as a cosmetic pass-through.
  const myNetworks = (state.networks || [])
    .filter((n) => !n.system || n.name === "bridge" || n.name === "mudp-mesh")
    .map((n) => {
      const isLAN = n.name === "mudp-mesh";
      const label = isLAN
        ? `${escapeHtml(n.name)} <span class="hint">(默认网关 / default gateway)</span>`
        : `${escapeHtml(n.name)}${n.system ? ' <span class="hint">(system)</span>' : ""}`;
      const checked = isLAN ? "checked" : "";
      return `<label class="check"><input type="checkbox" name="networks" value="${escapeHtml(n.fullName || n.name)}" ${checked}> ${label}</label>`;
    })
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
        gpuSelectHtml() +
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

        `<label class="check"><input type="checkbox" name="forward8080"> Forward container port 8080</label>` +
        `<label class="check"><input type="checkbox" name="forward80"> Forward container port 80</label>` +
        `<label class="check"><input type="checkbox" name="mountNetdisk" checked> Mount netdisk at /workspace</label>` +
        `<label class="check"><input type="checkbox" name="mountShm" checked> Mount host /dev/shm (shared memory)</label>` +
        // Collapsible advanced block. Empty fields inherit the image defaults
        // (the backend treats them as "unset"), so leaving this collapsed keeps
        // the simple wizard behavior for most users.
        `<details class="advanced-block"><summary>Advanced (optional overrides)</summary>` +
          `<input name="command" placeholder="Command (overrides image CMD), e.g. python app.py">` +
          `<input name="entrypoint" placeholder="Entrypoint (overrides image ENTRYPOINT)">` +
          `<input name="workingDir" placeholder="Working directory (overrides WORKDIR)">` +
          `<input name="hostname" placeholder="Container hostname">` +
          `<input name="runUser" placeholder="Run as user (e.g. root, 1000:1000)">` +
          `<div class="advanced-row">` +
            `<input name="cpuLimit" type="number" min="0" step="0.5" placeholder="CPU cores (0=unlimited)">` +
            `<input name="memoryMb" type="number" min="0" placeholder="Memory limit (MB, 0=unlimited)">` +
            `<input name="pidsLimit" type="number" min="0" placeholder="Max PIDs (0=unlimited)">` +
          `</div>` +
          `<input name="capAdd" placeholder="cap-add (comma-separated, e.g. SYS_PTRACE)">` +
          `<input name="capDrop" placeholder="cap-drop (comma-separated)">` +
          `<textarea name="labels" placeholder="Custom labels, one key=value per line&#10;e.g. mcp.capability=go,nodejs&#10;e.g. mcp.runtime=java17"></textarea>` +
        `</details>` +
      `</form>`,
    foot: `<button class="ghost" data-close>Cancel</button><button class="primary" id="createSubmit">Create and Start</button>`,
  });
  // Auto-fill the form from the selected image's admin-defined preset. Picking an
  // image with a preset pre-populates GPUs, env, ports, networks, and the SSH/VSCode
  // toggles so the per-image conventions (e.g. VNC_PW password, the app's port) are
  // applied without each user rediscovering them. Users can still override anything.
  $("#newContainer").querySelector('[name="image"]').addEventListener("change", (e) => applyPreset(e.target.value));
  $("#createSubmit").onclick = async () => {
    const fd = new FormData($("#newContainer"));
    const payload = Object.fromEntries(fd);
    payload.env = String(payload.env || "")
      .split(/\n+/)
      .map((s) => s.trim())
      .filter(Boolean);

    payload.forward8080 = fd.has("forward8080");
    payload.forward80 = fd.has("forward80");
    payload.mountNetdisk = fd.has("mountNetdisk");
    payload.mountShm = fd.has("mountShm");
    payload.networks = [...$("#newContainer").querySelectorAll('input[name="networks"]:checked')].map((i) => i.value);
    payload.restartPolicy = fd.get("restartPolicy") || "unless-stopped";
    // Advanced overrides (all optional; backend ignores empties/zeros).
    payload.command = (fd.get("command") || "").trim();
    payload.entrypoint = (fd.get("entrypoint") || "").trim();
    payload.workingDir = (fd.get("workingDir") || "").trim();
    payload.hostname = (fd.get("hostname") || "").trim();
    payload.runUser = (fd.get("runUser") || "").trim();
    // CPU cores → nanocpus (1 core = 1e9). 0/empty means unlimited.
    const cpuCores = parseFloat(fd.get("cpuLimit"));
    payload.nanoCpus = isFinite(cpuCores) && cpuCores > 0 ? Math.round(cpuCores * 1e9) : 0;
    payload.memoryMb = parseInt(fd.get("memoryMb"), 10) || 0;
    payload.pidsLimit = parseInt(fd.get("pidsLimit"), 10) || 0;
    payload.capAdd = csvList(fd.get("capAdd"));
    payload.capDrop = csvList(fd.get("capDrop"));
    payload.labels = kvLines(fd.get("labels"));
    // Forward the image preset's device passthrough (NVIDIA device nodes, CDI
    // devices) so GPUs stay connected and admins' device choices apply. These come
    // only from admin-configured presets; the user form has no device field.
    const selectedImage = state.images.find((i) => i.name === payload.image);
    if (selectedImage && selectedImage.preset) {
      payload.devices = selectedImage.preset.devices || [];
      payload.cdiDevices = selectedImage.preset.cdiDevices || [];
    }
    lastPayload = payload;
    await streamCreate(payload);
  };
}

// gpuSelectHtml renders the GPU dropdown based on the host's actual GPU count.
// Falls back to none/all when the count is unknown (non-GPU host or not yet loaded).
function gpuSelectHtml() {
  const count = Number(state.gpuCount || 0);
  let opts = `<option value="none">No GPU</option><option value="all">All GPUs</option>`;
  for (let i = 0; i < count; i++) {
    opts += `<option value="${i}">GPU ${i}</option>`;
  }
  // If we don't know the count yet, keep the legacy 0/1 fallback so users aren't
  // blocked on a slow GPU probe; once state.gpuCount loads, a re-open refreshes it.
  if (count === 0) {
    opts += `<option value="0">GPU 0</option><option value="1">GPU 1</option>`;
  }
  return `<select name="gpus">${opts}</select>`;
}

// applyPreset fills the create form from an image's admin-defined preset. Only the
// image-dependent fields are touched; the name stays as the user left it.
function applyPreset(imageName) {
  const img = state.images.find((i) => i.name === imageName);
  const form = $("#newContainer");
  if (!form || !img || !img.preset) return;
  const p = img.preset;
  if (p.gpus) form.querySelector('[name="gpus"]').value = p.gpus;
  if (p.env && p.env.length) form.querySelector('[name="env"]').value = p.env.join("\n");
  // Preset ports are container-side only; render as ":container" so the backend
  // auto-allocates a host port from the user's range.
  if (p.ports && p.ports.length) form.querySelector('[name="ports"]').value = p.ports.map((c) => ":" + c).join("\n");
  if (p.restartPolicy) form.querySelector('[name="restartPolicy"]').value = p.restartPolicy;
  form.querySelector('[name="forward8080"]').checked = !!p.forward8080;
  form.querySelector('[name="forward80"]').checked = !!p.forward80;
  if (p.mountNetdisk !== undefined) form.querySelector('[name="mountNetdisk"]').checked = p.mountNetdisk;
  if (p.mountShm !== undefined) form.querySelector('[name="mountShm"]').checked = p.mountShm;
  if (p.networks && p.networks.length) {
    form.querySelectorAll('input[name="networks"]').forEach((cb) => {
      if (p.networks.includes(cb.value)) cb.checked = true;
    });
  }
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
  if (retry) retry.onclick = () => { if (lastPayload) streamCreate(lastPayload); };
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
  const job = registerJob({ kind: "container.create", name: payload.name || "new container" });
  try {
    const headers = { "Content-Type": "application/json", Accept: "text/event-stream" };
    const csrfToken = readCSRFCookie() || state.csrfToken || "";
    if (csrfToken) headers["X-CSRF-Token"] = csrfToken;
    const res = await fetch("/api/containers/create/stream", {
      method: "POST",
      credentials: "same-origin",
      headers,
      body: JSON.stringify(payload),
      signal: job.signal,
    });
    if (!res.ok) {
      const data = await res.json().catch(() => ({}));
      state.create.error = data.error || `Request failed (${res.status})`;
      state.create.active = false;
      job.error(state.create.error);
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
        job.log(message);
        renderCreateProgress();
      } else if (event === "error") {
        state.create.steps.forEach((s) => {
          if (s.state === "active") s.state = "error";
        });
        state.create.error = data.message || "Creation failed";
        state.create.logs += `[error] ${state.create.error}\n`;
        job.error(state.create.error);
        renderCreateProgress();
        toast(state.create.error);
      } else if (event === "done") {
        state.create.active = false;
        job.done(data.message || "Container created");
        renderCreateProgress();
        toast("Container created", true);
        refreshAll().then(() => renderView());
        setTimeout(() => closeModal(), 700);
      } else if (event === "cancelled") {
        state.create.active = false;
        state.create.logs += `[cancelled] ${data.message || ""}\n`;
        job.cancel();
        renderCreateProgress();
      }
    });
  } catch (err) {
    state.create.active = false;
    if (job.signal.aborted) {
      state.create.error = "Cancelled";
      job.cancel();
    } else {
      state.create.error = err.message;
      job.error(err.message);
    }
    state.create.logs += `[error] ${state.create.error}\n`;
    renderCreateProgress();
    toast(state.create.error);
  }
}

function $(selector) {
  return document.querySelector(selector);
}

// csvList splits a comma/whitespace-separated string into trimmed tokens.
function csvList(raw) {
  return String(raw || "").split(/[\s,]+/).map((s) => s.trim()).filter(Boolean);
}

// kvLines parses "key=value" lines into an object, skipping invalid lines.
function kvLines(raw) {
  const out = {};
  for (const line of String(raw || "").split("\n")) {
    const idx = line.indexOf("=");
    if (idx <= 0) continue;
    const k = line.slice(0, idx).trim();
    const v = line.slice(idx + 1).trim();
    if (k) out[k] = v;
  }
  return Object.keys(out).length ? out : undefined;
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
