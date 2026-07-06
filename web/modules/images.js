// Images list, pull modal with SSE progress, and image deletion.

import { state, api, toast, refreshSection, renderView, isAdmin } from "../app.js";
import { showModal, setModalBody, closeModal, readSSE } from "./ui.js";

export function renderImages() {
  const admin = isAdmin();
  // Image lifecycle (pull/build/import/register/delete) is admin-only: only admins
  // curate the image catalog. Users consume the images admins publish + configure.
  const canEdit = admin;
  $("#view").innerHTML =
    `<div class="card">` +
      `<div class="card-head"><h2>Images</h2>` +
        (canEdit ? `<div class="head-tools">` +
          `<button class="ghost" id="buildImageBtn" title="Build from Dockerfile">🔨 Build</button>` +
          `<button class="ghost" id="importImageBtn" title="Load tarball">⬆ Import</button>` +
          `<button class="ghost" id="registerImageBtn" title="Register existing local image">📋 Register</button>` +
          `<button class="primary" id="pullImageBtn">+ Pull Image</button>` +
        `</div>` : "") +
      `</div>` +
      `<table class="data">` +
        `<thead><tr><th>Name</th><th>Source</th>${admin ? "<th>Visible to</th>" : ""}<th>Defaults</th><th class="actions">Actions</th></tr></thead>` +
        `<tbody>${state.images.map((img) => imageRow(img, canEdit)).join("") || `<tr class="empty-row"><td colspan="${admin ? 5 : 4}">No images available${canEdit ? ". Click “+ Pull Image” to add one." : "."}</td></tr>`}</tbody>` +
      `</table>` +
    `</div>`;
  document.querySelectorAll("[data-image-delete]").forEach((btn) => {
    btn.onclick = () => deleteImage(btn.dataset.imageDelete, btn.dataset.imageRef);
  });
  document.querySelectorAll("[data-image-preset]").forEach((btn) => {
    btn.onclick = () => openPresetModal(btn.dataset.imagePreset);
  });
  const buildBtn = $("#buildImageBtn");
  if (buildBtn) buildBtn.onclick = openBuildModal;
  const importBtn = $("#importImageBtn");
  if (importBtn) importBtn.onclick = openImportModal;
  const registerBtn = $("#registerImageBtn");
  if (registerBtn) registerBtn.onclick = openRegisterModal;
}

function imageRow(image, canEdit) {
  const admin = isAdmin();
  const preset = image.preset || {};
  const summary = presetSummary(preset);
  return (
    `<tr>` +
      `<td><div class="primary-line">${escapeHtml(image.name)}</div><div class="secondary-line mono">${escapeHtml(image.dockerRef)}</div>${preset.description ? `<div class="secondary-line">📝 ${escapeHtml(preset.description)}</div>` : ""}</td>` +
      `<td><div class="secondary-line">${escapeHtml(image.sourceRef)}</div></td>` +
      (admin ? `<td><div class="secondary-line">${escapeHtml((image.groups || []).join(", ") || "Unassigned")}</div></td>` : "") +
      `<td><div class="secondary-line">${summary}</div></td>` +
      `<td class="actions">` +
        (canEdit ? `<button class="icon" title="Configure defaults" data-image-preset="${escapeHtml(image.id)}">⚙</button><button class="icon danger" title="Delete" data-image-delete="${escapeHtml(image.id)}" data-image-ref="${escapeHtml(image.dockerRef)}">✕</button>` : "—") +
      `</td>` +
    `</tr>`
  );
}

// presetSummary renders a compact one-line description of an image's configured
// defaults so admins can see at a glance which images are pre-wired.
function presetSummary(p) {
  if (!p) return "—";
  const bits = [];
  if (p.gpus) bits.push("GPU:" + p.gpus);
  if (p.ssh) bits.push("SSH");
  if (p.vscode) bits.push("VSCode");
  if ((p.ports || []).length) bits.push("ports:" + p.ports.join(","));
  if ((p.devices || []).length) bits.push("dev:" + p.devices.length);
  if ((p.cdiDevices || []).length) bits.push("cdi:" + p.cdiDevices.length);
  return bits.length ? escapeHtml(bits.join(" · ")) : "—";
}

// openPresetModal opens the admin "image defaults" editor for an image. The preset
// auto-fills the create-container form when a user picks this image. All fields are
// optional; unset booleans mean "let the user decide".
export function openPresetModal(imageId) {
  const image = state.images.find((i) => String(i.id) === String(imageId));
  if (!image) return;
  const p = image.preset || {};
  const networkChecks = state.networks
    .filter((n) => !n.system)
    .map((n) => `<label class="check"><input type="checkbox" name="networks" value="${escapeHtml(n.fullName || n.name)}" ${(p.networks || []).includes(n.fullName || n.name) ? "checked" : ""}> ${escapeHtml(n.name)}</label>`)
    .join("");
  showModal({
    kind: "preset",
    title: "Defaults · " + image.name,
    body:
      `<form id="presetForm" class="compact">` +
        `<p class="hint">These defaults auto-fill the create-container form when a user picks this image. Leave fields blank to let users decide.</p>` +
        `<input name="description" placeholder="Description (what this image contains, e.g. PyTorch 2.1 + CUDA 12)" value="${escapeHtml(p.description || "")}">` +
        `<label class="field-label">GPUs</label>` +
        `<select name="gpus"><option value="">(user decides)</option><option value="none"${p.gpus === "none" ? " selected" : ""}>none</option><option value="all"${p.gpus === "all" ? " selected" : ""}>all</option></select>` +
        `<label class="field-label">Environment variables (one KEY=VALUE per line, e.g. VNC_PW=secret)</label>` +
        `<textarea name="env" spellcheck="false">${escapeHtml((p.env || []).join("\n"))}</textarea>` +
        `<label class="field-label">Container ports to map (one per line, e.g. 8080)</label>` +
        `<textarea name="ports" spellcheck="false">${escapeHtml((p.ports || []).join("\n"))}</textarea>` +
        `<div class="check-grid">` +
          presetCheck("ssh", "Enable SSH", p.ssh) +
          presetCheck("vscode", "Enable VS Code Web", p.vscode) +
          presetCheck("forward8080", "Forward port 8080", p.forward8080) +
          presetCheck("forward80", "Forward port 80", p.forward80) +
          presetCheck("mountNetdisk", "Mount netdisk at /netdisk", p.mountNetdisk) +
        `</div>` +
        (networkChecks ? `<label class="field-label">Networks</label><div class="check-grid">${networkChecks}</div>` : "") +
        `<label class="field-label">Restart policy</label>` +
        `<select name="restartPolicy">` +
          `<option value="">(user decides)</option>` +
          `<option value="unless-stopped"${p.restartPolicy === "unless-stopped" ? " selected" : ""}>unless-stopped</option>` +
          `<option value="always"${p.restartPolicy === "always" ? " selected" : ""}>always</option>` +
          `<option value="on-failure"${p.restartPolicy === "on-failure" ? " selected" : ""}>on-failure</option>` +
          `<option value="no"${p.restartPolicy === "no" ? " selected" : ""}>no</option>` +
        `</select>` +
        `<label class="field-label">Devices (one --device per line, e.g. /dev/nvidia0)</label>` +
        `<textarea name="devices" spellcheck="false">${escapeHtml((p.devices || []).join("\n"))}</textarea>` +
        `<label class="field-label">CDI devices (one per line, e.g. nvidia.com/gpu=0)</label>` +
        `<textarea name="cdiDevices" spellcheck="false">${escapeHtml((p.cdiDevices || []).join("\n"))}</textarea>` +
      `</form>`,
    foot: `<button class="ghost" data-close>Cancel</button><button class="primary" id="presetSubmit">Save Defaults</button>`,
  });
  $("#presetSubmit").onclick = async () => {
    const form = $("#presetForm");
    const fd = new FormData(form);
    const preset = {
      description: (fd.get("description") || "").trim(),
      gpus: (fd.get("gpus") || "").trim(),
      env: lines(fd.get("env")),
      ports: lines(fd.get("ports")),
      ssh: form.querySelector('[name=ssh]').checked || undefined,
      vscode: form.querySelector('[name=vscode]').checked || undefined,
      forward8080: form.querySelector('[name=forward8080]').checked || undefined,
      forward80: form.querySelector('[name=forward80]').checked || undefined,
      mountNetdisk: form.querySelector('[name=mountNetdisk]').checked || undefined,
      networks: [...form.querySelectorAll('input[name=networks]:checked')].map((i) => i.value),
      restartPolicy: (fd.get("restartPolicy") || "").trim(),
      devices: lines(fd.get("devices")),
      cdiDevices: lines(fd.get("cdiDevices")),
    };
    $("#presetSubmit").disabled = true;
    try {
      await api("/api/images/preset", { method: "POST", body: JSON.stringify({ imageId: Number(imageId), preset }) });
      closeModal();
      await refreshSection("images");
      renderView();
      toast("Image defaults saved", true);
    } catch (err) {
      toast(err.message);
      $("#presetSubmit").disabled = false;
    }
  };
}

// presetCheck renders a labeled checkbox reflecting a preset boolean (undefined→unchecked).
function presetCheck(name, label, value) {
  return `<label class="check"><input type="checkbox" name="${name}" ${value ? "checked" : ""}> ${escapeHtml(label)}</label>`;
}

// lines splits a textarea value into trimmed, non-empty lines.
function lines(raw) {
  return String(raw || "").split("\n").map((s) => s.trim()).filter(Boolean);
}

export function openPullModal() {
  state.pull = { active: false, logs: "", error: "", name: "" };
  const groupChecks = state.groups
    .map((g) => `<label class="check"><input type="checkbox" name="groupIds" value="${g.id}"> ${escapeHtml(g.name)}</label>`)
    .join("");
  showModal({
    kind: "pull",
    title: "Pull Image",
    body:
      `<form id="pullForm" class="compact">` +
        `<input name="sourceRef" placeholder="Source image, e.g. ubuntu:22.04" required>` +
        `<input name="name" placeholder="Display name, e.g. ubuntu">` +
        `<div style="display:flex;flex-wrap:wrap;gap:8px 14px;">${groupChecks || '<span class="hint">No groups yet.</span>'}</div>` +
      `</form>`,
    foot: `<button class="ghost" data-close>Cancel</button><button class="primary" id="pullSubmit">Pull and Publish</button>`,
  });
  $("#pullSubmit").onclick = async () => {
    const form = $("#pullForm");
    const fd = new FormData(form);
    const payload = {
      sourceRef: fd.get("sourceRef"),
      name: fd.get("name"),
      groupIds: [...form.querySelectorAll("input[name=groupIds]:checked")].map((i) => Number(i.value)),
    };
    await streamPull(payload);
  };
}

export function renderPullProgress() {
  if (state.modal.kind !== "pull") return;
  setModalBody(
    (state.pull.error
      ? `<div class="error-box">✗ ${escapeHtml(state.pull.error)}</div>`
      : `<div class="step active"><span class="step-icon"><span class="spinner"></span></span><span class="step-label">Pulling ${escapeHtml(state.pull.name)}</span></div>`) +
      `<pre class="log-output">${escapeHtml(state.pull.logs || "")}</pre>` +
      `<div style="display:flex;gap:8px;justify-content:flex-end;">` +
        (state.pull.error ? `<button class="primary" id="pullRetry">Retry</button>` : ``) +
        `<button class="ghost" data-close>${state.pull.error ? "Close" : "Hide"}</button>` +
      `</div>`
  );
  const retry = $("#pullRetry");
  if (retry) retry.onclick = openPullModal;
  const log = document.querySelector(".modal-body .log-output");
  if (log) log.scrollTop = log.scrollHeight;
}

export async function streamPull(payload) {
  state.pull = { active: true, logs: "", error: "", name: payload.sourceRef };
  renderPullProgress();
  try {
    const res = await fetch("/api/images/pull/stream", {
      method: "POST",
      credentials: "same-origin",
      headers: { "Content-Type": "application/json", Accept: "text/event-stream" },
      body: JSON.stringify(payload),
    });
    if (!res.ok) {
      const data = await res.json().catch(() => ({}));
      state.pull.error = data.error || `Request failed (${res.status})`;
      state.pull.active = false;
      renderPullProgress();
      toast(state.pull.error);
      return;
    }
    await readSSE(res, (event, data) => {
      if (event === "progress") {
        state.pull.logs += (data.message || "") + "\n";
        renderPullProgress();
      } else if (event === "error") {
        state.pull.error = data.message || "Pull failed";
        state.pull.logs += `[error] ${state.pull.error}\n`;
        state.pull.active = false;
        renderPullProgress();
        toast(state.pull.error);
      } else if (event === "done") {
        state.pull.active = false;
        state.pull.logs += `[done] Published as ${data.dockerRef}\n`;
        renderPullProgress();
        toast("Image pulled", true);
        refreshSection("images").then(() => renderView());
        setTimeout(() => closeModal(), 700);
      }
    });
  } catch (err) {
    state.pull.error = err.message;
    state.pull.active = false;
    renderPullProgress();
    toast(err.message);
  }
}

export async function deleteImage(imageId, dockerRef) {
  if (!confirm("Delete this managed image?")) return;
  try {
    await api("/api/images/delete", {
      method: "POST",
      body: JSON.stringify({ imageId: Number(imageId), dockerRef }),
    });
    await refreshSection("images");
    renderView();
    toast("Image deleted", true);
  } catch (err) {
    toast(err.message);
  }
}

const SAMPLE_DOCKERFILE = `FROM ubuntu:22.04
RUN apt-get update && apt-get install -y curl
WORKDIR /workspace
`;

export function openBuildModal() {
  const groupChecks = state.groups
    .map((g) => `<label class="check"><input type="checkbox" name="groupIds" value="${g.id}"> ${escapeHtml(g.name)}</label>`)
    .join("");
  showModal({
    kind: "build",
    title: "Build Image",
    body:
      `<form id="buildForm" class="compact">` +
        `<input name="tags" placeholder="Tags, comma-separated (e.g. myapp:1.0)" required>` +
        `<input name="name" placeholder="Display name (optional, derived from first tag)">` +
        `<div style="display:flex;flex-wrap:wrap;gap:8px 14px;">${groupChecks || '<span class="hint">No groups yet.</span>'}</div>` +
        `<textarea name="buildArgs" placeholder="Build args, one KEY=VALUE per line (optional)"></textarea>` +
        `<label class="field-label">Dockerfile</label>` +
        `<textarea name="dockerfile" class="mono stack-editor" spellcheck="false">${escapeHtml(SAMPLE_DOCKERFILE)}</textarea>` +
      `</form>`,
    foot: `<button class="ghost" data-close>Cancel</button><button class="primary" id="buildSubmit">Build</button>`,
  });
  $("#buildSubmit").onclick = async () => {
    const form = $("#buildForm");
    const fd = new FormData(form);
    const payload = {
      tags: String(fd.get("tags") || "").split(",").map((s) => s.trim()).filter(Boolean),
      name: (fd.get("name") || "").trim(),
      groupIds: [...form.querySelectorAll("input[name=groupIds]:checked")].map((i) => Number(i.value)),
      buildArgs: parseKV(fd.get("buildArgs") || ""),
      dockerfile: fd.get("dockerfile"),
    };
    if (payload.tags.length === 0 || !payload.dockerfile) {
      toast("Tags and Dockerfile are required");
      return;
    }
    await streamBuild(payload);
  };
}

async function streamBuild(payload) {
  state.pull = { active: true, logs: "", error: "", name: payload.tags.join(", ") };
  renderBuildProgress();
  try {
    const res = await fetch("/api/images/build/stream", {
      method: "POST",
      credentials: "same-origin",
      headers: { "Content-Type": "application/json", Accept: "text/event-stream" },
      body: JSON.stringify(payload),
    });
    if (!res.ok) {
      const data = await res.json().catch(() => ({}));
      state.pull.error = data.error || `Build failed (${res.status})`;
      renderBuildProgress();
      toast(state.pull.error);
      return;
    }
    await readSSE(res, (event, data) => {
      if (event === "progress") {
        state.pull.logs += (data.message || "") + "\n";
        renderBuildProgress();
      } else if (event === "error") {
        state.pull.error = data.message || "Build failed";
        state.pull.logs += `[error] ${state.pull.error}\n`;
        renderBuildProgress();
        toast(state.pull.error);
      } else if (event === "done") {
        state.pull.active = false;
        state.pull.logs += `[done] Built ${(data.tags || []).join(", ")}\n`;
        renderBuildProgress();
        toast("Image built", true);
        refreshSection("images").then(() => renderView());
        setTimeout(() => closeModal(), 800);
      }
    });
  } catch (err) {
    state.pull.error = err.message;
    renderBuildProgress();
    toast(err.message);
  }
}

function renderBuildProgress() {
  if (state.modal.kind !== "build") return;
  setModalBody(
    (state.pull.error
      ? `<div class="error-box">✗ ${escapeHtml(state.pull.error)}</div>`
      : `<div class="step active"><span class="step-icon"><span class="spinner"></span></span><span class="step-label">Building ${escapeHtml(state.pull.name)}</span></div>`) +
      `<pre class="log-output">${escapeHtml(state.pull.logs || "")}</pre>` +
      `<div style="display:flex;gap:8px;justify-content:flex-end;">` +
        (state.pull.error ? `<button class="primary" id="buildRetry">Retry</button>` : ``) +
        `<button class="ghost" data-close>${state.pull.error ? "Close" : "Hide"}</button>` +
      `</div>`
  );
  const retry = $("#buildRetry");
  if (retry) retry.onclick = openBuildModal;
  const log = document.querySelector(".modal-body .log-output");
  if (log) log.scrollTop = log.scrollHeight;
}

export function openImportModal() {
  showModal({
    kind: "import",
    title: "Import Image (tar)",
    body:
      `<form id="importForm" class="compact">` +
        `<input type="file" name="file" accept=".tar" required>` +
        `<p class="hint">Load an image previously saved with <code>docker save</code>.</p>` +
      `</form>`,
    foot: `<button class="ghost" data-close>Cancel</button><button class="primary" id="importSubmit">Import</button>`,
  });
  $("#importSubmit").onclick = async () => {
    const fileInput = $("#importForm").querySelector('input[name="file"]');
    if (!fileInput.files[0]) {
      toast("Select a tar file");
      return;
    }
    await streamImport(fileInput.files[0]);
  };
}

async function streamImport(file) {
  state.pull = { active: true, logs: "Importing…\n", error: "", name: file.name };
  renderImportProgress();
  try {
    const res = await fetch("/api/images/import", {
      method: "POST",
      credentials: "same-origin",
      body: file,
      headers: { Accept: "text/event-stream" },
    });
    if (!res.ok) {
      const data = await res.json().catch(() => ({}));
      state.pull.error = data.error || `Import failed (${res.status})`;
      renderImportProgress();
      toast(state.pull.error);
      return;
    }
    await readSSE(res, (event, data) => {
      if (event === "progress") {
        state.pull.logs += (data.message || "") + "\n";
        renderImportProgress();
      } else if (event === "error") {
        state.pull.error = data.message || "Import failed";
        renderImportProgress();
        toast(state.pull.error);
      } else if (event === "done") {
        state.pull.logs += "[done] Image loaded\n";
        renderImportProgress();
        toast("Image imported", true);
        refreshSection("images").then(() => renderView());
        setTimeout(() => closeModal(), 800);
      }
    });
  } catch (err) {
    state.pull.error = err.message;
    renderImportProgress();
    toast(err.message);
  }
}

function renderImportProgress() {
  if (state.modal.kind !== "import") return;
  setModalBody(
    (state.pull.error ? `<div class="error-box">✗ ${escapeHtml(state.pull.error)}</div>` : "") +
      `<pre class="log-output">${escapeHtml(state.pull.logs || "")}</pre>` +
      `<div style="display:flex;gap:8px;justify-content:flex-end;"><button class="ghost" data-close>${state.pull.error ? "Close" : "Hide"}</button></div>`
  );
  const log = document.querySelector(".modal-body .log-output");
  if (log) log.scrollTop = log.scrollHeight;
}

export function openRegisterModal() {
  const groupChecks = state.groups
    .map((g) => `<label class="check"><input type="checkbox" name="groupIds" value="${g.id}"> ${escapeHtml(g.name)}</label>`)
    .join("");
  showModal({
    kind: "register",
    title: "Register Local Image",
    body:
      `<form id="registerForm" class="compact">` +
        `<input name="dockerRef" placeholder="Existing local image tag, e.g. ubuntu:22.04" required>` +
        `<input name="name" placeholder="Display name (optional, derived from tag)">` +
        `<div style="display:flex;flex-wrap:wrap;gap:8px 14px;">${groupChecks || '<span class="hint">No groups yet.</span>'}</div>` +
      `</form>`,
    foot: `<button class="ghost" data-close>Cancel</button><button class="primary" id="registerSubmit">Register</button>`,
  });
  $("#registerSubmit").onclick = async () => {
    const form = $("#registerForm");
    const fd = new FormData(form);
    const payload = {
      dockerRef: (fd.get("dockerRef") || "").trim(),
      name: (fd.get("name") || "").trim(),
      groupIds: [...form.querySelectorAll("input[name=groupIds]:checked")].map((i) => Number(i.value)),
    };
    if (!payload.dockerRef) {
      toast("Image tag is required");
      return;
    }
    $("#registerSubmit").disabled = true;
    try {
      await api("/api/images/register", {
        method: "POST",
        body: JSON.stringify(payload),
      });
      closeModal();
      await refreshSection("images");
      renderView();
      toast("Image registered", true);
    } catch (err) {
      toast(err.message);
      $("#registerSubmit").disabled = false;
    }
  };
}

function parseKV(raw) {
  const out = {};
  for (const line of String(raw).split("\n")) {
    const [k, ...rest] = line.split("=");
    if (k && rest.length) out[k.trim()] = rest.join("=").trim();
  }
  return out;
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
