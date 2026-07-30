// @vitest-environment jsdom
import { describe, it, expect, afterEach } from "vitest";
import { fmtSpeed, showUploadOverlay } from "../../lib/upload.js";

describe("fmtSpeed", () => {
  it("formats bytes per second across units", () => {
    expect(fmtSpeed(0)).toBe("0 B/s");
    expect(fmtSpeed(512)).toBe("512 B/s");
    expect(fmtSpeed(2048)).toBe("2.00 KB/s");
    expect(fmtSpeed(5 * 1024 * 1024)).toBe("5.00 MB/s");
    expect(fmtSpeed(2 * 1024 * 1024 * 1024)).toBe("2.00 GB/s");
  });

  it("rounds and clamps odd input", () => {
    expect(fmtSpeed(1536.7)).toBe("1.50 KB/s");
    expect(fmtSpeed(-5)).toBe("0 B/s");
    expect(fmtSpeed(undefined)).toBe("0 B/s");
  });
});

afterEach(() => {
  document.querySelectorAll(".upload-overlay").forEach((el) => el.remove());
});

// The overlay is a bounded window: only the active batch + a small ring of
// recently-settled files are ever rendered, so the DOM node count is constant
// regardless of how many files an upload touches. The whole point of this card
// (and the reason it replaced the old per-file rendering) is that a
// multi-million-file folder must NOT create one row per file.
describe("showUploadOverlay bounded window", () => {
  it("renders an active row on addActive and retires it on settleActive", () => {
    const overlay = showUploadOverlay();
    const slot = overlay.addActive({ name: "a.pdf", size: 1024 });
    const active = document.querySelectorAll(".upload-active .upload-file-row");
    expect(active.length).toBe(1);
    expect(active[0].classList.contains("is-uploading")).toBe(true);

    overlay.settleActive(slot, "done");
    // Retired out of the active area…
    expect(document.querySelectorAll(".upload-active .upload-file-row").length).toBe(0);
    // …into the settled ring, marked done.
    const settled = document.querySelectorAll(".upload-settled .upload-file-row");
    expect(settled.length).toBe(1);
    expect(settled[0].classList.contains("is-done")).toBe(true);
    overlay.close();
  });

  it("updates an active row's progress via updateActive", () => {
    const overlay = showUploadOverlay();
    const slot = overlay.addActive({ name: "a", size: 100 });
    overlay.updateActive(slot, { loaded: 50, total: 100, percent: 50, speedBps: 1024 });
    const row = document.querySelector(".upload-active .upload-file-row");
    expect(row.querySelector(".upload-file-bar > .bar-fill").style.width).toBe("50%");
    expect(row.querySelector(".upload-file-status").textContent).toContain("50%");
    overlay.close();
  });

  it("marks a settled error row and surfaces its message", () => {
    const overlay = showUploadOverlay();
    const slot = overlay.addActive({ name: "bad", size: 10 });
    overlay.settleActive(slot, "error", "boom");
    const row = document.querySelector(".upload-settled .upload-file-row");
    expect(row.classList.contains("is-error")).toBe(true);
    expect(row.querySelector(".upload-file-status").textContent).toBe("boom");
    overlay.close();
  });

  it("reports done/failed/total counts through updateOverall", () => {
    const overlay = showUploadOverlay();
    overlay.updateOverall({ done: 3, failed: 1, total: 5, loaded: 100, bytesTotal: 500, speedBps: 0, percent: 20 });
    const counts = document.querySelector(".upload-counts").textContent;
    expect(counts).toContain("3");
    expect(counts).toContain("5");
    expect(counts).toContain("1");
    overlay.close();
  });

  it("arms a Retry button on a failed row and fires the callback when clicked", () => {
    const overlay = showUploadOverlay();
    const slot = overlay.addActive({ name: "broken.pdf", size: 5 });
    let retried = 0;
    overlay.markFailedWithRetry(slot, "crc32 mismatch", () => { retried++; });
    const row = document.querySelector(".upload-file-row");
    expect(row.classList.contains("is-error")).toBe(true);
    const btn = row.querySelector(".upload-file-retry");
    expect(btn.hidden).toBe(false);
    btn.click();
    expect(retried).toBe(1);
    overlay.close();
  });

  it("hides the Retry button on a done row", () => {
    const overlay = showUploadOverlay();
    const slot = overlay.addActive({ name: "ok.txt", size: 3 });
    overlay.settleActive(slot, "done");
    const btn = document.querySelector(".upload-file-row .upload-file-retry");
    expect(btn.hidden).toBe(true);
    overlay.close();
  });

  it("reactivates a failed row back into the active area for retry", () => {
    const overlay = showUploadOverlay();
    const slot = overlay.addActive({ name: "x", size: 1 });
    overlay.markFailedWithRetry(slot, "boom", () => {});
    expect(document.querySelectorAll(".upload-settled .upload-file-row").length).toBe(1);
    overlay.reactivate(slot, { name: "x", size: 1 });
    expect(document.querySelectorAll(".upload-active .upload-file-row").length).toBe(1);
    expect(document.querySelectorAll(".upload-settled .upload-file-row").length).toBe(0);
    overlay.close();
  });

  // THE regression guard: cycling far more files than the caps allow through the
  // card must leave the row count bounded. Old code rendered one row per file
  // eagerly — a million-file folder crashed the page. Here we simulate 5000
  // add/settle cycles and assert the DOM never exceeds active+settled caps.
  it("keeps DOM node count bounded across many add/settle cycles", () => {
    const overlay = showUploadOverlay();
    const N = 5000;
    for (let i = 0; i < N; i++) {
      const slot = overlay.addActive({ name: `f${i}`, size: 1 });
      overlay.updateActive(slot, { loaded: 1, total: 1, percent: 100, speedBps: 0 });
      overlay.settleActive(slot, "done");
    }
    const total = document.querySelectorAll(".upload-file-row").length;
    // Active area is empty (everything settled), settled ring is capped, plus the
    // overflow line summarises the rest. Must be far smaller than N.
    expect(total).toBeLessThan(32);
    const overflow = document.querySelector(".upload-overflow");
    expect(overflow.style.display).not.toBe("none");
    expect(parseInt(overflow.textContent.replace(/\D/g, ""), 10)).toBeGreaterThan(0);
    overlay.close();
  });
});
