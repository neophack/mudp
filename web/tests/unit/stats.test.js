// @vitest-environment jsdom
// Regression coverage for the container Stats modal's SSE stream: closing the
// modal via Esc/backdrop-click goes through ui.js's closeModal(), not the
// modal's own [data-close] button handler. Before the fix, only the button
// handler called es.close(), so Esc/backdrop-click left the EventSource open
// and streaming from the server indefinitely.

import { describe, it, expect, vi, beforeEach } from "vitest";

vi.mock("../../app.js", () => ({
  state: {},
  escapeHtml: (s) => String(s ?? ""),
}));

const { openStats } = await import("../../modules/stats.js");
const { closeModal } = await import("../../modules/ui.js");

describe("openStats SSE cleanup", () => {
  let instances;

  beforeEach(() => {
    document.body.innerHTML = "";
    instances = [];
    global.EventSource = class {
      constructor(url) {
        this.url = url;
        this.closed = false;
        instances.push(this);
      }
      addEventListener() {}
      close() {
        this.closed = true;
      }
    };
  });

  it("closes the EventSource when the modal is dismissed via closeModal (Esc/backdrop), not just the Close button", async () => {
    await openStats("c1", "mycontainer");
    expect(document.querySelector(".modal-backdrop.stats-modal")).toBeTruthy();
    expect(instances.length).toBe(1);
    expect(instances[0].closed).toBe(false);

    closeModal(); // simulates Esc/backdrop-click, which bypasses the [data-close] button

    expect(instances[0].closed).toBe(true);
    expect(document.querySelector(".modal-backdrop.stats-modal")).toBeFalsy();
  });

  it("still closes the EventSource via the explicit Close button", async () => {
    await openStats("c1", "mycontainer");
    const closeBtn = document.querySelector(".stats-modal [data-close]");
    expect(closeBtn).toBeTruthy();
    closeBtn.onclick();
    expect(instances[0].closed).toBe(true);
  });
});
