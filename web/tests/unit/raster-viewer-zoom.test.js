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

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { openRasterFrameViewer } from "../../lib/yuv.js";

const PER_FRAME = 4; // arbitrary byte count; decode() below ignores the actual bytes

function makeMockCtx() {
  return {
    setTransform: () => {},
    clearRect: () => {},
    drawImage: () => {},
    putImageData: () => {},
    imageSmoothingEnabled: true,
  };
}

function makeFetchMock(totalBytes) {
  return vi.fn(async (url, opts) => {
    const range = opts?.headers?.Range || "";
    if (range === "bytes=0-0") {
      // probeFileSize's size probe.
      return {
        status: 206,
        headers: { get: (k) => (k === "Content-Range" ? `bytes 0-0/${totalBytes}` : null) },
      };
    }
    // fetchFrameBytes's per-frame Range request.
    return {
      status: 206,
      headers: { get: () => null },
      arrayBuffer: async () => new ArrayBuffer(PER_FRAME),
    };
  });
}

async function flush() {
  await new Promise((r) => setTimeout(r, 0));
}

let fetchMock;
let viewer;
let originalGetContext;

function frameFetchCalls() {
  return fetchMock.mock.calls.filter(([, opts]) => opts?.headers?.Range !== "bytes=0-0");
}

function openViewer({ totalBytes }) {
  fetchMock = makeFetchMock(totalBytes);
  vi.stubGlobal("fetch", fetchMock);
  const bodyEl = document.createElement("div");
  document.body.appendChild(bodyEl);
  viewer = openRasterFrameViewer({
    url: "/api/netdisk/raw?path=test.yuv",
    bodyEl,
    width: 2,
    height: 2,
    isp: false,
    extraControlsHtml: "",
    bindExtraControls: null,
    frameSize: () => PER_FRAME,
    decode: () => new Uint8ClampedArray(2 * 2 * 4).fill(128),
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
  vi.stubGlobal(
    "ImageData",
    class {
      constructor(data, width, height) {
        this.data = data;
        this.width = width;
        this.height = height;
      }
    },
  );
});

afterEach(() => {
  if (viewer) viewer.destroy();
  viewer = null;
  HTMLCanvasElement.prototype.getContext = originalGetContext;
  vi.unstubAllGlobals();
});

describe("openRasterFrameViewer wheel-zoom caching", () => {
  it("redraws zoom-only changes from the cached frame instead of re-fetching over the network", async () => {
    const { canvasWrap } = openViewer({ totalBytes: PER_FRAME }); // 1 frame
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
    const { canvasWrap } = openViewer({ totalBytes: PER_FRAME });
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
    const { canvasWrap, frameSlider } = openViewer({ totalBytes: PER_FRAME * 2 }); // 2 frames
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
