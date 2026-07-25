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

// These cover the upload-queue display: files actively uploading must render
// above still-pending and already-settled ones, driven by CSS `order` rather
// than DOM reordering (reordering nodes mid-transfer would restart each row's
// progress-bar width transition).
describe("showUploadOverlay row ordering", () => {
  afterEach(() => {
    document.querySelectorAll(".upload-overlay").forEach((el) => el.remove());
  });

  function rowOrder(overlay, index) {
    void overlay;
    const row = document.querySelector(`.upload-file-row[data-idx="${index}"]`);
    return row.style.order;
  }

  it("starts every row in the pending order slot", () => {
    const overlay = showUploadOverlay([{ file: { name: "a", size: 1 } }, { file: { name: "b", size: 1 } }]);
    expect(rowOrder(overlay, 0)).toBe("1");
    expect(rowOrder(overlay, 1)).toBe("1");
  });

  it("moves an uploading row ahead of pending rows", () => {
    const overlay = showUploadOverlay([
      { file: { name: "a", size: 1 } },
      { file: { name: "b", size: 1 } },
      { file: { name: "c", size: 1 } },
    ]);
    overlay.setStatus(1, "uploading");
    expect(Number(rowOrder(overlay, 1))).toBeLessThan(Number(rowOrder(overlay, 0)));
    expect(Number(rowOrder(overlay, 1))).toBeLessThan(Number(rowOrder(overlay, 2)));
  });

  it("sinks done and error rows below pending ones", () => {
    const overlay = showUploadOverlay([
      { file: { name: "a", size: 1 } },
      { file: { name: "b", size: 1 } },
      { file: { name: "c", size: 1 } },
    ]);
    overlay.setStatus(0, "done");
    overlay.setStatus(1, "error", "boom");
    expect(Number(rowOrder(overlay, 0))).toBeGreaterThan(Number(rowOrder(overlay, 2)));
    expect(Number(rowOrder(overlay, 1))).toBeGreaterThan(Number(rowOrder(overlay, 2)));
  });
});
