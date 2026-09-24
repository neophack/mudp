package raster

import "testing"

// TestChromaByteOrderAllFormats extends the original i420/yv12 regression
// (ported from web/tests/unit/yuv.test.js — "I420" once rendered with wrong
// colors because its U/V byte offsets were swapped) to every subsampled
// format: the semi-planar nv12/nv21 pairs and the packed yuyv/uyvy
// macro-pixels must also keep their U/V byte order straight. A 2x2 frame has
// exactly one U and one V sample, so each format's convention is directly
// visible in its file bytes:
//
//	i420 [Y Y Y Y][U][V]      yv12 [Y Y Y Y][V][U]
//	nv12 [Y Y Y Y][U V]       nv21 [Y Y Y Y][V U]
//	yuyv macro [Y0 U Y1 V]    uyvy macro [U Y0 V Y1]
//
// All six must decode the logically same Y/U/V values to the same RGB.
func TestChromaByteOrderAllFormats(t *testing.T) {
	const (
		yVal = 128
		u    = 255 // uu = +127
		v    = 0   // vv = -128
	)
	// Exact expected values from yuvToRgb(128, 255, 0) with clampByte's
	// truncation (no tolerance needed — the unclamped channel sits at 175.704,
	// far from any integer boundary):
	//   r = 128 + 1.402·(-128)          =  -51.5 → clamp 0
	//   g = 128 - 0.344·127 + 0.714·128 = 175.7  → truncate 175
	//   b = 128 + 1.772·127             =  353.0 → clamp 255
	const wantR, wantG, wantB = 0, 175, 255

	for _, tc := range []struct {
		format string
		buf    []byte
	}{
		{"i420", []byte{yVal, yVal, yVal, yVal, u, v}},
		{"yv12", []byte{yVal, yVal, yVal, yVal, v, u}},
		{"nv12", []byte{yVal, yVal, yVal, yVal, u, v}},
		{"nv21", []byte{yVal, yVal, yVal, yVal, v, u}},
		{"yuyv", []byte{yVal, u, yVal, v, yVal, u, yVal, v}},
		{"uyvy", []byte{u, yVal, v, yVal, u, yVal, v, yVal}},
	} {
		t.Run(tc.format, func(t *testing.T) {
			rgba := YuvDecode(tc.format, tc.buf, 2, 2)
			for p := 0; p < 4; p++ {
				o := p * 4
				if rgba[o] != wantR || rgba[o+1] != wantG || rgba[o+2] != wantB {
					t.Fatalf("pixel %d = (%d,%d,%d), want (%d,%d,%d)",
						p, rgba[o], rgba[o+1], rgba[o+2], wantR, wantG, wantB)
				}
				if rgba[o+3] != 255 {
					t.Fatalf("pixel %d alpha = %d, want 255", p, rgba[o+3])
				}
			}
		})
	}
}

// TestYuvDecodeOddDimensions guards the odd-width/height case. A 4:2:0 chroma
// plane is floor(w/2) x floor(h/2) and a packed 4:2:2 row is strided by whole
// macro-pixels, so an odd dimension used to index past the end of the frame
// buffer and panic the request — reachable from the viewer's width/height
// inputs and from a public share link.
func TestYuvDecodeOddDimensions(t *testing.T) {
	frameSize := func(format string, w, h int) int {
		switch format {
		case "yuyv", "uyvy":
			return Packed422Stride(w) * h
		case "yuv444":
			return 3 * w * h
		default:
			return w*h + 2*(w/2)*(h/2)
		}
	}
	formats := []string{"i420", "yv12", "nv12", "nv21", "yuyv", "uyvy", "yuv444"}
	dims := [][2]int{{3, 3}, {1, 1}, {1, 5}, {5, 1}, {7, 3}, {2, 3}, {3, 2}}
	for _, format := range formats {
		for _, d := range dims {
			w, h := d[0], d[1]
			buf := make([]byte, frameSize(format, w, h))
			rgba := YuvDecode(format, buf, w, h)
			if len(rgba) != w*h*4 {
				t.Errorf("%s %dx%d: got %d bytes, want %d", format, w, h, len(rgba), w*h*4)
			}
		}
	}
}

// TestPacked422Stride pins the stride down: even widths must stay at exactly
// 2*w bytes so existing captures keep their frame count, odd widths round up
// to a whole macro-pixel.
func TestPacked422Stride(t *testing.T) {
	for _, tc := range []struct{ w, want int }{
		{0, 0}, {-4, 0}, {1, 4}, {2, 4}, {3, 8}, {4, 8}, {1920, 3840}, {1935, 3872},
	} {
		if got := Packed422Stride(tc.w); got != tc.want {
			t.Errorf("Packed422Stride(%d) = %d, want %d", tc.w, got, tc.want)
		}
	}
}
