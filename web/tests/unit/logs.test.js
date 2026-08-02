// @vitest-environment jsdom
// Regression coverage for the container Logs modal's "Follow" SSE stream:
// closing the modal via Esc/backdrop-click goes through ui.js's closeModal(),
// not the modal's own [data-close] button handler. Before the fix, only the
// button handler called stopFollow(), so Esc/backdrop-click left the log-tail
// EventSource open and streaming from the server indefinitely.

import { describe, it, expect, vi, beforeEach } from "vitest";

vi.mock("../../app.js", () => ({
  state: { logViewer: {} },
  api: vi.fn(async () => ({ logs: "" })),
  toast: vi.fn(),
}));

const { state } = await import("../../app.js");
const { renderLogViewer } = await import("../../modules/logs.js");
const { closeModal } = await import("../../modules/ui.js");

describe("renderLogViewer follow-stream cleanup", () => {
  let instances;

  beforeEach(() => {
    document.body.innerHTML = "";
    // closeModal() (exercised by these tests) sets state.logViewer.open =
    // false as a side effect; the mocked state object is shared across tests
    // in this file, so it has to be reset each time or renderLogViewer()
    // would see a stale "closed" viewer and bail out before rendering.
    state.logViewer = {
      open: true,
      id: "c1",
      title: "mycontainer",
      content: "existing log line\n",
      tail: 300,
      follow: true,
      grep: "",
      wrap: true,
    };
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

  it("closes the follow EventSource when the modal is dismissed via closeModal (Esc/backdrop)", () => {
    renderLogViewer(); // follow: true in state.logViewer starts the stream immediately
    expect(document.querySelector(".modal-backdrop.logs-modal")).toBeTruthy();
    expect(instances.length).toBe(1);
    expect(instances[0].closed).toBe(false);

    closeModal(); // simulates Esc/backdrop-click, which bypasses the [data-close] button

    expect(instances[0].closed).toBe(true);
  });

  it("still closes the follow EventSource via the explicit Close button", () => {
    renderLogViewer();
    const closeBtn = document.querySelector(".logs-modal [data-close]");
    expect(closeBtn).toBeTruthy();
    closeBtn.onclick();
    expect(instances[0].closed).toBe(true);
  });
});
