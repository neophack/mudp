// Shared file-viewer helpers used by both the netdisk viewer and the public
// share page viewer.

// DOMPurify's default allow-list keeps <form>/<style> (its focus is blocking
// script execution, not phishing or CSS injection), so a rendered markdown
// file could otherwise still carry a credential-harvesting form or CSS that
// repaints the page. Neither has any legitimate use inside a rendered
// markdown/text viewer, so they are forbidden outright.
const MARKDOWN_SANITIZE_OPTS = {
  FORBID_TAGS: ["style", "form", "input", "button", "select", "textarea", "object", "embed"],
  FORBID_ATTR: ["style"],
};

// renderMarkdownInto fetches `url` and renders it as sanitized markdown into
// `body`. marked.parse() alone passes raw HTML straight through (marked v9
// does not sanitize), so a crafted markdown file containing e.g.
// `<img onerror=...>` would execute script the moment it's viewed.
// DOMPurify strips that before the result ever reaches innerHTML. If either
// library failed to load, this falls back to plain text rather than ever
// inserting unsanitized HTML.
export async function renderMarkdownInto(url, body) {
  const res = await fetch(url, { credentials: "same-origin" });
  const text = await res.text();
  if (!body.isConnected) return;
  const marked = window.marked;
  const DOMPurify = window.DOMPurify;
  if (marked && typeof marked.parse === "function" && DOMPurify && typeof DOMPurify.sanitize === "function") {
    body.innerHTML = `<div class="viewer-markdown md-body"></div>`;
    body.querySelector(".viewer-markdown").innerHTML = DOMPurify.sanitize(marked.parse(text), MARKDOWN_SANITIZE_OPTS);
    return;
  }
  body.innerHTML = `<pre class="viewer-text"></pre>`;
  body.querySelector("pre").textContent = text;
}
