// Images list, pull modal with SSE progress, and image deletion.

import { state, api, toast, refreshSection, renderView, isAdmin, readCSRFCookie, t } from "../app.js";
import { showModal, setModalBody, closeModal, readSSE } from "./ui.js";
import { registerJob } from "./jobs.js";

export function renderImages() {
  const admin = isAdmin();
  // Image lifecycle (pull/build/import/register/delete) is admin-only: only admins
  // curate the image catalog. Users consume the images admins publish + configure.
  const canEdit = admin;
  $("#view").innerHTML =
    `<div class="card">` +
      `<div class="card-head"><h2>${t("images.title")}</h2>` +
        (canEdit ? `<div class="head-tools">` +
          `<button class="ghost" id="buildImageBtn" title="${t("images.buildTitle")}">${t("images.build")}</button>` +
          `<button class="ghost" id="importImageBtn" title="${t("images.importTitle")}">${t("images.import")}</button>` +
          `<button class="ghost" id="registerImageBtn" title="${t("images.registerTitle")}">${t("images.register")}</button>` +
          `<button class="primary" id="pullImageBtn">${t("images.pull")}</button>` +
        `</div>` : "") +
      `</div>` +
      `<table class="data">` +
        `<thead><tr><th>${t("common.name")}</th><th>${t("images.colSource")}</th>${admin ? `<th>${t("images.colVisible")}</th>` : ""}<th>${t("images.colDefaults")}</th><th class="actions">${t("common.actions")}</th></tr></thead>` +
        `<tbody>${state.images.map((img) => imageRow(img, canEdit)).join("") || `<tr class="empty-row"><td colspan="${admin ? 5 : 4}">${canEdit ? t("images.noImagesAdmin") : t("images.noImages")}</td></tr>`}</tbody>` +
      `</table>` +
    `</div>`;
  document.querySelectorAll("[data-image-delete]").forEach((btn) => {
    btn.onclick = () => deleteImage(btn.dataset.imageDelete, btn.dataset.imageRef);
  });
  document.querySelectorAll("[data-image-preset]").forEach((btn) => {
    btn.onclick = () => openPresetModal(btn.dataset.imagePreset);
  });
  document.querySelectorAll("[data-image-reregister]").forEach((btn) => {
    btn.onclick = () => openReRegisterModal(btn.dataset.imageReregister);
  });
  const buildBtn = $("#buildImageBtn");
  if (buildBtn) buildBtn.onclick = openBuildModal;
  const importBtn = $("#importImageBtn");
  if (importBtn) importBtn.onclick = openImportModal;
  const registerBtn = $("#registerImageBtn");
  if (registerBtn) registerBtn.onclick = openRegisterModal;
  const pullBtn = $("#pullImageBtn");
  if (pullBtn) pullBtn.onclick = openPullModal;
}

function imageRow(image, canEdit) {
  const admin = isAdmin();
  const preset = image.preset || {};
  const summary = presetSummary(preset);
  // A stale row's name is a raw Docker image ID (registered/committed by id);
  // badge it and offer a re-register action so it can be given a real name.
  const stale = image.isStale;
  const staleBadge = stale
    ? ` <span class="badge badge-warn" title="${t("images.staleTitle")}">${t("images.staleBadge")}</span>`
    : "";
  return (
    `<tr>` +
      `<td><div class="primary-line">${escapeHtml(image.name)}${staleBadge}</div><div class="secondary-line mono">${escapeHtml(image.dockerRef)}</div>${preset.description ? `<div class="secondary-line">📝 ${escapeHtml(preset.description)}</div>` : ""}</td>` +
      `<td><div class="secondary-line">${escapeHtml(image.sourceRef)}</div></td>` +
      (admin ? `<td><div class="secondary-line">${escapeHtml((image.groups || []).join(", ") || t("images.allUsers"))}</div></td>` : "") +
      `<td><div class="secondary-line">${summary}</div></td>` +
      `<td class="actions">` +
        (canEdit
          ? (stale ? `<button class="icon" title="${t("images.reRegisterTitle")}" data-image-reregister="${escapeHtml(image.id)}">⟳</button>` : "") +
            `<button class="icon" title="${t("images.presetHint")}" data-image-preset="${escapeHtml(image.id)}">⚙</button><button class="icon danger" title="${t("common.delete")}" data-image-delete="${escapeHtml(image.id)}" data-image-ref="${escapeHtml(image.dockerRef)}">✕</button>`
          : "—") +
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

  if ((p.ports || []).length) bits.push("ports:" + p.ports.join(","));
  if ((p.devices || []).length) bits.push("dev:" + p.devices.length);
  if ((p.cdiDevices || []).length) bits.push("cdi:" + p.cdiDevices.length);
  if (p.novncPasswordEnv) bits.push("noVNC:" + p.novncPasswordEnv);
  return bits.length ? escapeHtml(bits.join(" · ")) : "—";
}

// openPresetModal opens the admin "image defaults" editor for an image. The preset
// auto-fills the create-container form when a user picks this image. All fields are
// optional; unset booleans mean "let the user decide".
export function openPresetModal(imageId) {
  const image = state.images.find((i) => String(i.id) === String(imageId));
  if (!image) return;
  const p = image.preset || {};
  // Selectable networks form the candidate pool an admin exposes for this image.
  // The user creating a container from it can only pick networks in this pool
  // (intersected with what they can attach). Default networks below are the
  // subset pre-checked on create — they must live inside this pool.
  const poolNets = state.networks.filter((n) => !n.system);
  const selectableChecks = poolNets
    .map((n) => {
      const val = escapeHtml(n.fullName || n.name);
      const checked = (p.selectableNetworks || []).includes(n.fullName || n.name) ? "checked" : "";
      return `<label class="check"><input type="checkbox" name="selectableNetworks" value="${val}" ${checked}> ${escapeHtml(n.name)}</label>`;
    })
    .join("");
  // renderDefaultNetworks rebuilds the "default networks" grid from the values
  // currently checked in the selectable pool. It is called once for the initial
  // body (with the preset's saved selectable set) and again on every change so
  // the two lists can't drift — a default can never point at a network the admin
  // didn't expose as selectable. A network stays checked across a rebuild when it
  // was either a saved preset default or checked by the admin since opening.
  const renderDefaultNetworks = (selectedValues, alsoChecked) => {
    const extra = alsoChecked || new Set();
    const saved = new Set(p.networks || []);
    const items = selectedValues.map((val) => {
      const n = state.networks.find((x) => (x.fullName || x.name) === val);
      const checked = saved.has(val) || extra.has(val) ? "checked" : "";
      return `<label class="check"><input type="checkbox" name="networks" value="${escapeHtml(val)}" ${checked}> ${escapeHtml(n ? n.name : val)}</label>`;
    });
    return items.join("") || `<span class="hint">${t("images.defaultNetworksHint")}</span>`;
  };
  // The initially selectable set comes straight from the saved preset, since the
  // form isn't in the DOM yet when the body string is built.
  const initialSelectable = (p.selectableNetworks || []).slice();
  const groupChecks = (state.groups || [])
    .map((g) => {
      const checked = (image.groups || []).includes(g.name) ? "checked" : "";
      return `<label class="check"><input type="checkbox" name="groupIds" value="${g.id}" ${checked}> ${escapeHtml(g.name)}</label>`;
    })
    .join("");
  showModal({
    kind: "preset",
    title: t("images.titleEdit", { name: image.name }),
    body:
      `<form id="presetForm" class="compact">` +
        `<p class="hint">${t("images.presetHint")}</p>` +
        `<input name="description" placeholder="${t("images.descPlaceholder")}" value="${escapeHtml(p.description || "")}">` +
        `<label class="field-label">${t("images.gpus")}</label>` +
        `<select name="gpus"><option value="">${t("images.gpusUserDecides")}</option><option value="none"${p.gpus === "none" ? " selected" : ""}>none</option><option value="all"${p.gpus === "all" ? " selected" : ""}>all</option></select>` +
        `<label class="field-label">${t("images.envLabel")}</label>` +
        `<textarea name="env" id="presetEnvField" spellcheck="false">${escapeHtml((p.env || []).join("\n"))}</textarea>` +
        `<div style="display:flex;gap:8px;align-items:center;margin:-4px 0 6px;">` +
          `<select id="envGenType">` +
            `<option value="random_password:16">${t("images.genRandomPassword")}</option>` +
            `<option value="random_number:6">${t("images.genRandomNumber")}</option>` +
            `<option value="random_string:12">${t("images.genRandomString")}</option>` +
            `<option value="sequence:4">${t("images.genSequence")}</option>` +
          `</select>` +
          `<button type="button" class="ghost" id="envGenInsert">${t("images.genInsert")}</button>` +
        `</div>` +
        `<p class="hint">${t("images.envGenHint")}</p>` +
        `<label class="field-label">${t("images.portsLabel")}</label>` +
        `<textarea name="ports" spellcheck="false">${escapeHtml((p.ports || []).join("\n"))}</textarea>` +
        `<div class="check-grid">` +
          presetCheck("forward8080", t("images.forward8080"), p.forward8080) +
          presetCheck("forward8090", t("images.forward8090"), p.forward8090) +
          presetCheck("mountNetdisk", t("images.presetNetdisk"), p.mountNetdisk) +
          presetCheck("mountShm", t("images.presetShm"), p.mountShm) +
          presetCheck("mountSharedDisk", t("images.presetSharedDisk"), p.mountSharedDisk) +
          presetCheck("requireLogin", t("images.requireLogin"), p.requireLogin) +
        `</div>` +
        `<label class="field-label">${t("images.novncPasswordLabel")}</label>` +
        `<input name="novncPasswordEnv" placeholder="VNC_PW" value="${escapeHtml(p.novncPasswordEnv || "")}">` +
        `<p class="hint">${t("images.novncPasswordHint")}</p>` +
        (selectableChecks
          ? `<label class="field-label">${t("images.selectableNetworks")}</label><div class="check-grid" id="selectableNetworksGrid">${selectableChecks}</div>` +
            `<p class="hint">${t("images.selectableNetworksHint")}</p>` +
            `<label class="field-label">${t("images.defaultNetworks")}</label><div class="check-grid" id="defaultNetworksGrid">${renderDefaultNetworks(initialSelectable)}</div>` +
            `<p class="hint">${t("images.defaultNetworksHint")}</p>`
          : "") +
        `<label class="field-label">${t("images.restartPolicy")}</label>` +
        `<select name="restartPolicy">` +
          `<option value="">${t("images.gpusUserDecides")}</option>` +
          `<option value="unless-stopped"${p.restartPolicy === "unless-stopped" ? " selected" : ""}>unless-stopped</option>` +
          `<option value="always"${p.restartPolicy === "always" ? " selected" : ""}>always</option>` +
          `<option value="on-failure"${p.restartPolicy === "on-failure" ? " selected" : ""}>on-failure</option>` +
          `<option value="no"${p.restartPolicy === "no" ? " selected" : ""}>no</option>` +
        `</select>` +
        `<label class="field-label">${t("images.devices")}</label>` +
        `<textarea name="devices" spellcheck="false">${escapeHtml((p.devices && p.devices.length ? p.devices : DEFAULT_NVIDIA_DEVICES).join("\n"))}</textarea>` +
        `<label class="field-label">${t("images.cdiDevices")}</label>` +
        `<textarea name="cdiDevices" spellcheck="false">${escapeHtml((p.cdiDevices || []).join("\n"))}</textarea>` +
        `<label class="field-label">${t("images.visibleTo")}</label>` +
        `<div class="check-grid">${groupChecks || `<span class="hint">${t("images.noGroupsHint")}</span>`}</div>` +
        `<p class="hint">${t("images.visibleHint")}</p>` +
      `</form>`,
    foot: `<button class="ghost" data-close>${t("common.cancel")}</button><button class="primary" id="presetSubmit">${t("common.save")}</button>`,
  });
  $("#envGenInsert").onclick = () => {
    insertAtCursor($("#presetEnvField"), "{{" + $("#envGenType").value + "}}");
  };
  // Keep the default-networks grid in lockstep with the selectable pool: any
  // change to the pool rebuilds the defaults list so a default can never reference
  // a network that is no longer selectable. The previously-checked defaults for
  // networks still in the pool are preserved across the rebuild.
  const refreshDefaultNetworks = () => {
    const grid = $("#defaultNetworksGrid");
    if (!grid) return;
    const previouslyChecked = new Set([...document.querySelectorAll('input[name=networks]:checked')].map((i) => i.value));
    const selected = [...document.querySelectorAll('input[name=selectableNetworks]:checked')].map((i) => i.value);
    grid.innerHTML = renderDefaultNetworks(selected, previouslyChecked);
  };
  const selGrid = $("#selectableNetworksGrid");
  if (selGrid) selGrid.addEventListener("change", refreshDefaultNetworks);
  $("#presetSubmit").onclick = async () => {
    const form = $("#presetForm");
    const fd = new FormData(form);
    const selectableNetworks = [...form.querySelectorAll('input[name=selectableNetworks]:checked')].map((i) => i.value);
    const networks = [...form.querySelectorAll('input[name=networks]:checked')].map((i) => i.value);
    // Client-side mirror of the backend ValidatePreset check: a default network
    // must be among the selectable pool. The backend rejects it anyway, but
    // failing here gives immediate feedback without a round-trip.
    if (selectableNetworks.length && networks.some((n) => !selectableNetworks.includes(n))) {
      toast(t("images.defaultNetworkNotSelectable"));
      return;
    }
    const preset = {
      description: (fd.get("description") || "").trim(),
      gpus: (fd.get("gpus") || "").trim(),
      env: lines(fd.get("env")),
      ports: lines(fd.get("ports")),
      forward8080: form.querySelector('[name=forward8080]').checked || undefined,
      forward8090: form.querySelector('[name=forward8090]').checked || undefined,
      mountNetdisk: form.querySelector('[name=mountNetdisk]').checked || undefined,
      mountShm: form.querySelector('[name=mountShm]').checked || undefined,
      mountSharedDisk: form.querySelector('[name=mountSharedDisk]').checked || undefined,
      requireLogin: form.querySelector('[name=requireLogin]').checked || undefined,
      novncPasswordEnv: (fd.get("novncPasswordEnv") || "").trim(),
      selectableNetworks,
      networks,
      restartPolicy: (fd.get("restartPolicy") || "").trim(),
      devices: lines(fd.get("devices")),
      cdiDevices: lines(fd.get("cdiDevices")),
    };
    const groupIds = [...form.querySelectorAll("input[name=groupIds]:checked")].map((i) => Number(i.value));
    $("#presetSubmit").disabled = true;
    try {
      await Promise.all([
        api("/api/images/preset", { method: "POST", body: JSON.stringify({ imageId: Number(imageId), preset }) }),
        api("/api/images/groups", { method: "POST", body: JSON.stringify({ imageId: Number(imageId), groupIds }) }),
      ]);
      closeModal();
      await refreshSection("images");
      renderView();
      toast(t("images.presetUpdated"), true);
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

// insertAtCursor inserts text into a textarea at the current cursor position (or
// appends at the end if it isn't focused), replacing any selection.
function insertAtCursor(textarea, text) {
  const start = textarea.selectionStart ?? textarea.value.length;
  const end = textarea.selectionEnd ?? textarea.value.length;
  textarea.value = textarea.value.slice(0, start) + text + textarea.value.slice(end);
  const pos = start + text.length;
  textarea.focus();
  textarea.setSelectionRange(pos, pos);
}

// DEFAULT_NVIDIA_DEVICES pre-fills the devices field for images that need direct
// /dev access to NVIDIA GPUs (e.g. runtimes without --gpus/CDI support).
const DEFAULT_NVIDIA_DEVICES = [
  "/dev/nvidia0",
  "/dev/nvidia1",
  "/dev/nvidia2",
  "/dev/nvidia3",
  "/dev/nvidiactl",
  "/dev/nvidia-uvm",
  "/dev/nvidia-uvm-tools",
];

export function openPullModal() {
  state.pull = { active: false, logs: "", error: "", name: "" };
  const groupChecks = state.groups
    .map((g) => `<label class="check"><input type="checkbox" name="groupIds" value="${g.id}"> ${escapeHtml(g.name)}</label>`)
    .join("");
  showModal({
    kind: "pull",
    title: t("images.pullTitle"),
    body:
      `<form id="pullForm" class="compact">` +
        `<input name="sourceRef" placeholder="${t("images.pullSourcePlaceholder")}" required>` +
        `<input name="name" placeholder="${t("images.pullNamePlaceholder")}">` +
        `<div style="display:flex;flex-wrap:wrap;gap:8px 14px;">${groupChecks || `<span class="hint">${t("networks.noGroupsYet")}</span>`}</div>` +
      `</form>`,
    foot: `<button class="ghost" data-close>${t("common.cancel")}</button><button class="primary" id="pullSubmit">${t("images.pullPublish")}</button>`,
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
      : `<div class="step active"><span class="step-icon"><span class="spinner"></span></span><span class="step-label">${t("images.pulling", { name: escapeHtml(state.pull.name) })}</span></div>`) +
      `<pre class="log-output">${escapeHtml(state.pull.logs || "")}</pre>` +
      `<div style="display:flex;gap:8px;justify-content:flex-end;">` +
        (state.pull.error ? `<button class="primary" id="pullRetry">${t("common.retry")}</button>` : ``) +
        `<button class="ghost" data-close>${state.pull.error ? t("common.close") : t("common.hide")}</button>` +
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
  const job = registerJob({ kind: "image.pull", name: payload.sourceRef });
  try {
    const res = await fetch("/api/images/pull/stream", {
      method: "POST",
      credentials: "same-origin",
      headers: { "Content-Type": "application/json", Accept: "text/event-stream", "X-CSRF-Token": readCSRFCookie() || state.csrfToken || "" },
      body: JSON.stringify(payload),
      signal: job.signal,
    });
    if (!res.ok) {
      const data = await res.json().catch(() => ({}));
      state.pull.error = data.error || t("create.requestFailed", { status: res.status });
      state.pull.active = false;
      job.error(state.pull.error);
      renderPullProgress();
      toast(state.pull.error);
      return;
    }
    await readSSE(res, (event, data) => {
      if (event === "progress") {
        const line = data.message || "";
        state.pull.logs += line + "\n";
        job.log(line);
        renderPullProgress();
      } else if (event === "error") {
        state.pull.error = data.message || t("images.pullFailed");
        state.pull.logs += `[error] ${state.pull.error}\n`;
        state.pull.active = false;
        job.error(state.pull.error);
        renderPullProgress();
        toast(state.pull.error);
      } else if (event === "done") {
        state.pull.active = false;
        state.pull.logs += `[done] ${t("images.publishedAs", { ref: data.dockerRef })}\n`;
        job.done(t("images.publishedAs", { ref: data.dockerRef }));
        renderPullProgress();
        toast(t("images.imagePulled"), true);
        refreshSection("images").then(() => renderView());
        setTimeout(() => closeModal(), 700);
      } else if (event === "cancelled") {
        state.pull.active = false;
        state.pull.logs += `[cancelled] ${data.message || ""}\n`;
        job.cancel();
        renderPullProgress();
      }
    });
  } catch (err) {
    state.pull.active = false;
    if (job.signal.aborted) {
      state.pull.error = t("create.cancelled");
      job.cancel();
    } else {
      state.pull.error = err.message;
      job.error(err.message);
    }
    state.pull.logs += `[error] ${state.pull.error}\n`;
    renderPullProgress();
    toast(state.pull.error);
  }
}

export async function deleteImage(imageId, dockerRef) {
  if (!confirm(t("images.deleteConfirm"))) return;
  try {
    await api("/api/images/delete", {
      method: "POST",
      body: JSON.stringify({ imageId: Number(imageId), dockerRef }),
    });
    await refreshSection("images");
    renderView();
    toast(t("images.imageDeleted"), true);
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
    title: t("images.buildTitle2"),
    body:
      `<form id="buildForm" class="compact">` +
        `<input name="tags" placeholder="${t("images.tagsPlaceholder")}" required>` +
        `<input name="name" placeholder="${t("images.namePlaceholder2")}">` +
        `<div style="display:flex;flex-wrap:wrap;gap:8px 14px;">${groupChecks || `<span class="hint">${t("networks.noGroupsYet")}</span>`}</div>` +
        `<textarea name="buildArgs" placeholder="${t("images.buildArgsPlaceholder")}"></textarea>` +
        `<label class="field-label">${t("images.dockerfile")}</label>` +
        `<textarea name="dockerfile" class="mono stack-editor" spellcheck="false">${escapeHtml(SAMPLE_DOCKERFILE)}</textarea>` +
      `</form>`,
    foot: `<button class="ghost" data-close>${t("common.cancel")}</button><button class="primary" id="buildSubmit">${t("images.build")}</button>`,
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
      toast(t("images.tagsRequired"));
      return;
    }
    await streamBuild(payload);
  };
}

async function streamBuild(payload) {
  state.pull = { active: true, logs: "", error: "", name: payload.tags.join(", ") };
  renderBuildProgress();
  const job = registerJob({ kind: "image.build", name: payload.tags.join(", ") });
  try {
    const res = await fetch("/api/images/build/stream", {
      method: "POST",
      credentials: "same-origin",
      headers: { "Content-Type": "application/json", Accept: "text/event-stream", "X-CSRF-Token": readCSRFCookie() || state.csrfToken || "" },
      body: JSON.stringify(payload),
      signal: job.signal,
    });
    if (!res.ok) {
      const data = await res.json().catch(() => ({}));
      state.pull.error = data.error || t("images.buildFailed2", { status: res.status });
      job.error(state.pull.error);
      renderBuildProgress();
      toast(state.pull.error);
      return;
    }
    await readSSE(res, (event, data) => {
      if (event === "progress") {
        const line = data.message || "";
        state.pull.logs += line + "\n";
        job.log(line);
        renderBuildProgress();
      } else if (event === "error") {
        state.pull.error = data.message || t("images.buildFailed");
        state.pull.logs += `[error] ${state.pull.error}\n`;
        job.error(state.pull.error);
        renderBuildProgress();
        toast(state.pull.error);
      } else if (event === "done") {
        state.pull.active = false;
        const tagList = (data.tags || []).join(", ");
        state.pull.logs += `[done] ${t("images.built", { tags: tagList })}\n`;
        job.done(t("images.built", { tags: tagList }));
        renderBuildProgress();
        toast(t("images.imageBuilt"), true);
        refreshSection("images").then(() => renderView());
        setTimeout(() => closeModal(), 800);
      } else if (event === "cancelled") {
        state.pull.active = false;
        state.pull.logs += `[cancelled] ${data.message || ""}\n`;
        job.cancel();
        renderBuildProgress();
      }
    });
  } catch (err) {
    if (job.signal.aborted) {
      state.pull.error = t("create.cancelled");
      job.cancel();
    } else {
      state.pull.error = err.message;
      job.error(err.message);
    }
    state.pull.logs += `[error] ${state.pull.error}\n`;
    renderBuildProgress();
    toast(state.pull.error);
  }
}

function renderBuildProgress() {
  if (state.modal.kind !== "build") return;
  setModalBody(
    (state.pull.error
      ? `<div class="error-box">✗ ${escapeHtml(state.pull.error)}</div>`
      : `<div class="step active"><span class="step-icon"><span class="spinner"></span></span><span class="step-label">${t("images.building", { name: escapeHtml(state.pull.name) })}</span></div>`) +
      `<pre class="log-output">${escapeHtml(state.pull.logs || "")}</pre>` +
      `<div style="display:flex;gap:8px;justify-content:flex-end;">` +
        (state.pull.error ? `<button class="primary" id="buildRetry">${t("common.retry")}</button>` : ``) +
        `<button class="ghost" data-close>${state.pull.error ? t("common.close") : t("common.hide")}</button>` +
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
    title: t("images.importTitle2"),
    body:
      `<form id="importForm" class="compact">` +
        `<input type="file" name="file" accept=".tar" required>` +
        `<p class="hint">${t("images.importHint")}</p>` +
      `</form>`,
    foot: `<button class="ghost" data-close>${t("common.cancel")}</button><button class="primary" id="importSubmit">${t("images.import2")}</button>`,
  });
  $("#importSubmit").onclick = async () => {
    const fileInput = $("#importForm").querySelector('input[name="file"]');
    if (!fileInput.files[0]) {
      toast(t("images.selectTar"));
      return;
    }
    await streamImport(fileInput.files[0]);
  };
}

async function streamImport(file) {
  state.pull = { active: true, logs: t("images.importing") + "\n", error: "", name: file.name };
  renderImportProgress();
  const job = registerJob({ kind: "image.import", name: file.name });
  try {
    const res = await fetch("/api/images/import", {
      method: "POST",
      credentials: "same-origin",
      body: file,
      headers: { Accept: "text/event-stream", "X-CSRF-Token": readCSRFCookie() || state.csrfToken || "" },
      signal: job.signal,
    });
    if (!res.ok) {
      const data = await res.json().catch(() => ({}));
      state.pull.error = data.error || t("images.importFailed", { status: res.status });
      job.error(state.pull.error);
      renderImportProgress();
      toast(state.pull.error);
      return;
    }
    await readSSE(res, (event, data) => {
      if (event === "progress") {
        const line = data.message || "";
        state.pull.logs += line + "\n";
        job.log(line);
        renderImportProgress();
      } else if (event === "error") {
        state.pull.error = data.message || t("images.importFailed2");
        job.error(state.pull.error);
        renderImportProgress();
        toast(state.pull.error);
      } else if (event === "done") {
        state.pull.logs += `[done] ${t("images.imageLoaded")}\n`;
        job.done(t("images.imageLoaded"));
        renderImportProgress();
        toast(t("images.imageImported"), true);
        refreshSection("images").then(() => renderView());
        setTimeout(() => closeModal(), 800);
      } else if (event === "cancelled") {
        state.pull.active = false;
        state.pull.logs += `[cancelled] ${data.message || ""}\n`;
        job.cancel();
        renderImportProgress();
      }
    });
  } catch (err) {
    if (job.signal.aborted) {
      state.pull.error = t("create.cancelled");
      job.cancel();
    } else {
      state.pull.error = err.message;
      job.error(err.message);
    }
    renderImportProgress();
    toast(state.pull.error);
  }
}

function renderImportProgress() {
  if (state.modal.kind !== "import") return;
  setModalBody(
    (state.pull.error ? `<div class="error-box">✗ ${escapeHtml(state.pull.error)}</div>` : "") +
      `<pre class="log-output">${escapeHtml(state.pull.logs || "")}</pre>` +
      `<div style="display:flex;gap:8px;justify-content:flex-end;"><button class="ghost" data-close>${state.pull.error ? t("common.close") : t("common.hide")}</button></div>`
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
    title: t("images.registerTitle"),
    body:
      `<form id="registerForm" class="compact">` +
        `<input name="dockerRef" placeholder="${t("images.registerRefPlaceholder")}" required>` +
        `<input name="name" placeholder="${t("images.registerNamePlaceholder")}">` +
        `<div style="display:flex;flex-wrap:wrap;gap:8px 14px;">${groupChecks || `<span class="hint">${t("networks.noGroupsYet")}</span>`}</div>` +
      `</form>`,
    foot: `<button class="ghost" data-close>${t("common.cancel")}</button><button class="primary" id="registerSubmit">${t("images.register2")}</button>`,
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
      toast(t("images.tagRequired"));
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
      toast(t("images.imageRegistered"), true);
    } catch (err) {
      toast(err.message);
      $("#registerSubmit").disabled = false;
    }
  };
}

// openReRegisterModal re-tags an existing catalog row under a new display name.
// It is the fix-up for stale rows whose name is a raw image ID: pre-fill a
// sensible name from the source ref when possible, otherwise leave it blank.
export function openReRegisterModal(imageId) {
  const image = state.images.find((i) => String(i.id) === String(imageId));
  if (!image) return;
  // Suggest a name from the source ref unless that itself looks like a hash.
  let suggested = "";
  if (image.sourceRef) {
    const base = String(image.sourceRef).split("/").pop().split(":")[0];
    suggested = /^[0-9a-f]{12,}$/i.test(base) ? "" : base;
  }
  showModal({
    kind: "reregister",
    title: t("images.reRegisterTitle"),
    body:
      `<form id="reRegisterForm" class="compact">` +
        `<p class="hint">${escapeHtml(t("images.reRegisterHint", { name: image.name }))}</p>` +
        `<input name="name" placeholder="${t("images.reRegisterNamePlaceholder")}" value="${escapeHtml(suggested)}" required>` +
      `</form>`,
    foot: `<button class="ghost" data-close>${t("common.cancel")}</button><button class="primary" id="reRegisterSubmit">${t("images.reRegister")}</button>`,
  });
  $("#reRegisterSubmit").onclick = async () => {
    const form = $("#reRegisterForm");
    const fd = new FormData(form);
    const name = (fd.get("name") || "").trim();
    if (!name) {
      toast(t("images.reRegisterNameRequired"));
      return;
    }
    $("#reRegisterSubmit").disabled = true;
    try {
      await api("/api/images/reregister", {
        method: "POST",
        body: JSON.stringify({ imageId: Number(imageId), name }),
      });
      closeModal();
      await refreshSection("images");
      renderView();
      toast(t("images.imageReRegistered"), true);
    } catch (err) {
      toast(err.message);
      $("#reRegisterSubmit").disabled = false;
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
