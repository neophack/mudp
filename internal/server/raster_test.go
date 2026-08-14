package server

import (
	"bytes"
	"encoding/json"
	"image"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mudp/internal/raster"
)

// TestYuvFrameSizeParity checks the Go per-frame byte formulas against the
// ones web/lib/yuv.js shipped (now the only source of truth). A mismatch
// would make the backend read the wrong slice of the file and garble the
// preview, so this is the contract that lets us trust the port.
func TestYuvFrameSizeParity(t *testing.T) {
	const w, h = 1920, 1080
	cases := map[string]int{
		"i420":   w*h + 2*(w/2)*(h/2),
		"yv12":   w*h + 2*(w/2)*(h/2),
		"nv12":   w*h + 2*(w/2)*(h/2),
		"nv21":   w*h + 2*(w/2)*(h/2),
		"yuyv":   2 * w * h,
		"uyvy":   2 * w * h,
		"yuv444": 3 * w * h,
	}
	for format, want := range cases {
		got := yuvFrameSize(format, w, h)
		if got != want {
			t.Errorf("yuvFrameSize(%q) = %d, want %d", format, got, want)
		}
	}
	// Unknown formats fall through to the 4:2:0 planar size (default branch),
	// matching raster.YuvDecode's "unknown -> i420" behavior.
	if got, want := yuvFrameSize("bogus", w, h), w*h+2*(w/2)*(h/2); got != want {
		t.Errorf("yuvFrameSize(unknown) = %d, want %d", got, want)
	}
}

// TestRawFrameSizeParity mirrors the Bayer RAW per-frame size: 1 byte/sample
// at <=8 bits, 2 bytes/sample (16-bit LE container) above that.
func TestRawFrameSizeParity(t *testing.T) {
	const w, h = 1936, 1552
	for _, tc := range []struct {
		bitDepth int
		want     int
	}{
		{8, w * h},
		{10, 2 * w * h},
		{12, 2 * w * h},
		{14, 2 * w * h},
		{16, 2 * w * h},
	} {
		if got := rawFrameSize(w, h, tc.bitDepth); got != tc.want {
			t.Errorf("rawFrameSize(bitDepth=%d) = %d, want %d", tc.bitDepth, got, tc.want)
		}
	}
}

// TestParseRasterParams covers the YUV-vs-RAW dispatch and isp flag. The
// frontend always sends the matching parameter group, so presence of bitDepth
// selects RAW mode and anything else selects YUV.
func TestParseRasterParams(t *testing.T) {
	t.Run("yuv", func(t *testing.T) {
		p, msg := parseRasterParams(map[string][]string{
			"width": {"968"}, "height": {"776"}, "format": {"nv12"}, "isp": {"1"},
		})
		if msg != "" {
			t.Fatalf("unexpected error: %s", msg)
		}
		if p.Kind != "yuv" || p.Format != "nv12" || !p.ISP || p.Width != 968 || p.Height != 776 {
			t.Errorf("parsed params = %+v", p)
		}
	})
	t.Run("raw", func(t *testing.T) {
		p, msg := parseRasterParams(map[string][]string{
			"width": {"1936"}, "height": {"1552"}, "bitDepth": {"16"}, "bayerPattern": {"rggb"},
		})
		if msg != "" {
			t.Fatalf("unexpected error: %s", msg)
		}
		if p.Kind != "raw" || p.BitDepth != 16 || p.BayerPattern != "RGGB" || p.ISP {
			t.Errorf("parsed params = %+v", p)
		}
	})
	t.Run("bad width", func(t *testing.T) {
		if _, msg := parseRasterParams(map[string][]string{"width": {"x"}}); msg == "" {
			t.Error("expected an error for non-numeric width")
		}
	})
}

// writeSyntheticYUV writes a multi-frame i420 file: each frame is a full Y
// plane of 128 followed by U/V planes of 128, so every frame decodes to a
// flat mid-gray image. Returns the path and the per-frame byte count.
func writeSyntheticYUV(t *testing.T, path string, w, h, frames int) (perFrame int) {
	t.Helper()
	hw, hh := w/2, h/2
	perFrame = w*h + 2*hw*hh
	buf := bytes.Repeat([]byte{128}, perFrame*frames)
	if err := os.WriteFile(path, buf, 0o644); err != nil {
		t.Fatalf("write synthetic yuv: %v", err)
	}
	return
}

// TestServeRasterFrameJPEGYUV writes a tiny synthetic YUV file and asserts the
// handler returns a decodable JPEG of the requested frame's dimensions.
func TestServeRasterFrameJPEGYUV(t *testing.T) {
	const w, h = 8, 8
	tmp := t.TempDir()
	path := filepath.Join(tmp, "frame.yuv")
	perFrame := writeSyntheticYUV(t, path, w, h, 2)
	_ = perFrame // ensure the file is multi-frame; frame=1 below must exist

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet,
		"/api/netdisk/raster?path=frame.yuv&width=8&height=8&format=i420&frame=1", nil)
	serveRasterFrameJPEG(rec, req, path, rasterFrameParams{
		Kind: "yuv", Width: w, Height: h, Frame: 1, Format: "i420",
	})

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "image/jpeg" {
		t.Errorf("Content-Type = %q, want image/jpeg", ct)
	}
	img, _, err := image.Decode(bytes.NewReader(rec.Body.Bytes()))
	if err != nil {
		t.Fatalf("decode jpeg: %v", err)
	}
	if b := img.Bounds(); b.Dx() != w || b.Dy() != h {
		t.Errorf("jpeg bounds = %v, want %dx%d", b, w, h)
	}
}

// TestServeRasterFrameJPEGRaw covers a Bayer RAW frame: a flat 16-bit field
// demosaics to a uniform color, and the result must still be a valid JPEG of
// the right size.
func TestServeRasterFrameJPEGRaw(t *testing.T) {
	const w, h = 8, 8
	tmp := t.TempDir()
	path := filepath.Join(tmp, "frame.raw")
	// 16-bit LE samples, all 0x0080 (= 128 in a 16-bit container, mid-range).
	perFrame := rawFrameSize(w, h, 16)
	buf := bytes.Repeat([]byte{0x80, 0x00}, perFrame/2)
	if err := os.WriteFile(path, buf, 0o644); err != nil {
		t.Fatalf("write synthetic raw: %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet,
		"/api/netdisk/raster?path=frame.raw&width=8&height=8&bitDepth=16&bayerPattern=RGGB", nil)
	serveRasterFrameJPEG(rec, req, path, rasterFrameParams{
		Kind: "raw", Width: w, Height: h, BitDepth: 16, BayerPattern: "RGGB",
	})

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	img, _, err := image.Decode(bytes.NewReader(rec.Body.Bytes()))
	if err != nil {
		t.Fatalf("decode jpeg: %v", err)
	}
	if b := img.Bounds(); b.Dx() != w || b.Dy() != h {
		t.Errorf("jpeg bounds = %v, want %dx%d", b, w, h)
	}
}

// TestServeRasterFrameJPEGTruncated verifies the handler reports a clear error
// (and the right status) when the file is smaller than the declared frame,
// rather than silently encoding a partial image.
func TestServeRasterFrameJPEGTruncated(t *testing.T) {
	const w, h = 8, 8
	tmp := t.TempDir()
	path := filepath.Join(tmp, "frame.yuv")
	// Write half a frame's bytes.
	perFrame := writeSyntheticYUV(t, path, w, h, 1)
	if err := os.WriteFile(path, bytes.Repeat([]byte{1}, perFrame/2), 0o644); err != nil {
		t.Fatalf("truncate synthetic yuv: %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet,
		"/api/netdisk/raster?path=frame.yuv&width=8&height=8&format=i420", nil)
	serveRasterFrameJPEG(rec, req, path, rasterFrameParams{
		Kind: "yuv", Width: w, Height: h, Format: "i420",
	})

	if rec.Code != http.StatusRequestedRangeNotSatisfiable {
		t.Fatalf("status = %d, want 416, body = %s", rec.Code, rec.Body.String())
	}
}

// TestServeRasterFrameJPEGRejectsSymlink guards the same escape the inline
// download handler protects against: a symlinked sensor file must not be
// decoded (the symlink could point outside the netdisk root).
func TestServeRasterFrameJPEGRejectsSymlink(t *testing.T) {
	tmp := t.TempDir()
	real := filepath.Join(tmp, "real.yuv")
	writeSyntheticYUV(t, real, 4, 4, 1)
	link := filepath.Join(tmp, "link.yuv")
	if err := os.Symlink(real, link); err != nil {
		// Symlinks need elevated perms on some Windows configs; skip rather
		// than fail where the platform can't create them.
		t.Skipf("symlink unsupported: %v", err)
	}
	rec := httptest.NewRecorder()
	serveRasterFrameJPEG(rec, httptest.NewRequest(http.MethodGet, "/", nil), link, rasterFrameParams{
		Kind: "yuv", Width: 4, Height: 4, Format: "i420",
	})
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403 for symlink", rec.Code)
	}
}

// (image.Decode is used inline in the table cases above; it recognizes the
// JPEG the handler wrote because the image/jpeg driver registers itself via
// init when raster.go imports the package.)

// TestParseRasterParamsIspManual covers the collapsible ISP panel's parameter
// keys: any ispX key switches to manual mode layered over the kind's
// defaults, values clamp to sane bounds, and malformed input is rejected.
func TestParseRasterParamsIspManual(t *testing.T) {
	t.Run("raw manual full set", func(t *testing.T) {
		p, msg := parseRasterParams(map[string][]string{
			"width": {"8"}, "height": {"8"}, "bitDepth": {"12"}, "bayerPattern": {"RGGB"}, "isp": {"1"},
			"ispBlc":           {"10,20,30,40"},
			"ispGainR":         {"1.5"},
			"ispGainB":         {"0.8"},
			"ispCcm":           {"1,0,0,0,1,0,0,0,1"},
			"ispGamma":         {"2.0"},
			"ispSat":           {"1.3"},
			"ispContrast":      {"1.1"},
			"ispBright":        {"-5"},
			"ispChromaNr":      {"2"},
			"ispSharpen":       {"1.5"},
			"ispSharpenRadius": {"4"},
			"ispChromaSupp":    {"30"},
			"ispGray":          {"1"},
		})
		if msg != "" {
			t.Fatalf("unexpected error: %s", msg)
		}
		m := p.IspManual
		if m == nil {
			t.Fatal("expected manual ISP params")
		}
		if m.Blc != [4]int{10, 20, 30, 40} || m.GainR != 1.5 || m.GainB != 0.8 || m.Gamma != 2.0 ||
			m.Saturation != 1.3 || m.Contrast != 1.1 || m.Brightness != -5 || m.ChromaNrRadius != 2 ||
			m.Sharpen != 1.5 || m.SharpenRadius != 4 || m.ChromaSuppLow != 30 || !m.GrayMode {
			t.Errorf("parsed manual params = %+v", m)
		}
		if m.Ccm != [9]float64{1, 0, 0, 0, 1, 0, 0, 0, 1} {
			t.Errorf("ccm = %v", m.Ccm)
		}
		if m.MaxVal != 4095 {
			t.Errorf("raw manual MaxVal = %d, want 4095 from bitDepth 12", m.MaxVal)
		}
	})
	t.Run("yuv manual keeps yuv defaults for absent keys", func(t *testing.T) {
		p, msg := parseRasterParams(map[string][]string{
			"width": {"8"}, "height": {"8"}, "format": {"i420"}, "isp": {"1"},
			"ispGamma": {"2.6"},
		})
		if msg != "" {
			t.Fatalf("unexpected error: %s", msg)
		}
		m := p.IspManual
		if m == nil {
			t.Fatal("expected manual ISP params")
		}
		want := raster.DefaultYuvIspParams()
		want.Gamma = 2.6
		if *m != want {
			t.Errorf("yuv manual = %+v, want %+v", *m, want)
		}
	})
	t.Run("no isp keys stays auto", func(t *testing.T) {
		p, msg := parseRasterParams(map[string][]string{
			"width": {"8"}, "height": {"8"}, "bitDepth": {"12"}, "isp": {"1"},
		})
		if msg != "" {
			t.Fatalf("unexpected error: %s", msg)
		}
		if p.IspManual != nil {
			t.Error("IspManual should be nil without ispX keys")
		}
	})
	t.Run("clamps out-of-range values", func(t *testing.T) {
		p, msg := parseRasterParams(map[string][]string{
			"width": {"8"}, "height": {"8"}, "bitDepth": {"12"},
			"ispGainR": {"99"}, "ispGainB": {"0.0001"}, "ispGamma": {"-5"},
			"ispSat": {"9"}, "ispChromaNr": {"5000"}, "ispSharpenRadius": {"0"},
		})
		if msg != "" {
			t.Fatalf("unexpected error: %s", msg)
		}
		m := p.IspManual
		if m.GainR != 8 || m.GainB != 0.1 || m.Gamma != 0.05 || m.Saturation != 4 ||
			m.ChromaNrRadius != 64 || m.SharpenRadius != 1 {
			t.Errorf("clamped params = %+v", m)
		}
	})
	t.Run("sharpen zero disables", func(t *testing.T) {
		p, msg := parseRasterParams(map[string][]string{
			"width": {"8"}, "height": {"8"}, "bitDepth": {"12"}, "ispSharpen": {"0"},
		})
		if msg != "" {
			t.Fatalf("unexpected error: %s", msg)
		}
		if p.IspManual.EnableSharpen {
			t.Error("ispSharpen=0 should disable the sharpen stage")
		}
	})
	t.Run("malformed values rejected", func(t *testing.T) {
		for _, q := range []map[string][]string{
			{"width": {"8"}, "height": {"8"}, "bitDepth": {"12"}, "ispBlc": {"1,2,3"}},
			{"width": {"8"}, "height": {"8"}, "bitDepth": {"12"}, "ispBlc": {"1,2,3,x"}},
			{"width": {"8"}, "height": {"8"}, "bitDepth": {"12"}, "ispCcm": {"1,2,3"}},
			{"width": {"8"}, "height": {"8"}, "bitDepth": {"12"}, "ispCcm": {"1,2,3,4,5,6,7,8,x"}},
			{"width": {"8"}, "height": {"8"}, "bitDepth": {"12"}, "ispGainR": {"abc"}},
			{"width": {"8"}, "height": {"8"}, "format": {"i420"}, "ispGamma": {"NaN"}},
		} {
			if _, msg := parseRasterParams(q); msg == "" {
				t.Errorf("expected an error for %v", q)
			}
		}
	})
	t.Run("awb flag", func(t *testing.T) {
		p, msg := parseRasterParams(map[string][]string{
			"width": {"8"}, "height": {"8"}, "bitDepth": {"12"}, "awb": {"1"},
		})
		if msg != "" {
			t.Fatalf("unexpected error: %s", msg)
		}
		if !p.Awb {
			t.Error("awb=1 not parsed")
		}
	})
}

// TestServeRasterFrameJPEGRawIspManualGray: a manual ispGray=1 preview must
// come back grayscale (R==G==B within JPEG tolerance).
func TestServeRasterFrameJPEGRawIspManualGray(t *testing.T) {
	const w, h = 8, 8
	tmp := t.TempDir()
	path := filepath.Join(tmp, "frame.raw")
	buf := make([]byte, rawFrameSize(w, h, 16))
	for i := 0; i < w*h; i++ {
		buf[i*2] = 0x80 // 128 in a 16-bit container
	}
	if err := os.WriteFile(path, buf, 0o644); err != nil {
		t.Fatalf("write synthetic raw: %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet,
		"/api/netdisk/raster?path=frame.raw&width=8&height=8&bitDepth=16&bayerPattern=RGGB&isp=1&ispGray=1&ispGamma=1", nil)
	params, msg := parseRasterParams(req.URL.Query())
	if msg != "" {
		t.Fatalf("parse: %s", msg)
	}
	serveRasterFrameJPEG(rec, req, path, params)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	img, _, err := image.Decode(bytes.NewReader(rec.Body.Bytes()))
	if err != nil {
		t.Fatalf("decode jpeg: %v", err)
	}
	r, g, b, _ := img.At(4, 4).RGBA()
	if absInt(int(r>>8)-int(g>>8)) > 2 || absInt(int(g>>8)-int(b>>8)) > 2 {
		t.Errorf("gray mode pixel = (%d,%d,%d), want R≈G≈B", r>>8, g>>8, b>>8)
	}
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// writeSyntheticRaw writes a single-frame 16-bit LE Bayer dump where R sites
// hold lo and every other site holds hi (RGGB).
func writeSyntheticRaw(t *testing.T, path string, w, h int, lo, hi int) {
	t.Helper()
	buf := make([]byte, rawFrameSize(w, h, 16))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			v := hi
			if y%2 == 0 && x%2 == 0 { // R site in RGGB
				v = lo
			}
			i := (y*w + x) * 2
			buf[i] = byte(v & 0xFF)
			buf[i+1] = byte(v >> 8)
		}
	}
	if err := os.WriteFile(path, buf, 0o644); err != nil {
		t.Fatalf("write synthetic raw: %v", err)
	}
}

// TestServeRasterFrameJPEGAwb covers the panel's Gray-world AWB button: the
// same raster endpoint with awb=1 returns JSON gains instead of a JPEG. The
// RAW request carries manual zero black levels so the synthetic R=100/G=B=200
// field yields exactly gainR=2, gainB=1.
func TestServeRasterFrameJPEGAwb(t *testing.T) {
	const w, h = 8, 8
	tmp := t.TempDir()
	rawPath := filepath.Join(tmp, "frame.raw")
	writeSyntheticRaw(t, rawPath, w, h, 100, 200)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet,
		"/api/netdisk/raster?path=frame.raw&width=8&height=8&bitDepth=16&bayerPattern=RGGB&awb=1&ispBlc=0,0,0,0", nil)
	params, msg := parseRasterParams(req.URL.Query())
	if msg != "" {
		t.Fatalf("parse: %s", msg)
	}
	serveRasterFrameJPEG(rec, req, rawPath, params)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	var res struct {
		GainR float64 `json:"gainR"`
		GainB float64 `json:"gainB"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("unmarshal awb response: %v", err)
	}
	if res.GainR != 2 || res.GainB != 1 {
		t.Errorf("awb gains = %v/%v, want 2/1", res.GainR, res.GainB)
	}

	// YUV variant: a flat mid-gray frame is already neutral → 1/1.
	yuvPath := filepath.Join(tmp, "frame.yuv")
	writeSyntheticYUV(t, yuvPath, 4, 4, 1)
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet,
		"/api/netdisk/raster?path=frame.yuv&width=4&height=4&format=i420&awb=1", nil)
	params, msg = parseRasterParams(req.URL.Query())
	if msg != "" {
		t.Fatalf("parse: %s", msg)
	}
	serveRasterFrameJPEG(rec, req, yuvPath, params)
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("unmarshal yuv awb response: %v", err)
	}
	if res.GainR != 1 || res.GainB != 1 {
		t.Errorf("yuv awb gains = %v/%v, want 1/1", res.GainR, res.GainB)
	}
}
