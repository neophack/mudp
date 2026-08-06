// YUV raw-file viewer: parses raw YUV byte streams and renders them on a canvas.
//
// Used by both the in-app netdisk viewer (modules/netdisk.js) and the public
// share page (share.js). Both call openYuvViewer() with the file's raw URL and
// a body element; this module owns the canvas, the control panel, frame
// navigation, and on-demand byte fetching via HTTP Range requests (the backend
// serves files through /api/netdisk/raw which supports Range, so we only
// download the bytes of the frame being shown, not the whole file).
//
// Decoders are hand-written (no dependency). Each entry in YUV_FORMATS knows how
// to compute per-frame byte size and turn a frame's bytes into RGBA.
//
// The generic scaffold (canvas, frame nav/playback, zoom, ISP toggle, Range
// fetching) lives in openRasterFrameViewer and is also reused by the RAW
// Bayer viewer in lib/raw.js — see that function for the shared contract.

// ---------------- Format table ----------------

// Each format carries:
//   frameSize(w, h) -> bytes per frame
//   decode(buf, w, h) -> Uint8ClampedArray of RGBA (w*h*4)
const YUV_FORMATS = {
  // Planar 4:2:0, 12 bits/pixel. Y plane then Cb then Cr (a.k.a. I420).
  i420: {
    label: "I420 (YUV420P)",
    frameSize: (w, h) => w * h + 2 * ((w / 2) | 0) * ((h / 2) | 0),
    // Layout is Y, U, V: U comes right after Y, V after that.
    decode: (b, w, h) => decodePlanar420(b, w, h, 0, w * h, (w * h) + ((w / 2) | 0) * ((h / 2) | 0)),
  },
  // Planar 4:2:0 with V before U.
  yv12: {
    label: "YV12 (YVU420P)",
    frameSize: (w, h) => w * h + 2 * ((w / 2) | 0) * ((h / 2) | 0),
    // Layout is Y, V, U: V comes right after Y, U after that.
    decode: (b, w, h) => decodePlanar420(b, w, h, 0, (w * h) + ((w / 2) | 0) * ((h / 2) | 0), w * h),
  },
  // Semi-planar 4:2:0, interleaved UV (a.k.a. NV12). Common on Android/camera.
  nv12: {
    label: "NV12 (YUV420SP)",
    frameSize: (w, h) => w * h + 2 * ((w / 2) | 0) * ((h / 2) | 0),
    decode: (b, w, h) => decodeSemiPlanar420(b, w, h, false),
  },
  // Semi-planar 4:2:0, interleaved VU (a.k.a. NV21). Android default camera.
  nv21: {
    label: "NV21 (YVU420SP)",
    frameSize: (w, h) => w * h + 2 * ((w / 2) | 0) * ((h / 2) | 0),
    decode: (b, w, h) => decodeSemiPlanar420(b, w, h, true),
  },
  // Packed 4:2:2, 16 bits/pixel. Y0 U0 Y1 V0 per 2 horizontal pixels.
  yuyv: {
    label: "YUYV (YUY2)",
    frameSize: (w, h) => 2 * w * h,
    decode: (b, w, h) => decodePacked422(b, w, h, [0, 1, 3]),
  },
  // Packed 4:2:2 with U first.
  uyvy: {
    label: "UYVY",
    frameSize: (w, h) => 2 * w * h,
    decode: (b, w, h) => decodePacked422(b, w, h, [1, 0, 2]),
  },
  // Planar 4:4:4, 24 bits/pixel. Full-resolution U and V.
  yuv444: {
    label: "YUV444P",
    frameSize: (w, h) => 3 * w * h,
    decode: (b, w, h) => decodePlanar444(b, w, h),
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

// ---------------- Decoders ----------------
//
// YUV→RGB conversion uses the BT.601 studio-swing formula. We clamp per-pixel
// via Uint8ClampedArray so the caller can write the result straight into an
// ImageData buffer.

function yuvToRgb(y, u, v) {
  // u/v are 0..255 centered at 128.
  const uu = u - 128;
  const vv = v - 128;
  const r = y + 1.402 * vv;
  const g = y - 0.344 * uu - 0.714 * vv;
  const b = y + 1.772 * uu;
  return [r, g, b];
}

function decodePlanar420(buf, w, h, yOff, uOff, vOff) {
  const out = new Uint8ClampedArray(w * h * 4);
  const hw = (w / 2) | 0;
  for (let j = 0; j < h; j++) {
    for (let i = 0; i < w; i++) {
      const ci = (i >> 1);
      const cj = (j >> 1);
      const y = buf[yOff + j * w + i];
      const u = buf[uOff + cj * hw + ci];
      const v = buf[vOff + cj * hw + ci];
      const [r, g, b] = yuvToRgb(y, u, v);
      const o = (j * w + i) * 4;
      out[o] = r;
      out[o + 1] = g;
      out[o + 2] = b;
      out[o + 3] = 255;
    }
  }
  return out;
}

function decodeSemiPlanar420(buf, w, h, vuFirst) {
  const out = new Uint8ClampedArray(w * h * 4);
  const hw = (w / 2) | 0;
  const uvOff = w * h;
  for (let j = 0; j < h; j++) {
    for (let i = 0; i < w; i++) {
      const ci = (i >> 1);
      const cj = (j >> 1);
      const y = buf[j * w + i];
      const uvIdx = uvOff + (cj * hw + ci) * 2;
      // In NV12 the pair is (U,V); in NV21 it is (V,U).
      const u = vuFirst ? buf[uvIdx + 1] : buf[uvIdx];
      const v = vuFirst ? buf[uvIdx] : buf[uvIdx + 1];
      const [r, g, b] = yuvToRgb(y, u, v);
      const o = (j * w + i) * 4;
      out[o] = r;
      out[o + 1] = g;
      out[o + 2] = b;
      out[o + 3] = 255;
    }
  }
  return out;
}

// yuvOffsets: indices into a 4-byte macro-pixel for [Y, U, V] respectively.
function decodePacked422(buf, w, h, idx) {
  const out = new Uint8ClampedArray(w * h * 4);
  for (let j = 0; j < h; j++) {
    for (let i = 0; i < w; i++) {
      const macro = ((j * w + i) >> 1) * 4; // 4 bytes per 2 horizontal pixels
      const y = buf[macro + idx[0]];
      const u = buf[macro + idx[1]];
      const v = buf[macro + idx[2]];
      const [r, g, b] = yuvToRgb(y, u, v);
      const o = (j * w + i) * 4;
      out[o] = r;
      out[o + 1] = g;
      out[o + 2] = b;
      out[o + 3] = 255;
      // The second pixel in the macro shares U,V; it has its own Y one byte later.
      if (i + 1 < w) {
        const y2 = buf[macro + idx[0] + 2];
        const [r2, g2, b2] = yuvToRgb(y2, u, v);
        const o2 = (j * w + i + 1) * 4;
        out[o2] = r2;
        out[o2 + 1] = g2;
        out[o2 + 2] = b2;
        out[o2 + 3] = 255;
        i++; // handled both pixels
      }
    }
  }
  return out;
}

function decodePlanar444(buf, w, h) {
  const out = new Uint8ClampedArray(w * h * 4);
  const uOff = w * h;
  const vOff = 2 * w * h;
  for (let j = 0; j < h; j++) {
    for (let i = 0; i < w; i++) {
      const y = buf[j * w + i];
      const u = buf[uOff + j * w + i];
      const v = buf[vOff + j * w + i];
      const [r, g, b] = yuvToRgb(y, u, v);
      const o = (j * w + i) * 4;
      out[o] = r;
      out[o + 1] = g;
      out[o + 2] = b;
      out[o + 3] = 255;
    }
  }
  return out;
}

export { YUV_FORMATS };

// ---------------- ISP (image signal processing) ----------------
//
// Raw sensor YUV dumps ("Linear_...yuv") are linear-light and un-white-balanced:
// a plain YUV→RGB map looks dark, desaturated, and tinted. applyIsp runs a
// minimal but effective pipeline directly on the decoded RGBA buffer:
//
//   1. Auto white balance (gray-world): scale R/G/B so their channel averages
//      converge — removes the color cast.
//   2. sRGB gamma (OETF, γ≈2.2): lifts dark mid-tones into the perceptual range
//      where the image looks naturally exposed instead of crushed.
//   3. Saturation boost: raw conversions come out muted; a mild gain on chroma
//      distance from the pixel's luma restores vivid color.
//
// All three are O(n) single-pass over the pixel buffer; on a 968×776 frame this
// is well under a frame, so toggling the checkbox re-renders instantly.

// Precomputed sRGB gamma LUT (input 0..255 linear → 0..255 sRGB). Linear input
// below ~0.0031 is handled by the linear segment of the sRGB transfer function.
const SRGB_GAMMA_LUT = (() => {
  const lut = new Uint8ClampedArray(256);
  for (let i = 0; i < 256; i++) {
    const v = i / 255;
    // Convert linear-light to sRGB-encoded. Values below the breakpoint use a
    // linear ramp; above it use the 2.4-power curve.
    const enc = v <= 0.0031308 ? v * 12.92 : 1.055 * Math.pow(v, 1 / 2.4) - 0.055;
    lut[i] = Math.round(enc * 255);
  }
  return lut;
})();

// applyIsp transforms an RGBA buffer in place. options.saturation is a multiplier
// around 1.0 (1.4 = +40% saturation). Returns the same buffer for chaining.
export function applyIsp(rgba, options = {}) {
  const saturation = options.saturation != null ? options.saturation : 1.4;
  const n = rgba.length / 4;

  // --- Pass 1: accumulate channel sums for gray-world white balance. ---
  let sumR = 0, sumG = 0, sumB = 0;
  for (let p = 0; p < n; p++) {
    const o = p * 4;
    sumR += rgba[o];
    sumG += rgba[o + 1];
    sumB += rgba[o + 2];
  }
  // Gray-world gains: scale each channel so its mean equals the overall mean.
  // Guard against divide-by-zero on a fully black frame (leave gains at 1).
  const avg = (sumR + sumG + sumB) / (3 * n) || 1;
  const gainR = avg / (sumR / n || 1);
  const gainG = avg / (sumG / n || 1);
  const gainB = avg / (sumB / n || 1);

  // --- Pass 2: white balance → gamma → saturation, write back. ---
  for (let p = 0; p < n; p++) {
    const o = p * 4;
    // White balance (clamp to 0..255 — Uint8ClampedArray does this on assign).
    let r = rgba[o] * gainR;
    let g = rgba[o + 1] * gainG;
    let b = rgba[o + 2] * gainB;
    if (r > 255) r = 255; if (r < 0) r = 0;
    if (g > 255) g = 255; if (g < 0) g = 0;
    if (b > 255) b = 255; if (b < 0) b = 0;
    // sRGB gamma via LUT.
    r = SRGB_GAMMA_LUT[r | 0];
    g = SRGB_GAMMA_LUT[g | 0];
    b = SRGB_GAMMA_LUT[b | 0];
    // Saturation: pull toward / push away from the pixel's luma.
    if (saturation !== 1) {
      const y = 0.299 * r + 0.587 * g + 0.114 * b;
      r = y + (r - y) * saturation;
      g = y + (g - y) * saturation;
      b = y + (b - y) * saturation;
      if (r > 255) r = 255; if (r < 0) r = 0;
      if (g > 255) g = 255; if (g < 0) g = 0;
      if (b > 255) b = 255; if (b < 0) b = 0;
    }
    rgba[o] = r;
    rgba[o + 1] = g;
    rgba[o + 2] = b;
    // alpha unchanged
  }
  return rgba;
}

// ---------------- Viewer ----------------
//
// openYuvViewer builds the YUV preview UI inside bodyEl. It returns a control
// object with destroy() so the host can tear down fetches + canvas when the
// modal closes (registered via onModalClose / resetViewerState).
//
// It's a thin config wrapper over openRasterFrameViewer, the generic frame
// viewer scaffold (canvas, dimension inputs, frame nav/playback, zoom, ISP
// toggle, Range-based fetching) shared with the RAW Bayer viewer in
// lib/raw.js — only the format dropdown and the frameSize/decode functions
// are YUV-specific.

export function openYuvViewer({ name, url, bodyEl }) {
  const parsed = parseYuvInfoFromName(name) || {};
  const initialFormat = parsed.format || DEFAULT_FORMAT;
  // Labels are static English strings from our own table, so no escaping needed.
  const formatOptionsHtml = Object.keys(YUV_FORMATS)
    .map((k) => `<option value="${k}"${k === initialFormat ? " selected" : ""}>${YUV_FORMATS[k].label}</option>`)
    .join("");

  return openRasterFrameViewer({
    url,
    bodyEl,
    width: parsed.width || 1920,
    height: parsed.height || 1080,
    // Auto-enable the ISP pipeline when the filename signals a raw/linear dump;
    // the user can still toggle it off to compare.
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
    decode: (buf, state) => YUV_FORMATS[state.format].decode(buf, state.width, state.height),
    statusLabel: (state) => YUV_FORMATS[state.format].label,
  });
}

// openRasterFrameViewer is the generic scaffold behind both the YUV and RAW
// viewers: it owns the canvas, dimension inputs, frame slider/playback, zoom,
// ISP toggle, and on-demand byte fetching. Callers supply the format-specific
// pieces: extra control markup + wiring (bindExtraControls), and functions to
// compute per-frame byte size and decode a frame's bytes to RGBA.
export function openRasterFrameViewer({
  url,
  bodyEl,
  width,
  height,
  isp,
  extraControlsHtml,
  bindExtraControls,
  frameSize,
  decode,
  statusLabel,
  // Optional: called once from destroy() so a caller that attached extra
  // resources to state in bindExtraControls (e.g. RAW's worker pool) can
  // release them.
  onDestroy,
}) {
  const state = {
    url,
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
    // activeFetchAbort lets destroy() cancel an in-flight Range request so the
    // viewer never paints into a detached canvas after close.
    abort: null,
    destroyed: false,
    // Cache of the last decoded (and, if enabled, ISP-processed) RGBA buffer,
    // keyed by the inputs that affect its pixels (frame/isp/dimensions). Zoom
    // is purely a display-scale change, so applyZoom() redraws from this
    // cache instead of re-fetching and re-decoding the frame over the
    // network on every wheel tick — without it, each zoom step round-trips
    // through an async fetch before the cursor-anchored scroll correction
    // can run, and out-of-order completions during fast wheel scrolling make
    // the image appear to jump/scroll on its own.
    cachedRgba: null,
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

  // Probe the file size (HEAD) so we can compute frame count and validate the
  // width/height/format guess against the actual byte stream.
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
    const offset = state.frame * per;
    status.textContent = `Loading frame ${state.frame + 1}/${state.frameCount}…`;
    try {
      const buf = await fetchFrameBytes(url, offset, per, controller.signal);
      if (state.destroyed || controller.signal.aborted) return;
      if (buf.length < per) {
        status.textContent = `Frame ${state.frame + 1} is truncated (got ${buf.length} of ${per} bytes). Check width/height/format.`;
        return;
      }
      // decode may return a Promise (e.g. RAW's worker-pool demosaic runs off
      // the main thread); awaiting a plain value is a no-op resolve.
      const rgba = await decode(buf, state);
      if (state.destroyed || controller.signal.aborted) return;
      // Run the ISP pipeline when the user enabled it (auto-on for linear/raw
      // dumps). This is a pure transform on the decoded RGBA buffer.
      if (state.isp) {
        applyIsp(rgba);
      }
      drawFrame(ctx, canvas, rgba, state.width, state.height, state.zoom);
      state.cachedRgba = rgba;
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
  // a cached decode of the current frame is available (the common case —
  // only the display scale changed) this redraws synchronously; otherwise it
  // has to wait for paintCurrentFrame to finish (it's async — it re-fetches
  // and re-decodes the frame) before the canvas has its new size to scroll
  // against.
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
      state.cachedRgba &&
      state.cachedFrame === state.frame &&
      state.cachedIsp === state.isp &&
      state.cachedWidth === state.width &&
      state.cachedHeight === state.height;
    if (canRedrawFromCache) {
      // Fast path: same pixel data, only the display scale changed. Redraw
      // synchronously from the cached buffer and apply the scroll correction
      // immediately — no network round-trip, so there's no window for a
      // stale/out-of-order completion to jerk the view during fast scrolling.
      drawFrame(ctx, canvas, state.cachedRgba, state.width, state.height, state.zoom);
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

// drawFrame paints the RGBA buffer onto the canvas at the requested zoom. The
// canvas backing store is sized to the full zoomed pixel dimensions (w*zoom ×
// h*zoom) so every source pixel maps to a real screen pixel — no browser
// upscaling, which keeps pixel-peeping sharp at any zoom. The CSS size matches
// the backing size 1:1, and the surrounding .raster-canvas-wrap scrolls to
// reveal the parts that overflow the viewport (the image is never squashed
// to fit).
function drawFrame(ctx, canvas, rgba, w, h, zoom) {
  const off = document.createElement("canvas");
  off.width = w;
  off.height = h;
  off.getContext("2d").putImageData(new ImageData(rgba, w, h), 0, 0);

  const dispW = Math.max(1, Math.round(w * zoom));
  const dispH = Math.max(1, Math.round(h * zoom));
  canvas.width = dispW;
  canvas.height = dispH;
  canvas.style.width = `${dispW}px`;
  canvas.style.height = `${dispH}px`;
  ctx.setTransform(1, 0, 0, 1, 0, 0);
  ctx.imageSmoothingEnabled = false; // nearest-neighbor for pixel-peeping zoom
  ctx.clearRect(0, 0, dispW, dispH);
  ctx.drawImage(off, 0, 0, w, h, 0, 0, dispW, dispH);
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

// fetchFrameBytes fetches exactly one frame's bytes via an HTTP Range request.
// The backend serves raw files through /api/netdisk/raw (http.ServeContent),
// which honors Range. Falls back to a full GET if Range is not supported.
async function fetchFrameBytes(url, offset, length, signal) {
  const end = offset + length - 1;
  const res = await fetch(url, {
    credentials: "same-origin",
    headers: { Range: `bytes=${offset}-${end}` },
    signal,
  });
  if (res.status === 206 || res.status === 200) {
    const buf = new Uint8Array(await res.arrayBuffer());
    // A 200 (server ignored Range) returns the whole file; slice our frame out.
    if (res.status === 200) {
      return buf.subarray(offset, offset + length);
    }
    return buf;
  }
  throw new Error(`Unexpected response ${res.status}`);
}
