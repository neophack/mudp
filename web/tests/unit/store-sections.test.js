// @vitest-environment jsdom
import { describe, it, expect, beforeEach, vi } from "vitest";
import { store, refreshSection } from "../../src/store.js";

// The load-failure visibility pass: a section whose fetch fails must be
// flagged in store.sectionErrors (so views can say "load failed" instead of
// rendering a silent empty list), and a later success must clear the flag.
// api() only needs global fetch + document.cookie, both available under
// jsdom, so stubbing fetch is enough to drive the real code path.
function nextFetchResponds(status, body) {
  globalThis.fetch = vi.fn(async () => ({
    ok: status === 200,
    status,
    json: async () => body,
  }));
}

describe("refreshSection error flagging", () => {
  beforeEach(() => {
    store.containers = [];
    store.dashboard = null;
    store.usage = [];
    delete store.sectionErrors.containers;
    delete store.sectionErrors.dashboard;
  });

  it("flags a section whose fetch failed and leaves prior data untouched", async () => {
    store.containers = [{ id: "keep" }];
    nextFetchResponds(500, { error: "docker unavailable" });
    await refreshSection("containers");
    expect(store.sectionErrors.containers).toBe(true);
    expect(store.containers).toEqual([{ id: "keep" }]);
  });

  it("clears the flag once the section loads again", async () => {
    store.sectionErrors.containers = true;
    nextFetchResponds(200, [{ id: "fresh" }]);
    await refreshSection("containers");
    expect(store.sectionErrors.containers).toBeUndefined();
    expect(store.containers).toEqual([{ id: "fresh" }]);
  });

  it("tracks multi-section refreshes independently", async () => {
    const calls = [];
    globalThis.fetch = vi.fn(async (url) => {
      calls.push(String(url));
      if (String(url).includes("/api/dashboard")) {
        return { ok: false, status: 503, json: async () => ({ error: "down" }) };
      }
      return { ok: true, status: 200, json: async () => [] };
    });
    await refreshSection("containers", "dashboard");
    expect(store.sectionErrors.containers).toBeUndefined();
    expect(store.sectionErrors.dashboard).toBe(true);
    expect(calls.some((u) => u.includes("/api/containers"))).toBe(true);
  });

  it("mirrors dashboard usage into the usage section on success", async () => {
    const usage = [{ username: "alice", containers: 2 }];
    nextFetchResponds(200, { usage });
    await refreshSection("dashboard");
    expect(store.usage).toEqual(usage);
    expect(store.sectionErrors.dashboard).toBeUndefined();
  });
});
