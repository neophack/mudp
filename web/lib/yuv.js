// YUV raw-file viewer: previews raw YUV byte streams by rendering them on a
// canvas.
//
// Used by both the in-app netdisk viewer (modules/netdisk.js) and the public
// share page (share.js). Both call openYuvViewer() with two URLs for the file:
//   - rawURL  — the byte-serving endpoint (/api/netdisk/raw), used only to
//               learn the file size (and thus the frame count) via a Range
//               probe. The bytes themselves are never shipped to the browser.
//   - rasterURL — the server-side decode endpoint (/api/netdisk/raster) that
//               decodes one frame and returns it as a JPEG.
//
// Decoding (YUV->RGB, the ISP pass) now runs entirely in the backend
// (internal/raster), which is the same code that used to be compiled to WASM.
// This file just knows each format's per-frame byte size (to compute the frame
// count from the probed file size) and asks the backend for a JPEG per frame.
//
// The generic scaffold (canvas, frame nav/playback, zoom, ISP toggle, JPEG
// fetching, cached wheel-zoom) lives in openRasterFrameViewer and is also
// reused by the RAW Bayer viewer in lib/raw.js — see that function for the
// shared contract.

// ---------------- Format table ----------------

// Each format carries a label and frameSize(w, h) -> bytes per frame. This
// mirrors internal/server/raster.go's yuvFrameSize so the frontend's frame
// count matches the offsets the backend reads. Decoding itself is server-side.
const YUV_FORMATS = {
  // Planar 4:2:0, 12 bits/pixel. Y plane then Cb then Cr (a.k.a. I420).
  i420: {
    label: "I420 (YUV420P)",
    frameSize: (w, h) => w * h + 2 * ((w / 2) | 0) * ((h / 2) | 0),
  },
  // Planar 4:2:0 with V before U.
  yv12: {
    label: "YV12 (YVU420P)",
    frameSize: (w, h) => w * h + 2 * ((w / 2) | 0) * ((h / 2) | 0),
  },
  // Semi-planar 4:2:0, interleaved UV (a.k.a. NV12). Common on Android/camera.
  nv12: {
    label: "NV12 (YUV420SP)",
    frameSize: (w, h) => w * h + 2 * ((w / 2) | 0) * ((h / 2) | 0),
  },
  // Semi-planar 4:2:0, interleaved VU (a.k.a. NV21). Android default camera.
  nv21: {
    label: "NV21 (YVU420SP)",
    frameSize: (w, h) => w * h + 2 * ((w / 2) | 0) * ((h / 2) | 0),
  },
  // Packed 4:2:2, 16 bits/pixel. Y0 U0 Y1 V0 per 2 horizontal pixels.
  yuyv: {
    label: "YUYV (YUY2)",
    frameSize: (w, h) => 2 * w * h,
  },
  // Packed 4:2:2 with U first.
  uyvy: {
    label: "UYVY",
    frameSize: (w, h) => 2 * w * h,
  },
  // Planar 4:4:4, 24 bits/pixel. Full-resolution U and V.
  yuv444: {
    label: "YUV444P",
    frameSize: (w, h) => 3 * w * h,
  },
};

const DEFAULT_FORMAT = "i420";

// ---------------- Filename parsing ----------------

// parseYuvInfoFromName tries to recover width/height/format from common naming
// conventions (1920x1080, 1080p, _nv12, .yuyv, Linear_..._yuv444_968x776_8bit,
// etc). Returns null when nothing could be inferred; the caller then falls
// back to user input.
export function parseYuvInfoFromName(name) {
  const lower = String(name || "").toLowerCase();
  const info = {};

  // Dimensions: only accept 'x' or 'X' as the separator — NOT '_' or '-'.
  // Otherwise "yuv444_968x776" mis-parses as width=444 (the trailing digits of
  // the format token) × 968. We also strip out format keywords first so a token
  // like "yuv444" can't donate its digits to a dimension match.
  const sanitized = lower.replace(/yuv\d{3,4}|[iyn]\d{3}|yuyv|uyvy|nv12|nv21|yv12/i, " ");
  const dim = sanitized.match(/(\d{2,5})\s*[xX]\s*(\d{2,5})/);
  if (dim) {
    info.width = parseInt(dim[1], 10);
    info.height = parseInt(dim[2], 10);
  } else {
    // Shorthand: 1080p, 720p, 480p, 4k/2160p
    const p = lower.match(/(?:^|[_\-. ])(\d{3,4})p\b/);
    if (p) {
      const h = parseInt(p[1], 10);
      // Standard 16:9 dimensions for the common heights.
      const widths = { 2160: 3840, 1440: 2560, 1080: 1920, 720: 1280, 576: 720, 480: 720, 360: 640, 240: 320 };
      info.height = h;
      info.width = widths[h] || Math.round((h * 16) / 9);
    }
  }

  // Bit depth: "8bit", "10bit", "16bit", "8b", "10b". Defaults to 8.
  const bitMatch = lower.match(/(?:^|[_\-. ])(8|10|12|16)(?:bit|b)\b/);
  if (bitMatch) {
    info.bitDepth = parseInt(bitMatch[1], 10);
  }

  // Format keyword. The regex requires the token to be bracketed by non-alphanum
  // boundaries so "yuv444" matches as a whole, not as a substring of something
  // else. Check explicit format names first, then file extension.
  const fmtMap = {
    i420: "i420", yuv420p: "i420", yuv420: "i420",
    yv12: "yv12", yvu420p: "yv12",
    nv12: "nv12", yuv420sp: "nv12",
    nv21: "nv21", yvu420sp: "nv21",
    yuyv: "yuyv", yuy2: "yuyv", yunv: "yuyv",
    uyvy: "uyvy", uyvn: "uyvy",
    yuv444: "yuv444", yuv444p: "yuv444", "444p": "yuv444",
  };
  for (const key of Object.keys(fmtMap)) {
    const re = new RegExp(`(?:^|[_\\-. ])${key}(?:[_\\-. ]|$)`);
    if (re.test(lower)) {
      info.format = fmtMap[key];
      break;
    }
  }
  // An extension like .yuyv/.nv12 is itself a strong signal.
  if (!info.format) {
    const ext = lower.split(".").pop();
    if (fmtMap[ext]) info.format = fmtMap[ext];
  }

  // "Linear" or "raw" in the name signals an unprocessed sensor dump that needs
  // the ISP pipeline (gamma + white balance) rather than plain YUV→RGB.
  if (/(?:^|[_\-. ])(linear|raw)(?:[_\-. ]|$)/.test(lower)) {
    info.linear = true;
  }

  return info.width || info.height || info.format || info.linear ? info : null;
}

export function guessYuvFormat(name) {
  const info = parseYuvInfoFromName(name);
  return (info && info.format) || DEFAULT_FORMAT;
}

export { YUV_FORMATS };

// ---------------- Viewer ----------------
//
// openYuvViewer builds the YUV preview UI inside bodyEl. It returns a control
// object with destroy() so the host can tear down fetches + canvas when the
// modal closes (registered via onModalClose / resetViewerState).
//
// It's a thin config wrapper over openRasterFrameViewer, the generic frame
// viewer scaffold (canvas, dimension inputs, frame nav/playback, zoom, ISP
// toggle, JPEG fetching) shared with the RAW Bayer viewer in lib/raw.js — only
// the format dropdown, the frameSize function, and the per-frame raster URL
// builder are YUV-specific.

export function openYuvViewer({ name, url, rasterUrl, bodyEl }) {
  const parsed = parseYuvInfoFromName(name) || {};
  const initialFormat = parsed.format || DEFAULT_FORMAT;
  // Labels are static English strings from our own table, so no escaping needed.
  const formatOptionsHtml = Object.keys(YUV_FORMATS)
    .map((k) => `<option value="${k}"${k === initialFormat ? " selected" : ""}>${YUV_FORMATS[k].label}</option>`)
    .join("");

  return openRasterFrameViewer({
    url,
    rasterUrl,
    bodyEl,
    width: parsed.width || 1920,
    height: parsed.height || 1080,
    // Auto-enable the ISP pipeline when the filename signals a raw/linear dump;
    // the user can still toggle it off to compare. The ISP pass runs server-side.
    isp: !!parsed.linear,
    extraControlsHtml:
      `<div class="raster-control-group">` +
        `<label>Format</label>` +
        `<select id="yuvFormat">${formatOptionsHtml}</select>` +
      `</div>`,
    bindExtraControls(root, state, onExtraChange) {
      state.format = initialFormat;
      const formatSelect = root.querySelector("#yuvFormat");
      formatSelect.addEventListener("change", () => {
        state.format = formatSelect.value;
        onExtraChange();
      });
    },
    frameSize: (state) => YUV_FORMATS[state.format].frameSize(state.width, state.height),
    frameURL: (state) =>
      `${rasterUrl}&width=${state.width}&height=${state.height}&frame=${state.frame}` +
      `&format=${encodeURIComponent(state.format)}&isp=${state.isp ? 1 : 0}`,
    statusLabel: (state) => YUV_FORMATS[state.format].label,
  });
}

// openRasterFrameViewer is the generic scaffold behind both the YUV and RAW
// viewers: it owns the canvas, dimension inputs, frame slider/playback, zoom,
// ISP toggle, and on-demand JPEG fetching. Callers supply the format-specific
// pieces: extra control markup + wiring (bindExtraControls), and functions to
// compute per-frame byte size (frameSize — only for the frame-count math) and
// build the backend decode URL for the current frame (frameURL).
//
// Decoding is server-side: each paint fetches one JPEG from frameURL, decodes
// it to an ImageBitmap, and draws it onto the canvas at the current zoom. The
// raw sensor bytes never reach the browser.
export function openRasterFrameViewer({
  url,
  rasterUrl,
  bodyEl,
  width,
  height,
  isp,
  extraControlsHtml,
  bindExtraControls,
  frameSize,
  frameURL,
  statusLabel,
  // Optional: called once from destroy() so a caller that attached extra
  // resources to state in bindExtraControls can release them.
  onDestroy,
}) {
  const state = {
    url,
    rasterUrl,
    width,
    height,
    isp: !!isp,
    frame: 0,
    frameCount: 0,
    totalBytes: 0,
    zoom: 1,
    // Playback: play/pause state, target frames-per-second, loop toggle, and
    // the timer id of the pending play tick (cleared on pause/destroy).
    playing: false,
    fps: 24,
    loop: true,
    playTimer: null,
    // activeFetchAbort lets destroy() cancel an in-flight JPEG request so the
    // viewer never paints into a detached canvas after close.
    abort: null,
    destroyed: false,
    // Cache of the last decoded frame's ImageBitmap, keyed by the inputs that
    // affect its pixels (frame/isp/dimensions/format). Zoom is purely a
    // display-scale change, so applyZoom() redraws from this cache instead of
    // re-fetching the JPEG over the network on every wheel tick — without it,
    // each zoom step round-trips through an async fetch before the
    // cursor-anchored scroll correction can run, and out-of-order completions
    // during fast wheel scrolling make the image appear to jump/scroll on its
    // own.
    cachedBitmap: null,
    cachedFrame: -1,
    cachedIsp: null,
    cachedWidth: -1,
    cachedHeight: -1,
  };

  bodyEl.innerHTML = rasterLayoutHtml({ isp: state.isp, extraControlsHtml });
  const canvas = bodyEl.querySelector("#rasterCanvas");
  const canvasWrap = bodyEl.querySelector("#rasterCanvasWrap");
  const ctx = canvas.getContext("2d");
  const ispCheckbox = bodyEl.querySelector("#rasterIsp");
  const widthInput = bodyEl.querySelector("#rasterWidth");
  const heightInput = bodyEl.querySelector("#rasterHeight");
  const frameSlider = bodyEl.querySelector("#rasterFrameSlider");
  const frameInput = bodyEl.querySelector("#rasterFrameInput");
  const frameMax = bodyEl.querySelector("#rasterFrameMax");
  const prevBtn = bodyEl.querySelector("#rasterPrevFrame");
  const nextBtn = bodyEl.querySelector("#rasterNextFrame");
  const playBtn = bodyEl.querySelector("#rasterPlayBtn");
  const loopCheckbox = bodyEl.querySelector("#rasterLoop");
  const fpsInput = bodyEl.querySelector("#rasterFps");
  const zoomVal = bodyEl.querySelector("#rasterZoomVal");
  const zoomFitBtn = bodyEl.querySelector("#rasterZoomFit");
  const zoomResetBtn = bodyEl.querySelector("#rasterZoomReset");
  const status = bodyEl.querySelector("#rasterStatus");

  widthInput.value = state.width;
  heightInput.value = state.height;
  if (zoomVal) zoomVal.textContent = `${Math.round(state.zoom * 100)}%`;
  fpsInput.value = state.fps;

  if (bindExtraControls) bindExtraControls(bodyEl, state, recomputeFrameCount);

  // Probe the file size (a single-byte Range request against the byte-serving
  // raw endpoint) so we can compute frame count and validate the
  // width/height/format guess against the actual byte stream. The raw bytes
  // themselves are never downloaded.
  probeFileSize(url).then((size) => {
    if (state.destroyed) return;
    state.totalBytes = size;
    recomputeFrameCount();
  });

  function recomputeFrameCount() {
    const per = frameSize(state);
    if (per <= 0 || state.totalBytes <= 0) {
      state.frameCount = 1;
    } else {
      state.frameCount = Math.max(1, Math.floor(state.totalBytes / per));
    }
    if (state.frame >= state.frameCount) state.frame = state.frameCount - 1;
    if (state.frame < 0) state.frame = 0;
    const max = state.frameCount - 1;
    frameSlider.max = max;
    frameSlider.value = state.frame;
    frameInput.value = state.frame + 1;
    frameInput.max = state.frameCount;
    frameMax.textContent = ` / ${state.frameCount}`;
    // Dim the navigation/playback controls when there's only one frame.
    const multi = state.frameCount > 1;
    frameSlider.disabled = !multi;
    prevBtn.disabled = !multi;
    nextBtn.disabled = !multi;
    playBtn.disabled = !multi;
    if (!multi && state.playing) pause();
    paintCurrentFrame();
  }

  async function paintCurrentFrame() {
    if (state.destroyed) return;
    const per = frameSize(state);
    if (state.width <= 0 || state.height <= 0) {
      status.textContent = "Enter a valid width and height.";
      return;
    }
    if (per <= 0) {
      status.textContent = "Unsupported dimensions for this format.";
      return;
    }
    // Cancel any in-flight fetch before starting a new one so rapid frame
    // changes / control tweaks don't paint a stale frame on top of a newer one.
    if (state.abort) state.abort.abort();
    const controller = new AbortController();
    state.abort = controller;
    status.textContent = `Loading frame ${state.frame + 1}/${state.frameCount}…`;
    try {
      const bitmap = await fetchFrameBitmap(frameURL(state), controller.signal);
      if (state.destroyed || controller.signal.aborted) {
        bitmap?.close?.();
        return;
      }
      drawFrame(ctx, canvas, bitmap, state.width, state.height, state.zoom);
      // Replace the previously cached bitmap (if any) and release it; an
      // ImageBitmap owns GPU/native memory, so closing avoids leaks across
      // many frame changes.
      if (state.cachedBitmap && state.cachedBitmap !== bitmap) {
        state.cachedBitmap.close?.();
      }
      state.cachedBitmap = bitmap;
      state.cachedFrame = state.frame;
      state.cachedIsp = state.isp;
      state.cachedWidth = state.width;
      state.cachedHeight = state.height;
      if (zoomVal) zoomVal.textContent = `${Math.round(state.zoom * 100)}%`;
      status.textContent = `${state.width}×${state.height} ${statusLabel(state)} · frame ${state.frame + 1}/${state.frameCount} · zoom ${Math.round(state.zoom * 100)}%${state.isp ? " · ISP on" : ""}`;
    } catch (err) {
      if (state.destroyed || controller.signal.aborted) return;
      status.textContent = `Failed to load frame: ${err.message || err}`;
    } finally {
      if (state.abort === controller) state.abort = null;
    }
  }

  function onDimChange() {
    const w = Math.max(1, parseInt(widthInput.value, 10) || 0);
    const h = Math.max(1, parseInt(heightInput.value, 10) || 0);
    if (w === state.width && h === state.height) return;
    state.width = w;
    state.height = h;
    recomputeFrameCount();
  }
  function onFrameChange(v) {
    let f = Math.max(0, Math.min(state.frameCount - 1, parseInt(v, 10) || 0));
    if (f === state.frame) return;
    state.frame = f;
    frameSlider.value = f;
    frameInput.value = f + 1;
    paintCurrentFrame();
  }
  // applyZoom sets the zoom level and repaints. When anchorClientX/Y are given
  // (viewport coordinates, e.g. from a wheel event), the point of the image
  // under that coordinate is kept under the same screen position after the
  // resize — this is what makes wheel-zoom feel like it zooms "into" the
  // cursor instead of always growing from the canvas's centered origin. When
  // a cached decode of the current frame is available (the common case — only
  // the display scale changed) this redraws synchronously; otherwise it has to
  // wait for paintCurrentFrame to finish (it's async — it re-fetches the
  // JPEG) before the canvas has its new size to scroll against.
  function applyZoom(newZoom, anchorClientX, anchorClientY) {
    const clamped = Math.max(0.1, Math.min(64, newZoom));
    if (clamped === state.zoom) return;
    let anchor = null;
    if (anchorClientX != null && anchorClientY != null) {
      const canvasRect = canvas.getBoundingClientRect();
      const wrapRect = canvasWrap.getBoundingClientRect();
      anchor = {
        // Position of the cursor within the *source image*, in unzoomed pixels.
        imgX: (anchorClientX - canvasRect.left) / state.zoom,
        imgY: (anchorClientY - canvasRect.top) / state.zoom,
        // Position of the cursor within the scroll container's viewport — this
        // is what must stay fixed on screen.
        viewportX: anchorClientX - wrapRect.left,
        viewportY: anchorClientY - wrapRect.top,
      };
    }
    state.zoom = clamped;
    if (zoomVal) zoomVal.textContent = `${Math.round(state.zoom * 100)}%`;
    const canRedrawFromCache =
      state.cachedBitmap &&
      state.cachedFrame === state.frame &&
      state.cachedIsp === state.isp &&
      state.cachedWidth === state.width &&
      state.cachedHeight === state.height;
    if (canRedrawFromCache) {
      // Fast path: same pixel data, only the display scale changed. Redraw
      // synchronously from the cached bitmap and apply the scroll correction
      // immediately — no network round-trip, so there's no window for a
      // stale/out-of-order completion to jerk the view during fast scrolling.
      drawFrame(ctx, canvas, state.cachedBitmap, state.width, state.height, state.zoom);
      status.textContent = `${state.width}×${state.height} ${statusLabel(state)} · frame ${state.frame + 1}/${state.frameCount} · zoom ${Math.round(state.zoom * 100)}%${state.isp ? " · ISP on" : ""}`;
      if (anchor) {
        canvasWrap.scrollLeft = anchor.imgX * state.zoom - anchor.viewportX;
        canvasWrap.scrollTop = anchor.imgY * state.zoom - anchor.viewportY;
      }
      return;
    }
    const painted = paintCurrentFrame();
    if (anchor) {
      painted.then(() => {
        if (state.destroyed) return;
        canvasWrap.scrollLeft = anchor.imgX * state.zoom - anchor.viewportX;
        canvasWrap.scrollTop = anchor.imgY * state.zoom - anchor.viewportY;
      });
    }
  }
  function onWheel(e) {
    // Ctrl/Cmd + wheel, or plain wheel over the canvas, zooms toward the cursor.
    // We preventDefault so the page doesn't also scroll. Each notch multiplies
    // zoom by ~1.15 (wheel up zooms in, down zooms out).
    e.preventDefault();
    const delta = -e.deltaY;
    const factor = delta > 0 ? 1.15 : 1 / 1.15;
    applyZoom(state.zoom * factor, e.clientX, e.clientY);
  }
  // computeFitZoom returns the largest zoom that shows the whole image inside
  // the current canvas-wrap viewport without cropping either axis. Zoom is a
  // single scalar applied to both dimensions (see drawFrame), so the image
  // never stretches — it's always scaled uniformly.
  function computeFitZoom() {
    const cs = getComputedStyle(canvasWrap);
    const padX = parseFloat(cs.paddingLeft || "0") + parseFloat(cs.paddingRight || "0");
    const padY = parseFloat(cs.paddingTop || "0") + parseFloat(cs.paddingBottom || "0");
    const availW = Math.max(1, canvasWrap.clientWidth - padX);
    const availH = Math.max(1, canvasWrap.clientHeight - padY);
    return Math.min(availW / state.width, availH / state.height);
  }
  function fitToWindow() {
    applyZoom(computeFitZoom());
  }
  function onIspToggle() {
    state.isp = !!ispCheckbox.checked;
    paintCurrentFrame();
  }

  // --- Drag-to-pan: click and drag the image to scroll it, the way desktop
  // image viewers (IrfanView, Preview, Photoshop) work — hunting for a
  // scrollbar to move around a zoomed-in image is not how anyone actually
  // does it. Restricted to mouse (pointerType === "mouse") so touch devices
  // keep their native scroll/pinch-zoom gestures untouched.
  let pan = null;
  function onPointerDown(e) {
    if (e.pointerType && e.pointerType !== "mouse") return;
    if (e.button !== 0) return;
    pan = {
      pointerId: e.pointerId,
      startX: e.clientX,
      startY: e.clientY,
      startScrollLeft: canvasWrap.scrollLeft,
      startScrollTop: canvasWrap.scrollTop,
    };
    canvasWrap.setPointerCapture(e.pointerId);
    canvasWrap.classList.add("panning");
    e.preventDefault();
  }
  function onPointerMove(e) {
    if (!pan || pan.pointerId !== e.pointerId) return;
    canvasWrap.scrollLeft = pan.startScrollLeft - (e.clientX - pan.startX);
    canvasWrap.scrollTop = pan.startScrollTop - (e.clientY - pan.startY);
  }
  function endPan(e) {
    if (!pan || (e && pan.pointerId !== e.pointerId)) return;
    canvasWrap.classList.remove("panning");
    pan = null;
  }
  // Double-click toggles between "fit to window" and 100% (actual pixels),
  // zooming toward the click point — the same toggle desktop viewers bind to
  // double-click.
  function onDblClick(e) {
    const fitZoom = computeFitZoom();
    const atFit = Math.abs(state.zoom - fitZoom) < 0.01;
    applyZoom(atFit ? 1 : fitZoom, e.clientX, e.clientY);
  }

  // --- Playback: play advances one frame per 1000/fps ms, looping or stopping
  // at the last frame depending on the Loop checkbox. Pause on user frame nav
  // (prev/next/slider) is intentionally NOT done — the user may scrub while
  // playing — but pressing Play again toggles to Pause.
  function play() {
    if (state.playing || state.frameCount <= 1) return;
    state.playing = true;
    playBtn.textContent = "⏸ Pause";
    scheduleNextFrame();
  }
  function pause() {
    state.playing = false;
    if (state.playTimer) {
      clearTimeout(state.playTimer);
      state.playTimer = null;
    }
    if (playBtn) playBtn.textContent = "▶ Play";
  }
  function togglePlay() {
    if (state.playing) pause();
    else play();
  }
  function scheduleNextFrame() {
    if (!state.playing) return;
    const interval = Math.max(1, 1000 / Math.max(1, state.fps));
    state.playTimer = setTimeout(async () => {
      if (!state.playing) return;
      let next = state.frame + 1;
      if (next >= state.frameCount) {
        if (state.loop) {
          next = 0;
        } else {
          pause();
          return;
        }
      }
      state.frame = next;
      frameSlider.value = next;
      frameInput.value = next + 1;
      await paintCurrentFrame();
      scheduleNextFrame();
    }, interval);
  }

  widthInput.addEventListener("change", onDimChange);
  heightInput.addEventListener("change", onDimChange);
  ispCheckbox.addEventListener("change", onIspToggle);
  frameSlider.addEventListener("input", () => onFrameChange(frameSlider.value));
  frameInput.addEventListener("change", () => onFrameChange(Number(frameInput.value) - 1));
  prevBtn.addEventListener("click", () => onFrameChange(state.frame - 1));
  nextBtn.addEventListener("click", () => onFrameChange(state.frame + 1));
  playBtn.addEventListener("click", togglePlay);
  loopCheckbox.addEventListener("change", () => { state.loop = !!loopCheckbox.checked; });
  fpsInput.addEventListener("change", () => {
    state.fps = Math.max(1, Math.min(120, parseInt(fpsInput.value, 10) || 24));
    fpsInput.value = state.fps;
  });
  zoomFitBtn.addEventListener("click", fitToWindow);
  zoomResetBtn.addEventListener("click", () => applyZoom(1));
  // Wheel zoom on the canvas area. passive:false so we can preventDefault and
  // stop the page/scroll container from also scrolling while zooming.
  canvasWrap.addEventListener("wheel", onWheel, { passive: false });
  canvasWrap.addEventListener("pointerdown", onPointerDown);
  canvasWrap.addEventListener("pointermove", onPointerMove);
  canvasWrap.addEventListener("pointerup", endPan);
  canvasWrap.addEventListener("pointercancel", endPan);
  canvasWrap.addEventListener("dblclick", onDblClick);

  return {
    state,
    // repaint(): re-render the current frame at the current size (e.g. after a
    // fullscreen toggle changes the available canvas width).
    repaint: paintCurrentFrame,
    destroy() {
      state.destroyed = true;
      pause();
      if (state.abort) state.abort.abort();
      if (state.cachedBitmap) {
        state.cachedBitmap.close?.();
        state.cachedBitmap = null;
      }
      if (onDestroy) onDestroy(state);
    },
  };
}

function rasterLayoutHtml({ isp, extraControlsHtml }) {
  return (
    `<div class="viewer-raster-layout">` +
      `<div class="raster-canvas-wrap" id="rasterCanvasWrap"><canvas id="rasterCanvas" class="viewer-page raster-canvas"></canvas></div>` +
      `<aside class="raster-controls">` +
        `<div class="raster-control-group">` +
          `<label>Width</label>` +
          `<input id="rasterWidth" type="number" min="1" step="1">` +
        `</div>` +
        `<div class="raster-control-group">` +
          `<label>Height</label>` +
          `<input id="rasterHeight" type="number" min="1" step="1">` +
        `</div>` +
        (extraControlsHtml || "") +
        `<div class="raster-control-group raster-checkbox-group">` +
          `<label class="check"><input type="checkbox" id="rasterIsp"${isp ? " checked" : ""}><span>ISP (auto white balance + gamma)</span></label>` +
          `<span class="hint">Enable for raw/linear sensor dumps that look grey or tinted.</span>` +
        `</div>` +
        `<div class="raster-control-group">` +
          `<label>Playback</label>` +
          `<div class="raster-play-bar">` +
            `<button id="rasterPrevFrame" class="ghost" title="Previous frame">◀</button>` +
            `<button id="rasterPlayBtn" class="ghost" title="Play / Pause">▶ Play</button>` +
            `<button id="rasterNextFrame" class="ghost" title="Next frame">▶|</button>` +
          `</div>` +
          `<div class="raster-frame-slider-row">` +
            `<input id="rasterFrameSlider" type="range" min="0" max="0" value="0">` +
            `<span class="raster-frame-readout"><input id="rasterFrameInput" type="number" min="1" value="1"><span id="rasterFrameMax" class="hint"> / 1</span></span>` +
          `</div>` +
          `<div class="raster-fps-row">` +
            `<label class="check"><input type="checkbox" id="rasterLoop" checked><span>Loop</span></label>` +
            `<label class="raster-fps-label">FPS <input id="rasterFps" type="number" min="1" max="120" step="1" value="24"></label>` +
          `</div>` +
        `</div>` +
        `<div class="raster-control-group raster-zoom-readout">` +
          `<label>Zoom (scroll on the image, centered on cursor)</label>` +
          `<div class="raster-zoom-row">` +
            `<span id="rasterZoomVal">100%</span>` +
            `<div class="raster-zoom-btns">` +
              `<button class="ghost" id="rasterZoomFit" title="Fit image to window">Fit</button>` +
              `<button class="ghost" id="rasterZoomReset" title="Reset zoom to 100%">Reset</button>` +
            `</div>` +
          `</div>` +
        `</div>` +
        `<div class="raster-status" id="rasterStatus">Loading…</div>` +
      `</aside>` +
    `</div>`
  );
}

// drawFrame paints the decoded frame onto the canvas at the requested zoom.
// The canvas backing store is sized to the full zoomed pixel dimensions
// (w*zoom × h*zoom) so every source pixel maps to a real screen pixel — no
// browser upscaling, which keeps pixel-peeping sharp at any zoom. The CSS size
// matches the backing size 1:1, and the surrounding .raster-canvas-wrap
// scrolls to reveal the parts that overflow the viewport (the image is never
// squashed to fit). `bitmap` is an ImageBitmap decoded from the backend's
// JPEG response.
function drawFrame(ctx, canvas, bitmap, w, h, zoom) {
  const dispW = Math.max(1, Math.round(w * zoom));
  const dispH = Math.max(1, Math.round(h * zoom));
  canvas.width = dispW;
  canvas.height = dispH;
  canvas.style.width = `${dispW}px`;
  canvas.style.height = `${dispH}px`;
  ctx.setTransform(1, 0, 0, 1, 0, 0);
  ctx.imageSmoothingEnabled = false; // nearest-neighbor for pixel-peeping zoom
  ctx.clearRect(0, 0, dispW, dispH);
  ctx.drawImage(bitmap, 0, 0, w, h, 0, 0, dispW, dispH);
}

// probeFileSize learns the file's total byte length without downloading it.
// We can't use HEAD: the chi router registers /api/netdisk/raw with r.Get only,
// so a HEAD returns 405. Instead we issue a Range request for a single byte
// (bytes=0-0); the server replies 206 with a Content-Range header of the form
// "bytes 0-0/<total>", which carries the full size. Falls back to the GET
// Content-Length if Range is unsupported.
async function probeFileSize(url) {
  try {
    const res = await fetch(url, {
      credentials: "same-origin",
      headers: { Range: "bytes=0-0" },
    });
    // 206 = Range honored: parse the total out of Content-Range.
    if (res.status === 206) {
      const cr = res.headers.get("Content-Range") || "";
      // "bytes 0-0/12345"
      const m = cr.match(/\/(\d+)$/);
      if (m) return parseInt(m[1], 10);
    }
    // 200 = server ignored Range; Content-Length is the whole file size.
    if (res.status === 200) {
      const len = Number(res.headers.get("Content-Length") || 0);
      // Discard the body so the connection can be reused.
      res.body?.cancel?.();
      return len > 0 ? len : 0;
    }
    return 0;
  } catch {
    return 0;
  }
}

// fetchFrameBitmap fetches one decoded frame as a JPEG from the backend raster
// endpoint and decodes it to an ImageBitmap ready to draw. Abortable so rapid
// frame changes cancel the previous in-flight request. Throws on a non-OK
// response (the body carries a JSON error message from the server).
async function fetchFrameBitmap(url, signal) {
  const res = await fetch(url, { credentials: "same-origin", signal });
  if (!res.ok) {
    let detail = `HTTP ${res.status}`;
    try {
      const err = await res.json();
      if (err && err.error) detail = err.error;
    } catch {
      /* response had no JSON body; keep the status code */
    }
    throw new Error(detail);
  }
  const blob = await res.blob();
  // createImageBitmap is the cheapest path: it decodes off the main thread and
  // hands back a handle the canvas can draw without an intermediate <img>.
  return createImageBitmap(blob);
}
