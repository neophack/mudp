import { describe, it, expect } from "vitest";
import { parseRawInfoFromName, rawFrameSize, decodeBayer } from "../../lib/raw.js";

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

// TestDecodeBayerFlatField: a flat field (each CFA color constant across the
// whole frame) is the simplest correctness check for the bilinear demosaic.
// Interior pixels (at least one sample away from every edge) reconstruct
// exactly, since every neighbor they average is a real same-color sample; the
// outermost ring instead averages in edge-clamped duplicates of the wrong
// color, so this test deliberately only checks the interior.
describe("decodeBayer", () => {
  // 4x4 RGGB, 8-bit, one byte per sample:
  //   R G R G      200 100 200 100
  //   G B G B  =   100  50 100  50
  //   R G R G      200 100 200 100
  //   G B G B      100  50 100  50
  const buf = new Uint8Array([
    200, 100, 200, 100,
    100, 50, 100, 50,
    200, 100, 200, 100,
    100, 50, 100, 50,
  ]);
  const at = (rgba, w, x, y) => {
    const o = (y * w + x) * 4;
    return [rgba[o], rgba[o + 1], rgba[o + 2], rgba[o + 3]];
  };

  it("reconstructs a flat field to the same RGB at every interior pixel", () => {
    const rgba = decodeBayer(buf, 4, 4, 8, "RGGB");
    for (const y of [1, 2]) {
      for (const x of [1, 2]) {
        expect(at(rgba, 4, x, y)).toEqual([200, 100, 50, 255]);
      }
    }
  });

  it("swaps R/B reconstruction for BGGR", () => {
    // Same buffer, reinterpreted as BGGR: the CFA sites that were R become B
    // and vice versa, so the flat-field R/B constants swap; G is unaffected.
    const rgba = decodeBayer(buf, 4, 4, 8, "BGGR");
    for (const y of [1, 2]) {
      for (const x of [1, 2]) {
        expect(at(rgba, 4, x, y)).toEqual([50, 100, 200, 255]);
      }
    }
  });

  it("reads >8-bit samples as little-endian 16-bit values", () => {
    // 2x2 RGGB at 16-bit: R=1000 (0,0), G=500 (0,1)/(1,0), B=100 (1,1).
    const buf16 = new Uint8Array([
      0xe8, 0x03, // 1000 (R)
      0xf4, 0x01, // 500 (G)
      0xf4, 0x01, // 500 (G)
      0x64, 0x00, // 100 (B)
    ]);
    const rgba = decodeBayer(buf16, 2, 2, 16, "RGGB");
    const maxVal = (1 << 16) - 1;
    // Only the CFA site's own channel is read directly (unaveraged), so it's
    // the one value exact regardless of edge-clamping at the other channels.
    expect(rgba[0]).toBe(Math.round((1000 / maxVal) * 255)); // R at (0,0)
    expect(rgba[4 * 3 + 2]).toBe(Math.round((100 / maxVal) * 255)); // B at (1,1)
  });

  // TestDecodeBayerRowBandsMatchFullDecode: decodeBayerParallel splits the
  // frame into row bands and calls decodeBayer(..., rowStart, rowEnd) per
  // band (one call per worker in the real pool). This checks that band
  // decoding — using the *absolute* y for CFA parity/edge-clamping, per the
  // rowStart/rowEnd doc comment — produces byte-identical output to decoding
  // the whole frame at once, for a split that lands on an odd row (3) so an
  // asymmetric, non-2-aligned band boundary is actually exercised.
  it("decoding row bands separately matches decoding the whole frame", () => {
    const width = 6;
    const height = 6;
    // Deterministic non-flat data so a band-boundary bug (e.g. using a
    // relative instead of absolute y) would change pixel values, not just
    // get masked by every row looking the same.
    const full = new Uint8Array(width * height);
    for (let i = 0; i < full.length; i++) full[i] = (i * 17 + 5) % 256;

    const whole = decodeBayer(full, width, height, 8, "RGGB");
    const bandA = decodeBayer(full, width, height, 8, "RGGB", 0, 3);
    const bandB = decodeBayer(full, width, height, 8, "RGGB", 3, 6);

    const stitched = new Uint8ClampedArray(width * height * 4);
    stitched.set(bandA, 0);
    stitched.set(bandB, 3 * width * 4);

    expect(Array.from(stitched)).toEqual(Array.from(whole));
  });
});
