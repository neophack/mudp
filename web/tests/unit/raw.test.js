import { describe, it, expect } from "vitest";
import { parseRawInfoFromName, rawFrameSize } from "../../lib/raw.js";

describe("parseRawInfoFromName", () => {
  it("parses the capture tool's naming convention", () => {
    const info = parseRawInfoFromName("RAW_1936x1552_16bits_RGGB_Linear_20251011144549.raw");
    expect(info).toEqual({
      width: 1936,
      height: 1552,
      bitDepth: 16,
      bayerPattern: "RGGB",
      linear: true,
    });
  });

  it("parses other bit depths and Bayer patterns without a Linear tag", () => {
    const info = parseRawInfoFromName("cam_640x480_8bits_bggr.raw");
    expect(info).toEqual({ width: 640, height: 480, bitDepth: 8, bayerPattern: "BGGR" });
  });

  it("returns null when nothing can be inferred", () => {
    expect(parseRawInfoFromName("dump.raw")).toBeNull();
  });
});

describe("rawFrameSize", () => {
  it("uses 1 byte/sample at 8-bit and 2 bytes/sample above 8-bit", () => {
    expect(rawFrameSize(4, 4, 8)).toBe(16);
    expect(rawFrameSize(4, 4, 16)).toBe(32);
    expect(rawFrameSize(4, 4, 12)).toBe(32);
  });
});

// decodeBayer's demosaic algorithm now runs in WASM — see
// internal/raster/bayer_test.go for the ported flat-field, 16-bit
// little-endian, and row-band-vs-whole-frame regression tests that used to
// live here.
