// RAW Bayer-file viewer: previews raw single-channel Bayer sensor dumps by
// demosaicing them to RGB.
//
// Reuses the generic frame-viewer scaffold from lib/yuv.js (canvas, dimension
// inputs, frame navigation/playback, zoom, JPEG fetching, and the ISP
// auto-white-balance + gamma pipeline) — this module only supplies the
// RAW-specific pieces: filename parsing, the Bayer color-filter-array layout,
// and the per-frame byte size used to compute the frame count. The demosaic
// itself runs server-side (internal/raster/bayer.go) via the
// /api/netdisk/raster endpoint, which returns one decoded JPEG per frame.

import { openRasterFrameViewer } from "./yuv.js";

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
// tools), so we read 2 bytes per sample. This mirrors
// internal/server/raster.go's rawFrameSize.
function bytesPerSample(bitDepth) {
  return bitDepth <= 8 ? 1 : 2;
}

export function rawFrameSize(width, height, bitDepth) {
  return width * height * bytesPerSample(bitDepth);
}

// ---------------- Viewer ----------------

// openRawViewer builds the RAW Bayer preview UI inside bodyEl, reusing
// openRasterFrameViewer for everything except the bit-depth/Bayer-pattern
// controls and the per-frame raster URL. Returns the same {state, repaint,
// destroy} handle as openYuvViewer.
export function openRawViewer({ name, url, rasterUrl, bodyEl }) {
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
    rasterUrl,
    bodyEl,
    width: parsed.width || 1920,
    height: parsed.height || 1080,
    // Undemosaiced linear Bayer data is always dark and tinted straight off
    // the sensor (no gamma, no white balance), so the ISP pipeline defaults
    // on regardless of whether the filename says "Linear" — the user can
    // still switch it off to compare against the raw values. The ISP pass
    // runs server-side.
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
    frameURL: (state) =>
      `${rasterUrl}&width=${state.width}&height=${state.height}&frame=${state.frame}` +
      `&bitDepth=${state.bitDepth}&bayerPattern=${encodeURIComponent(state.bayerPattern)}` +
      `&isp=${state.isp ? 1 : 0}`,
    statusLabel: (state) => `RAW ${state.bitDepth}-bit ${state.bayerPattern}`,
  });
}
