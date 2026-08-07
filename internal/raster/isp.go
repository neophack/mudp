package raster

import "math"

// srgbGammaLUT: precomputed sRGB gamma LUT (input 0..255 linear -> 0..255
// sRGB). Linear input below ~0.0031 is handled by the linear segment of the
// sRGB transfer function.
var srgbGammaLUT = func() [256]byte {
	var lut [256]byte
	for i := 0; i < 256; i++ {
		v := float64(i) / 255
		// Convert linear-light to sRGB-encoded. Values below the breakpoint
		// use a linear ramp; above it use the 2.4-power curve.
		var enc float64
		if v <= 0.0031308 {
			enc = v * 12.92
		} else {
			enc = 1.055*math.Pow(v, 1/2.4) - 0.055
		}
		lut[i] = clampByte(math.Round(enc * 255))
	}
	return lut
}()

// ApplyIsp transforms an RGBA buffer in place:
//
//  1. Auto white balance (gray-world): scale R/G/B so their channel averages
//     converge — removes the color cast.
//  2. sRGB gamma (OETF, gamma≈2.2): lifts dark mid-tones into the perceptual
//     range where the image looks naturally exposed instead of crushed.
//  3. Saturation boost: raw conversions come out muted; a gain on chroma
//     distance from the pixel's luma restores vivid color. saturation is a
//     multiplier around 1.0 (1.4 = +40% saturation).
func ApplyIsp(rgba []byte, saturation float64) {
	n := len(rgba) / 4
	if n == 0 {
		return
	}

	// --- Pass 1: accumulate channel sums for gray-world white balance. ---
	var sumR, sumG, sumB float64
	for p := 0; p < n; p++ {
		o := p * 4
		sumR += float64(rgba[o])
		sumG += float64(rgba[o+1])
		sumB += float64(rgba[o+2])
	}
	nf := float64(n)
	// Gray-world gains: scale each channel so its mean equals the overall
	// mean. Guard against divide-by-zero on a fully black frame (gains stay 1).
	avg := (sumR + sumG + sumB) / (3 * nf)
	if avg == 0 {
		avg = 1
	}
	gainR := avg / orOne(sumR/nf)
	gainG := avg / orOne(sumG/nf)
	gainB := avg / orOne(sumB/nf)

	// --- Pass 2: white balance -> gamma -> saturation, write back. ---
	for p := 0; p < n; p++ {
		o := p * 4
		r := clampByte(float64(rgba[o]) * gainR)
		g := clampByte(float64(rgba[o+1]) * gainG)
		b := clampByte(float64(rgba[o+2]) * gainB)
		rf := float64(srgbGammaLUT[r])
		gf := float64(srgbGammaLUT[g])
		bf := float64(srgbGammaLUT[b])
		if saturation != 1 {
			y := 0.299*rf + 0.587*gf + 0.114*bf
			rf = y + (rf-y)*saturation
			gf = y + (gf-y)*saturation
			bf = y + (bf-y)*saturation
		}
		rgba[o] = clampByte(rf)
		rgba[o+1] = clampByte(gf)
		rgba[o+2] = clampByte(bf)
		// alpha unchanged
	}
}

func orOne(v float64) float64 {
	if v == 0 {
		return 1
	}
	return v
}
