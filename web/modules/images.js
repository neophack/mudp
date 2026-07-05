// Images list, pull modal with SSE progress, and image deletion.

import { state, api, toast, refreshAll, renderView, isAdmin, canMutate } from "../app.js";
import { showModal, setModalBody, closeModal, readSSE } from "./ui.js";

export function renderImages() {
  const admin = isAdmin();
  const canEdit = canMutate();
  $("#view").innerHTML =
    `<div class="card">` +
      `<div class="card-head"><h2>Images</h2>` +
        (canEdit ? `<div class="head-tools">` +
          `<button class="ghost" id="buildImageBtn" title="Build from Dockerfile">🔨 Build</button>` +
          `<button class="ghost" id="importImageBtn" title="Load tarball">⬆ Import</button>` +
          `<button class="primary" id="pullImageBtn">+ Pull Image</button>` +
        `</div>` : "") +
      `</div>` +
      `<table class="data">` +
        `<thead><tr><th>Name</th><th>Source</th>${admin ? "<th>Visible to</th>" : ""}<th class="actions">Actions</th></tr></thead>` +
        `<tbody>${state.images.map(imageRow).join("") || `<tr class="empty-row"><td colspan="${admin ? 4 : 3}">No images available${canEdit ? ". Click “+ Pull Image” to add one." : "."}</td></tr>`}</tbody>` +
      `</table>` +
    `</div>`;
  document.querySelectorAll("[data-image-delete]").forEach((btn) => {
    btn.onclick = () => deleteImage(btn.dataset.imageDelete, btn.dataset.imageRef);
  });
  const buildBtn = $("#buildImageBtn");
  if (buildBtn) buildBtn.onclick = openBuildModal;
  const importBtn = $("#importImageBtn");
  if (importBtn) importBtn.onclick = openImportModal;
}

function imageRow(image) {
  const admin = isAdmin();
  const canEdit = canMutate();
  return (
    `<tr>` +
      `<td><div class="primary-line">${escapeHtml(image.name)}</div><div class="secondary-line mono">${escapeHtml(image.dockerRef)}</div></td>` +
      `<td><div class="secondary-line">${escapeHtml(image.sourceRef)}</div></td>` +
      (admin ? `<td><div class="secondary-line">${escapeHtml((image.groups || []).join(", ") || "Unassigned")}</div></td>` : "") +
      `<td class="actions">${canEdit ? `<button class="icon danger" title="Delete" data-image-delete="${escapeHtml(image.id)}" data-image-ref="${escapeHtml(image.dockerRef)}">✕</button>` : "—"}</td>` +
    `</tr>`
  );
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
        setTimeout(async () => {
          closeModal();
          await refreshAll();
          renderView();
        }, 700);
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
    await refreshAll();
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
  showModal({
    kind: "build",
    title: "Build Image",
    body:
      `<form id="buildForm" class="compact">` +
        `<input name="tags" placeholder="Tags, comma-separated (e.g. myapp:1.0)" required>` +
        `<textarea name="buildArgs" placeholder="Build args, one KEY=VALUE per line (optional)"></textarea>` +
        `<label class="field-label">Dockerfile</label>` +
        `<textarea name="dockerfile" class="mono stack-editor" spellcheck="false">${escapeHtml(SAMPLE_DOCKERFILE)}</textarea>` +
      `</form>`,
    foot: `<button class="ghost" data-close>Cancel</button><button class="primary" id="buildSubmit">Build</button>`,
  });
  $("#buildSubmit").onclick = async () => {
    const fd = new FormData($("#buildForm"));
    const payload = {
      tags: String(fd.get("tags") || "").split(",").map((s) => s.trim()).filter(Boolean),
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
        setTimeout(async () => {
          closeModal();
          await refreshAll();
          renderView();
        }, 800);
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
        setTimeout(async () => {
          closeModal();
          await refreshAll();
          renderView();
        }, 800);
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
