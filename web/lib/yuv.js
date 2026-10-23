// YUV raw-file viewer: previews raw YUV byte streams by rendering them on a
// canvas.
//
// Used by both the in-app netdisk viewer (ViewerDialog.vue) and the public
// share page (share.js). Both call openYuvViewer() with two URLs for the file:
//   - rawURL  — the byte-serving endpoint (/api/netdisk/raw), used only to
//               learn the file size (and thus the frame count) via a Range
//               probe. The bytes themselves are never shipped to the browser.
//   - rasterURL — the server-side decode endpoint (/api/netdisk/raster) that
//               decodes one frame and returns it as a JPEG.
//
// Decoding (YUV->RGB, the ISP pass) runs entirely in the backend
// (internal/raster). This file just knows each format's per-frame byte size
// (to compute the frame count from the probed file size) and asks the backend
// for a JPEG per frame.
//
// The UI is a professional-viewer layout (lib/raster.css): a toolbar with
// frame transport + scrubber + zoom cluster, a dark viewport with wheel-zoom /
// drag-pan / pixel-probe, a collapsible right-hand parameter panel, and a
// status bar. The generic scaffold lives in openRasterFrameViewer and is also
// reused by the RAW Bayer viewer in lib/raw.js — see that function for the
// shared contract.

// ---------------- Format table ----------------

// packed422Stride is the row stride in bytes of a packed 4:2:2 frame: whole
// 4-byte macro-pixels, one per pair of horizontal pixels, rounded up. Mirrors
// raster.Packed422Stride in internal/raster/yuv.go.
function packed422Stride(w) {
  return w <= 0 ? 0 : (((w + 1) / 2) | 0) * 4;
}

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
  // Packed 4:2:2, 16 bits/pixel. Y0 U0 Y1 V0 per 2 horizontal pixels. Rows are
  // strided by whole 4-byte macro-pixels, so an odd width pads to the next
  // pair; identical to 2*w*h for even widths.
  yuyv: {
    label: "YUYV (YUY2)",
    frameSize: (w, h) => packed422Stride(w) * h,
  },
  // Packed 4:2:2 with U first.
  uyvy: {
    label: "UYVY",
    frameSize: (w, h) => packed422Stride(w) * h,
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

export { YUV_FORMATS };

// ---------------- ISP parameter panel ----------------
//
// The ISP section of the parameter panel exposes the ported MiniIsp pipeline's
// tunables (see internal/raster/miniisp.go). Until the user touches a control
// the viewer stays in "auto" mode and sends no ispX parameters — the backend
// then runs the kind's defaults (board-calibrated for RAW, neutral for YUV).
// The first edit flips to manual mode and every subsequent frame request
// carries the full parameter set.

// CCM presets, mirrored from internal/raster/miniisp.go. Index 0 is "manual"
// (the 3x3 matrix inputs become editable); the RAW default is the board's
// 4032K calibration (index 2).
export const ISP_CCM_PRESETS = [
  { label: "Manual" },
  { label: "Identity", ccm: [1, 0, 0, 0, 1, 0, 0, 0, 1] },
  { label: "Board 4032K", ccm: [0.996, -0.078, 0.082, 0.0, 1.066, -0.066, 0.195, -0.598, 1.402] },
  { label: "sRGB typical", ccm: [1.66, -0.55, -0.11, -0.25, 1.45, -0.2, -0.07, -0.28, 1.35] },
  { label: "High saturation", ccm: [1.85, -0.7, -0.15, -0.32, 1.62, -0.3, -0.1, -0.38, 1.48] },
];

// defaultIspPanelValues returns the panel's initial values. RAW mirrors the
// board-calibrated defaults (MiniIsp.cpp Params::reset) so an untouched panel
// matches what the backend serves in auto mode; YUV uses the neutral RGB-domain
// defaults (the Bayer-only stages are irrelevant there).
export function defaultIspPanelValues(isRaw) {
  return isRaw
    ? {
        blc: [39, 225, 225, 48],
        gainR: 1.07,
        gainB: 1.12,
        ccmPreset: 2,
        ccm: [...ISP_CCM_PRESETS[2].ccm],
        gamma: 2.27,
        saturation: 1.15,
        contrast: 1.22,
        brightness: 0,
        chromaNr: 3,
        sharpenOn: true,
        sharpen: 1.0,
        sharpenRadius: 5,
        chromaSupp: 60,
        gray: false,
      }
    : {
        blc: [0, 0, 0, 0],
        gainR: 1,
        gainB: 1,
        ccmPreset: 1,
        ccm: [...ISP_CCM_PRESETS[1].ccm],
        gamma: 2.2,
        saturation: 1.15,
        contrast: 1.0,
        brightness: 0,
        chromaNr: 3,
        sharpenOn: true,
        sharpen: 1.0,
        sharpenRadius: 5,
        chromaSupp: 0,
        gray: false,
      };
}

// ispQuerySuffix serializes the manual ISP parameters into raster-endpoint
// query keys (empty in auto mode). Must stay in sync with
// parseIspManual in internal/server/raster.go.
export function ispQuerySuffix(state) {
  if (!state.ispManual) return "";
  const p = state.ispParams;
  const parts = [
    `ispGainR=${p.gainR}`,
    `ispGainB=${p.gainB}`,
    `ispCcm=${p.ccm.map((v) => +v.toFixed(4)).join(",")}`,
    `ispGamma=${p.gamma}`,
    `ispSat=${p.saturation}`,
    `ispContrast=${p.contrast}`,
    `ispBright=${p.brightness}`,
    `ispSharpen=${p.sharpenOn ? p.sharpen : 0}`,
    `ispSharpenRadius=${p.sharpenRadius}`,
    `ispChromaSupp=${p.chromaSupp}`,
  ];
  if (state.rawMode) {
    parts.unshift(
      `ispBlc=${p.blc.join(",")}`,
      `ispChromaNr=${p.chromaNr}`,
      `ispGray=${p.gray ? 1 : 0}`,
    );
  }
  return `&${parts.join("&")}`;
}

// ---------------- Toolbar icons ----------------

// 16x16 filled icons, currentColor. Inline SVG keeps the viewer
// framework-free (no icon-font dependency on the share page).
const ICONS = {
  prev: `<svg viewBox="0 0 16 16" aria-hidden="true"><path d="M3 2.8h1.6v10.4H3zM13 2.8 6.2 8l6.8 5.2z"/></svg>`,
  next: `<svg viewBox="0 0 16 16" aria-hidden="true"><path d="M11.4 2.8H13v10.4h-1.6zM3 2.8 9.8 8 3 13.2z"/></svg>`,
  play: `<svg viewBox="0 0 16 16" aria-hidden="true"><path d="M4.2 2.6v10.8L13.4 8z"/></svg>`,
  pause: `<svg viewBox="0 0 16 16" aria-hidden="true"><path d="M4 2.8h2.8v10.4H4zM9.2 2.8H12v10.4H9.2z"/></svg>`,
  zoomIn: `<svg viewBox="0 0 16 16" aria-hidden="true"><path d="M6.5 1.5a5 5 0 1 0 3.09 8.93l3.49 3.49 1.06-1.06-3.49-3.49A5 5 0 0 0 6.5 1.5m0 1.5a3.5 3.5 0 1 1 0 7 3.5 3.5 0 0 1 0-7M5.75 4.2h1.5v1.55h1.55v1.5H7.25V8.8h-1.5V7.25H4.2v-1.5h1.55z"/></svg>`,
  zoomOut: `<svg viewBox="0 0 16 16" aria-hidden="true"><path d="M6.5 1.5a5 5 0 1 0 3.09 8.93l3.49 3.49 1.06-1.06-3.49-3.49A5 5 0 0 0 6.5 1.5m0 1.5a3.5 3.5 0 1 1 0 7 3.5 3.5 0 0 1 0-7M4.2 5.75h4.6v1.5H4.2z"/></svg>`,
  panel: `<svg viewBox="0 0 16 16" aria-hidden="true"><path d="M1.5 3.5h13v1.4h-13zM1.5 7.3h13v1.4h-13zM1.5 11.1h13v1.4h-13z"/><circle cx="5" cy="4.2" r="1.9" fill="#1b1d23"/><circle cx="11" cy="8" r="1.9" fill="#1b1d23"/><circle cx="6.5" cy="11.8" r="1.9" fill="#1b1d23"/></svg>`,
};

// ---------------- Viewer ----------------

// openYuvViewer builds the YUV preview UI inside bodyEl. It returns a control
// object with destroy() so the host can tear down fetches + canvas when the
// modal closes (registered via onModalClose / resetViewerState).
//
// It's a thin config wrapper over openRasterFrameViewer, the generic frame
// viewer scaffold (toolbar, canvas, parameter panel, frame nav/playback, zoom,
// ISP toggle, JPEG fetching) shared with the RAW Bayer viewer in lib/raw.js —
// only the format dropdown, the frameSize function, and the per-frame raster
// URL builder are YUV-specific.

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
      `<label class="raster-field">Format` +
        `<select id="yuvFormat">${formatOptionsHtml}</select>` +
      `</label>`,
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
      `&format=${encodeURIComponent(state.format)}&isp=${state.isp ? 1 : 0}${ispQuerySuffix(state)}`,
    statusLabel: (state) => YUV_FORMATS[state.format].label,
  });
}

// openRasterFrameViewer is the generic scaffold behind both the YUV and RAW
// viewers. Layout (lib/raster.css):
//
//   ┌ toolbar: ◀ ▶ ▶|  [frame scrubber  3/120]  − 100% + Fit 1:1  ⚙ ┐
//   │ viewport (dark, wheel-zoom, drag-pan, pixel probe) │ panel    │
//   └ status bar: dims · format · frame · zoom · size | #hex R G B ┘
//
// Callers supply the format-specific pieces: extra control markup + wiring
// (bindExtraControls), and functions to compute per-frame byte size (frameSize
// — only for the frame-count math) and build the backend decode URL for the
// current frame (frameURL).
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
  // rawMode shows the Bayer-only panel controls (black level, chroma NR,
  // gray mode) and seeds the panel with the board-calibrated defaults.
  rawMode,
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
    rawMode: !!rawMode,
    frame: 0,
    frameCount: 0,
    totalBytes: 0,
    zoom: 1,
    // Until the user zooms manually, the first successfully painted frame is
    // auto-fitted to the viewport (what every desktop viewer does on open).
    autoFit: true,
    // Manual ISP panel state: ispParams mirrors the panel controls,
    // ispManual flips on the first user edit (until then no ispX params are
    // sent and the backend runs its defaults), and ispRev keys the frame
    // cache so a parameter change invalidates the cached bitmap.
    ispParams: defaultIspPanelValues(!!rawMode),
    ispManual: false,
    ispRev: 0,
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
    cachedKey: "",
  };

  bodyEl.innerHTML = rasterLayoutHtml({ isp: state.isp, rawMode: state.rawMode, extraControlsHtml });
  const rootEl = bodyEl.querySelector("#rasterRoot");
  const canvas = bodyEl.querySelector("#rasterCanvas");
  const canvasWrap = bodyEl.querySelector("#rasterCanvasWrap");
  const ctx = canvas.getContext("2d");
  const ispCheckbox = bodyEl.querySelector("#rasterIsp");
  const ispBadge = bodyEl.querySelector("#rasterIspBadge");
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
  const zoomInBtn = bodyEl.querySelector("#rasterZoomIn");
  const zoomOutBtn = bodyEl.querySelector("#rasterZoomOut");
  const zoomFitBtn = bodyEl.querySelector("#rasterZoomFit");
  const zoomResetBtn = bodyEl.querySelector("#rasterZoomReset");
  const panelToggle = bodyEl.querySelector("#rasterPanelToggle");
  const status = bodyEl.querySelector("#rasterStatus");
  const pixelBox = bodyEl.querySelector("#rasterPixel");
  const pixelSwatch = bodyEl.querySelector("#rasterPixelSwatch");
  const pixelText = bodyEl.querySelector("#rasterPixelText");

  // ISP panel plumbing. paintCurrentFrame is defined below via function
  // hoisting, so the debounce timer can reference it directly.
  let ispDebounceTimer = null;
  function scheduleIspRepaint() {
    if (ispDebounceTimer) clearTimeout(ispDebounceTimer);
    // Sliders fire "input" continuously while dragging; each repaint is a
    // server round-trip, so coalesce bursts into one trailing request.
    ispDebounceTimer = setTimeout(() => {
      ispDebounceTimer = null;
      paintCurrentFrame();
    }, 250);
  }
  function markIspManual() {
    state.ispManual = true;
    state.ispRev++;
    updateIspBadge();
    if (state.isp) scheduleIspRepaint();
  }
  // The badge in the ISP section header tells at a glance which parameter
  // source is active: the backend defaults (Auto), the panel (Manual), or
  // no ISP pass at all (Off).
  function updateIspBadge() {
    const mode = !state.isp ? "off" : state.ispManual ? "manual" : "auto";
    ispBadge.dataset.mode = mode;
    ispBadge.textContent = mode === "off" ? "Off" : mode === "manual" ? "Manual" : "Auto";
  }
  // The cached bitmap's key is the frame's own fetch URL, which already
  // encodes every input that decides the decoded pixels: frame index,
  // dimensions, the ISP toggle and its parameters, and the caller-supplied
  // controls (YUV format, RAW bit depth / Bayer pattern). Keying on the ISP
  // state alone left those caller-supplied controls out, so a zoom taken after
  // a format/pattern change whose fetch failed would redraw the *previous*
  // pattern's bitmap from cache and overwrite the error message with a
  // normal-looking status line.
  function frameCacheKey() {
    return frameURL(state);
  }
  function setStatus(text, isError = false) {
    status.textContent = text;
    status.classList.toggle("is-error", isError);
  }
  function statusLine() {
    const size = state.totalBytes > 0 ? ` · ${fmtBytes(state.totalBytes)}` : "";
    return (
      `${state.width}×${state.height} ${statusLabel(state)} · frame ${state.frame + 1}/${state.frameCount}` +
      ` · zoom ${Math.round(state.zoom * 100)}%${ispStatusSuffix(state)}${size}`
    );
  }

  widthInput.value = state.width;
  heightInput.value = state.height;
  zoomVal.textContent = `${Math.round(state.zoom * 100)}%`;
  fpsInput.value = state.fps;
  updateIspBadge();

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
    frameMax.textContent = `/ ${state.frameCount}`;
    // Dim the navigation/playback controls when there's only one frame.
    const multi = state.frameCount > 1;
    frameSlider.disabled = !multi;
    frameInput.disabled = !multi;
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
      setStatus("Enter a valid width and height.", true);
      return;
    }
    if (per <= 0) {
      setStatus("Unsupported dimensions for this format.", true);
      return;
    }
    // Cancel any in-flight fetch before starting a new one so rapid frame
    // changes / control tweaks don't paint a stale frame on top of a newer one.
    if (state.abort) state.abort.abort();
    const controller = new AbortController();
    state.abort = controller;
    setStatus(`Loading frame ${state.frame + 1}/${state.frameCount}…`);
    // Capture the key before awaiting so the cache records the URL that was
    // actually fetched, not whatever the controls hold when it resolves.
    const key = frameCacheKey();
    try {
      const bitmap = await fetchFrameBitmap(key, controller.signal);
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
      state.cachedKey = key;
      zoomVal.textContent = `${Math.round(state.zoom * 100)}%`;
      setStatus(statusLine());
      // First paint: fit the frame to the viewport once, synchronously (the
      // host laid the viewer out before this fetch resolved) — unless the
      // user already zoomed manually.
      if (state.autoFit) {
        state.autoFit = false;
        fitToWindow();
      }
    } catch (err) {
      if (state.destroyed || controller.signal.aborted) return;
      setStatus(`Failed to load frame: ${err.message || err}`, true);
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
    state.autoFit = false;
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
    zoomVal.textContent = `${Math.round(state.zoom * 100)}%`;
    const canRedrawFromCache = !!state.cachedBitmap && state.cachedKey === frameCacheKey();
    if (canRedrawFromCache) {
      // Fast path: same pixel data, only the display scale changed. Redraw
      // synchronously from the cached bitmap and apply the scroll correction
      // immediately — no network round-trip, so there's no window for a
      // stale/out-of-order completion to jerk the view during fast scrolling.
      drawFrame(ctx, canvas, state.cachedBitmap, state.width, state.height, state.zoom);
      setStatus(statusLine());
      if (anchor) {
        applyAnchorScroll(anchor);
      }
      return;
    }
    const painted = paintCurrentFrame();
    if (anchor) {
      painted.then(() => {
        if (state.destroyed) return;
        applyAnchorScroll(anchor);
      });
    }
  }
  // applyAnchorScroll sets scrollLeft/scrollTop so that the image point
  // recorded in anchor stays under the cursor's viewport position. The
  // scroll container (.raster-canvas-wrap) has padding, and the canvas (a
  // flex child with margin:auto) starts at that padding offset inside the
  // content area. When the canvas overflows (the normal zoomed-in case)
  // flex margins collapse to 0, so the canvas's content-area origin is at
  // (padLeft, padTop). The scroll math must include this offset, otherwise
  // every wheel tick drifts the image by one padding-width toward the top-left.
  function applyAnchorScroll(anchor) {
    const cs = getComputedStyle(canvasWrap);
    const padLeft = parseFloat(cs.paddingLeft) || 0;
    const padTop = parseFloat(cs.paddingTop) || 0;
    canvasWrap.scrollLeft = padLeft + anchor.imgX * state.zoom - anchor.viewportX;
    canvasWrap.scrollTop = padTop + anchor.imgY * state.zoom - anchor.viewportY;
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
    const z = Math.min(availW / state.width, availH / state.height);
    // Guards for headless/hidden hosts where clientWidth is 0 or the
    // dimensions aren't numbers yet: fall back to 100% instead of NaN.
    return Number.isFinite(z) && z > 0 ? z : 1;
  }
  function fitToWindow() {
    applyZoom(computeFitZoom());
  }
  function onIspToggle() {
    state.isp = !!ispCheckbox.checked;
    updateIspBadge();
    updateIspPanelDisabled();
    paintCurrentFrame();
  }

  // ---- ISP parameter panel ----
  // The tunables live inside #rasterIspPanel (a <details>, collapsed by
  // default). They write into state.ispParams and go through markIspManual(),
  // which flips the viewer into manual mode and schedules a debounced repaint.
  const ispPanel = bodyEl.querySelector("#rasterIspPanel");
  const ispInputs = () => Array.from(ispPanel.querySelectorAll("input, select, button"));
  const ispVal = (id) => ispPanel.querySelector(`#${id}Val`);

  // Readouts for each slider, formatted per kind of value.
  const readoutFormats = {
    ispGainR: (v) => (+v).toFixed(2),
    ispGainB: (v) => (+v).toFixed(2),
    ispGamma: (v) => (+v).toFixed(2),
    ispSaturation: (v) => (+v).toFixed(2),
    ispContrast: (v) => (+v).toFixed(2),
    ispBrightness: (v) => `${Math.round(+v)}`,
    ispChromaNr: (v) => `${Math.round(+v)}`,
    ispSharpen: (v) => (+v).toFixed(2),
    ispSharpenRadius: (v) => `${Math.round(+v)}`,
    ispChromaSupp: (v) => `${Math.round(+v)}`,
  };

  function syncIspControls() {
    const p = state.ispParams;
    const num = (id, v) => {
      const el = ispPanel.querySelector(`#${id}`);
      if (el) el.value = v;
    };
    if (state.rawMode) {
      num("ispBlcR", p.blc[0]);
      num("ispBlcGr", p.blc[1]);
      num("ispBlcGb", p.blc[2]);
      num("ispBlcB", p.blc[3]);
      num("ispChromaNr", p.chromaNr);
    }
    num("ispGainR", p.gainR);
    num("ispGainB", p.gainB);
    num("ispGamma", p.gamma);
    num("ispSaturation", p.saturation);
    num("ispContrast", p.contrast);
    num("ispBrightness", p.brightness);
    num("ispSharpen", p.sharpen);
    num("ispSharpenRadius", p.sharpenRadius);
    num("ispChromaSupp", p.chromaSupp);
    num("ispCcmPreset", p.ccmPreset);
    for (let i = 0; i < 9; i++) num(`ispCcm${i}`, p.ccm[i]);
    const sharpenOn = ispPanel.querySelector("#ispSharpenOn");
    if (sharpenOn) sharpenOn.checked = p.sharpenOn;
    const gray = ispPanel.querySelector("#ispGray");
    if (gray) gray.checked = p.gray;
    const matrixRow = ispPanel.querySelector("#ispCcmMatrixRow");
    if (matrixRow) matrixRow.hidden = p.ccmPreset !== 0;
    for (const [id, fmt] of Object.entries(readoutFormats)) {
      const el = ispPanel.querySelector(`#${id}`);
      const out = ispVal(id);
      if (el && out) out.textContent = fmt(el.value);
    }
    updateIspPanelDisabled();
  }

  // The panel only has effect while the master ISP switch is on, and the
  // sharpen strength/radius controls only while sharpening is enabled.
  function updateIspPanelDisabled() {
    const sharpenOn = state.ispParams.sharpenOn;
    for (const el of ispInputs()) {
      if (el.id === "ispSharpenOn" || el.id === "ispGray" || el.id === "ispCcmPreset") {
        el.disabled = !state.isp;
        continue;
      }
      const sharpenChild = el.id === "ispSharpen" || el.id === "ispSharpenRadius";
      el.disabled = !state.isp || (sharpenChild && !sharpenOn);
    }
  }

  function bindSlider(id, apply) {
    const el = ispPanel.querySelector(`#${id}`);
    if (!el) return;
    el.addEventListener("input", () => {
      apply(el.value);
      const out = ispVal(id);
      if (out) out.textContent = readoutFormats[id](el.value);
      markIspManual();
    });
  }

  function bindNumber(id, apply) {
    const el = ispPanel.querySelector(`#${id}`);
    if (!el) return;
    // "change" (not "input"): typed/pasted values shouldn't repaint on every
    // keystroke of a half-typed number.
    el.addEventListener("change", () => {
      apply(el.value);
      markIspManual();
    });
  }

  if (state.rawMode) {
    for (let i = 0; i < 4; i++) {
      bindNumber(["ispBlcR", "ispBlcGr", "ispBlcGb", "ispBlcB"][i], (v) => {
        state.ispParams.blc[i] = Math.max(0, Math.min(65535, parseInt(v, 10) || 0));
      });
    }
    bindSlider("ispChromaNr", (v) => {
      state.ispParams.chromaNr = Math.max(0, Math.min(64, Math.round(+v)));
    });
    const gray = ispPanel.querySelector("#ispGray");
    gray.addEventListener("change", () => {
      state.ispParams.gray = !!gray.checked;
      markIspManual();
    });
  }
  bindSlider("ispGainR", (v) => { state.ispParams.gainR = +v; });
  bindSlider("ispGainB", (v) => { state.ispParams.gainB = +v; });
  bindSlider("ispGamma", (v) => { state.ispParams.gamma = +v; });
  bindSlider("ispSaturation", (v) => { state.ispParams.saturation = +v; });
  bindSlider("ispContrast", (v) => { state.ispParams.contrast = +v; });
  bindSlider("ispBrightness", (v) => { state.ispParams.brightness = Math.round(+v); });
  bindSlider("ispSharpen", (v) => { state.ispParams.sharpen = +v; });
  bindSlider("ispSharpenRadius", (v) => { state.ispParams.sharpenRadius = Math.round(+v); });
  bindSlider("ispChromaSupp", (v) => { state.ispParams.chromaSupp = Math.round(+v); });

  const sharpenOn = ispPanel.querySelector("#ispSharpenOn");
  sharpenOn.addEventListener("change", () => {
    state.ispParams.sharpenOn = !!sharpenOn.checked;
    updateIspPanelDisabled();
    markIspManual();
  });

  const ccmPreset = ispPanel.querySelector("#ispCcmPreset");
  ccmPreset.addEventListener("change", () => {
    const idx = parseInt(ccmPreset.value, 10);
    state.ispParams.ccmPreset = idx;
    if (ISP_CCM_PRESETS[idx].ccm) state.ispParams.ccm = [...ISP_CCM_PRESETS[idx].ccm];
    // Re-sync so manual matrix inputs reflect the applied preset when the
    // user flips back to Manual.
    syncIspControls();
    markIspManual();
  });
  for (let i = 0; i < 9; i++) {
    bindNumber(`ispCcm${i}`, (v) => {
      state.ispParams.ccm[i] = Math.max(-8, Math.min(8, parseFloat(v) || 0));
    });
  }

  // Gray-world AWB: ask the backend for gains computed from the current
  // frame's actual pixels (the browser only ever holds the decoded JPEG).
  const awbBtn = ispPanel.querySelector("#ispAwbBtn");
  awbBtn.addEventListener("click", async () => {
    if (state.destroyed || !state.isp) return;
    awbBtn.disabled = true;
    setStatus("Computing gray-world AWB…");
    try {
      const res = await fetch(`${frameURL(state)}&awb=1`, { credentials: "same-origin" });
      if (!res.ok) throw new Error(`HTTP ${res.status}`);
      const gains = await res.json();
      if (state.destroyed) return;
      state.ispParams.gainR = +gains.gainR;
      state.ispParams.gainB = +gains.gainB;
      syncIspControls();
      markIspManual();
    } catch (err) {
      if (!state.destroyed) setStatus(`AWB failed: ${err.message || err}`, true);
    } finally {
      if (!state.destroyed) awbBtn.disabled = false;
    }
  });

  // Reset restores the kind's defaults and drops back to auto mode (an
  // untouched panel is exactly "the defaults", so no ispX params are sent).
  const resetBtn = ispPanel.querySelector("#ispResetBtn");
  resetBtn.addEventListener("click", () => {
    state.ispParams = defaultIspPanelValues(state.rawMode);
    state.ispManual = false;
    state.ispRev++;
    syncIspControls();
    updateIspBadge();
    if (state.isp) paintCurrentFrame();
  });

  syncIspControls();

  function ispStatusSuffix(s) {
    if (!s.isp) return "";
    return s.ispManual ? " · ISP manual" : " · ISP on";
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
  // at the last frame depending on the Loop switch. Pause on user frame nav
  // (prev/next/slider) is intentionally NOT done — the user may scrub while
  // playing — but pressing Play again toggles to Pause.
  function play() {
    if (state.playing || state.frameCount <= 1) return;
    state.playing = true;
    playBtn.innerHTML = ICONS.pause;
    playBtn.title = "Pause (Space)";
    scheduleNextFrame();
  }
  function pause() {
    state.playing = false;
    if (state.playTimer) {
      clearTimeout(state.playTimer);
      state.playTimer = null;
    }
    if (playBtn) {
      playBtn.innerHTML = ICONS.play;
      playBtn.title = "Play (Space)";
    }
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

  // --- Keyboard shortcuts, the way desktop viewers bind them: arrows step
  // frames, Space toggles playback, +/- zoom, 0 fits, 1 goes to 100%. Skipped
  // while focus is in a form control so typing dimensions never fights the
  // shortcuts. The listener dies with the viewer (removed in destroy()).
  function onKeydown(e) {
    if (e.ctrlKey || e.metaKey || e.altKey) return;
    const t = e.target;
    if (t && (t.tagName === "INPUT" || t.tagName === "SELECT" || t.tagName === "TEXTAREA" || t.isContentEditable)) return;
    switch (e.key) {
      case "ArrowLeft":
        onFrameChange(state.frame - 1);
        e.preventDefault();
        break;
      case "ArrowRight":
        onFrameChange(state.frame + 1);
        e.preventDefault();
        break;
      case " ":
        togglePlay();
        e.preventDefault();
        break;
      case "+":
      case "=":
        applyZoom(state.zoom * 1.25);
        break;
      case "-":
      case "_":
        applyZoom(state.zoom / 1.25);
        break;
      case "0":
        fitToWindow();
        break;
      case "1":
        applyZoom(1);
        break;
    }
  }

  // --- Pixel probe: hovering the canvas reads the pixel under the cursor into
  // the status bar (swatch + R,G,B + hex + image coordinates) — the info-panel
  // readout professional viewers put under the image. Uses a single-pixel
  // getImageData read, cheap enough for mousemove.
  function onCanvasMove(e) {
    const rect = canvas.getBoundingClientRect();
    const px = Math.floor(e.clientX - rect.left);
    const py = Math.floor(e.clientY - rect.top);
    if (px < 0 || py < 0 || px >= canvas.width || py >= canvas.height || state.zoom <= 0) {
      pixelBox.hidden = true;
      return;
    }
    const [r, g, b] = ctx.getImageData(px, py, 1, 1).data;
    const hex = `#${((1 << 24) | (r << 16) | (g << 8) | b).toString(16).slice(1).toUpperCase()}`;
    const ix = Math.min(state.width - 1, Math.floor(px / state.zoom));
    const iy = Math.min(state.height - 1, Math.floor(py / state.zoom));
    pixelSwatch.style.background = hex;
    pixelText.textContent = `${hex}  R${r} G${g} B${b}  ${ix},${iy}`;
    pixelBox.hidden = false;
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
  zoomOutBtn.addEventListener("click", () => applyZoom(state.zoom / 1.25));
  zoomInBtn.addEventListener("click", () => applyZoom(state.zoom * 1.25));
  // The percentage readout is itself a button: click it to snap to 100%.
  zoomVal.addEventListener("click", () => applyZoom(1));
  zoomFitBtn.addEventListener("click", fitToWindow);
  zoomResetBtn.addEventListener("click", () => applyZoom(1));
  panelToggle.addEventListener("click", () => {
    const collapsed = rootEl.classList.toggle("panel-collapsed");
    panelToggle.setAttribute("aria-pressed", String(!collapsed));
  });
  // Wheel zoom on the canvas area. passive:false so we can preventDefault and
  // stop the page/scroll container from also scrolling while zooming.
  canvasWrap.addEventListener("wheel", onWheel, { passive: false });
  canvasWrap.addEventListener("pointerdown", onPointerDown);
  canvasWrap.addEventListener("pointermove", onPointerMove);
  canvasWrap.addEventListener("pointerup", endPan);
  canvasWrap.addEventListener("pointercancel", endPan);
  canvasWrap.addEventListener("dblclick", onDblClick);
  canvas.addEventListener("mousemove", onCanvasMove);
  canvas.addEventListener("mouseleave", () => { pixelBox.hidden = true; });
  document.addEventListener("keydown", onKeydown);

  return {
    state,
    // repaint(): re-render the current frame at the current size (e.g. after a
    // fullscreen toggle changes the available canvas width).
    repaint: paintCurrentFrame,
    destroy() {
      state.destroyed = true;
      pause();
      document.removeEventListener("keydown", onKeydown);
      if (ispDebounceTimer) {
        clearTimeout(ispDebounceTimer);
        ispDebounceTimer = null;
      }
      if (state.abort) state.abort.abort();
      if (state.cachedBitmap) {
        state.cachedBitmap.close?.();
        state.cachedBitmap = null;
      }
      if (onDestroy) onDestroy(state);
    },
  };
}

function rasterLayoutHtml({ isp, rawMode, extraControlsHtml }) {
  return (
    `<div class="raster-root" id="rasterRoot">` +
      `<div class="raster-toolbar">` +
        `<div class="raster-tb-group">` +
          `<button id="rasterPrevFrame" class="raster-ibtn" title="Previous frame (←)" disabled>${ICONS.prev}</button>` +
          `<button id="rasterPlayBtn" class="raster-ibtn" title="Play (Space)" disabled>${ICONS.play}</button>` +
          `<button id="rasterNextFrame" class="raster-ibtn" title="Next frame (→)" disabled>${ICONS.next}</button>` +
        `</div>` +
        `<div class="raster-tb-scrub">` +
          `<input id="rasterFrameSlider" type="range" min="0" max="0" value="0" disabled>` +
          `<span class="raster-frame-readout">` +
            `<input id="rasterFrameInput" type="number" min="1" max="1" value="1" disabled>` +
            `<span id="rasterFrameMax">/ 1</span>` +
          `</span>` +
        `</div>` +
        `<div class="raster-tb-group">` +
          `<button id="rasterZoomOut" class="raster-ibtn" title="Zoom out (−)">${ICONS.zoomOut}</button>` +
          `<button id="rasterZoomVal" class="raster-zoom-readout" title="Zoom to 100% (1)">100%</button>` +
          `<button id="rasterZoomIn" class="raster-ibtn" title="Zoom in (+)">${ICONS.zoomIn}</button>` +
          `<button id="rasterZoomFit" class="raster-tbtn" title="Fit to window (0)">Fit</button>` +
          `<button id="rasterZoomReset" class="raster-tbtn" title="Actual pixels (1)">1:1</button>` +
        `</div>` +
        `<button id="rasterPanelToggle" class="raster-ibtn" title="Toggle parameter panel" aria-pressed="true">${ICONS.panel}</button>` +
      `</div>` +
      `<div class="raster-main">` +
        `<div class="raster-canvas-wrap" id="rasterCanvasWrap"><canvas id="rasterCanvas" class="raster-canvas"></canvas></div>` +
        `<aside class="raster-panel" id="rasterPanel">` +
          `<details class="raster-sec" open>` +
            `<summary class="raster-sec-head">Decode</summary>` +
            `<div class="raster-sec-body">` +
              `<div class="raster-grid2">` +
                `<label class="raster-field">Width<input id="rasterWidth" type="number" min="1" step="1"></label>` +
                `<label class="raster-field">Height<input id="rasterHeight" type="number" min="1" step="1"></label>` +
              `</div>` +
              (extraControlsHtml || "") +
            `</div>` +
          `</details>` +
          `<div class="raster-sec-fixed">` +
            `<span class="raster-sec-title">ISP</span>` +
            `<span class="raster-isp-badge" id="rasterIspBadge">Auto</span>` +
            `<label class="raster-switch" title="Run the ISP pipeline (demosaic, white balance, gamma…) on the decoded frame.">` +
              `<input type="checkbox" id="rasterIsp"${isp ? " checked" : ""}><i></i>Enable` +
            `</label>` +
          `</div>` +
          ispPanelHtml(rawMode) +
          `<details class="raster-sec" open>` +
            `<summary class="raster-sec-head">Playback</summary>` +
            `<div class="raster-sec-body">` +
              `<div class="raster-inline-row">` +
                `<label class="raster-switch"><input type="checkbox" id="rasterLoop" checked><i></i>Loop</label>` +
                `<label class="raster-field-inline">FPS<input id="rasterFps" type="number" min="1" max="120" step="1" value="24"></label>` +
              `</div>` +
            `</div>` +
          `</details>` +
        `</aside>` +
      `</div>` +
      `<div class="raster-statusbar">` +
        `<span id="rasterStatus" class="raster-status-info">Loading…</span>` +
        `<span class="raster-pixel" id="rasterPixel" hidden>` +
          `<i class="raster-swatch" id="rasterPixelSwatch"></i>` +
          `<span id="rasterPixelText"></span>` +
        `</span>` +
      `</div>` +
    `</div>`
  );
}

// ispPanelHtml renders the collapsible ISP parameter section — a native
// <details> with no "open" attribute, so it starts collapsed. Bayer-only
// stages (black level, chroma NR, gray mode) render for RAW files only; the
// values are seeded from defaultIspPanelValues by syncIspControls().
function ispPanelHtml(rawMode) {
  const sliderRow = (id, label, min, max, step, title) =>
    `<div class="isp-row" title="${title || ""}">` +
      `<div class="isp-row-head"><label for="${id}">${label}</label><output class="isp-val" id="${id}Val"></output></div>` +
      `<input type="range" id="${id}" min="${min}" max="${max}" step="${step}">` +
    `</div>`;
  const blcInputs = ["R", "Gr", "Gb", "B"]
    .map((ch, i) => {
      const id = ["ispBlcR", "ispBlcGr", "ispBlcGb", "ispBlcB"][i];
      return `<input type="number" id="${id}" min="0" max="65535" step="1" title="Black level ${ch}">`;
    })
    .join("");
  const ccmMatrix = Array.from({ length: 9 }, (_, i) =>
    `<input type="number" id="ispCcm${i}" step="0.01" title="CCM ${Math.floor(i / 3)},${i % 3}">`,
  ).join("");
  const presetOptions = ISP_CCM_PRESETS
    .map((p, i) => `<option value="${i}">${p.label}</option>`)
    .join("");
  return (
    `<details id="rasterIspPanel" class="raster-sec raster-isp-panel">` +
      `<summary>ISP parameters</summary>` +
      `<div class="raster-sec-body">` +
        (rawMode
          ? `<div class="isp-row isp-row-multi" title="Black level correction per CFA channel (R/Gr/Gb/B). Sensor black floor; subtract before anything else.">` +
              `<label>Black level R/Gr/Gb/B</label>` +
              `<div class="isp-blc-grid">${blcInputs}</div>` +
            `</div>`
          : "") +
        sliderRow("ispGainR", "WB gain R", "0.1", "8", "0.01",
          "White balance gain for the red channel (G is fixed at 1.0).") +
        sliderRow("ispGainB", "WB gain B", "0.1", "8", "0.01",
          "White balance gain for the blue channel (G is fixed at 1.0).") +
        `<div class="isp-actions">` +
          `<button type="button" class="raster-mini-btn" id="ispAwbBtn" title="Compute gray-world white balance gains from this frame's pixels (server-side).">Gray-world AWB</button>` +
          `<button type="button" class="raster-mini-btn" id="ispResetBtn" title="Restore the default ISP parameters for this file kind.">Reset</button>` +
        `</div>` +
        sliderRow("ispGamma", "Gamma", "0.5", "4", "0.01",
          "Gamma encoding: output = pow(v, 1/gamma). 2.2 ≈ sRGB.") +
        sliderRow("ispSaturation", "Saturation", "0", "2", "0.01",
          "Color saturation around luma (1.0 = unchanged, 0 = grayscale).") +
        sliderRow("ispContrast", "Contrast", "0.5", "2", "0.01",
          "Linear stretch around mid gray (1.0 = unchanged).") +
        sliderRow("ispBrightness", "Brightness", "-100", "100", "1",
          "Offset added after gamma.") +
        `<div class="isp-row" title="Color correction matrix applied in the linear domain.">` +
          `<div class="isp-row-head"><label for="ispCcmPreset">CCM preset</label></div>` +
          `<select id="ispCcmPreset">${presetOptions}</select>` +
        `</div>` +
        `<div class="isp-row isp-row-multi" id="ispCcmMatrixRow" hidden>` +
          `<label>CCM matrix (row-major)</label>` +
          `<div class="raster-ccm-grid">${ccmMatrix}</div>` +
        `</div>` +
        (rawMode
          ? sliderRow("ispChromaNr", "Chroma NR radius", "0", "8", "1",
              "Box-blur radius on the chroma-difference planes (0 = off). Smooths color noise.") +
            `<div class="isp-row" title="Skip the demosaic and show the raw Bayer as grayscale.">` +
              `<label class="raster-switch"><input type="checkbox" id="ispGray"><i></i>Gray mode (raw Bayer)</label>` +
            `</div>`
          : "") +
        `<div class="isp-row" title="Multi-band luma unsharp mask (high/mid/low-mid bands with luma-dependent gain).">` +
          `<label class="raster-switch"><input type="checkbox" id="ispSharpenOn"><i></i>Sharpen</label>` +
        `</div>` +
        sliderRow("ispSharpen", "Sharpen strength", "0", "4", "0.05",
          "Overall sharpen multiplier (1.0 = calibrated default).") +
        sliderRow("ispSharpenRadius", "Sharpen radius", "1", "10", "1",
          "Mid-band unsharp radius in pixels (high band is fixed 3x3).") +
        sliderRow("ispChromaSupp", "Shadow chroma suppress", "0", "128", "1",
          "Below this luma, color collapses toward gray (0 = off).") +
      `</div>` +
    `</details>`
  );
}

// drawFrame paints the decoded frame onto the canvas at the requested zoom.
// The canvas backing store is sized to the full zoomed pixel dimensions
// (w*zoom × h*zoom) so every source pixel maps to a real screen pixel — no
// browser upscaling, which keeps pixel-peeping sharp at any zoom. The CSS size
// matches the backing size 1:1, and the surrounding .raster-canvas-wrap
// scrolls to reveal the parts that overflow the viewport (the image is never
// squashed to fit). `bitmap` is an ImageBitmap decoded from the backend's
// JPEG response. Downscaling (zoom < 1, e.g. fit-to-window) switches to
// smooth interpolation — nearest-neighbor minification aliases badly; scaling
// up stays nearest-neighbor so individual pixels stay crisp.
function drawFrame(ctx, canvas, bitmap, w, h, zoom) {
  const dispW = Math.max(1, Math.round(w * zoom));
  const dispH = Math.max(1, Math.round(h * zoom));
  canvas.width = dispW;
  canvas.height = dispH;
  canvas.style.width = `${dispW}px`;
  canvas.style.height = `${dispH}px`;
  ctx.setTransform(1, 0, 0, 1, 0, 0);
  ctx.imageSmoothingEnabled = zoom < 1;
  ctx.clearRect(0, 0, dispW, dispH);
  ctx.drawImage(bitmap, 0, 0, w, h, 0, 0, dispW, dispH);
}

// fmtBytes renders a byte count for the status bar (KB/MB/GB).
function fmtBytes(n) {
  if (n >= 1024 ** 3) return `${(n / 1024 ** 3).toFixed(2)} GB`;
  if (n >= 1024 ** 2) return `${(n / 1024 ** 2).toFixed(1)} MB`;
  return `${Math.max(1, Math.round(n / 1024))} KB`;
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
