package server

import (
	"bytes"
	"fmt"
	"image"
	"image/jpeg"
	"io"
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
		return 2 * w * h
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

	var rgba []byte
	if p.Kind == "raw" {
		rgba = raster.BayerDecode(buf, p.Width, p.Height, p.BitDepth, p.BayerPattern, 0, p.Height)
	} else {
		rgba = raster.YuvDecode(p.Format, buf, p.Width, p.Height)
	}
	if p.ISP {
		raster.ApplyIsp(rgba, 1.4)
	}

	// raster produces R/G/B/A in that byte order, which is exactly
	// image.RGBA's pixel layout, so we hand it the slice directly (no copy).
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
		return p, ""
	}
	p.Kind = "yuv"
	if vs := q["format"]; len(vs) > 0 && vs[0] != "" {
		p.Format = strings.ToLower(vs[0])
	}
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
