import { describe, it, expect } from "vitest";
import { fmtSpeed } from "../../lib/upload.js";

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
