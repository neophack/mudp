// @vitest-environment jsdom
// Regression coverage for 96ee8c8 (stop wheel-zoom jitter and top-align bug
// in raster viewer). Before that fix, every wheel tick re-fetched and
// re-decoded the current frame over the network before applying the
// cursor-anchored scroll correction; during fast scrolling, out-of-order
// completions of those async round-trips made the image appear to scroll on
// its own. applyZoom() now redraws from a cached decode (keyed on
// frame/isp/dimensions) when only the display scale changed, applying the
// scroll correction synchronously, and only falls back to the async
// fetch+decode path when the cache doesn't match the current frame.
//
// Decoding moved server-side: the scaffold now fetches a JPEG per frame
// (fetchFrameBitmap) instead of raw bytes + a client decode. This test stubs
// fetch + createImageBitmap so the wheel-zoom caching contract is still
// exercised end to end.

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { openRasterFrameViewer } from "../../lib/yuv.js";

// A throwaway stand-in for a decoded frame. The scaffold only forwards it to
// ctx.drawImage, which the mock context records as a no-op.
const FAKE_BITMAP = { __fakeBitmap: true };

function makeMockCtx() {
  return {
    setTransform: () => {},
    clearRect: () => {},
    drawImage: () => {},
    imageSmoothingEnabled: true,
  };
}

function makeFetchMock() {
  return vi.fn(async () => ({
    // The scaffold's fetchFrameBitmap only needs ok + blob(); the body itself
    // is irrelevant because createImageBitmap is stubbed below.
    ok: true,
    blob: async () => new Blob(),
  }));
}

async function flush() {
  await new Promise((r) => setTimeout(r, 0));
}

let fetchMock;
let createImageBitmapMock;
let viewer;
let originalGetContext;
let originalCreateImageBitmap;

function frameFetchCalls() {
  return fetchMock.mock.calls.filter(([url]) => typeof url === "string" && url.includes("frame="));
}

function lastFrameFetchURL() {
  const calls = frameFetchCalls();
  return calls.length ? calls[calls.length - 1][0] : "";
}

function openViewer({ totalBytes, frameURL, bindExtraControls }) {
  fetchMock = makeFetchMock();
  vi.stubGlobal("fetch", fetchMock);
  createImageBitmapMock = vi.fn(async () => FAKE_BITMAP);
  vi.stubGlobal("createImageBitmap", createImageBitmapMock);

  // probeFileSize issues a single-byte Range probe against the raw URL; the
  // scaffold keys frame-count math off its parsed total. The frame-count
  // itself is irrelevant to the wheel-zoom cache, so we feed it 1 frame worth
  // of bytes (or 2 frames in the cache-miss case).
  const realFetch = fetchMock;
  vi.stubGlobal("fetch", async (url, opts) => {
    const range = opts?.headers?.Range || "";
    if (range === "bytes=0-0") {
      return {
        status: 206,
        headers: { get: (k) => (k === "Content-Range" ? `bytes 0-0/${totalBytes}` : null) },
      };
    }
    return realFetch(url, opts);
  });

  const bodyEl = document.createElement("div");
  document.body.appendChild(bodyEl);
  viewer = openRasterFrameViewer({
    url: "/api/netdisk/raw?path=test.yuv",
    rasterUrl: "/api/netdisk/raster?path=test.yuv",
    bodyEl,
    width: 2,
    height: 2,
    isp: false,
    extraControlsHtml: "",
    bindExtraControls: bindExtraControls || null,
    frameSize: () => 1, // 1 byte per frame -> totalBytes == frame count
    frameURL: frameURL || ((state) => `/api/netdisk/raster?frame=${state.frame}`),
    statusLabel: () => "test",
  });
  return {
    canvasWrap: bodyEl.querySelector("#rasterCanvasWrap"),
    frameSlider: bodyEl.querySelector("#rasterFrameSlider"),
  };
}

beforeEach(() => {
  document.body.innerHTML = "";
  originalGetContext = HTMLCanvasElement.prototype.getContext;
  // jsdom ships no <canvas> rasteriser; stand in a no-op recording context,
  // same approach worldmap.test.js uses.
  HTMLCanvasElement.prototype.getContext = () => makeMockCtx();
  originalCreateImageBitmap = globalThis.createImageBitmap;
});

afterEach(() => {
  if (viewer) viewer.destroy();
  viewer = null;
  HTMLCanvasElement.prototype.getContext = originalGetContext;
  if (originalCreateImageBitmap !== undefined) {
    globalThis.createImageBitmap = originalCreateImageBitmap;
  } else {
    delete globalThis.createImageBitmap;
  }
  vi.unstubAllGlobals();
});

describe("openRasterFrameViewer wheel-zoom caching", () => {
  it("redraws zoom-only changes from the cached frame instead of re-fetching over the network", async () => {
    const { canvasWrap } = openViewer({ totalBytes: 1 }); // 1 frame
    await flush();
    expect(viewer.state.cachedFrame).toBe(0);
    expect(frameFetchCalls().length).toBe(1);

    for (let i = 0; i < 5; i++) {
      canvasWrap.dispatchEvent(new WheelEvent("wheel", { deltaY: -100, clientX: 5, clientY: 5, cancelable: true }));
    }
    await flush();

    // Five wheel ticks, still only the one fetch from the initial paint.
    expect(frameFetchCalls().length).toBe(1);
    expect(viewer.state.zoom).not.toBe(1);
  });

  it("applies the cursor-anchored scroll correction synchronously on a cache hit", async () => {
    const { canvasWrap } = openViewer({ totalBytes: 1 });
    await flush();
    const zoomBefore = viewer.state.zoom;

    canvasWrap.dispatchEvent(new WheelEvent("wheel", { deltaY: -100, clientX: 5, clientY: 5, cancelable: true }));

    // No await: on a cache hit, the scroll correction must already be applied
    // by the time the (synchronous) wheel handler returns. The bug this
    // guards was a window where an async repaint let the correction apply
    // late/out of order, making the image visibly jump.
    expect(viewer.state.zoom).not.toBe(zoomBefore);
    const factor = viewer.state.zoom / zoomBefore;
    expect(canvasWrap.scrollLeft).toBeCloseTo(5 * factor - 5, 5);
    expect(canvasWrap.scrollTop).toBeCloseTo(5 * factor - 5, 5);
  });

  it("falls back to an async repaint (and re-fetches) when the cache doesn't match the current frame", async () => {
    const { canvasWrap, frameSlider } = openViewer({ totalBytes: 2 }); // 2 frames
    await flush();
    expect(frameFetchCalls().length).toBe(1);

    // Switch to frame 1 but don't let its fetch resolve yet -- the cache
    // still holds frame 0's decode at this point.
    frameSlider.value = "1";
    frameSlider.dispatchEvent(new Event("input"));
    expect(viewer.state.frame).toBe(1);
    expect(viewer.state.cachedFrame).toBe(0);

    canvasWrap.dispatchEvent(new WheelEvent("wheel", { deltaY: -100, clientX: 5, clientY: 5, cancelable: true }));

    // A stale cache must never be reused for a different frame: this has to
    // fall back to the async fetch+decode path, not silently redraw frame 0's
    // pixels under frame 1's zoom.
    expect(frameFetchCalls().length).toBeGreaterThan(1);
    await flush();
    expect(viewer.state.cachedFrame).toBe(1);
  });
});

// The cached bitmap is keyed on the frame's fetch URL, which covers the
// caller-supplied controls too (YUV format, RAW bit depth / Bayer pattern).
// Keying on the ISP state alone left those out: after changing the format,
// a wheel-zoom would take the cache fast path and redraw the *previous*
// format's decode — and, because the fast path also rewrites the status line,
// replace the failed fetch's error message with a normal-looking one.
describe("openRasterFrameViewer frame cache keying", () => {
  it("invalidates the cache when a caller-supplied control changes the frame URL", async () => {
    let format = "i420";
    let onChange = null;
    const { canvasWrap } = openViewer({
      totalBytes: 1,
      frameURL: (state) => `/api/netdisk/raster?frame=${state.frame}&format=${format}`,
      bindExtraControls: (root, state, notify) => {
        onChange = notify;
      },
    });
    await flush();
    expect(frameFetchCalls().length).toBe(1);
    expect(lastFrameFetchURL()).toContain("format=i420");

    // Change the format, then zoom before its repaint has resolved -- exactly
    // the window where the i420 bitmap was still sitting in the cache while
    // the viewer had already moved to nv12.
    format = "nv12";
    onChange();
    const afterChange = frameFetchCalls().length;
    expect(afterChange).toBeGreaterThan(1);

    canvasWrap.dispatchEvent(new WheelEvent("wheel", { deltaY: -100, clientX: 5, clientY: 5, cancelable: true }));

    // The zoom must not take the cache fast path: the cached bitmap holds
    // i420 pixels and the viewer is now showing nv12. Taking it would both
    // redraw the wrong decode and overwrite the status line.
    expect(frameFetchCalls().length).toBeGreaterThan(afterChange);
    await flush();
    expect(lastFrameFetchURL()).toContain("format=nv12");
  });
});
