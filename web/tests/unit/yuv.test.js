import { describe, it, expect } from "vitest";
import { YUV_FORMATS } from "../../lib/yuv.js";

// TestI420AndYV12ChromaPlaneOrder is a regression test: the i420 and yv12
// entries in YUV_FORMATS called decodePlanar420 with their U/V byte offsets
// swapped, so the "I420" decoder actually read bytes in YV12 order and vice
// versa -- every I420 file (the viewer's default format) rendered with wrong
// colors. A 2x2 frame has one 1-byte U sample and one 1-byte V sample, so the
// two layouts differ only in which of those two bytes is which:
//   I420 file layout: [Y Y Y Y][U][V]
//   YV12 file layout: [Y Y Y Y][V][U]
// Both must decode to the SAME color when fed the logically same U/V values,
// laid out in each format's own byte order.
describe("YUV_FORMATS chroma plane order", () => {
  const Y = [128, 128, 128, 128];
  const U = 255; // uu = +127
  const V = 0; // vv = -128
  // Expected from yuvToRgb(128, 255, 0):
  //   r = 128 + 1.402*(-128)          ≈ 0   (clamped)
  //   g = 128 - 0.344*127 - 0.714*-128 ≈ 176
  //   b = 128 + 1.772*127              ≈ 255 (clamped)
  const expected = [0, 176, 255];
  // If U and V were swapped, yuvToRgb(128, 0, 255) instead gives ≈[255,81,0]
  // -- the opposite corner of the color space, so a broken decoder cannot
  // pass this assertion by accident.
  const tolerance = 3;

  function expectPixelNear(rgba, [r, g, b]) {
    expect(Math.abs(rgba[0] - r)).toBeLessThanOrEqual(tolerance);
    expect(Math.abs(rgba[1] - g)).toBeLessThanOrEqual(tolerance);
    expect(Math.abs(rgba[2] - b)).toBeLessThanOrEqual(tolerance);
    expect(rgba[3]).toBe(255);
  }

  it("decodes I420 (Y, U, V order) correctly", () => {
    const buf = new Uint8Array([...Y, U, V]);
    const rgba = YUV_FORMATS.i420.decode(buf, 2, 2);
    expectPixelNear(rgba.slice(0, 4), expected);
  });

  it("decodes YV12 (Y, V, U order) correctly", () => {
    const buf = new Uint8Array([...Y, V, U]);
    const rgba = YUV_FORMATS.yv12.decode(buf, 2, 2);
    expectPixelNear(rgba.slice(0, 4), expected);
  });

  it("frameSize matches the buffer layout used above", () => {
    expect(YUV_FORMATS.i420.frameSize(2, 2)).toBe(6);
    expect(YUV_FORMATS.yv12.frameSize(2, 2)).toBe(6);
  });
});
