// @vitest-environment jsdom
// Coverage for the fnOS-style update UI: the update window (version
// transition, release notes, manual downloads) and the full-screen upgrade
// view's polling state machine (download progress → install → restart →
// success/reload, and the rollback-message error path).

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

const apiMock = vi.fn();

vi.mock("../../app.js", () => ({
  state: {},
  api: (...args) => apiMock(...args),
  toast: vi.fn(),
  escapeHtml: (s) => String(s ?? "").replace(/[&<>"]/g, (m) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;" }[m])),
  t: (key, params) => (params ? `${key}:${JSON.stringify(params)}` : key),
}));

vi.mock("../../modules/ui.js", () => ({
  showModal: vi.fn(({ body, foot }) => {
    document.querySelectorAll(".modal-backdrop").forEach((el) => el.remove());
    const wrap = document.createElement("div");
    wrap.className = "modal-backdrop";
    wrap.innerHTML = `<div class="modal-body">${body}</div>` + (foot ? `<div class="modal-foot">${foot}</div>` : "");
    document.body.appendChild(wrap);
  }),
  closeModal: vi.fn(() => {
    document.querySelectorAll(".modal-backdrop").forEach((el) => el.remove());
  }),
}));

const { openUpgradeModal } = await import("../../modules/upgrade.js");

const CHECK = {
  current: "v1.0.0",
  latest: "v9.9.9",
  available: true,
  notes: "## Changes\n- fix <crash> on start\n- add b",
  releasedAt: "2026-08-01T00:00:00Z",
  downloads: {
    windows: "https://x/mudp-windows-amd64-v9.9.9.zip",
    linux: "https://x/mudp-linux-amd64-v9.9.9.tar.gz",
  },
};

beforeEach(() => {
  vi.useFakeTimers();
  apiMock.mockReset();
  document.body.innerHTML = "";
  delete window.location;
  window.location = { reload: vi.fn() };
});

afterEach(() => {
  vi.useRealTimers();
});

async function tick(ms) {
  await vi.advanceTimersByTimeAsync(ms);
}

describe("update window", () => {
  it("shows the version transition, notes, and manual downloads", async () => {
    await openUpgradeModal(CHECK);
    const body = document.querySelector(".modal-body").innerHTML;
    expect(body).toContain("v9.9.9");
    expect(body).toContain("v1.0.0 → v9.9.9");
    // Markdown list markers and headings are stripped, content is escaped.
    expect(document.querySelectorAll(".upgrade-notes li").length).toBe(3);
    expect(document.querySelector(".upgrade-notes").innerHTML).not.toContain("- fix");
    expect(document.querySelector(".upgrade-notes").innerHTML).toContain("fix &lt;crash&gt;");
    expect(body).toContain("mudp-windows-amd64-v9.9.9.zip");
    expect(document.getElementById("upgradeNowBtn")).not.toBeNull();
  });

  it("falls back to fetching the check when called without one", async () => {
    apiMock.mockResolvedValueOnce(CHECK);
    await openUpgradeModal();
    expect(apiMock).toHaveBeenCalledWith("/api/update/check");
    expect(document.getElementById("upgradeNowBtn")).not.toBeNull();
  });

  it("renders the quiet up-to-date window without an action button", async () => {
    await openUpgradeModal({ ...CHECK, available: false });
    expect(document.querySelector(".modal-body").innerHTML).toContain("upgrade.upToDateTitle");
    expect(document.getElementById("upgradeNowBtn")).toBeNull();
  });
});

describe("full-screen upgrade view", () => {
  it("walks download → install → restart → success and reloads", async () => {
    await openUpgradeModal(CHECK);
    apiMock.mockImplementation(async (url, opts) => {
      if (opts && opts.method === "POST") return { started: true };
      if (url === "/api/admin/upgrade") return { phase: "running:download", read: 5 << 20, total: 10 << 20 };
      return { version: "v1.0.0" };
    });
    document.getElementById("upgradeNowBtn").click();

    // The update window is replaced by the locked overlay.
    expect(document.querySelector(".modal-backdrop")).toBeNull();
    expect(document.querySelector(".upgrade-overlay")).not.toBeNull();
    expect(apiMock).toHaveBeenCalledWith("/api/admin/upgrade", expect.objectContaining({ method: "POST" }));
    expect(document.getElementById("upgradePhase").textContent).toBe("upgrade.preparing");

    // Download progress paints the ring percentage and byte counts.
    await tick(1100);
    expect(document.getElementById("upgradePhase").textContent).toBe("upgrade.downloading");
    expect(document.getElementById("upgradeRingText").textContent).toBe("50%");
    expect(document.getElementById("upgradeRing").classList.contains("is-spin")).toBe(false);

    // Swap confirmed server-side → installing (indeterminate spinner).
    apiMock.mockImplementation(async (url) => {
      if (url === "/api/admin/upgrade") return { phase: "running:restarting" };
      return { version: "v1.0.0" };
    });
    await tick(1100);
    expect(document.getElementById("upgradePhase").textContent).toBe("upgrade.installing");
    expect(document.getElementById("upgradeRing").classList.contains("is-spin")).toBe(true);

    // Old process gone (fetch refuses) → restarting; then the new version
    // answers → success ring and a page reload.
    apiMock.mockImplementation(async (url) => {
      if (url === "/api/admin/upgrade") throw new Error("connection refused");
      return { version: "v1.0.0" };
    });
    await tick(1100);
    expect(document.getElementById("upgradePhase").textContent).toBe("upgrade.restarting");

    apiMock.mockImplementation(async (url) => {
      if (url === "/api/admin/upgrade") throw new Error("connection refused");
      return { version: "v9.9.9" };
    });
    await tick(1100);
    expect(document.getElementById("upgradeRing").classList.contains("is-ok")).toBe(true);
    expect(document.getElementById("upgradePhase").textContent).toBe("upgrade.success");
    await tick(1300);
    expect(window.location.reload).toHaveBeenCalled();
  });

  it("paints the error state with a close button when the server fails", async () => {
    await openUpgradeModal(CHECK);
    apiMock.mockImplementation(async (url) => {
      if (url === "/api/admin/upgrade") return { phase: "error", message: "no release asset" };
      return { version: "v1.0.0" };
    });
    document.getElementById("upgradeNowBtn").click();
    await tick(1100);
    expect(document.getElementById("upgradePhase").textContent).toContain("no release asset");
    expect(document.getElementById("upgradeDetail").textContent).toBe("upgrade.rolledBack");
    const close = document.getElementById("upgradeCloseBtn");
    expect(close).not.toBeNull();
    close.click();
    expect(document.querySelector(".upgrade-overlay")).toBeNull();
  });

  it("adopts an already-running upgrade when the start POST conflicts", async () => {
    await openUpgradeModal(CHECK);
    apiMock.mockImplementation(async (url, opts) => {
      if (opts && opts.method === "POST") throw new Error("an upgrade is already in progress");
      if (url === "/api/admin/upgrade") return { phase: "running:download", read: 1, total: 2 };
      return { version: "v1.0.0" };
    });
    document.getElementById("upgradeNowBtn").click();
    await tick(1100);
    // The 409 didn't kill the overlay — the poll re-attached to the run.
    expect(document.querySelector(".upgrade-overlay")).not.toBeNull();
    expect(document.getElementById("upgradePhase").textContent).toBe("upgrade.downloading");
    expect(document.getElementById("upgradeCloseBtn")).toBeNull();
  });
});
