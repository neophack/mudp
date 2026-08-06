// @vitest-environment jsdom
// Coverage for the b9762b2 external-key feature: a per-token secret, separate
// from the URL-embedded token, that the remote (external) MCP listener
// requires as an Authorization: Bearer header. LAN access never needs it.
//
// Guards three things the server change alone can't: (1) the GEN button that
// mints the key only appears once an admin has actually published a remote
// domain -- otherwise it's a dead control; (2) the Authorization header is
// only ever rendered on the External scope, never on the LAN scope, even
// when a key already exists for the token; (3) generating a key reopens the
// config modal straight on the External scope with the new value copyable,
// instead of leaving the admin to hunt for it.

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

const apiMock = vi.fn();
const canMutateMock = vi.fn(() => true);
const isAdminMock = vi.fn(() => false);
const renderViewMock = vi.fn();
const toastMock = vi.fn();

vi.mock("../../app.js", () => ({
  state: {},
  $: (sel) => document.querySelector(sel),
  api: (...args) => apiMock(...args),
  toast: (...args) => toastMock(...args),
  copyText: vi.fn(async () => {}),
  renderView: (...args) => renderViewMock(...args),
  canMutate: (...args) => canMutateMock(...args),
  isAdmin: (...args) => isAdminMock(...args),
  displayNameForUsername: (u) => u,
  t: (key, params) => {
    if (typeof params === "object" && params !== null) {
      let out = key;
      for (const [k, v] of Object.entries(params)) out = out.replace(`{${k}}`, v);
      return out;
    }
    return key;
  },
}));

vi.mock("../../modules/ui.js", () => ({
  showModal: vi.fn(({ body, foot }) => {
    document.querySelectorAll(".modal-backdrop").forEach((el) => el.remove());
    const wrap = document.createElement("div");
    wrap.className = "modal-backdrop mcp-modal";
    wrap.innerHTML = `<div class="modal-body">${body}</div>` + (foot ? `<div class="modal-foot">${foot}</div>` : "");
    document.body.appendChild(wrap);
  }),
  closeModal: vi.fn(() => {
    document.querySelectorAll(".modal-backdrop").forEach((el) => el.remove());
  }),
}));

const { state } = await import("../../app.js");
const { renderMCP } = await import("../../modules/mcp.js");

const TOKEN_ID = "tok-1";
function baseToken(overrides = {}) {
  return {
    id: TOKEN_ID,
    containerId: "abcdef123456",
    containerName: "web",
    label: "agent",
    token: "tok-secret",
    externalKey: "",
    onSafeNetwork: true,
    owner: "alice",
    createdAt: "2026-01-01T00:00:00Z",
    lastUsedAt: null,
    expiresAt: null,
    inUse: false,
    ...overrides,
  };
}

const REMOTE_ON = { enabled: true, baseUrl: "https://mudp.example.com", domain: "mudp.example.com", safeNetwork: "openwrt-lan" };

beforeEach(() => {
  apiMock.mockReset();
  canMutateMock.mockReset();
  canMutateMock.mockReturnValue(true);
  isAdminMock.mockReset();
  isAdminMock.mockReturnValue(false);
  renderViewMock.mockReset();
  toastMock.mockReset();
  state.mcpTokens = null;
  state.mcpRemote = null;
  document.body.innerHTML = `<div id="view"></div>`;
  vi.stubGlobal("confirm", () => true);

  // renderMCP only fetches when state.mcpTokens/mcpRemote are unset, so every
  // test seeds those directly; this default implementation exists for the
  // rotate-external round-trip, which re-fetches both after minting a key.
  apiMock.mockImplementation(async (path) => {
    if (path === "/api/mcp/tokens") return state.mcpTokens || [];
    if (path === "/api/mcp/remote") return state.mcpRemote || null;
    const m = /^\/api\/mcp\/tokens\/([^/]+)\/rotate-external$/.exec(path);
    if (m) {
      const tk = (state.mcpTokens || []).find((t) => t.id === m[1]);
      const externalKey = "ext-key-xyz";
      if (tk) tk.externalKey = externalKey;
      return { id: m[1], externalKey, label: tk ? tk.label : "" };
    }
    throw new Error(`unexpected api call: ${path}`);
  });
});

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("MCP external-key UI", () => {
  it("hides the GEN button on a LAN-only deployment and shows it once a remote domain is published", async () => {
    state.mcpTokens = [baseToken()];
    state.mcpRemote = null;
    await renderMCP();
    expect(document.querySelector("[data-mcp-genkey]")).toBeNull();

    state.mcpRemote = REMOTE_ON;
    await renderMCP();
    const genBtn = document.querySelector("[data-mcp-genkey]");
    expect(genBtn).not.toBeNull();
    expect(genBtn.dataset.mcpGenkey).toBe(TOKEN_ID);
  });

  it("never renders the GEN button for a read-only (non-mutating) viewer, even with a remote domain published", async () => {
    canMutateMock.mockReturnValue(false);
    state.mcpTokens = [baseToken()];
    state.mcpRemote = REMOTE_ON;
    await renderMCP();
    expect(document.querySelector("[data-mcp-genkey]")).toBeNull();
  });

  it("never shows the Authorization header on the local scope, even when an external key already exists", async () => {
    state.mcpTokens = [baseToken({ externalKey: "already-set-key" })];
    state.mcpRemote = REMOTE_ON;
    await renderMCP();

    document.querySelector("[data-mcp-view]").click();
    // Config modal opens on the local scope by default.
    expect(document.querySelector(".mcp-scope-btn.active").dataset.scope).toBe("local");
    expect(document.getElementById("mcpAuthHeaderWrap").innerHTML.trim()).toBe("");
    expect(document.getElementById("mcpConfigEditor").value).not.toContain("Authorization");
  });

  it("shows a generate-key hint (not a header value) when the remote scope is selected but no key exists yet", async () => {
    state.mcpTokens = [baseToken({ externalKey: "" })];
    state.mcpRemote = REMOTE_ON;
    await renderMCP();

    document.querySelector("[data-mcp-view]").click();
    document.querySelector('.mcp-scope-btn[data-scope="remote"]').click();

    const wrap = document.getElementById("mcpAuthHeaderWrap");
    expect(wrap.textContent).toContain("mcp.noExternalKeyHint");
    expect(document.getElementById("mcpAuthHeaderJson")).toBeNull();
    expect(document.getElementById("mcpConfigEditor").value).not.toContain("Authorization");
  });

  it("shows the Authorization header (JSON + Bearer forms) once the remote scope is selected and a key exists", async () => {
    state.mcpTokens = [baseToken({ externalKey: "already-set-key" })];
    state.mcpRemote = REMOTE_ON;
    await renderMCP();

    document.querySelector("[data-mcp-view]").click();
    document.querySelector('.mcp-scope-btn[data-scope="remote"]').click();

    expect(document.getElementById("mcpAuthHeaderJson").textContent).toContain("already-set-key");
    expect(document.getElementById("mcpAuthHeaderBearer").textContent).toBe("Bearer already-set-key");
    // The copyable config JSON itself picks up the same header on this scope.
    const config = JSON.parse(document.getElementById("mcpConfigEditor").value);
    expect(config.mcpServers.agent.headers.Authorization).toBe("Bearer already-set-key");
  });

  it("GEN mints an external key via rotate-external and reopens the modal directly on the remote scope with the new value", async () => {
    state.mcpTokens = [baseToken({ externalKey: "" })];
    state.mcpRemote = REMOTE_ON;
    await renderMCP();

    document.querySelector("[data-mcp-genkey]").click();
    await new Promise((r) => setTimeout(r, 0));

    const rotateCall = apiMock.mock.calls.find((c) => c[0] === `/api/mcp/tokens/${TOKEN_ID}/rotate-external`);
    expect(rotateCall).toBeTruthy();
    expect(rotateCall[1]).toMatchObject({ method: "POST" });

    // Reopened straight to External, not left on Local for the admin to find.
    expect(document.querySelector(".mcp-scope-btn.active").dataset.scope).toBe("remote");
    expect(document.getElementById("mcpAuthHeaderBearer").textContent).toBe("Bearer ext-key-xyz");
    // The token embedded in the /mcp/{token} URL is untouched by rotation.
    expect(document.getElementById("mcpConfigEditor").value).toContain("tok-secret");
  });

  it("leaves an unpublished (no remote domain) config modal with no scope toggle and no Authorization section at all", async () => {
    state.mcpTokens = [baseToken({ externalKey: "" })];
    state.mcpRemote = null;
    await renderMCP();

    document.querySelector("[data-mcp-view]").click();
    expect(document.querySelector(".mcp-scope-btn")).toBeNull();
    expect(document.getElementById("mcpAuthHeaderWrap").innerHTML.trim()).toBe("");
  });
});
