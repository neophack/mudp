// New-container modal and its SSE-driven progress panel.

import { state, toast, refreshAll, renderView, readCSRFCookie, isAdmin, t } from "../app.js";
import { showModal, setModalBody, closeModal, readSSE } from "./ui.js";
import { registerJob } from "./jobs.js";

const STAGE_ORDER = ["image", "create", "start", "refresh", "done"];

// lastPayload holds the most recent create request so the Retry button in the
// progress panel can resubmit it instead of wiping the form (which would also
// stack a second modal backdrop — see openCreateModal's showModal call).
let lastPayload = null;
function stageLabels() {
  return {
    image: t("create.stageImage"),
    create: t("create.stageCreate"),
    start: t("create.stageStart"),
    refresh: t("create.stageRefresh"),
    done: t("create.stageDone"),
  };
}

export function openCreateModal() {
  state.create = { active: false, steps: [], logs: "", error: "" };
  const imageOptions = state.images
    .map((img) => {
      const hasPreset = img.preset && (img.preset.gpus || (img.preset.ports && img.preset.ports.length) || img.preset.description);
      return `<option value="${escapeHtml(img.name)}">${escapeHtml(img.name)}${hasPreset ? " ⚙" : ""}</option>`;
    })
    .join("");
  // The server marks each network attachable or not (own networks, anything an
  // admin granted to one of your groups, and "bridge" unless it was restricted
  // to other groups). host/none are listed read-only on the Networks view but
  // can't be joined, so they're filtered out here rather than producing an
  // "attach failed" error on submit.
  const myNetworks = (state.networks || [])
    .filter((n) => n.attachable)
    .map((n) => `<label class="check"><input type="checkbox" name="networks" value="${escapeHtml(n.fullName || n.name)}"> ${escapeHtml(n.name)}${networkHint(n)}</label>`)
    .join("");
  const myVolumes = (state.volumes || []).map((v) => v.name);
  const prefix = Number(state.me?.portPrefix || 0);
  const portHint = prefix > 0 ? t("create.portHintAssigned", { lo: prefix * 100, hi: prefix * 100 + 99 }) : t("create.portHintAsk");
  // Networks an admin marked for mudp forwarding behave differently enough to
  // say so: the host port still comes from the user's range, but it is relayed
  // to the container's own address instead of published by Docker (which does
  // not work on such a host). UDP mappings only work this way, so mention them
  // where they are relevant.
  const forwardNets = (state.networks || []).filter((n) => n.forward && n.attachable).map((n) => n.name);
  const forwardHint = forwardNets.length
    ? `<p class="hint">${t("create.forwardHint", { nets: escapeHtml(forwardNets.join(", ")) })}</p>`
    : "";
  showModal({
    kind: "create",
    title: t("create.title"),
    body:
      `<form id="newContainer" class="compact">` +
        `<input name="name" placeholder="${t("create.namePlaceholder")}" required>` +
        `<select name="image" required><option value="">${t("create.selectImage")}</option>${imageOptions}</select>` +
        gpuSelectHtml() +
        `<textarea name="env" placeholder="${t("create.envPlaceholder")}"></textarea>` +
        `<textarea name="ports" placeholder="${t("create.portsPlaceholder")}\n${escapeHtml(portHint)}"></textarea>` +
        `<textarea name="mounts" placeholder="${t("create.mountsPlaceholder")}${myVolumes.length ? '\n' + escapeHtml(t("create.mountsAvail")) + escapeHtml(myVolumes.join(', ')) : ''}"></textarea>` +
        (myNetworks ? `<label class="field-label">${t("create.networks")}</label><div class="check-grid">${myNetworks}</div>${forwardHint}` : "") +
        `<label class="field-label">${t("create.restartPolicy")}</label>` +
        `<select name="restartPolicy">` +
          `<option value="unless-stopped" selected>${t("create.policyUnlessStopped")}</option>` +
          `<option value="always">${t("create.policyAlways")}</option>` +
          `<option value="on-failure">${t("create.policyOnFailure")}</option>` +
          `<option value="no">${t("create.policyNo")}</option>` +
        `</select>` +

        `<label class="check"><input type="checkbox" name="forward8080"> ${t("create.forward8080")}</label>` +
        `<label class="check"><input type="checkbox" name="forward80"> ${t("create.forward80")}</label>` +
        `<label class="check"><input type="checkbox" name="mountNetdisk" checked> ${t("create.mountNetdisk")}</label>` +
        `<label class="check"><input type="checkbox" name="mountShm" checked> ${t("create.mountShm")}</label>` +
        // Collapsible advanced block. Empty fields inherit the image defaults
        // (the backend treats them as "unset"), so leaving this collapsed keeps
        // the simple wizard behavior for most users.
        `<details class="advanced-block"><summary>${t("create.advanced")}</summary>` +
          `<input name="command" placeholder="${t("create.commandPlaceholder")}">` +
          `<input name="entrypoint" placeholder="${t("create.entrypointPlaceholder")}">` +
          `<input name="workingDir" placeholder="${t("create.workingDirPlaceholder")}">` +
          `<input name="hostname" placeholder="${t("create.hostnamePlaceholder")}">` +
          `<input name="runUser" placeholder="${t("create.runUserPlaceholder")}">` +
          `<div class="advanced-row">` +
            `<input name="cpuLimit" type="number" min="0" step="0.5" placeholder="${t("create.cpuPlaceholder")}">` +
            `<input name="memoryMb" type="number" min="0" placeholder="${t("create.memoryPlaceholder")}">` +
            `<input name="pidsLimit" type="number" min="0" placeholder="${t("create.pidsPlaceholder")}">` +
          `</div>` +
          // cap-add grants host-wide privileges, so the backend accepts it from
          // admins only; hiding it for everyone else avoids offering a field whose
          // every non-empty value would be rejected. cap-drop only removes
          // privileges and stays available to all users.
          (isAdmin() ? `<input name="capAdd" placeholder="${t("create.capAddPlaceholder")}">` : "") +
          `<input name="capDrop" placeholder="${t("create.capDropPlaceholder")}">` +
          `<textarea name="labels" placeholder="${t("create.labelsPlaceholder")}"></textarea>` +
        `</details>` +
      `</form>`,
    foot: `<button class="ghost" data-close>${t("common.cancel")}</button><button class="primary" id="createSubmit">${t("create.createAndStart")}</button>`,
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
    // Device passthrough (NVIDIA device nodes, CDI devices) is not sent from here:
    // the backend applies the selected image's admin-defined preset itself, so GPUs
    // stay connected without the client being trusted for host device specs.
    lastPayload = payload;
    await streamCreate(payload);
  };
}

// networkHint labels a network that isn't one the user created themselves, and
// flags the ones whose ports mudp forwards rather than Docker publishing them.
function networkHint(n) {
  const forward = n.forward ? ` <span class="hint">${t("create.netHintForward")}</span>` : "";
  if (n.system) return ` <span class="hint">${t("create.netHintSystem")}</span>` + forward;
  if (n.external) return ` <span class="hint">${t("create.netHintHost")}</span>` + forward;
  if (n.shared) return ` <span class="hint">${t("create.netHintShared")}</span>` + forward;
  return forward;
}

// gpuSelectHtml renders the GPU dropdown based on the host's actual GPU count.
// Falls back to none/all when the count is unknown (non-GPU host or not yet loaded).
function gpuSelectHtml() {
  const count = Number(state.gpuCount || 0);
  let opts = `<option value="none">${t("create.noGpu")}</option><option value="all">${t("create.allGpus")}</option>`;
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
      const labels = stageLabels();
      const label = labels[s.stage] || s.stage;
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
        (state.create.error ? `<button class="primary" id="createRetry">${t("common.retry")}</button>` : ``) +
        `<button class="ghost" data-close>${state.create.error ? t("common.close") : t("common.hide")}</button>` +
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
      state.create.error = data.error || t("create.requestFailed", { status: res.status });
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
        state.create.error = data.message || t("create.creationFailed");
        state.create.logs += `[error] ${state.create.error}\n`;
        job.error(state.create.error);
        renderCreateProgress();
        toast(state.create.error);
      } else if (event === "done") {
        state.create.active = false;
        job.done(data.message || t("create.created"));
        renderCreateProgress();
        toast(t("create.created"), true);
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
      state.create.error = t("create.cancelled");
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
