import { describe, it, expect } from "vitest";
import { YUV_FORMATS } from "../../lib/yuv.js";

// Per-format frameSize() is the one piece of format-specific logic still in
// JS (decoding itself now runs server-side — see internal/raster/yuv_test.go
// for the ported chroma-plane-order regression test that used to live here).
describe("YUV_FORMATS frameSize", () => {
  it("i420 and yv12 (4:2:0 planar) use the same byte layout", () => {
    expect(YUV_FORMATS.i420.frameSize(2, 2)).toBe(6);
    expect(YUV_FORMATS.yv12.frameSize(2, 2)).toBe(6);
  });
});
