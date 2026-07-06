// Bootstrap page: bootstrap scripts, offline packages, build script.

import { state, api, toast, refreshAll, renderView, escapeHtml } from "../app.js";
import { showModal, setModalBody, closeModal, readSSE } from "./ui.js";

export function renderBootstrap() {
  if (!state.offlinePackagesLoaded) loadOfflinePackages();
  if (!state.buildScriptLoaded) loadBuildScript();

  const pkgs = state.offlinePackages || [];

  $("#view").innerHTML =
    `<div class="stack">` +
      // ── Bootstrap Scripts ──
      `<div class="card"><div class="card-head"><h2>Bootstrap Scripts</h2><div class="head-tools">` +
        `<button class="primary" id="buildSshBtn">Build SSH Offline Pkg</button>` +
        `<button class="primary" id="buildVscodeBtn">Build VS Code Offline Pkg</button>` +
      `</div></div>` +
      `<div class="card-body"><form id="scriptSettings" class="compact">` +
        `<h3>SSH Bootstrap</h3>` +
        `<p class="hint">Available env vars: <code>$MUDP_ACCESS_PASSWORD</code> · <code>$MUDP_CONNECTION_USER</code> · <code>$MUDP_OFFLINE_PACKAGE_DIR</code></p>` +
        `<textarea name="sshScript" class="mono" rows="10" spellcheck="false">${escapeHtml(state.scripts.sshScript || "")}</textarea>` +
        `<h3>VS Code Bootstrap</h3>` +
        `<textarea name="vscodeScript" class="mono" rows="10" spellcheck="false">${escapeHtml(state.scripts.vscodeScript || "")}</textarea>` +
        `<button>Save Scripts</button>` +
      `</form></div></div>` +

      // ── Offline Packages ──
      `<div class="card"><div class="card-head"><h2>Offline Packages</h2>` +
        `<div class="head-tools"><button class="primary" id="uploadPkgBtn">+ Upload Package</button></div>` +
      `</div>` +
      `<div class="card-body">` +
        `<p class="hint">Upload pre-built install archives for intranet environments. Packages are injected into containers at start-up via <code>$MUDP_OFFLINE_PACKAGE_DIR</code>. ` +
        `Supported formats: <code>.run</code> <code>.tar.gz</code> <code>.sh</code> <code>.deb</code> <code>.rpm</code> <code>.apk</code></p>` +
      `</div>` +
      `<table class="data">` +
        `<thead><tr><th>Name</th><th>Service</th><th>OS / Arch</th><th>Image</th><th>Size</th><th>Uploaded</th><th class="actions">Actions</th></tr></thead>` +
        `<tbody>${pkgs.length ? pkgs.map(pkgRow).join("") : `<tr class="empty-row"><td colspan="7">No packages uploaded yet. Use the upload button or build buttons above to create one.</td></tr>`}</tbody>` +
      `</table></div>` +

      // ── Build Script ──
      `<div class="card"><div class="card-head"><h2>Offline Package Builder</h2>` +
        `<div class="head-tools">` +
          `<button class="ghost" id="resetBuildScriptBtn">Reset to Default</button>` +
          `<a class="ghost btn" href="/api/scripts/offline/build-script" download="build-mudp-offline-package.sh" id="downloadBuildScriptBtn">Download Script</a>` +
        `</div>` +
      `</div>` +
      `<div class="card-body"><form id="buildScriptForm" class="compact">` +
        `<p class="hint">Customise this shell script, then download and run it on a machine <strong>with</strong> internet access to generate an offline package. ` +
        `Alternatively, use the <strong>Build SSH Offline Pkg</strong> / <strong>Build VS Code Offline Pkg</strong> buttons above to build directly on this server (requires Docker + internet on the server).</p>` +
        `<textarea id="buildScriptEditor" class="mono" rows="14" spellcheck="false">${escapeHtml(state.buildScript || "")}</textarea>` +
        `<div style="display:flex;gap:8px;margin-top:8px;">` +
          `<button type="submit">Save Custom Script</button>` +
        `</div>` +
      `</form></div></div>` +
    `</div>`;

  // -- Bootstrap scripts form --
  $("#scriptSettings").onsubmit = async (e) => {
    e.preventDefault();
    const fd = new FormData(e.target);
    try {
      await api("/api/scripts", {
        method: "POST",
        body: JSON.stringify({ sshScript: fd.get("sshScript"), vscodeScript: fd.get("vscodeScript") }),
      });
      await refreshAll();
      renderView();
      toast("Scripts saved", true);
    } catch (err) {
      toast(err.message);
    }
  };

  // -- Offline build buttons --
  const buildSsh = $("#buildSshBtn");
  if (buildSsh) buildSsh.onclick = () => openOfflineBuildModal("ssh");
  const buildVscode = $("#buildVscodeBtn");
  if (buildVscode) buildVscode.onclick = () => openOfflineBuildModal("vscode");

  // -- Offline package upload button --
  const uploadBtn = $("#uploadPkgBtn");
  if (uploadBtn) uploadBtn.onclick = openUploadModal;

  // -- Package row actions --
  document.querySelectorAll("[data-pkg-download]").forEach((btn) => {
    btn.onclick = () => { window.location.href = `/api/scripts/offline/packages/download?id=${btn.dataset.pkgDownload}`; };
  });
  document.querySelectorAll("[data-pkg-delete]").forEach((btn) => {
    btn.onclick = () => deletePackage(btn.dataset.pkgDelete, btn.dataset.pkgName);
  });

  // -- Build script form --
  $("#buildScriptForm").onsubmit = async (e) => {
    e.preventDefault();
    const script = $("#buildScriptEditor").value;
    try {
      await api("/api/scripts/offline/build-script", {
        method: "POST",
        body: JSON.stringify({ buildScript: script }),
      });
      state.buildScript = script;
      toast("Build script saved", true);
    } catch (err) {
      toast(err.message);
    }
  };

  const resetBtn = $("#resetBuildScriptBtn");
  if (resetBtn) resetBtn.onclick = async () => {
    if (!confirm("Reset to the default build script? Your customizations will be lost.")) return;
    try {
      await api("/api/scripts/offline/build-script", {
        method: "POST",
        body: JSON.stringify({ buildScript: "" }),
      });
      state.buildScript = "";
      state.buildScriptLoaded = false;
      await loadBuildScript();
      renderView();
      toast("Build script reset to default", true);
    } catch (err) {
      toast(err.message);
    }
  };

  const dlBtn = $("#downloadBuildScriptBtn");
  if (dlBtn) dlBtn.href = "/api/scripts/offline/build-script";
}

// ── Loaders ──

async function loadOfflinePackages() {
  state.offlinePackagesLoaded = true;
  try {
    state.offlinePackages = (await api("/api/scripts/offline/packages")) || [];
  } catch {
    state.offlinePackages = [];
  }
  if (state.tab === "bootstrap") renderView();
}

async function loadBuildScript() {
  state.buildScriptLoaded = true;
  try {
    const res = await fetch("/api/scripts/offline/build-script", { credentials: "same-origin" });
    state.buildScript = res.ok ? await res.text() : "";
  } catch {
    state.buildScript = "";
  }
  if (state.tab === "bootstrap") renderView();
}

// ── Row renderer ──

function pkgRow(p) {
  const svc = { ssh: "SSH", vscode: "VSCode", all: "All" }[p.service] || p.service;
  const osArch = [p.os, p.arch].filter(Boolean).join(" / ") || "—";
  const img = p.imageName || (p.imageRef ? p.imageRef : "—");
  const size = formatBytes(p.size);
  const ext = (p.filename || "").split(".").pop().toLowerCase();
  const fmtBadge = ext ? `<span class="badge mono">.${ext}</span>` : "";
  const when = p.createdAt ? new Date(p.createdAt).toLocaleString() : "";
  return (
    `<tr>` +
      `<td><div class="primary-line">${escapeHtml(p.name)} ${fmtBadge}</div>` +
        `<div class="secondary-line mono hint">${escapeHtml(p.filename || "")}</div>` +
        (p.description ? `<div class="secondary-line hint">${escapeHtml(p.description)}</div>` : ``) +
      `</td>` +
      `<td>${escapeHtml(svc)}</td>` +
      `<td>${escapeHtml(osArch)}</td>` +
      `<td class="hint">${escapeHtml(img)}</td>` +
      `<td class="hint">${escapeHtml(size)}</td>` +
      `<td class="hint">${escapeHtml(when)}</td>` +
      `<td class="actions">` +
        `<button class="ghost" data-pkg-download="${p.id}">Download</button>` +
        `<button class="icon danger" data-pkg-delete="${p.id}" data-pkg-name="${escapeHtml(p.name)}">✕</button>` +
      `</td>` +
    `</tr>`
  );
}

// ── Upload modal ──

function openUploadModal() {
  const images = (state.images || []);
  const imgOpts = `<option value="">All images (generic)</option>` +
    images.map((img) => `<option value="${escapeHtml(img.name)}">${escapeHtml(img.name)}</option>`).join("");

  showModal({
    kind: "pkgUpload",
    title: "Upload Offline Package",
    body:
      `<form id="pkgUploadForm" class="compact" enctype="multipart/form-data">` +
        `<label class="field-label">Package file <span class="hint">(.run .tar.gz .sh .deb .rpm .apk)</span></label>` +
        `<input type="file" name="file" id="pkgFile" accept=".run,.tar.gz,.tgz,.sh,.deb,.rpm,.apk,.tar,.tar.xz,.tar.bz2" required>` +
        `<label class="field-label">Display name</label>` +
        `<input type="text" name="name" placeholder="e.g. SSH Ubuntu 22.04 amd64">` +
        `<label class="field-label">Service</label>` +
        `<select name="service">` +
          `<option value="all">All (SSH + VS Code)</option>` +
          `<option value="ssh">SSH only</option>` +
          `<option value="vscode">VS Code only</option>` +
        `</select>` +
        `<div style="display:grid;grid-template-columns:1fr 1fr;gap:8px;">` +
          `<div><label class="field-label">OS</label>` +
          `<select name="os">` +
            `<option value="">Any</option>` +
            `<option>ubuntu</option><option>debian</option><option>alpine</option>` +
            `<option>centos</option><option>rhel</option><option>fedora</option><option>openeuler</option>` +
          `</select></div>` +
          `<div><label class="field-label">Arch</label>` +
          `<select name="arch">` +
            `<option value="">Any</option>` +
            `<option>amd64</option><option>arm64</option><option>armv7</option>` +
          `</select></div>` +
        `</div>` +
        `<label class="field-label">Bind to image <span class="hint">(optional — restricts to one catalog image)</span></label>` +
        `<select name="imageName">${imgOpts}</select>` +
        `<label class="field-label">Description</label>` +
        `<input type="text" name="description" placeholder="Optional notes">` +
        `<div id="uploadProgress" style="display:none;margin-top:8px;"><span class="spinner"></span> Uploading…</div>` +
      `</form>`,
    foot: `<button class="ghost" data-close>Cancel</button><button class="primary" id="doUploadBtn">Upload</button>`,
  });

  $("#doUploadBtn").onclick = async () => {
    const form = $("#pkgUploadForm");
    const file = form.querySelector("[name=file]").files[0];
    if (!file) { toast("Select a file first"); return; }

    const fd = new FormData(form);
    const selectedImg = (state.images || []).find((img) => img.name === fd.get("imageName"));
    if (selectedImg) fd.set("imageRef", selectedImg.dockerRef || "");

    fd.delete("file");
    fd.append("file", file, file.name);

    const progress = $("#uploadProgress");
    if (progress) progress.style.display = "";
    const uploadBtn = $("#doUploadBtn");
    if (uploadBtn) uploadBtn.disabled = true;

    try {
      const res = await fetch("/api/scripts/offline/packages", {
        method: "POST",
        credentials: "same-origin",
        body: fd,
      });
      const data = await res.json().catch(() => ({}));
      if (!res.ok) throw new Error(data.error || "Upload failed");
      state.offlinePackagesLoaded = false;
      await loadOfflinePackages();
      closeModal();
      renderView();
      toast("Package uploaded", true);
    } catch (err) {
      toast(err.message);
      if (progress) progress.style.display = "none";
      if (uploadBtn) uploadBtn.disabled = false;
    }
  };
}

// ── Delete package ──

async function deletePackage(id, name) {
  if (!confirm(`Delete offline package "${name}"?`)) return;
  try {
    await api("/api/scripts/offline/packages/delete", { method: "POST", body: JSON.stringify({ id: Number(id) }) });
    state.offlinePackagesLoaded = false;
    await loadOfflinePackages();
    renderView();
    toast("Package deleted", true);
  } catch (err) {
    toast(err.message);
  }
}

// ── Offline package build modal / stream ──

function openOfflineBuildModal(which) {
  const images = (state.images || []);
  const imgOpts = `<option value="">Any image (generic)</option>` +
    images.map((img) => `<option value="${escapeHtml(img.name)}">${escapeHtml(img.name)}</option>`).join("");
  const label = which === "ssh" ? "SSH" : "VS Code";

  showModal({
    kind: "offlineBuild",
    title: `Build ${label} Offline Package`,
    body:
      `<div class="compact">` +
        `<p class="hint">Runs the build script on this server (requires Docker + internet access) to create an offline <code>.run</code> package for the selected target OS/arch. ` +
        `The package is automatically saved and listed in Offline Packages.</p>` +
        `<div style="display:grid;grid-template-columns:1fr 1fr;gap:8px;margin-bottom:8px;">` +
          `<div><label class="field-label">OS</label>` +
          `<select id="offlineBuildOS">` +
            `<option value="ubuntu" selected>Ubuntu</option>` +
            `<option value="debian">Debian</option>` +
            `<option value="alpine">Alpine</option>` +
            `<option value="centos">CentOS / Rocky</option>` +
            `<option value="rhel">RHEL</option>` +
            `<option value="fedora">Fedora</option>` +
            `<option value="openeuler">openEuler</option>` +
          `</select></div>` +
          `<div><label class="field-label">Arch</label>` +
          `<select id="offlineBuildArch">` +
            `<option value="amd64" selected>amd64 (x86_64)</option>` +
            `<option value="arm64">arm64 (aarch64)</option>` +
            `<option value="armv7">armv7</option>` +
          `</select></div>` +
        `</div>` +
        `<label class="field-label">Bind to image <span class="hint">(optional)</span></label>` +
        `<select id="offlineBuildImage">${imgOpts}</select>` +
      `</div>`,
    foot: `<button class="ghost" data-close>Cancel</button><button class="primary" id="startOfflineBuild">Build</button>`,
  });

  $("#startOfflineBuild").onclick = () => {
    const os = $("#offlineBuildOS").value;
    const arch = $("#offlineBuildArch").value;
    const imageName = $("#offlineBuildImage").value;
    const imageRef = imageName ? ((state.images || []).find((i) => i.name === imageName) || {}).dockerRef || "" : "";
    streamOfflineBuild(which, os, arch, imageName, imageRef);
  };
}

function streamOfflineBuild(which, os, arch, imageName, imageRef) {
  const label = which === "ssh" ? "SSH" : "VS Code";
  const buildState = { logs: "", error: "", finished: false, name: `${label} ${os}/${arch}`, which, os, arch, imageName, imageRef };
  state._offlineBuild = buildState;
  renderOfflineBuildProgress(buildState);

  (async () => {
    let streamError = null;
    try {
      const res = await fetch("/api/scripts/offline/build/stream", {
        method: "POST",
        credentials: "same-origin",
        headers: { "Content-Type": "application/json", Accept: "text/event-stream" },
        body: JSON.stringify({ service: which, os, arch, imageName, imageRef }),
      });
      if (!res.ok) {
        const data = await res.json().catch(() => ({}));
        streamError = data.error || `Build failed (${res.status})`;
        buildState.error = streamError;
        buildState.logs += `[error] ${streamError}\n`;
        renderOfflineBuildProgress(buildState);
        toast(streamError);
      } else {
        await readSSE(res, (event, data) => {
          if (event === "progress") {
            buildState.logs += (data.message || "") + "\n";
            renderOfflineBuildProgress(buildState);
          } else if (event === "error") {
            streamError = data.message || "Build failed";
            buildState.error = streamError;
            buildState.logs += `[error] ${streamError}\n`;
            renderOfflineBuildProgress(buildState);
            toast(streamError);
          } else if (event === "done") {
            buildState.finished = true;
            buildState.logs += `[done] ${data.message || "Package saved"}\n`;
            renderOfflineBuildProgress(buildState);
            toast(data.message || `${label} offline package ready`, true);
          }
        });
      }
    } catch (err) {
      streamError = err.message;
      buildState.error = err.message;
      buildState.logs += `[error] ${err.message}\n`;
      renderOfflineBuildProgress(buildState);
    }
    if (buildState.finished) {
      setTimeout(async () => {
        closeModal();
        state.offlinePackagesLoaded = false;
        await loadOfflinePackages();
        renderView();
      }, 1500);
    }
  })();
}

function renderOfflineBuildProgress(buildState) {
  if (state.modal.kind !== "offlineBuild") return;
  const done = buildState.finished;
  const statusBox = buildState.error
    ? `<div class="error-box">${escapeHtml(buildState.error)}</div>`
    : done
      ? `<div class="step done"><span class="step-icon">✓</span><span class="step-label">${escapeHtml(buildState.name)} package ready</span></div>`
      : `<div class="step active"><span class="step-icon"><span class="spinner"></span></span><span class="step-label">Building ${escapeHtml(buildState.name)} package…</span></div>`;
  setModalBody(
    statusBox +
      `<pre class="log-output">${escapeHtml(buildState.logs || "")}</pre>` +
      `<div style="display:flex;gap:8px;justify-content:flex-end;">` +
        (buildState.error
          ? `<button class="primary" id="offlineBuildRetry">Retry</button>` : ``) +
        `<button class="ghost" data-close>${buildState.error ? "Close" : "Hide"}</button>` +
      `</div>`
  );
  const retry = $("#offlineBuildRetry");
  if (retry) retry.onclick = () => streamOfflineBuild(buildState.which, buildState.os, buildState.arch, buildState.imageName, buildState.imageRef);
  const log = document.querySelector(".modal-body .log-output");
  if (log) log.scrollTop = log.scrollHeight;
}

// ── Utilities ──

function formatBytes(n) {
  if (!n) return "0 B";
  const units = ["B", "KB", "MB", "GB"];
  let i = 0;
  while (n >= 1024 && i < units.length - 1) { n /= 1024; i++; }
  return `${n.toFixed(i > 0 ? 1 : 0)} ${units[i]}`;
}

function $(selector) {
  return document.querySelector(selector);
}
