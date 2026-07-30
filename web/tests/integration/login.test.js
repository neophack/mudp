import { describe, it, expect, beforeEach, vi } from "vitest";
import { escapeHtml } from "../../lib/common.js";
import { renderLogin } from "../../modules/login.js";

// Mock app.js dependencies so the login module can render in isolation.
// The factory is hoisted; we attach the mutable mocks to globalThis so tests
// can reset and inspect them.
vi.mock("../../app.js", () => {
  const mockState = { feishu: false, me: null, csrfToken: "" };
  const mockApi = vi.fn();
  const mockRenderPending = vi.fn();
  const mockRefreshAll = vi.fn();
  const mockRender = vi.fn();
  globalThis.__loginMocks = { mockState, mockApi, mockRenderPending, mockRefreshAll, mockRender };
  return {
    state: mockState,
    api: (...args) => mockApi(...args),
    renderPending: (...args) => mockRenderPending(...args),
    refreshAll: (...args) => mockRefreshAll(...args),
    render: (...args) => mockRender(...args),
    escapeHtml,
  };
});

const getMocks = () => globalThis.__loginMocks;

describe("renderLogin", () => {
  beforeEach(() => {
    document.body.innerHTML = '<div id="app"></div>';
    const { mockState, mockApi, mockRenderPending, mockRefreshAll, mockRender } = getMocks();
    mockState.me = null;
    mockState.csrfToken = "";
    mockApi.mockReset();
    mockRenderPending.mockClear();
    mockRefreshAll.mockClear();
    mockRender.mockClear();
  });

  it("renders the login form", async () => {
    const { mockApi } = getMocks();
    mockApi.mockResolvedValue({ enabled: false });
    await renderLogin();
    expect(document.querySelector("#loginForm")).not.toBeNull();
    expect(document.querySelector("input[name='username']")).not.toBeNull();
    expect(document.querySelector("input[name='password']")).not.toBeNull();
  });

  it("submits credentials and boots the app on success", async () => {
    const { mockApi, mockState, mockRender } = getMocks();
    mockApi
      .mockResolvedValueOnce({ enabled: false })
      .mockResolvedValueOnce({ user: { id: 1, username: "admin", role: "admin" }, csrfToken: "tok" })
      .mockResolvedValueOnce({ id: 1, username: "admin", role: "admin" });

    await renderLogin();
    const form = document.querySelector("#loginForm");
    form.querySelector("input[name='username']").value = "admin";
    form.querySelector("input[name='password']").value = "secret";
    form.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));

    await vi.waitFor(() => expect(mockRender).toHaveBeenCalled(), { timeout: 500 });

    expect(mockApi).toHaveBeenCalledWith("/api/login", {
      method: "POST",
      body: JSON.stringify({ username: "admin", password: "secret" }),
    });
    expect(mockState.me.username).toBe("admin");
    expect(mockState.csrfToken).toBe("tok");
  });

  it("shows the pending screen for pending users", async () => {
    const { mockApi, mockState, mockRefreshAll } = getMocks();
    mockApi
      .mockResolvedValueOnce({ enabled: false })
      .mockResolvedValueOnce({ user: { id: 2, username: "pending", role: "user", pending: true }, csrfToken: "tok2" });

    await renderLogin();
    const form = document.querySelector("#loginForm");
    form.querySelector("input[name='username']").value = "pending";
    form.querySelector("input[name='password']").value = "secret";
    form.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));

    await vi.waitFor(() => expect(mockState.me?.pending).toBe(true), { timeout: 500 });
    expect(mockRefreshAll).not.toHaveBeenCalled();
  });
});
