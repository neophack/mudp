// RAW Bayer-file viewer: parses raw single-channel Bayer sensor dumps and
// renders them on a canvas by demosaicing to RGB.
//
// Reuses the generic frame-viewer scaffold from lib/yuv.js (canvas, dimension
// inputs, frame navigation/playback, zoom, Range-based fetch, and the ISP
// auto-white-balance + gamma pipeline) — this module only supplies the
// RAW-specific pieces: filename parsing, the Bayer color-filter-array layout,
// and the demosaic decode call. The demosaic itself runs in the Go-compiled
// WASM module (see cmd/rasterwasm, internal/raster/bayer.go), via
// lib/wasmRaster.js.

import { openRasterFrameViewer } from "./yuv.js";
import * as wasmRaster from "./wasmRaster.js";

// ---------------- Filename parsing ----------------

// parseRawInfoFromName recovers width/height/bitDepth/bayerPattern from the
// capture tool's naming convention, e.g.:
//   RAW_1936x1552_16bits_RGGB_Linear_20251011144549.raw
// Returns null when nothing could be inferred; the caller falls back to
// defaults the user can still edit in the viewer's controls.
export function parseRawInfoFromName(name) {
  const lower = String(name || "").toLowerCase();
  const info = {};

  const dim = lower.match(/(\d{2,6})\s*x\s*(\d{2,6})/);
  if (dim) {
    info.width = parseInt(dim[1], 10);
    info.height = parseInt(dim[2], 10);
  }

  const bitMatch = lower.match(/(?:^|[_\-. ])(8|10|12|14|16)\s*bits?(?:[_\-. ]|$)/);
  if (bitMatch) info.bitDepth = parseInt(bitMatch[1], 10);

  const patMatch = lower.match(/(?:^|[_\-. ])(rggb|bggr|grbg|gbrg)(?:[_\-. ]|$)/);
  if (patMatch) info.bayerPattern = patMatch[1].toUpperCase();

  if (/(?:^|[_\-. ])linear(?:[_\-. ]|$)/.test(lower)) info.linear = true;

  return info.width || info.height || info.bitDepth || info.bayerPattern || info.linear ? info : null;
}

// ---------------- Bayer CFA layout ----------------

// Each pattern is the 2x2 tile of color-filter labels, row-major, as seen
// starting at pixel (0,0), e.g. RGGB is [[R,G],[G,B]]:
//   R G R G ...
//   G B G B ...
export const BAYER_PATTERNS = {
  RGGB: [["R", "G"], ["G", "B"]],
  BGGR: [["B", "G"], ["G", "R"]],
  GRBG: [["G", "R"], ["B", "G"]],
  GBRG: [["G", "B"], ["R", "G"]],
};
const DEFAULT_PATTERN = "RGGB";
const DEFAULT_BIT_DEPTH = 16;

// bytesPerSample: 8-bit dumps pack one sample per byte; anything above 8 bits
// is conventionally stored unpacked in a 16-bit little-endian container
// (RAW10/RAW12/RAW14/RAW16 all commonly ship this way from ISP capture
// tools), so we read 2 bytes per sample and normalize using the declared bit
// depth's value range.
function bytesPerSample(bitDepth) {
  return bitDepth <= 8 ? 1 : 2;
}

export function rawFrameSize(width, height, bitDepth) {
  return width * height * bytesPerSample(bitDepth);
}

// decodeBayer demosaics a single-channel Bayer buffer into RGBA via bilinear
// interpolation, in the WASM module (internal/raster/bayer.go has the actual
// algorithm doc). rowStart/rowEnd (default: the whole image) let a caller
// decode just a horizontal band — used by decodeBayerParallel to split the
// work across a Worker pool; the promise resolves to a buffer covering only
// that band (height = rowEnd - rowStart).
export async function decodeBayer(buf, width, height, bitDepth, patternKey, rowStart = 0, rowEnd = height) {
  return wasmRaster.bayerDecode(patternKey, bitDepth, width, height, rowStart, rowEnd, buf);
}

// ---------------- Parallel decode (Web Worker pool) ----------------
//
// decodeBayer is an O(width*height) per-pixel loop with several neighbor
// reads per pixel — on a multi-megapixel sensor dump (the case this viewer
// targets) that's tens of millions of reads, enough to visibly stall the UI
// thread. decodeBayerParallel splits the frame into one horizontal band per
// pooled worker and demosaics the bands concurrently, then stitches the
// results back together.

// Below this pixel count the per-message clone/postMessage overhead isn't
// worth it — decode single-threaded instead.
const PARALLEL_THRESHOLD_PIXELS = 400_000;

function workerPoolSize() {
  const cores = (typeof navigator !== "undefined" && navigator.hardwareConcurrency) || 4;
  return Math.max(1, Math.min(8, cores));
}

// createWorkerPool spins up the pool once per viewer instance so repeated
// frame decodes (scrubbing, playback) reuse warm workers instead of paying
// worker-startup cost every frame. Returns [] when Workers aren't available
// (e.g. a non-browser test runtime) so callers can fall back transparently.
export function createWorkerPool() {
  if (typeof Worker === "undefined") return [];
  const workers = [];
  for (let i = 0; i < workerPoolSize(); i++) {
    workers.push(new Worker(new URL("./rawDecodeWorker.js", import.meta.url), { type: "module" }));
  }
  return workers;
}

export function terminateWorkerPool(workers) {
  if (!workers) return;
  for (const w of workers) w.terminate();
}

// decodeBayerParallel dispatches one row-band per worker in the pool and
// resolves once every band is back, or falls back to the synchronous decode
// when there's no pool, a single-worker pool, or the image is too small for
// the split to pay off.
export function decodeBayerParallel(workers, buf, width, height, bitDepth, patternKey) {
  if (!workers || workers.length < 2 || width * height < PARALLEL_THRESHOLD_PIXELS) {
    return Promise.resolve(decodeBayer(buf, width, height, bitDepth, patternKey));
  }
  const n = workers.length;
  const bandHeight = Math.ceil(height / n);
  const jobs = [];
  for (let i = 0; i < n; i++) {
    const rowStart = i * bandHeight;
    if (rowStart >= height) break;
    const rowEnd = Math.min(height, rowStart + bandHeight);
    const worker = workers[i];
    jobs.push(
      new Promise((resolve, reject) => {
        const onMessage = (e) => {
          worker.removeEventListener("message", onMessage);
          worker.removeEventListener("error", onError);
          resolve(e.data);
        };
        const onError = (err) => {
          worker.removeEventListener("message", onMessage);
          worker.removeEventListener("error", onError);
          reject(err);
        };
        worker.addEventListener("message", onMessage);
        worker.addEventListener("error", onError);
        worker.postMessage({ buf, width, height, bitDepth, patternKey, rowStart, rowEnd });
      }),
    );
  }
  return Promise.all(jobs).then((results) => {
    const out = new Uint8ClampedArray(width * height * 4);
    for (const { rgba, rowStart } of results) {
      out.set(rgba, rowStart * width * 4);
    }
    return out;
  });
}

// ---------------- Viewer ----------------

// openRawViewer builds the RAW Bayer preview UI inside bodyEl, reusing
// openRasterFrameViewer for everything except the bit-depth/Bayer-pattern
// controls and the demosaic decode. Returns the same {state, repaint,
// destroy} handle as openYuvViewer.
export function openRawViewer({ name, url, bodyEl }) {
  const parsed = parseRawInfoFromName(name) || {};
  const initialBitDepth = parsed.bitDepth || DEFAULT_BIT_DEPTH;
  const initialPattern = parsed.bayerPattern || DEFAULT_PATTERN;

  const bitOptionsHtml = [8, 10, 12, 14, 16]
    .map((b) => `<option value="${b}"${b === initialBitDepth ? " selected" : ""}>${b}-bit</option>`)
    .join("");
  const patternOptionsHtml = Object.keys(BAYER_PATTERNS)
    .map((p) => `<option value="${p}"${p === initialPattern ? " selected" : ""}>${p}</option>`)
    .join("");

  return openRasterFrameViewer({
    url,
    bodyEl,
    width: parsed.width || 1920,
    height: parsed.height || 1080,
    // Undemosaiced linear Bayer data is always dark and tinted straight off
    // the sensor (no gamma, no white balance), so the ISP pipeline defaults
    // on regardless of whether the filename says "Linear" — the user can
    // still switch it off to compare against the raw values.
    isp: true,
    extraControlsHtml:
      `<div class="raster-control-group">` +
        `<label>Bit depth</label>` +
        `<select id="rawBitDepth">${bitOptionsHtml}</select>` +
      `</div>` +
      `<div class="raster-control-group">` +
        `<label>Bayer pattern</label>` +
        `<select id="rawBayerPattern">${patternOptionsHtml}</select>` +
      `</div>`,
    bindExtraControls(root, state, onExtraChange) {
      state.bitDepth = initialBitDepth;
      state.bayerPattern = initialPattern;
      // One pool per open viewer, reused across every frame it decodes;
      // terminated in onDestroy below.
      state.workers = createWorkerPool();
      const bitSelect = root.querySelector("#rawBitDepth");
      const patternSelect = root.querySelector("#rawBayerPattern");
      bitSelect.addEventListener("change", () => {
        state.bitDepth = parseInt(bitSelect.value, 10);
        onExtraChange();
      });
      patternSelect.addEventListener("change", () => {
        state.bayerPattern = patternSelect.value;
        onExtraChange();
      });
    },
    frameSize: (state) => rawFrameSize(state.width, state.height, state.bitDepth),
    decode: (buf, state) => decodeBayerParallel(state.workers, buf, state.width, state.height, state.bitDepth, state.bayerPattern),
    statusLabel: (state) => `RAW ${state.bitDepth}-bit ${state.bayerPattern}`,
    onDestroy: (state) => terminateWorkerPool(state.workers),
  });
}
