package server

import (
	"bytes"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"

	"mudp/internal/raster"
)

// This file serves server-side-decoded previews of raw sensor dumps (YUV and
// Bayer RAW files) as single-frame JPEGs. The browser used to fetch the raw
// bytes and decode them in a Go-compiled WebAssembly module (cmd/rasterwasm);
// that round-tripped the full, uncompressed frame data over the network for
// every paint. Now the backend reuses the same decode algorithms
// (internal/raster, already compiled into this binary) and ships back only a
// compressed JPEG, which is both smaller on the wire and removes the WASM
// toolchain from the build.
//
// The frontend viewer scaffold (web/lib/yuv.js openRasterFrameViewer) keeps
// its frame slider/scrubbing, zoom/pan, playback, and ISP toggle — only the
// per-frame fetch switched from "raw bytes + client decode" to "one JPEG".

// maxRasterPixels bounds the frame size serveRasterFrameJPEG will decode. See
// the check there for why an unbounded value is fatal rather than merely slow.
const maxRasterPixels = 64 << 20

// rasterFrameParams carries everything serveRasterFrameJPEG needs to decode a
// single frame. The Kind is either "yuv" (use Format) or "raw" (use BitDepth +
// BayerPattern); the two modes mirror openYuvViewer / openRawViewer in the
// frontend.
type rasterFrameParams struct {
	Kind         string // "yuv" or "raw"
	Width        int
	Height       int
	Frame        int
	ISP          bool
	Format       string // YUV format key (i420/yv12/nv12/nv21/yuyv/uyvy/yuv444)
	BitDepth     int    // RAW bit depth (8/10/12/14/16)
	BayerPattern string // RAW Bayer pattern (RGGB/BGGR/GRBG/GBRG)
	// IspManual is non-nil when the viewer's collapsible ISP panel sent
	// explicit parameters (any ispX query key). It starts from the defaults
	// for the kind so partial parameter sets keep the rest at their defaults.
	IspManual *raster.IspParams
	// Awb requests the gray-world auto-white-balance gains for the frame
	// (JSON response) instead of a decoded JPEG — the panel's "Gray-world
	// AWB" button.
	Awb bool
}

// yuvFrameSize returns the per-frame byte count for a YUV format, porting the
// frameSize formulas from web/lib/yuv.js so the backend computes the same
// offsets the frontend did. Mirrors the layout assumptions of
// raster.YuvDecode.
func yuvFrameSize(format string, w, h int) int {
	hw := w / 2
	hh := h / 2
	switch format {
	case "yuyv", "uyvy": // packed 4:2:2, 16 bits/pixel
		// Rows are strided by whole 4-byte macro-pixels, so an odd width pads
		// to the next pair. Identical to 2*w*h for even widths.
		return raster.Packed422Stride(w) * h
	case "yuv444": // planar 4:4:4, 24 bits/pixel
		return 3 * w * h
	default: // i420/yv12/nv12/nv21 planar/semi-planar 4:2:0, 12 bits/pixel
		return w*h + 2*hw*hh
	}
}

// rawFrameSize returns the per-frame byte count for a Bayer RAW dump: 8-bit
// dumps pack one sample per byte, anything deeper is stored unpacked in a
// 16-bit little-endian container. Ported from web/lib/raw.js rawFrameSize.
func rawFrameSize(w, h, bitDepth int) int {
	if bitDepth <= 8 {
		return w * h
	}
	return 2 * w * h
}

func (p rasterFrameParams) frameSize() int {
	if p.Kind == "raw" {
		return rawFrameSize(p.Width, p.Height, p.BitDepth)
	}
	return yuvFrameSize(p.Format, p.Width, p.Height)
}

// serveRasterFrameJPEG decodes one frame of the raw sensor file at full and
// writes it back as a JPEG. The caller has already resolved + validated full
// (netdisk root containment, symlink checks); this function owns only the
// read/decode/encode pipeline. Width/height/format/etc. come from the request
// query (the frontend's filename-parsed defaults) so the same endpoint serves
// any YUV/RAW layout without the backend sniffing the file.
func serveRasterFrameJPEG(w http.ResponseWriter, r *http.Request, full string, p rasterFrameParams) {
	if p.Width <= 0 || p.Height <= 0 {
		writeErr(w, http.StatusBadRequest, "invalid width/height")
		return
	}
	// Width/height are raw query parameters and the decode path allocates on
	// the order of 36 bytes per pixel (the ISP pipeline's working planes), so
	// an unbounded value does not fail as a recovered panic — it aborts the
	// whole process with an out-of-memory fatal error, and the share route
	// reaches this code without an account. Cap the frame first. 64 MP is
	// roughly an order of magnitude past the largest dump these viewers open.
	if int64(p.Width)*int64(p.Height) > maxRasterPixels {
		writeErr(w, http.StatusBadRequest, "frame dimensions are too large to preview")
		return
	}
	perFrame := p.frameSize()
	if perFrame <= 0 {
		writeErr(w, http.StatusBadRequest, "unsupported dimensions for this format")
		return
	}
	if p.Frame < 0 {
		p.Frame = 0
	}
	offset := int64(p.Frame) * int64(perFrame)

	f, err := openNoFollow(full)
	if err != nil {
		writeErr(w, http.StatusForbidden, err.Error())
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		writeErr(w, http.StatusForbidden, "not a regular file")
		return
	}

	buf := make([]byte, perFrame)
	// ReadAt reads from a SectionReader-like offset without a seek and is safe
	// for concurrent use; a short read means the frame is truncated (file
	// smaller than width*height*format implies — wrong dimensions guess).
	// io.EOF / io.ErrUnexpectedEOF accompany a short read and are expected
	// there, so only surface genuinely unexpected errors.
	n, err := f.ReadAt(buf, offset)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("read failed: %v", err))
		return
	}
	if n < perFrame {
		writeErr(w, http.StatusRequestedRangeNotSatisfiable,
			fmt.Sprintf("frame %d is truncated (got %d of %d bytes); check width/height/format", p.Frame, n, perFrame))
		return
	}

	// AWB mode: report the gray-world gains for this frame instead of a
	// JPEG. The RAW variant averages BLC-corrected Bayer sites; the YUV
	// variant averages the decoded RGB channels. Manual BLC matters here —
	// the gains must be computed against the same black point the preview
	// will subtract.
	if p.Awb {
		var gainR, gainB float64
		if p.Kind == "raw" {
			params := raster.DefaultIspParams(p.BitDepth)
			if p.IspManual != nil {
				params = *p.IspManual
			}
			gainR, gainB = raster.ComputeGrayWorldGains(buf, p.Width, p.Height, p.BitDepth, p.BayerPattern, params)
		} else {
			gainR, gainB = raster.GrayWorldGainsRGBA(raster.YuvDecode(p.Format, buf, p.Width, p.Height))
		}
		writeJSON(w, http.StatusOK, map[string]any{"gainR": gainR, "gainB": gainB})
		return
	}

	var rgba []byte
	if p.Kind == "raw" {
		if p.ISP {
			// ISP on: the full ported pipeline (directional demosaic, BLC,
			// chroma NR, WB, CCM, gamma, tone, multi-band sharpen) replaces
			// the old "bilinear demosaic + auto WB/gamma" pass. Without
			// manual parameters it runs the board-calibrated defaults.
			params := raster.DefaultIspParams(p.BitDepth)
			if p.IspManual != nil {
				params = *p.IspManual
			}
			rgba = raster.ProcessBayer(buf, p.Width, p.Height, p.BitDepth, p.BayerPattern, params)
		} else {
			rgba = raster.BayerDecode(buf, p.Width, p.Height, p.BitDepth, p.BayerPattern, 0, p.Height)
		}
	} else {
		rgba = raster.YuvDecode(p.Format, buf, p.Width, p.Height)
		if p.ISP {
			if p.IspManual != nil {
				raster.ApplyIspParams(rgba, p.Width, p.Height, *p.IspManual)
			} else {
				raster.ApplyIsp(rgba, 1.4)
			}
		}
	}

	// raster produces R/G/B/A in that byte order, which is exactly
	// image.RGBA's pixel layout, so we hand it the slice directly (no copy).
	// A decoder that bailed out (nil) or returned fewer pixels than the Rect
	// claims would make jpeg.Encode index past the end of Pix, so check the
	// length rather than letting that surface as a recovered panic.
	if len(rgba) < 4*p.Width*p.Height {
		writeErr(w, http.StatusBadRequest, "frame could not be decoded with these parameters")
		return
	}
	img := &image.RGBA{Pix: rgba, Stride: 4 * p.Width, Rect: image.Rect(0, 0, p.Width, p.Height)}

	// A fresh per-request buffer so cancellation/early-return can't leak a
	// partial write to a shared buffer. Quality 85 is visually lossless for
	// preview while an order of magnitude smaller than the raw RGBA.
	var out bytes.Buffer
	if err := jpeg.Encode(&out, img, &jpeg.Options{Quality: 85}); err != nil {
		writeErr(w, http.StatusInternalServerError, fmt.Sprintf("jpeg encode failed: %v", err))
		return
	}

	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, max-age=0")
	w.Header().Set("Content-Length", strconv.Itoa(out.Len()))
	_, _ = w.Write(out.Bytes())
}

// ispParamKeys are the query keys that switch the decode into manual ISP
// mode (the viewer's collapsible panel sends them once the user touches any
// control). Absent keys keep the kind's defaults.
var ispParamKeys = []string{
	"ispBlc", "ispGainR", "ispGainB", "ispCcm", "ispGamma", "ispSat",
	"ispContrast", "ispBright", "ispChromaNr", "ispSharpen", "ispSharpenRadius",
	"ispChromaSupp", "ispGray",
}

// parseFloatClamped parses one optional float query value, clamped to
// [lo,hi]. Returns (value, present, errorMessage).
func parseFloatClamped(q map[string][]string, key string, lo, hi float64) (float64, bool, string) {
	vs := q[key]
	if len(vs) == 0 || vs[0] == "" {
		return 0, false, ""
	}
	v, err := strconv.ParseFloat(vs[0], 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, false, "invalid " + key
	}
	if v < lo {
		v = lo
	} else if v > hi {
		v = hi
	}
	return v, true, ""
}

// parseIspManual builds the manual ISP parameter set from the ispX query
// keys, layered over the kind's defaults (board-calibrated for RAW, neutral
// for YUV). Returns (nil, "") when no ispX key is present (auto mode).
func parseIspManual(q map[string][]string, kind string, bitDepth int) (*raster.IspParams, string) {
	anyKey := false
	for _, k := range ispParamKeys {
		if vs := q[k]; len(vs) > 0 && vs[0] != "" {
			anyKey = true
			break
		}
	}
	if !anyKey {
		return nil, ""
	}

	var p raster.IspParams
	if kind == "raw" {
		p = raster.DefaultIspParams(bitDepth)
	} else {
		p = raster.DefaultYuvIspParams()
	}

	// Black levels: "r,gr,gb,b", each 0..65535.
	if vs := q["ispBlc"]; len(vs) > 0 && vs[0] != "" {
		parts := strings.Split(vs[0], ",")
		if len(parts) != 4 {
			return nil, "invalid ispBlc (want r,gr,gb,b)"
		}
		for i, s := range parts {
			v, err := strconv.Atoi(strings.TrimSpace(s))
			if err != nil || v < 0 || v > 65535 {
				return nil, "invalid ispBlc value"
			}
			p.Blc[i] = v
		}
	}
	// CCM: 9 comma-separated floats, each clamped to -8..8 (any practical
	// matrix plus headroom for creative edits, while keeping a malicious
	// value from producing Inf/NaN pixels downstream).
	if vs := q["ispCcm"]; len(vs) > 0 && vs[0] != "" {
		parts := strings.Split(vs[0], ",")
		if len(parts) != 9 {
			return nil, "invalid ispCcm (want 9 comma-separated values)"
		}
		for i, s := range parts {
			v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
			if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
				return nil, "invalid ispCcm value"
			}
			p.Ccm[i] = clampF(v, -8, 8)
		}
	}

	type floatField struct {
		key    string
		lo, hi float64
		dst    *float64
	}
	for _, f := range []floatField{
		{"ispGainR", 0.1, 8, &p.GainR},
		{"ispGainB", 0.1, 8, &p.GainB},
		{"ispGamma", 0.05, 10, &p.Gamma},
		{"ispSat", 0, 4, &p.Saturation},
		{"ispContrast", 0.1, 4, &p.Contrast},
		{"ispBright", -255, 255, &p.Brightness},
		{"ispSharpen", 0, 8, &p.Sharpen},
		{"ispChromaSupp", 0, 255, &p.ChromaSuppLow},
	} {
		v, present, msg := parseFloatClamped(q, f.key, f.lo, f.hi)
		if msg != "" {
			return nil, msg
		}
		if present {
			*f.dst = v
		}
	}
	// Sharpen strength 0 also disables the stage (the panel's checkbox).
	if p.Sharpen <= 0 {
		p.EnableSharpen = false
	}

	// Integer radii, parsed as clamped floats (fractional input truncates).
	if v, present, msg := parseFloatClamped(q, "ispChromaNr", 0, 64); msg != "" {
		return nil, msg
	} else if present {
		p.ChromaNrRadius = int(v)
	}
	if v, present, msg := parseFloatClamped(q, "ispSharpenRadius", 1, 32); msg != "" {
		return nil, msg
	} else if present {
		p.SharpenRadius = int(v)
	}

	if vs := q["ispGray"]; len(vs) > 0 && (vs[0] == "1" || strings.EqualFold(vs[0], "true")) {
		p.GrayMode = true
	}
	return &p, ""
}

func clampF(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// parseRasterParams reads the viewer's query string into rasterFrameParams.
// Returns the params plus a human-readable error string ("" when ok). The
// frontend always knows whether it opened a YUV or RAW file and sends the
// matching parameters, so Kind is inferred from which group is present.
func parseRasterParams(q map[string][]string) (rasterFrameParams, string) {
	atoi := func(key string, fallback int) (int, bool) {
		vs := q[key]
		if len(vs) == 0 || vs[0] == "" {
			return fallback, true
		}
		n, err := strconv.Atoi(vs[0])
		if err != nil || n < 0 {
			return 0, false
		}
		return n, true
	}
	p := rasterFrameParams{Format: "i420", BitDepth: 16, BayerPattern: "RGGB"}

	w, ok := atoi("width", 0)
	if !ok {
		return p, "invalid width"
	}
	p.Width = w
	h, ok := atoi("height", 0)
	if !ok {
		return p, "invalid height"
	}
	p.Height = h
	fr, ok := atoi("frame", 0)
	if !ok {
		return p, "invalid frame"
	}
	p.Frame = fr

	if vs := q["isp"]; len(vs) > 0 && (vs[0] == "1" || strings.EqualFold(vs[0], "true")) {
		p.ISP = true
	}
	if vs := q["awb"]; len(vs) > 0 && (vs[0] == "1" || strings.EqualFold(vs[0], "true")) {
		p.Awb = true
	}

	// RAW vs YUV is decided by which parameters the caller sent. RAW viewers
	// always send bitDepth (and pattern); YUV viewers send format.
	if _, hasRaw := q["bitDepth"]; hasRaw {
		p.Kind = "raw"
		bd, ok := atoi("bitDepth", 16)
		if !ok {
			return p, "invalid bitDepth"
		}
		p.BitDepth = bd
		if vs := q["bayerPattern"]; len(vs) > 0 && vs[0] != "" {
			p.BayerPattern = strings.ToUpper(vs[0])
		}
		ispManual, msg := parseIspManual(q, p.Kind, p.BitDepth)
		if msg != "" {
			return p, msg
		}
		p.IspManual = ispManual
		return p, ""
	}
	p.Kind = "yuv"
	if vs := q["format"]; len(vs) > 0 && vs[0] != "" {
		p.Format = strings.ToLower(vs[0])
	}
	ispManual, msg := parseIspManual(q, p.Kind, p.BitDepth)
	if msg != "" {
		return p, msg
	}
	p.IspManual = ispManual
	return p, ""
}

// netdiskRasterFrame decodes one frame of a YUV/RAW file in the current user's
// netdisk and returns it as a JPEG. Mirrors netdiskRaw's path/symlink
// resolution; only the response body differs (decoded JPEG vs. raw bytes).
func (a *App) netdiskRasterFrame(w http.ResponseWriter, r *http.Request) {
	root, err := a.userNetdiskRoot(currentUser(r))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	full, _, err := cleanUserPath(root, r.URL.Query().Get("path"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if _, err := os.Stat(full); err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	params, msg := parseRasterParams(r.URL.Query())
	if msg != "" {
		writeErr(w, http.StatusBadRequest, msg)
		return
	}
	serveRasterFrameJPEG(w, r, full, params)
}

// netdiskBackupRasterFrame is the backup-disk variant of netdiskRasterFrame:
// same resolution as netdiskBackupRaw, then decodes one frame to JPEG so a
// backup YUV/RAW preview reuses the same server-side decode path.
func (a *App) netdiskBackupRasterFrame(w http.ResponseWriter, r *http.Request) {
	root, err := a.userBackupRoot(currentUser(r))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	full, _, err := cleanUserPath(root, r.URL.Query().Get("path"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if _, err := os.Stat(full); err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	params, msg := parseRasterParams(r.URL.Query())
	if msg != "" {
		writeErr(w, http.StatusBadRequest, msg)
		return
	}
	serveRasterFrameJPEG(w, r, full, params)
}

// netdiskShareRasterFrame is the public-share variant of netdiskRasterFrame:
// same resolution + password + scope checks as netdiskShareRaw, then decodes a
// single frame to JPEG so a share visitor can preview YUV/RAW without the
// owner's bytes leaving the server undecoded.
func (a *App) netdiskShareRasterFrame(w http.ResponseWriter, r *http.Request) {
	share, ownerRoot, err := a.resolveShare(r.URL.Query().Get("token"))
	if err != nil {
		status := http.StatusNotFound
		if err == errShareExpired {
			status = http.StatusGone
		}
		writeErr(w, status, err.Error())
		return
	}
	if err := checkSharePassword(r, share); err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": err.Error(), "needsPassword": true})
		return
	}
	reqPath := r.URL.Query().Get("path")
	if reqPath == "" && len(share.Paths) == 1 {
		reqPath = share.Paths[0]
	}
	if !shareContains(share.Paths, reqPath) {
		writeErr(w, http.StatusForbidden, "path is not in this share")
		return
	}
	full, _, err := cleanUserPath(ownerRoot, reqPath)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if _, err := os.Stat(full); err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	params, msg := parseRasterParams(r.URL.Query())
	if msg != "" {
		writeErr(w, http.StatusBadRequest, msg)
		return
	}
	serveRasterFrameJPEG(w, r, full, params)
}
