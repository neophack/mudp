// Shared UI primitives: modal helpers, SSE reader, and the global backdrop
// click handler. These are pure infrastructure used across feature modules.

import { state } from "../app.js";
import { closeTerminal } from "./terminal.js";

// Render a framed modal with head/body/foot slots.
export function showModal({ kind, title, body, foot }) {
  state.modal = { open: true, kind, data: null };
  const wrapper = document.createElement("div");
  wrapper.className = `modal-backdrop ${kind}-modal`;
  wrapper.innerHTML =
    `<div class="modal">` +
    `<div class="modal-head"><h2>${escapeHtml(title)}</h2><button class="ghost" data-close>Close</button></div>` +
    `<div class="modal-body">${body}</div>` +
    (foot ? `<div class="modal-foot">${foot}</div>` : ``) +
    `</div>`;
  document.body.appendChild(wrapper);
  bindClose(wrapper);
}

// Replace the body of the currently open modal (used by progress panels).
export function setModalBody(html) {
  const body = document.querySelector(".modal-body");
  if (body) body.innerHTML = html;
  const wrapper = document.querySelector(".modal-backdrop");
  if (wrapper) bindClose(wrapper);
}

// Render a modal whose inner HTML is fully supplied by the caller. Optionally
// rebinds [data-close] buttons (pass false when binding happens elsewhere).
export function showModalNoShell(extraClass, sizeClass, innerHtml, bind = true) {
  document.querySelector(`.modal-backdrop.${extraClass}`)?.remove();
  const wrapper = document.createElement("div");
  wrapper.className = `modal-backdrop ${extraClass}`;
  wrapper.innerHTML = `<div class="modal ${sizeClass}">${innerHtml}</div>`;
  document.body.appendChild(wrapper);
  if (bind) bindClose(wrapper);
}

export function bindClose(wrapper) {
  wrapper.querySelectorAll("[data-close]").forEach((btn) => {
    btn.onclick = closeModal;
  });
}

// Per-modal teardown callbacks. Modal feature modules that own long-lived
// resources (SSE streams, intervals) register a cleanup fn so closeModal stops
// them — mirroring the terminal handling below. This prevents stream leaks when
// a modal is dismissed via Esc or backdrop click (which bypass per-button wiring).
const modalTeardown = new Map();
let modalSeq = 0;

// onModalClose registers a teardown callback for the modal currently being set
// up and returns a token. The token is stashed on the modal wrapper so the
// matching teardown runs only when that specific modal closes.
export function onModalClose(wrapper, fn) {
  const token = ++modalSeq;
  modalTeardown.set(token, fn);
  if (wrapper) wrapper.dataset.teardown = String(token);
  return token;
}

function runTeardown(wrapper) {
  const token = wrapper && wrapper.dataset.teardown;
  if (!token) return;
  const fn = modalTeardown.get(Number(token));
  if (fn) {
    try { fn(); } catch { /* best-effort cleanup */ }
  }
  modalTeardown.delete(Number(token));
}

// Close every open modal. Tears down an in-flight terminal if present.
export function closeModal() {
  if (document.querySelector(".modal-backdrop.terminal-modal")) {
    closeTerminal();
  }
  document.querySelectorAll(".modal-backdrop").forEach((el) => {
    if (el.classList.contains("logs-modal")) state.logViewer.open = false;
    runTeardown(el);
    el.remove();
  });
  state.modal = { open: false, kind: "", data: null };
}

// Stream an SSE response, invoking onEvent(event, data) for each message.
export async function readSSE(res, onEvent) {
  const reader = res.body.getReader();
  const decoder = new TextDecoder();
  let buffer = "";
  let eventName = "";
  const handle = (line) => {
    if (line.startsWith("event:")) {
      eventName = line.slice(6).trim();
    } else if (line.startsWith("data:")) {
      const raw = line.slice(5).trim();
      let data = {};
      try {
        data = JSON.parse(raw);
      } catch {
        data = { message: raw };
      }
      onEvent(eventName, data);
    }
  };
  while (true) {
    const { value, done } = await reader.read();
    if (done) break;
    buffer += decoder.decode(value, { stream: true });
    const lines = buffer.split("\n");
    buffer = lines.pop();
    lines.forEach((l) => {
      if (l.trim()) handle(l);
    });
  }
  if (buffer.trim()) handle(buffer);
}

// Backdrop click closes the topmost modal — but only when the press both
// started AND ended on the backdrop itself. This avoids closing the modal when
// the user drags to select text inside an input and releases the mouse over the
// backdrop ring (a plain `click` check on e.target would fire in that case and
// kill the New Container form mid-selection).
let pressStartedOnBackdrop = false;
document.addEventListener("mousedown", (e) => {
  pressStartedOnBackdrop = !!(e.target.classList && e.target.classList.contains("modal-backdrop"));
});
document.addEventListener("click", (e) => {
  const endedOnBackdrop = !!(e.target.classList && e.target.classList.contains("modal-backdrop"));
  if (pressStartedOnBackdrop && endedOnBackdrop) {
    closeModal();
  }
  pressStartedOnBackdrop = false;
});

// Esc closes the topmost modal. Standard app convention; no modifier needed.
document.addEventListener("keydown", (e) => {
  if (e.key === "Escape" && document.querySelector(".modal-backdrop")) {
    closeModal();
  }
});

// Small HTML escaper used only for modal titles supplied as plain strings.
function escapeHtml(v) {
  return String(v ?? "").replace(/[&<>"']/g, (m) => ({
    "&": "&amp;",
    "<": "&lt;",
    ">": "&gt;",
    '"': "&quot;",
    "'": "&#39;",
  }[m]));
}
