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

// Close every open modal. Tears down an in-flight terminal if present.
export function closeModal() {
  if (document.querySelector(".modal-backdrop.terminal-modal")) {
    closeTerminal();
  }
  document.querySelectorAll(".modal-backdrop").forEach((el) => {
    if (el.classList.contains("logs-modal")) state.logViewer.open = false;
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

// Backdrop click closes the topmost modal.
document.addEventListener("click", (e) => {
  if (e.target.classList && e.target.classList.contains("modal-backdrop")) {
    closeModal();
  }
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
