package raster

// bayerPatterns: each entry is the 2x2 tile of color-filter labels, row-major,
// as seen starting at pixel (0,0), e.g. RGGB is [[R,G],[G,B]]:
//
//	R G R G ...
//	G B G B ...
var bayerPatterns = map[string][2][2]byte{
	"RGGB": {{'R', 'G'}, {'G', 'B'}},
	"BGGR": {{'B', 'G'}, {'G', 'R'}},
	"GRBG": {{'G', 'R'}, {'B', 'G'}},
	"GBRG": {{'G', 'B'}, {'R', 'G'}},
}

// BayerDecode demosaics a single-channel Bayer buffer into RGBA via bilinear
// interpolation: a sample pixel keeps its own channel value; the other two
// channels at that pixel are averaged from same-color neighbors (edge-clamped
// at the image border). This is a fast, dependency-free approximation good
// enough for previewing — not a full ISP-grade demosaic.
//
// rowStart/rowEnd (pass 0, height for the whole image) let a caller decode
// just a horizontal band — a leftover from when the browser split a frame
// across a Web Worker pool. The server decoder always decodes the whole frame
// (0, height), but the band form is kept for the row-band unit tests and
// callers that may want it later. The returned slice covers only that band
// (height = rowEnd - rowStart); sampling still indexes with the *absolute* y
// so the CFA parity and edge-clamping stay correct regardless of the band's
// offset.
func BayerDecode(buf []byte, w, h, bitDepth int, patternKey string, rowStart, rowEnd int) []byte {
	pattern, ok := bayerPatterns[patternKey]
	if !ok {
		pattern = bayerPatterns["RGGB"]
	}
	maxInt := (1 << uint(bitDepth)) - 1
	maxVal := float64(maxInt)

	var sample func(x, y int) float64
	if bitDepth <= 8 {
		sample = func(x, y int) float64 {
			cx, cy := clampInt(x, 0, w-1), clampInt(y, 0, h-1)
			return float64(buf[cy*w+cx])
		}
	} else {
		// >8-bit dumps are conventionally stored unpacked in a 16-bit
		// little-endian container (RAW10/RAW12/RAW14/RAW16 all commonly ship
		// this way from ISP capture tools).
		sample = func(x, y int) float64 {
			cx, cy := clampInt(x, 0, w-1), clampInt(y, 0, h-1)
			o := (cy*w + cx) * 2
			return float64(uint16(buf[o]) | uint16(buf[o+1])<<8)
		}
	}
	colorAt := func(x, y int) byte {
		return pattern[mod2(y)][mod2(x)]
	}

	out := make([]byte, w*(rowEnd-rowStart)*4)
	for y := rowStart; y < rowEnd; y++ {
		for x := 0; x < w; x++ {
			c := colorAt(x, y)
			var r, g, b float64
			if c == 'G' {
				g = sample(x, y)
				// The color immediately to the left tells us this G site's
				// CFA orientation: that axis carries one color, the
				// perpendicular axis carries the other.
				horiz := colorAt(x-1, y)
				hVal := (sample(x-1, y) + sample(x+1, y)) / 2
				vVal := (sample(x, y-1) + sample(x, y+1)) / 2
				if horiz == 'R' {
					r, b = hVal, vVal
				} else {
					b, r = hVal, vVal
				}
			} else {
				own := sample(x, y)
				g = (sample(x-1, y) + sample(x+1, y) + sample(x, y-1) + sample(x, y+1)) / 4
				diag := (sample(x-1, y-1) + sample(x+1, y-1) + sample(x-1, y+1) + sample(x+1, y+1)) / 4
				if c == 'R' {
					r, b = own, diag
				} else {
					b, r = own, diag
				}
			}
			o := ((y-rowStart)*w + x) * 4
			out[o] = clampByte(r / maxVal * 255)
			out[o+1] = clampByte(g / maxVal * 255)
			out[o+2] = clampByte(b / maxVal * 255)
			out[o+3] = 255
		}
	}
	return out
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// mod2 mirrors JS's `x & 1` on a possibly-negative x: two's-complement AND
// gives the same parity as the non-negative case (e.g. -1 & 1 == 1).
func mod2(x int) int {
	return x & 1
}
