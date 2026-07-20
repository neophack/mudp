// @vitest-environment jsdom
// Unit tests for the centralized auto-refresh loop (modules/refresh.js).
// app.js and the view modules are mocked so the loop's decision logic
// (skip conditions, change detection, tab policy) is tested in isolation.

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

vi.mock("../../app.js", () => ({
  state: {
    me: { username: "alice", role: "admin" },
    tab: "containers",
    containers: [{ id: "c1" }],
    audit: [],
    auditSearch: {},
  },
  api: vi.fn(async () => []),
  refreshSection: vi.fn(async () => {}),
  renderView: vi.fn(),
}));

vi.mock("../../modules/notifications.js", () => ({
  fetchNotifications: vi.fn(async () => {}),
  updateNotificationBadge: vi.fn(),
}));

vi.mock("../../modules/usage.js", () => ({ renderUsage: vi.fn(async () => {}) }));
vi.mock("../../modules/netdisk.js", () => ({ renderNetdisk: vi.fn(async () => {}) }));
vi.mock("../../modules/mcp.js", () => ({ refreshMCPTokens: vi.fn(async () => {}) }));

const { state, refreshSection, renderView } = await import("../../app.js");
const { fetchNotifications, updateNotificationBadge } = await import("../../modules/notifications.js");
const { renderNetdisk } = await import("../../modules/netdisk.js");
const { tickBlocked, startAutoRefresh, refreshActiveTab } = await import("../../modules/refresh.js");

function setHidden(value) {
  Object.defineProperty(document, "hidden", { value, configurable: true });
}

beforeEach(() => {
  document.body.innerHTML = "";
  setHidden(false);
  state.me = { username: "alice", role: "admin" };
  state.tab = "containers";
  state.containers = [{ id: "c1" }];
  state.auditSearch = {};
  vi.clearAllMocks();
});

afterEach(() => {
  setHidden(false);
});

describe("tickBlocked", () => {
  it("is clear on a plain logged-in page", () => {
    expect(tickBlocked()).toBe(false);
  });

  it("blocks when logged out", () => {
    state.me = null;
    expect(tickBlocked()).toBe(true);
  });

  it("blocks when the page is hidden", () => {
    setHidden(true);
    expect(tickBlocked()).toBe(true);
  });

  it("blocks while a modal is open", () => {
    document.body.innerHTML = `<div class="modal-backdrop"></div>`;
    expect(tickBlocked()).toBe(true);
  });

  it("blocks while the user edits a control inside #view", () => {
    document.body.innerHTML = `<div id="view"><input id="f"></div>`;
    document.querySelector("#f").focus();
    expect(tickBlocked()).toBe(true);
  });

  it("does not block for controls outside #view (header search box)", () => {
    document.body.innerHTML = `<input id="f"><div id="view"></div>`;
    document.querySelector("#f").focus();
    expect(tickBlocked()).toBe(false);
  });
});

describe("refreshActiveTab", () => {
  it("re-renders on first data and on change, but not on identical data", async () => {
    await refreshActiveTab();
    expect(renderView).toHaveBeenCalledTimes(1);

    await refreshActiveTab();
    expect(renderView).toHaveBeenCalledTimes(1); // unchanged: DOM left alone

    state.containers = [{ id: "c1" }, { id: "c2" }];
    await refreshActiveTab();
    expect(renderView).toHaveBeenCalledTimes(2);
  });

  it("updates the notification badge on every tick", async () => {
    await refreshActiveTab();
    expect(fetchNotifications).toHaveBeenCalled();
    expect(updateNotificationBadge).toHaveBeenCalled();
  });

  it("does nothing while blocked", async () => {
    document.body.innerHTML = `<div class="modal-backdrop"></div>`;
    await refreshActiveTab();
    expect(refreshSection).not.toHaveBeenCalled();
    expect(renderView).not.toHaveBeenCalled();
  });

  it("never polls hardware data (it has its own poll loop), but the bell badge still refreshes", async () => {
    state.tab = "hardware";
    await refreshActiveTab();
    expect(refreshSection).not.toHaveBeenCalled();
    expect(fetchNotifications).toHaveBeenCalled();
  });

  it("never polls the static scripts tab, but does poll mcp/disks for live data", async () => {
    // scripts is a static config form — never polled.
    state.tab = "scripts";
    await refreshActiveTab();
    expect(refreshSection).not.toHaveBeenCalled();
    expect(renderView).not.toHaveBeenCalled();
    vi.clearAllMocks();

    // mcp polls its token list (via refreshMCPTokens), disks polls state.disks.
    for (const tab of ["mcp", "disks"]) {
      state.tab = tab;
      await refreshActiveTab();
    }
    expect(refreshSection).toHaveBeenCalled(); // disks loader calls refreshSection
  });

  it("skips audit polling while filters are active", async () => {
    state.tab = "audit";
    state.auditSearch = { actor: "bob" };
    await refreshActiveTab();
    expect(refreshSection).not.toHaveBeenCalled();
  });

  it("tab-switch refresh skips self-fetching tabs (renderView already fetched)", async () => {
    state.tab = "netdisk";
    await refreshActiveTab({ includeSelfFetch: false });
    expect(renderNetdisk).not.toHaveBeenCalled();

    // ...but a full tick (e.g. visibility resume) still refreshes netdisk.
    await refreshActiveTab();
    expect(renderNetdisk).toHaveBeenCalledTimes(1);
  });
});

describe("startAutoRefresh", () => {
  it("is idempotent: repeated calls schedule exactly one loop", () => {
    vi.useFakeTimers();
    try {
      startAutoRefresh();
      startAutoRefresh();
      startAutoRefresh();
      expect(vi.getTimerCount()).toBe(1);
    } finally {
      vi.clearAllTimers();
      vi.useRealTimers();
    }
  });
});
