package raster

import "math"

// Mini ISP pipeline, ported from the reference capture tool's MiniIsp.cpp
// (D:\pqtools\rawcapture_imgui\src\MiniIsp.cpp, itself ported from an Android
// MiniIsp.java). Pure static computation, no state, no allocations beyond the
// working planes.
//
// Order: BLC black-level correction → edge-directed demosaic
// (Hamilton-Adams directional interpolation + chroma difference) → chroma
// noise reduction (chroma-difference plane low-pass) → white balance gains →
// 3x3 CCM → gamma (4096-entry LUT) → saturation/contrast/brightness → shadow
// chroma suppression → luma sharpening (multi-band unsharp mask with
// piecewise luma-dependent gain). Gray mode skips the demosaic and shows the
// raw Bayer directly.
//
// The default parameter values are the board-calibrated readings documented
// in MiniIsp.cpp's Params::reset(): they make a dump from the matching sensor
// preview close to the board's own YUV output. Other sensors still work —
// adjust the parameters or hit the gray-world AWB button.

// IspParams mirrors isp::Params from the reference implementation.
type IspParams struct {
	// Blc holds the four per-CFA-channel black levels: 0=R 1=Gr 2=Gb 3=B.
	// Defaults are the measured black floor at ISO2464 (see MiniIsp.cpp).
	Blc [4]int
	// GainR/GainB are the white balance gains (G channel fixed at 1.0).
	GainR float64
	GainB float64
	// Ccm is the 3x3 color correction matrix, row-major. Default is the
	// board's 4032K calibration (each row sums to 1.0).
	Ccm [9]float64
	// Gamma encoding exponent: output = pow(v, 1/gamma).
	Gamma float64
	// Saturation multiplies chroma distance from luma (1.0 = unchanged).
	Saturation float64
	// Contrast linearly stretches around 127.5 (1.0 = unchanged).
	Contrast float64
	// Brightness is an offset in the post-gamma 0..255 domain.
	Brightness float64
	// ChromaNrRadius is the box-blur radius (pixels) applied to the
	// chroma-difference planes; 0 disables. Smooths chroma the way the
	// board's chroma NR + 4:2:0 subsampling does.
	ChromaNrRadius int
	// EnableSharpen toggles the multi-band luma unsharp mask.
	EnableSharpen bool
	// Sharpen is the overall sharpen multiplier (0 = off).
	Sharpen float64
	// SharpenRadius is the mid-band unsharp base radius. The high band is a
	// fixed 3x3 and the low-mid band a fixed radius 25 (weight 0.3).
	SharpenRadius int
	// ChromaSuppLow: below this luma, color collapses toward neutral gray
	// (0 = disabled). Suppresses magenta/cyan mottle in deep shadows.
	ChromaSuppLow float64
	// MaxVal is the raw data peak (e.g. 1023 for 10-bit; 4095 when 12-bit
	// data rides in a 16-bit container).
	MaxVal int
	// GrayMode skips the demosaic and displays the raw Bayer as grayscale.
	GrayMode bool
}

// Board-calibrated default pieces (MiniIsp.cpp Params::reset).
var (
	// blcBoardDefault: R/Gr/Gb/B measured black floor — the G channels carry a
	// significant DC step at this ISO; subtracting only the nominal level
	// leaves a green residue in the shadows.
	blcBoardDefault = [4]int{39, 225, 225, 48}
	// CcmBoard4032K is the board's effective 4032K matrix (rows sum to 1.0).
	CcmBoard4032K = [9]float64{
		0.996, -0.078, 0.082,
		0.000, 1.066, -0.066,
		0.195, -0.598, 1.402,
	}
	// CcmIdentity leaves colors unchanged.
	CcmIdentity = [9]float64{
		1, 0, 0,
		0, 1, 0,
		0, 0, 1,
	}
	// CcmSrgbTypical is a typical sRGB-style CCM (rows sum ≈ 1).
	CcmSrgbTypical = [9]float64{
		1.66, -0.55, -0.11,
		-0.25, 1.45, -0.20,
		-0.07, -0.28, 1.35,
	}
	// CcmSaturated is a high-saturation enhancement CCM.
	CcmSaturated = [9]float64{
		1.85, -0.70, -0.15,
		-0.32, 1.62, -0.30,
		-0.10, -0.38, 1.48,
	}
)

// CcmPresetNames labels the selectable CCM presets; index 0 is "manual"
// (edit the 3x3 matrix directly).
var CcmPresetNames = []string{"Manual", "Identity", "Board 4032K", "sRGB typical", "High saturation"}

// CcmPreset returns the preset matrix for index (1..len-1). Index 0 (manual)
// returns ok=false.
func CcmPreset(index int) (m [9]float64, ok bool) {
	switch index {
	case 1:
		return CcmIdentity, true
	case 2:
		return CcmBoard4032K, true
	case 3:
		return CcmSrgbTypical, true
	case 4:
		return CcmSaturated, true
	}
	return CcmIdentity, false
}

// DefaultIspMaxVal returns (1<<bitDepth)-1, clamped to a sane bit depth.
func DefaultIspMaxVal(bitDepth int) int {
	if bitDepth < 1 {
		bitDepth = 1
	}
	if bitDepth > 16 {
		bitDepth = 16
	}
	return (1 << uint(bitDepth)) - 1
}

// DefaultIspParams returns the board-calibrated defaults (MiniIsp.cpp
// Params::reset) with MaxVal derived from the dump's bit depth.
func DefaultIspParams(bitDepth int) IspParams {
	return IspParams{
		Blc:            blcBoardDefault,
		GainR:          1.07,
		GainB:          1.12,
		Ccm:            CcmBoard4032K,
		Gamma:          2.27,
		Saturation:     1.15,
		Contrast:       1.22,
		Brightness:     0,
		ChromaNrRadius: 3,
		EnableSharpen:  true,
		Sharpen:        1.00,
		SharpenRadius:  5,
		ChromaSuppLow:  60.0,
		MaxVal:         DefaultIspMaxVal(bitDepth),
		GrayMode:       false,
	}
}

// ispGammaLUT builds the 4096-entry gamma table (output 0..255) mapping the
// normalized input 0..1 onto pow(v, 1/gamma).
func ispGammaLUT(gamma float64) [4096]int {
	if gamma <= 0.05 {
		gamma = 2.2
	}
	inv := 1 / gamma
	var lut [4096]int
	for i := 0; i < 4096; i++ {
		lut[i] = clampInt(int(math.Pow(float64(i)/4095.0, inv)*255.0+0.5), 0, 255)
	}
	return lut
}

// ispCfaTables resolves the 2x2-phase tables for a Bayer pattern key:
// channelIdx maps a CFA position to the Blc index (0=R 1=Gr 2=Gb 3=B) and
// color maps it to 0=R 1=G 2=B. Falls back to RGGB like BayerDecode.
type ispCfaTables struct {
	channel [4]int // index = (y&1)*2 + (x&1)
	color   [4]int
}

func ispCfa(patternKey string) ispCfaTables {
	tile, ok := bayerPatterns[patternKey]
	if !ok {
		tile = bayerPatterns["RGGB"]
	}
	var t ispCfaTables
	for y := 0; y < 2; y++ {
		for x := 0; x < 2; x++ {
			ph := y*2 + x
			switch tile[y][x] {
			case 'R':
				t.channel[ph] = 0
				t.color[ph] = 0
			case 'B':
				t.channel[ph] = 3
				t.color[ph] = 2
			default: // 'G': Gr when the row's other site is R, else Gb
				if tile[y][x^1] == 'R' {
					t.channel[ph] = 1
				} else {
					t.channel[ph] = 2
				}
				t.color[ph] = 1
			}
		}
	}
	return t
}

// ispSample reads one raw sample: 8-bit dumps pack a sample per byte, deeper
// dumps ship unpacked in a 16-bit little-endian container (same convention as
// BayerDecode / rawFrameSize).
func ispSample(buf []byte, bitDepth int, i int) float64 {
	if bitDepth <= 8 {
		return float64(buf[i])
	}
	return float64(uint16(buf[i*2]) | uint16(buf[i*2+1])<<8)
}

// boxBlurF32 is a separable box blur (edge-clamped) over a float32 plane. It
// is the low-pass base for both chroma NR and the sharpen bands.
func boxBlurF32(plane []float32, w, h, radius int) {
	if radius <= 0 || w <= 0 || h <= 0 {
		return
	}
	n := float32(2*radius + 1)
	tmp := make([]float32, len(plane))
	cl := func(v, hi int) int { return clampInt(v, 0, hi) }
	// Horizontal.
	for y := 0; y < h; y++ {
		row := y * w
		var acc float32
		for x := -radius; x <= radius; x++ {
			acc += plane[row+cl(x, w-1)]
		}
		for x := 0; x < w; x++ {
			tmp[row+x] = acc / n
			acc += plane[row+cl(x+radius+1, w-1)] - plane[row+cl(x-radius, w-1)]
		}
	}
	// Vertical, processed in column tiles: a pure column walk has stride-w
	// accesses and thrashes the cache on multi-megapixel frames, while a
	// 64-column tile keeps each row's working set in cache.
	const tile = 64
	for x0 := 0; x0 < w; x0 += tile {
		xEnd := x0 + tile
		if xEnd > w {
			xEnd = w
		}
		for x := x0; x < xEnd; x++ {
			var acc float32
			for y := -radius; y <= radius; y++ {
				acc += tmp[cl(y, h-1)*w+x]
			}
			for y := 0; y < h; y++ {
				plane[y*w+x] = acc / n
				acc += tmp[cl(y+radius+1, h-1)*w+x] - tmp[cl(y-radius, h-1)*w+x]
			}
		}
	}
}

// ProcessBayer runs the full ISP pipeline on one raw Bayer frame and returns
// RGBA bytes (R,G,B,A byte order, alpha 255). buf holds w*h samples (1 byte
// each for ≤8-bit dumps, 2 bytes LE otherwise). patternKey selects the CFA
// layout (RGGB/BGGR/GRBG/GBRG, default RGGB).
func ProcessBayer(buf []byte, w, h, bitDepth int, patternKey string, p IspParams) []byte {
	if w <= 0 || h <= 0 {
		return nil
	}
	samples := w * h
	if bitDepth <= 8 {
		if len(buf) < samples {
			return nil
		}
	} else if len(buf) < 2*samples {
		return nil
	}
	maxVal := p.MaxVal
	if maxVal <= 0 {
		maxVal = 1023
	}

	lut := ispGammaLUT(p.Gamma)
	cfa := ispCfa(patternKey)

	out := make([]byte, samples*4)
	invMax := 1.0 / float64(maxVal)
	gR, gB := float32(p.GainR), float32(p.GainB)
	var m [9]float32
	for i, v := range p.Ccm {
		m[i] = float32(v)
	}
	sat, contrast, bright := float32(p.Saturation), float32(p.Contrast), float32(p.Brightness)

	if p.GrayMode {
		// Grayscale direct view of the raw Bayer: BLC + gamma + brightness.
		for y := 0; y < h; y++ {
			ph0 := (y & 1) * 2
			row := y * w
			for x := 0; x < w; x++ {
				v := int(ispSample(buf, bitDepth, row+x)) - p.Blc[cfa.channel[ph0+(x&1)]]
				v = clampInt(v, 0, maxVal)
				idx := v * 4095 / maxVal
				if idx > 4095 {
					idx = 4095
				}
				g8 := clampInt(int((float32(lut[idx])-127.5)*contrast+127.5+bright), 0, 255)
				o := (row + x) * 4
				out[o] = byte(g8)
				out[o+1] = byte(g8)
				out[o+2] = byte(g8)
				out[o+3] = 255
			}
		}
		return out
	}

	// ---- Demosaic: edge-directed directional interpolation
	// (Hamilton-Adams) + chroma-difference interpolation ----
	//
	// The board ISP's NDDM outputs are sharp; a "pack 2x2 + bilinear 2x
	// upsample" approach halves every channel's resolution first and loses
	// all high-frequency detail. Instead: the green plane gets
	// gradient-directed interpolation with second-order correction (keeps
	// edge detail); R/B are recovered from "color minus green" difference
	// planes (the difference field is low-frequency, so edges stay sharp
	// with no zipper). Works for all four Bayer layouts; borders use
	// same-parity mirror extension so colors stay correct (simple clamping
	// would sample the wrong color at the edges and tint them).
	mirx := func(v int) int {
		if v < 0 {
			v = -v
		} else if v >= w {
			v = 2*(w-1) - v
		}
		return clampInt(v, 0, w-1)
	}
	miry := func(v int) int {
		if v < 0 {
			v = -v
		} else if v >= h {
			v = 2*(h-1) - v
		}
		return clampInt(v, 0, h-1)
	}

	// Phase neighborhood color table: for each 2x2 phase, the color of the
	// horizontal axis neighbor. The vertical neighbor is implied — at a G site
	// the two axes always carry the opposite colors of each other.
	var horizCol [4]int
	for py := 0; py < 2; py++ {
		for px := 0; px < 2; px++ {
			ph := py*2 + px
			horizCol[ph] = cfa.color[py*2+(px^1)]
		}
	}

	// BLC + clamp → corrected sample plane P (both interpolation passes read
	// the neighborhood repeatedly; precompute to avoid re-subtracting BLC).
	pPlane := make([]float32, samples)
	for y := 0; y < h; y++ {
		ph0 := (y & 1) * 2
		row := y * w
		for x := 0; x < w; x++ {
			v := int(ispSample(buf, bitDepth, row+x)) - p.Blc[cfa.channel[ph0+(x&1)]]
			pPlane[row+x] = float32(clampInt(v, 0, maxVal))
		}
	}

	// Pass 1: full green plane Gp. G sites keep their value; R/B sites
	// interpolate directionally with second-order correction.
	gp := make([]float32, samples)
	for y := 0; y < h; y++ {
		row := y * w
		ph0 := (y & 1) * 2
		for x := 0; x < w; x++ {
			i := row + x
			if cfa.color[ph0+(x&1)] == 1 {
				gp[i] = pPlane[i]
				continue
			}
			xm, xp := mirx(x-1), mirx(x+1)
			ym, yp := miry(y-1), miry(y+1)
			xm2, xp2 := mirx(x-2), mirx(x+2)
			ym2, yp2 := miry(y-2), miry(y+2)
			c := pPlane[i]
			gl, gr := pPlane[row+xm], pPlane[row+xp]
			gu, gd := pPlane[ym*w+x], pPlane[yp*w+x]
			// Second-order correction: the known channel's (R/B) axial
			// second derivative sharpens the green estimate at edges.
			gh := 0.5*(gl+gr) + 0.5*(c-0.5*(pPlane[row+xm2]+pPlane[row+xp2]))
			gv := 0.5*(gu+gd) + 0.5*(c-0.5*(pPlane[ym2*w+x]+pPlane[yp2*w+x]))
			dh := float32(math.Abs(float64(gl - gr)))
			dv := float32(math.Abs(float64(gu - gd)))
			switch {
			case dh < dv:
				gp[i] = gh
			case dv < dh:
				gp[i] = gv
			default:
				gp[i] = 0.5 * (gh + gv)
			}
		}
	}

	// Pass 2a: R/B chroma-difference interpolation, stored as full-res
	// difference planes Dr/Db (= reconstructed value - G). The difference
	// field is naturally low-frequency, so the later low-pass removes chroma
	// noise without touching luma detail.
	dr := make([]float32, samples)
	db := make([]float32, samples)
	for y := 0; y < h; y++ {
		row := y * w
		ph0 := (y & 1) * 2
		rowM := miry(y-1) * w
		rowP := miry(y+1) * w
		for x := 0; x < w; x++ {
			i := row + x
			ph := ph0 + (x & 1)
			cc := cfa.color[ph]
			g := gp[i]
			var rp, bp float32
			xm, xp := mirx(x-1), mirx(x+1)
			if cc == 1 {
				// G site: one axis carries R, the other B (phase decides).
				horizDiff := 0.5*(pPlane[row+xm]+pPlane[row+xp]) - 0.5*(gp[row+xm]+gp[row+xp])
				vertDiff := 0.5*(pPlane[rowM+x]+pPlane[rowP+x]) - 0.5*(gp[rowM+x]+gp[rowP+x])
				if horizCol[ph] == 0 {
					rp, bp = g+horizDiff, g+vertDiff
				} else {
					bp, rp = g+horizDiff, g+vertDiff
				}
			} else {
				// R/B site: own value kept; diagonal neighborhood is the
				// other color, recovered via chroma-difference interpolation.
				diagDiff := 0.25*(pPlane[rowM+xm]+pPlane[rowM+xp]+pPlane[rowP+xm]+pPlane[rowP+xp]) -
					0.25*(gp[rowM+xm]+gp[rowM+xp]+gp[rowP+xm]+gp[rowP+xp])
				if cc == 0 {
					rp, bp = pPlane[i], g+diagDiff
				} else {
					bp, rp = pPlane[i], g+diagDiff
				}
			}
			dr[i] = rp - g
			db[i] = bp - g
		}
	}

	// Pass 2b: chroma noise reduction (chroma-difference plane low-pass).
	boxBlurF32(dr, w, h, p.ChromaNrRadius)
	boxBlurF32(db, w, h, p.ChromaNrRadius)

	// Pass 2c: reconstruct R/B + WB + CCM + gamma + saturation/contrast/
	// brightness (per pixel).
	for y := 0; y < h; y++ {
		row := y * w
		for x := 0; x < w; x++ {
			i := row + x
			g := gp[i]
			rp := g + dr[i]
			bp := g + db[i]

			// Normalize to 0..1 + white balance gains.
			fr := rp * float32(invMax) * gR
			fg := g * float32(invMax)
			fb := bp * float32(invMax) * gB

			// 3x3 CCM.
			cr := m[0]*fr + m[1]*fg + m[2]*fb
			cg := m[3]*fr + m[4]*fg + m[5]*fb
			cb := m[6]*fr + m[7]*fg + m[8]*fb

			// Clamp 0..1 → gamma LUT.
			idx := int(clampf(cr, 0, 1) * 4095)
			r8 := lut[idx]
			idx = int(clampf(cg, 0, 1) * 4095)
			g8 := lut[idx]
			idx = int(clampf(cb, 0, 1) * 4095)
			b8 := lut[idx]

			// Saturation (around Rec.601 luma) + contrast (stretch around
			// 127.5) + brightness offset.
			if sat != 1 || contrast != 1 || bright != 0 {
				luma := 0.299*float32(r8) + 0.587*float32(g8) + 0.114*float32(b8)
				r8 = int((luma+(float32(r8)-luma)*sat-127.5)*contrast + 127.5 + bright)
				g8 = int((luma+(float32(g8)-luma)*sat-127.5)*contrast + 127.5 + bright)
				b8 = int((luma+(float32(b8)-luma)*sat-127.5)*contrast + 127.5 + bright)
			}

			// Shadow chroma suppression: the lower the luma, the more color
			// collapses toward neutral gray.
			if supp := float32(p.ChromaSuppLow); supp > 0 {
				luma := 0.299*float32(r8) + 0.587*float32(g8) + 0.114*float32(b8)
				if luma < supp {
					k := luma / supp
					r8 = int(luma + (float32(r8)-luma)*k)
					g8 = int(luma + (float32(g8)-luma)*k)
					b8 = int(luma + (float32(b8)-luma)*k)
				}
			}

			o := i * 4
			out[o] = byte(clampInt(r8, 0, 255))
			out[o+1] = byte(clampInt(g8, 0, 255))
			out[o+2] = byte(clampInt(b8, 0, 255))
			out[o+3] = 255
		}
	}

	ispSharpenRGBA(out, w, h, p)
	return out
}

// Piecewise luma-gain curves for the sharpen bands, fit against measured
// board YUV frames ("per-luma 5x5 local contrast" alignment): low gains in
// the shadows (avoid noise overshoot), strong gains in the mid-tones
// (restore texture), slightly reduced in highlights (avoid edge halos).
var (
	sharpenG1X = [7]float32{0, 40, 70, 100, 140, 180, 255}
	sharpenG1Y = [7]float32{0.10, 0.12, 0.90, 1.90, 2.40, 2.00, 1.40}
	sharpenG5X = [7]float32{0, 55, 85, 110, 140, 180, 255}
	sharpenG5Y = [7]float32{0.30, 0.50, 1.15, 1.60, 1.90, 1.70, 1.30}
)

func sharpenGainAt(xs, ys *[7]float32, v float32) float32 {
	if v <= xs[0] {
		return ys[0]
	}
	if v >= xs[6] {
		return ys[6]
	}
	for k := 1; k < 7; k++ {
		if v < xs[k] {
			t := (v - xs[k-1]) / (xs[k] - xs[k-1])
			return ys[k-1] + (ys[k]-ys[k-1])*t
		}
	}
	return ys[6]
}

// ispSharpenRGBA applies the multi-band luma unsharp mask in place on an RGBA
// buffer: detail = high-band(3x3)·g1(L) + (mid-band(sharpenRadius) +
// 0.3·low-mid-band(radius 25))·g5(L), scaled by p.Sharpen, added to every
// channel (luma-only addition avoids color fringing).
func ispSharpenRGBA(rgba []byte, w, h int, p IspParams) {
	if !p.EnableSharpen || p.Sharpen <= 0 || w < 3 || h < 3 {
		return
	}
	n := w * h
	if len(rgba) < n*4 {
		return
	}
	l := make([]float32, n)
	acc := make([]float32, n)
	band := make([]float32, n)
	for i := 0; i < n; i++ {
		o := i * 4
		l[i] = 0.299*float32(rgba[o]) + 0.587*float32(rgba[o+1]) + 0.114*float32(rgba[o+2])
	}
	// High band (3x3).
	copy(band, l)
	boxBlurF32(band, w, h, 1)
	for i := 0; i < n; i++ {
		acc[i] = (l[i] - band[i]) * sharpenGainAt(&sharpenG1X, &sharpenG1Y, l[i])
	}
	// Mid band (configurable radius) + low-mid band (radius 25, weight 0.3).
	radius := p.SharpenRadius
	if radius < 1 {
		radius = 1
	}
	copy(band, l)
	boxBlurF32(band, w, h, radius)
	for i := 0; i < n; i++ {
		acc[i] += (l[i] - band[i]) * sharpenGainAt(&sharpenG5X, &sharpenG5Y, l[i])
	}
	copy(band, l)
	boxBlurF32(band, w, h, 25)
	for i := 0; i < n; i++ {
		acc[i] += 0.3 * (l[i] - band[i]) * sharpenGainAt(&sharpenG5X, &sharpenG5Y, l[i])
	}
	for i := 0; i < n; i++ {
		d := acc[i] * float32(p.Sharpen)
		if d == 0 {
			continue
		}
		o := i * 4
		rgba[o] = byte(clampInt(int(float32(rgba[o])+d), 0, 255))
		rgba[o+1] = byte(clampInt(int(float32(rgba[o+1])+d), 0, 255))
		rgba[o+2] = byte(clampInt(int(float32(rgba[o+2])+d), 0, 255))
	}
}

// ApplyIspParams is the RGB-domain ISP subset for already-decoded frames
// (the YUV viewer path): white balance gains → 3x3 CCM → pow gamma →
// saturation/contrast/brightness → shadow chroma suppression → multi-band
// luma sharpen. Blc, GrayMode and ChromaNrRadius are Bayer-domain stages
// and are ignored here. The buffer is transformed in place.
func ApplyIspParams(rgba []byte, w, h int, p IspParams) {
	n := len(rgba) / 4
	if n == 0 {
		return
	}
	lut := ispGammaLUT(p.Gamma)
	m := &p.Ccm
	sat, contrast, bright := p.Saturation, p.Contrast, p.Brightness
	for i := 0; i < n; i++ {
		o := i * 4
		// Normalize 0..1 + white balance gains.
		fr := float64(rgba[o]) / 255 * p.GainR
		fg := float64(rgba[o+1]) / 255
		fb := float64(rgba[o+2]) / 255 * p.GainB

		// 3x3 CCM.
		cr := m[0]*fr + m[1]*fg + m[2]*fb
		cg := m[3]*fr + m[4]*fg + m[5]*fb
		cb := m[6]*fr + m[7]*fg + m[8]*fb

		r8 := lut[int(clampf(float32(cr), 0, 1)*4095)]
		g8 := lut[int(clampf(float32(cg), 0, 1)*4095)]
		b8 := lut[int(clampf(float32(cb), 0, 1)*4095)]

		if sat != 1 || contrast != 1 || bright != 0 {
			luma := 0.299*float64(r8) + 0.587*float64(g8) + 0.114*float64(b8)
			r8 = int((luma+(float64(r8)-luma)*sat-127.5)*contrast + 127.5 + bright)
			g8 = int((luma+(float64(g8)-luma)*sat-127.5)*contrast + 127.5 + bright)
			b8 = int((luma+(float64(b8)-luma)*sat-127.5)*contrast + 127.5 + bright)
		}
		if p.ChromaSuppLow > 0 {
			luma := 0.299*float64(r8) + 0.587*float64(g8) + 0.114*float64(b8)
			if luma < p.ChromaSuppLow {
				k := luma / p.ChromaSuppLow
				r8 = int(luma + (float64(r8)-luma)*k)
				g8 = int(luma + (float64(g8)-luma)*k)
				b8 = int(luma + (float64(b8)-luma)*k)
			}
		}

		rgba[o] = byte(clampInt(r8, 0, 255))
		rgba[o+1] = byte(clampInt(g8, 0, 255))
		rgba[o+2] = byte(clampInt(b8, 0, 255))
	}

	ispSharpenRGBA(rgba, w, h, p)
}

func clampf(v, lo, hi float32) float32 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// ComputeGrayWorldGains runs gray-world auto white balance over the raw
// Bayer frame: average the BLC-corrected R/G/B sites and return
// gainR=avgG/avgR, gainB=avgG/avgB (G gain fixed at 1.0). Sampling steps over
// aligned 2x2 blocks (whole CFA tiles, so all four phases contribute) and
// grows the stride until ~2M samples. Gains clamp to 0.1..8.
func ComputeGrayWorldGains(buf []byte, w, h, bitDepth int, patternKey string, p IspParams) (gainR, gainB float64) {
	gainR, gainB = 1, 1
	if w <= 0 || h <= 0 {
		return
	}
	samples := w * h
	if bitDepth <= 8 {
		if len(buf) < samples {
			return
		}
	} else if len(buf) < 2*samples {
		return
	}
	maxVal := p.MaxVal
	if maxVal <= 0 {
		maxVal = 1023
	}
	cfa := ispCfa(patternKey)

	step := 2
	for (w/step)*(h/step)*4 > 2_000_000 {
		step *= 2
	}
	var sumR, sumG, sumB float64
	var nR, nG, nB float64
	for y := 0; y+1 < h; y += step {
		for x := 0; x+1 < w; x += step {
			for dy := 0; dy < 2; dy++ {
				row := (y + dy) * w
				for dx := 0; dx < 2; dx++ {
					xx, yy := x+dx, y+dy
					v := int(ispSample(buf, bitDepth, row+xx)) - p.Blc[cfa.channel[(yy&1)*2+(xx&1)]]
					v = clampInt(v, 0, maxVal)
					switch cfa.color[(yy&1)*2+(xx&1)] {
					case 0:
						sumR += float64(v)
						nR++
					case 1:
						sumG += float64(v)
						nG++
					default:
						sumB += float64(v)
						nB++
					}
				}
			}
		}
	}
	if nR == 0 || nG == 0 || nB == 0 {
		return
	}
	avgR := sumR / nR
	avgG := sumG / nG
	avgB := sumB / nB
	gr := avgG / math.Max(avgR, 1e-3)
	gb := avgG / math.Max(avgB, 1e-3)
	return clampF64(gr, 0.1, 8), clampF64(gb, 0.1, 8)
}

// DefaultYuvIspParams returns the manual-mode defaults for the RGB-domain
// subset used on already-decoded (YUV) frames: neutral white balance, pow
// gamma 2.2, a slight saturation lift, sharpening on.
func DefaultYuvIspParams() IspParams {
	return IspParams{
		GainR:         1,
		GainB:         1,
		Ccm:           CcmIdentity,
		Gamma:         2.2,
		Saturation:    1.15,
		Contrast:      1,
		Brightness:    0,
		EnableSharpen: true,
		Sharpen:       1,
		SharpenRadius: 5,
	}
}

// GrayWorldGainsRGBA is ComputeGrayWorldGains for an already-decoded RGBA
// frame (the YUV viewer's AWB button): average the channels and return
// gainR=avgG/avgR, gainB=avgG/avgB, clamped to 0.1..8.
func GrayWorldGainsRGBA(rgba []byte) (gainR, gainB float64) {
	n := len(rgba) / 4
	if n == 0 {
		return 1, 1
	}
	var sumR, sumG, sumB float64
	for i := 0; i < n; i++ {
		o := i * 4
		sumR += float64(rgba[o])
		sumG += float64(rgba[o+1])
		sumB += float64(rgba[o+2])
	}
	nf := float64(n)
	avgR := sumR / nf
	avgG := sumG / nf
	avgB := sumB / nf
	// A fully black frame would divide by zero; mirror the raw-domain guard.
	if avgG < 1e-3 {
		return 1, 1
	}
	return clampF64(avgG/math.Max(avgR, 1e-3), 0.1, 8), clampF64(avgG/math.Max(avgB, 1e-3), 0.1, 8)
}

func clampF64(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
