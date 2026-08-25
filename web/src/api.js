// Fetch-based API client with the session/CSRF contract the Go server
// enforces: every mutating request carries X-CSRF-Token read live from the
// mudp_csrf cookie, and a 403 CSRF failure is recovered in place by pulling a
// fresh token from GET /api/me (the handler mints one against the still-valid
// session) and retrying once.

import { store } from "@/store";

export function readCSRFCookie() {
  const m = document.cookie.match(/(?:^|; )mudp_csrf=([^;]+)/);
  return m ? decodeURIComponent(m[1]) : "";
}

export async function api(path, opts = {}, retryCSRF = true) {
  // Pull headers out of opts so the merged Content-Type below is not clobbered
  // by a trailing ...opts spread re-applying the caller's original headers.
  const { headers, ...rest } = opts;
  const method = (opts.method || "GET").toUpperCase();
  const mergedHeaders = { "Content-Type": "application/json", ...(headers || {}) };
  // Always prefer the live cookie value: the CSRF cookie is SameSite=Strict,
  // so it may not be sent on a cross-site top-level navigation and the cached
  // store.csrfToken can end up empty/stale. Reading document.cookie each request
  // keeps the header in sync with the cookie the browser will actually send.
  const csrfToken = readCSRFCookie() || store.csrfToken || "";
  if (csrfToken && method !== "GET" && method !== "HEAD") {
    mergedHeaders["X-CSRF-Token"] = csrfToken;
  }
  const res = await fetch(path, {
    credentials: "same-origin",
    headers: mergedHeaders,
    ...rest,
  });
  const data = await res.json().catch(() => ({}));
  if (!res.ok) {
    if (res.status === 403 && retryCSRF && /csrf/i.test(data.error || "")) {
      const me = await refreshCSRFToken();
      if (me) return api(path, opts, false);
    }
    const err = new Error(data.error || res.statusText);
    err.pending = data.pending === true;
    throw err;
  }
  return data;
}

// refreshCSRFToken asks /api/me for a fresh token (the handler mints one when
// the request arrives without a CSRF cookie). Returns the token, or "" if the
// session is gone too — in which case the caller should surface the original
// error rather than retry.
async function refreshCSRFToken() {
  try {
    const me = await api("/api/me", {}, false);
    if (!me || me.authenticated === false) return "";
    store.csrfToken = me.csrfToken || readCSRFCookie() || "";
    return store.csrfToken;
  } catch {
    return "";
  }
}

// streamSSE POSTs a JSON body and pumps the Server-Sent-Events response,
// invoking onEvent(event, data) per message. Returns the fetch Response so
// callers can check res.ok / read error payloads before streaming.
export async function streamSSE(path, body) {
  const csrfToken = readCSRFCookie() || store.csrfToken || "";
  return fetch(path, {
    method: "POST",
    credentials: "same-origin",
    headers: { "Content-Type": "application/json", "X-CSRF-Token": csrfToken },
    body: JSON.stringify(body || {}),
  });
}

// readSSE parses an SSE body from a fetch Response, dispatching each `data:`
// line (JSON when parseable, else {message}) with its `event:` name.
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

// copyText writes text to the clipboard. It prefers the modern Clipboard API
// when available, and falls back to the legacy execCommand("copy") trick so
// copying still works on non-secure origins (e.g. plain HTTP LAN installs).
export async function copyText(text) {
  if (navigator.clipboard && navigator.clipboard.writeText) {
    try {
      await navigator.clipboard.writeText(text);
      return;
    } catch {
      // fall through to execCommand fallback
    }
  }
  const ta = document.createElement("textarea");
  ta.value = text;
  ta.setAttribute("readonly", "");
  ta.style.position = "fixed";
  ta.style.opacity = "0";
  document.body.appendChild(ta);
  ta.focus();
  ta.select();
  ta.setSelectionRange(0, text.length);
  const ok = document.execCommand("copy");
  document.body.removeChild(ta);
  if (!ok) throw new Error("copy failed");
}
