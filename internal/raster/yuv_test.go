package raster

import "testing"

// TestI420AndYV12ChromaPlaneOrder is a regression test ported from
// web/tests/unit/yuv.test.js: the i420 and yv12 formats once had their U/V
// byte offsets swapped, so "I420" (the viewer's default format) rendered
// with wrong colors. A 2x2 frame has one 1-byte U sample and one 1-byte V
// sample, so the two layouts differ only in which of those two bytes is
// which:
//
//	I420 file layout: [Y Y Y Y][U][V]
//	YV12 file layout: [Y Y Y Y][V][U]
//
// Both must decode to the SAME color when fed the logically same U/V values,
// laid out in each format's own byte order.
func TestI420AndYV12ChromaPlaneOrder(t *testing.T) {
	const (
		yVal = 128
		u    = 255 // uu = +127
		v    = 0   // vv = -128
	)
	// Expected from yuvToRgb(128, 255, 0):
	//   r = 128 + 1.402*(-128)          ~= 0   (clamped)
	//   g = 128 - 0.344*127 - 0.714*-128 ~= 176
	//   b = 128 + 1.772*127              ~= 255 (clamped)
	wantR, wantG, wantB := 0, 176, 255
	const tolerance = 3

	checkPixel := func(t *testing.T, rgba []byte) {
		t.Helper()
		if d := abs(int(rgba[0]) - wantR); d > tolerance {
			t.Errorf("r = %d, want ~%d", rgba[0], wantR)
		}
		if d := abs(int(rgba[1]) - wantG); d > tolerance {
			t.Errorf("g = %d, want ~%d", rgba[1], wantG)
		}
		if d := abs(int(rgba[2]) - wantB); d > tolerance {
			t.Errorf("b = %d, want ~%d", rgba[2], wantB)
		}
		if rgba[3] != 255 {
			t.Errorf("a = %d, want 255", rgba[3])
		}
	}

	t.Run("i420 (Y, U, V order)", func(t *testing.T) {
		buf := []byte{yVal, yVal, yVal, yVal, u, v}
		rgba := YuvDecode("i420", buf, 2, 2)
		checkPixel(t, rgba[0:4])
	})

	t.Run("yv12 (Y, V, U order)", func(t *testing.T) {
		buf := []byte{yVal, yVal, yVal, yVal, v, u}
		rgba := YuvDecode("yv12", buf, 2, 2)
		checkPixel(t, rgba[0:4])
	})
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
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
