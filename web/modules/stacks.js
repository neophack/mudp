// Stacks: deploy docker-compose projects via the host's `docker compose` CLI.
// Compose body + env live in the DB; deploys stream progress over SSE.

import { state, api, toast, refreshSection, renderView, canMutate, displayNameForUsername, readCSRFCookie } from "../app.js";
import { showModal, showModalNoShell, closeModal, readSSE, onModalClose } from "./ui.js";
import { loadXterm, TERM_THEME } from "./terminal.js";
import { registerJob } from "./jobs.js";

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
      `<td><div class="secondary-line">${escapeHtml(displayNameForUsername(s.owner) || "—")}</div></td>` +
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

// appendStackLine adds a line to the persistent run log and writes it to the
// active terminal (if the modal is currently open). The lines array lets us
// replay the whole session when the user reopens the terminal after hiding it.
function appendStackLine(line) {
  state.stackRun.lines.push(line);
  state.stackRun.controller?.write(line);
}

async function openStackTerminal(title) {
  // If the modal is already open, just bring it to the front.
  if (document.querySelector(".modal-backdrop.stack-run-modal")) {
    return state.stackRun.controller || { write: () => {}, setStatus: () => {}, close: closeModal };
  }

  showModalNoShell(
    "stack-run-modal",
    "wide stack-run-modal",
    `<div class="modal-head">` +
      `<div class="term-title"><h2>${escapeHtml(title)}</h2></div>` +
      `<div class="modal-tools"><button class="ghost" data-close>Close</button></div>` +
    `</div>` +
    `<div class="modal-body">` +
      `<div id="stackTermBox" class="term-box"></div>` +
      `<div class="term-statusbar">` +
        `<span class="term-stat" id="stackTermStatus"><span class="dot dot-muted"></span>Running…</span>` +
        `<span class="term-stat term-keys">Read-only build log</span>` +
      `</div>` +
    `</div>`,
    false
  );
  const wrapper = document.querySelector(".modal-backdrop.stack-run-modal");
  document.querySelectorAll(".stack-run-modal [data-close]").forEach((b) => (b.onclick = closeModal));

  try {
    await loadXterm();
  } catch {
    const box = $("#stackTermBox");
    if (box) box.textContent = "Failed to load terminal library";
    return { write: () => {}, setStatus: () => {}, close: closeModal };
  }

  const term = new window.Terminal({
    cursorBlink: false,
    fontSize: 13,
    lineHeight: 1.25,
    fontFamily: "'Cascadia Code', 'Fira Code', 'SFMono-Regular', Consolas, 'Liberation Mono', monospace",
    scrollback: 10000,
    convertEol: true,
    disableStdin: true,
    theme: TERM_THEME,
  });
  const fitAddon = new window.FitAddon.FitAddon();
  term.loadAddon(fitAddon);
  term.open($("#stackTermBox"));
  fitAddon.fit();

  const resizeObs = new ResizeObserver(() => {
    try { fitAddon.fit(); } catch {}
  });
  resizeObs.observe(wrapper);

  const cleanup = () => {
    resizeObs.disconnect();
    try { term.dispose(); } catch {}
    if (state.stackRun.controller === controller) {
      state.stackRun.controller = null;
    }
  };
  onModalClose(wrapper, cleanup);

  // Docker pull/build progress lines are meant to overwrite the same row.
  // The server strips \r, so we reconstruct the overwrite behavior here:
  // consecutive progress lines use \r to redraw, normal lines advance.
  let lastWasProgress = false;
  const PROGRESS_RE = /(?:Downloading|Extracting|Pulling|Pushing|Waiting|Importing|Verifying|Loading|Saving|Uploading).*?\d+(?:\.\d+)?\s*(?:MB|GB|kB|B)/;
  const controller = {
    term,
    write(line) {
      const raw = String(line || "");
      const isProgress = PROGRESS_RE.test(raw);
      if (isProgress) {
        if (lastWasProgress) {
          // Move up one line, clear it, then redraw the updated progress.
          term.write("\x1b[1A\x1b[2K" + raw + "\r");
        } else {
          term.write(raw + "\r");
        }
        lastWasProgress = true;
      } else {
        if (lastWasProgress) {
          // Leave the progress line visible and start a fresh line.
          term.write("\n" + raw + "\r\n");
        } else {
          term.write(raw + "\r\n");
        }
        lastWasProgress = false;
      }
    },
    setStatus(text, kind = "muted") {
      const el = document.getElementById("stackTermStatus");
      if (!el) return;
      const dotClass = kind === "ok" ? "dot-ok" : kind === "error" ? "dot-danger" : "dot-muted";
      el.innerHTML = `<span class="dot ${dotClass}"></span>${escapeHtml(text)}`;
    },
    close: closeModal,
  };

  // Replay the buffered session so reopening the modal shows the same output.
  for (const line of state.stackRun.lines) {
    controller.write(line);
  }

  state.stackRun.controller = controller;
  return controller;
}

async function deployStack(id) {
  // Reopen the existing terminal if the same stack is still being deployed.
  if (state.stackRun.active && state.stackRun.stackId === id && state.stackRun.verb === "up") {
    const term = await openStackTerminal("Stack Deploy");
    syncStackStatus(term);
    return;
  }
  // Otherwise start a fresh run.
  state.stackRun = { active: true, stackId: id, verb: "up", lines: [], error: "", done: false, controller: null };
  const term = await openStackTerminal("Stack Deploy");
  runStackStream(id, "up", term, "deployed");
}

async function downStack(id) {
  if (!confirm("Tear down this stack? Containers will be stopped and removed (volumes kept).")) return;
  if (state.stackRun.active && state.stackRun.stackId === id && state.stackRun.verb === "down") {
    const term = await openStackTerminal("Stack Down");
    syncStackStatus(term);
    return;
  }
  state.stackRun = { active: true, stackId: id, verb: "down", lines: [], error: "", done: false, controller: null };
  const term = await openStackTerminal("Stack Down");
  runStackStream(id, "down", term, "torn down");
}

function syncStackStatus(term) {
  if (state.stackRun.error) term.setStatus(state.stackRun.error, "error");
  else if (state.stackRun.done) term.setStatus("Complete", "ok");
  else term.setStatus("Running…", "muted");
}

function isActiveRun(id, verb) {
  return state.stackRun.stackId === id && state.stackRun.verb === verb;
}

async function runStackStream(id, verb, term, doneVerb) {
  const stack = (state.stacks || []).find((s) => s.id === id);
  const stackName = stack?.name || `stack ${id}`;
  const job = registerJob({ kind: `stack.${verb}`, name: stackName });
  try {
    const res = await fetch(`/api/stacks/${verb}/stream`, {
      method: "POST",
      credentials: "same-origin",
      headers: { "Content-Type": "application/json", Accept: "text/event-stream", "X-CSRF-Token": readCSRFCookie() || state.csrfToken || "" },
      body: JSON.stringify({ id }),
      signal: job.signal,
    });
    if (!res.ok) {
      const data = await res.json().catch(() => ({}));
      const msg = data.error || `${verb === "up" ? "Deploy" : "Down"} failed (${res.status})`;
      job.error(msg);
      if (isActiveRun(id, verb)) {
        appendStackLine(msg);
        state.stackRun.error = msg;
        state.stackRun.active = false;
        state.stackRun.done = true;
        term.setStatus(msg, "error");
      }
      toast(msg);
      return;
    }
    await streamStack(res, id, verb, term, doneVerb, job);
  } catch (err) {
    if (job.signal.aborted) {
      job.cancel();
      if (isActiveRun(id, verb)) {
        const msg = "Cancelled";
        appendStackLine(`[cancelled] ${msg}`);
        state.stackRun.error = msg;
        state.stackRun.active = false;
        state.stackRun.done = true;
        term.setStatus(msg, "muted");
      }
    } else {
      job.error(err.message);
      if (isActiveRun(id, verb)) {
        appendStackLine(err.message);
        state.stackRun.error = err.message;
        state.stackRun.active = false;
        state.stackRun.done = true;
        term.setStatus(err.message, "error");
      }
      toast(err.message);
    }
  }
}

async function streamStack(res, id, verb, term, doneVerb, job) {
  await readSSE(res, (event, data) => {
    // Ignore stale output if the user switched to a different stack/verb.
    if (!isActiveRun(id, verb)) return;
    if (event === "progress") {
      const line = data.message || "";
      appendStackLine(line);
      job.log(line);
    } else if (event === "error") {
      const msg = data.message || "failed";
      appendStackLine(`[error] ${msg}`);
      job.error(msg);
      state.stackRun.error = msg;
      state.stackRun.active = false;
      state.stackRun.done = true;
      term.setStatus(msg, "error");
      toast(msg);
    } else if (event === "done" || event === "cancelled") {
      const ok = event === "done";
      appendStackLine(`[${event}] ${data.message || ""}`);
      if (ok) job.done(data.message || "Complete");
      else job.cancel();
      state.stackRun.active = false;
      state.stackRun.done = true;
      term.setStatus(ok ? "Complete" : "Cancelled", ok ? "ok" : "muted");
      toast(`Stack ${doneVerb}`, ok);
      refreshSection("stacks", "containers").then(() => renderView());
      // Only auto-close if this controller still owns the active terminal.
      if (ok) {
        setTimeout(() => {
          if (state.stackRun.controller === term && document.querySelector(".modal-backdrop.stack-run-modal")) {
            term.close();
          }
        }, 1200);
      }
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
