// @vitest-environment jsdom
// Regression coverage for the Security > Logs pager: renderLogs() used to
// derive a `total` as `page*pageSize + logs.length` and then disable the
// Next button once `to >= total` -- which is true by construction on every
// page, since `total` is defined from what was just fetched. The Next button
// was therefore permanently disabled from the very first render. The fix
// infers "more pages exist" from whether the page just fetched came back
// full (view.hasMoreLogs = logs.length === pageSize).

import { describe, it, expect, vi, beforeEach } from "vitest";

const apiMock = vi.fn();

vi.mock("../../app.js", () => ({
  $: (sel) => document.querySelector(sel),
  state: { me: { role: "admin" } },
  api: (...args) => apiMock(...args),
  toast: vi.fn(),
  displayNameForUsername: (u) => u,
  t: (key) => key,
}));

vi.mock("../../modules/worldmap.js", () => ({
  drawWorldMap: vi.fn(),
}));

const { renderSecurity } = await import("../../modules/security.js");

function logRow(i) {
  return { id: i, event: "page_view", createdAt: new Date().toISOString() };
}

function flush() {
  return new Promise((r) => setTimeout(r, 0));
}

describe("Security > Logs pagination", () => {
  beforeEach(() => {
    document.body.innerHTML = '<div id="view"></div>';
    apiMock.mockReset();
  });

  it("enables Next when a full page comes back, and disables it once a short page ends the list", async () => {
    // First call (page 0): the overview tab's initial auto-load fires stats +
    // geo requests before we navigate to Logs; resolve those benignly.
    apiMock.mockImplementation(async (url) => {
      const u = String(url);
      if (u.includes("/access/stats")) return {};
      if (u.includes("/access/geo")) return [];
      if (u.includes("/access/logs")) {
        const offset = Number(new URL(u, "http://localhost").searchParams.get("offset") || 0);
        // Page 1 (offset 0): a full 25-row page -> more pages should exist.
        // Page 2 (offset 25): a short 3-row page -> that's the end of the list.
        const rows = offset === 0 ? 25 : 3;
        return Array.from({ length: rows }, (_, i) => logRow(offset + i));
      }
      throw new Error(`unexpected path ${u}`);
    });

    renderSecurity(); // renders the default "overview" tab, kicks off its load
    await flush();

    document.querySelector('[data-subtab="logs"]').onclick();
    await flush(); // let loadLogs(true)'s awaited api() call resolve and repaint

    const next = document.querySelector("#nextPage");
    expect(next).toBeTruthy();
    expect(next.disabled).toBe(false);

    next.onclick(); // page++, loadLogs(true) -> the short 3-row page
    await flush();

    const nextAfter = document.querySelector("#nextPage");
    expect(nextAfter.disabled).toBe(true);
  });
});
