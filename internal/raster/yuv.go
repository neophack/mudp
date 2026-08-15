// Package raster decodes raw YUV and Bayer sensor pixel dumps into RGBA, and
// applies the ISP touch-up pass used to preview linear/raw captures. This is
// a straight port of the algorithms that used to live in web/lib/yuv.js and
// web/lib/raw.js; it's compiled into the mudp binary and called server-side by
// internal/server/raster.go, which decodes one frame and returns it to the
// browser as a JPEG (the client no longer runs a WASM decoder).
package raster

// clampByte matches the clamp-on-assign behavior of a JS Uint8ClampedArray.
func clampByte(v float64) byte {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return byte(v)
}

// yuvToRgb uses the BT.601 studio-swing formula. u/v are 0..255 centered at 128.
func yuvToRgb(y, u, v byte) (r, g, b float64) {
	uu := float64(u) - 128
	vv := float64(v) - 128
	yy := float64(y)
	r = yy + 1.402*vv
	g = yy - 0.344*uu - 0.714*vv
	b = yy + 1.772*uu
	return
}

func setPixel(out []byte, o int, r, g, b float64) {
	out[o] = clampByte(r)
	out[o+1] = clampByte(g)
	out[o+2] = clampByte(b)
	out[o+3] = 255
}

// YuvDecode decodes one frame's raw bytes to RGBA for the given format key:
// i420, yv12, nv12, nv21, yuyv, uyvy, yuv444. Unknown formats decode as i420.
func YuvDecode(format string, buf []byte, w, h int) []byte {
	switch format {
	case "yv12":
		// Layout is Y, V, U: V comes right after Y, U after that.
		hw, hh := w/2, h/2
		return decodePlanar420(buf, w, h, 0, w*h+hw*hh, w*h)
	case "nv12":
		return decodeSemiPlanar420(buf, w, h, false)
	case "nv21":
		return decodeSemiPlanar420(buf, w, h, true)
	case "yuyv":
		return decodePacked422(buf, w, h, [3]int{0, 1, 3})
	case "uyvy":
		return decodePacked422(buf, w, h, [3]int{1, 0, 2})
	case "yuv444":
		return decodePlanar444(buf, w, h)
	default: // "i420"
		// Layout is Y, U, V: U comes right after Y, V after that.
		hw, hh := w/2, h/2
		return decodePlanar420(buf, w, h, 0, w*h, w*h+hw*hh)
	}
}

// A 4:2:0 chroma plane is floor(w/2) x floor(h/2), so an odd luma width or
// height leaves a final column/row with no chroma sample of its own. Clamping
// the subsampled coordinate makes it reuse the last one — the conventional
// handling, and what keeps these reads inside the buffer the frame-size
// formula actually allocates. Without the clamp, an odd dimension indexed one
// chroma row/column past the plane and panicked the request.
//
// When w or h is 1 the chroma plane is empty; those frames decode as luma-only
// (neutral 128 chroma) rather than indexing a zero-length plane.
const neutralChroma = byte(128)

func decodePlanar420(buf []byte, w, h, yOff, uOff, vOff int) []byte {
	out := make([]byte, w*h*4)
	hw, hh := w/2, h/2
	hasChroma := hw > 0 && hh > 0
	for j := 0; j < h; j++ {
		cj := 0
		if hasChroma {
			cj = clampInt(j>>1, 0, hh-1)
		}
		for i := 0; i < w; i++ {
			y := buf[yOff+j*w+i]
			u, v := neutralChroma, neutralChroma
			if hasChroma {
				ci := clampInt(i>>1, 0, hw-1)
				u = buf[uOff+cj*hw+ci]
				v = buf[vOff+cj*hw+ci]
			}
			r, g, b := yuvToRgb(y, u, v)
			setPixel(out, (j*w+i)*4, r, g, b)
		}
	}
	return out
}

func decodeSemiPlanar420(buf []byte, w, h int, vuFirst bool) []byte {
	out := make([]byte, w*h*4)
	hw, hh := w/2, h/2
	hasChroma := hw > 0 && hh > 0
	uvOff := w * h
	for j := 0; j < h; j++ {
		cj := 0
		if hasChroma {
			cj = clampInt(j>>1, 0, hh-1)
		}
		for i := 0; i < w; i++ {
			y := buf[j*w+i]
			u, v := neutralChroma, neutralChroma
			if hasChroma {
				ci := clampInt(i>>1, 0, hw-1)
				uvIdx := uvOff + (cj*hw+ci)*2
				// In NV12 the pair is (U,V); in NV21 it is (V,U).
				if vuFirst {
					u, v = buf[uvIdx+1], buf[uvIdx]
				} else {
					u, v = buf[uvIdx], buf[uvIdx+1]
				}
			}
			r, g, b := yuvToRgb(y, u, v)
			setPixel(out, (j*w+i)*4, r, g, b)
		}
	}
	return out
}

// idx holds the indices into a 4-byte macro-pixel for [Y, U, V] respectively.
//
// A macro-pixel packs 2 horizontal pixels into 4 bytes, so an odd width leaves
// a half-used macro at the end of each row and rows are strided by
// ceil(w/2)*4 — see packed422Stride, which yuvFrameSize matches. Deriving the
// macro offset from a global pixel counter instead (as this did originally)
// makes every row after the first straddle the previous row's trailing macro
// and runs past the end of the buffer on the last row.
func decodePacked422(buf []byte, w, h int, idx [3]int) []byte {
	out := make([]byte, w*h*4)
	stride := Packed422Stride(w)
	for j := 0; j < h; j++ {
		row := j * stride
		for i := 0; i < w; i += 2 {
			macro := row + (i>>1)*4
			y := buf[macro+idx[0]]
			u := buf[macro+idx[1]]
			v := buf[macro+idx[2]]
			r, g, b := yuvToRgb(y, u, v)
			setPixel(out, (j*w+i)*4, r, g, b)
			// The second pixel in the macro shares U,V; its own Y is one byte later.
			if i+1 < w {
				y2 := buf[macro+idx[0]+2]
				r2, g2, b2 := yuvToRgb(y2, u, v)
				setPixel(out, (j*w+i+1)*4, r2, g2, b2)
			}
		}
	}
	return out
}

// Packed422Stride is the row stride in bytes of a packed 4:2:2 frame: whole
// 4-byte macro-pixels, one per pair of horizontal pixels, rounded up. Equal to
// 2*w for even widths.
func Packed422Stride(w int) int {
	if w <= 0 {
		return 0
	}
	return ((w + 1) / 2) * 4
}

func decodePlanar444(buf []byte, w, h int) []byte {
	out := make([]byte, w*h*4)
	uOff := w * h
	vOff := 2 * w * h
	for j := 0; j < h; j++ {
		for i := 0; i < w; i++ {
			y := buf[j*w+i]
			u := buf[uOff+j*w+i]
			v := buf[vOff+j*w+i]
			r, g, b := yuvToRgb(y, u, v)
			setPixel(out, (j*w+i)*4, r, g, b)
		}
	}
	return out
}
