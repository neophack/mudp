package raster

import (
	"math"
	"runtime"
	"sync"
)

// parallelFor splits [0,n) into one chunk per worker and runs f on them
// concurrently (the calling goroutine works too). Every stage fanned out here
// writes disjoint output ranges with a per-element computation that does not
// depend on the chunking, so results are bit-identical to the sequential
// loop — matching the reference's row-parallel thread pool while keeping the
// Go port deterministic.
func parallelFor(n int, f func(lo, hi int)) {
	workers := runtime.GOMAXPROCS(0)
	if workers > 16 {
		workers = 16 // the reference pool caps at 15; more only adds overhead
	}
	if workers < 2 || n < 4*workers {
		f(0, n)
		return
	}
	chunk := (n + workers - 1) / workers
	var wg sync.WaitGroup
	for lo := 0; lo < n; lo += chunk {
		hi := min(n, lo+chunk)
		wg.Add(1)
		go func(lo, hi int) {
			defer wg.Done()
			f(lo, hi)
		}(lo, hi)
	}
	wg.Wait()
}

// Mini ISP pipeline, ported from the reference capture tool's MiniIsp.cpp
// (D:\pqtools\rawcapture_imgui\src\MiniIsp.cpp, itself ported from an Android
// MiniIsp.java). Pure static computation. The arithmetic mirrors the reference
// operation-for-operation in float32 so a dump renders the same bytes here as
// in the reference viewer (the float math is ordered identically; only the
// libm primitives — log2/exp2/atan2/pow — may differ by an ulp between
// toolchains).
//
// Order: BLC black-level correction → edge-directed demosaic
// (Hamilton-Adams directional interpolation + chroma difference) → chroma
// noise reduction (chroma-difference plane low-pass) → ISP digital gain +
// white balance gains → 3x3 CCM → local tone mapping DRC (log-luminance
// base/detail decomposition) → gamma (4096-entry LUT) → saturation/contrast/
// brightness → shadow chroma suppression → luma noise reduction (guided
// filter) → luma sharpening (multi-band unsharp mask with piecewise
// luma-dependent gain) → per-luma channel tint → board chroma map (YCbCr
// domain, trilinear over hue/chroma-magnitude/luma).
//
// The default parameters are the board-calibrated fits documented in
// MiniIsp.cpp: they render a dump from the matching sensor close to the
// board's own YUV output, so the RAW view and the board YUV view of the same
// capture match. Parameters that vary with gain come from the ISO-tier table
// (ApplyIsoTuning).

// IspParams mirrors isp::Params from the reference implementation. Scalar
// fields are float64 for a natural Go API; the pipeline converts each to
// float32 once and does all arithmetic there, exactly where the reference
// (whose fields are C float) does.
type IspParams struct {
	// Blc holds the four per-CFA-channel black levels: 0=R 1=Gr 2=Gb 3=B.
	// Default 0: the board dumps RAW after BLC (BlackLevel_*=0 in the .txt).
	Blc [4]int
	// GainR/GainB are the white balance gains (G channel fixed at 1.0).
	GainR float64
	GainB float64
	// AwbBiasR/B multiply the gray-world gains in AutoWhiteBalance (the
	// board's AWB preference per ISO tier: bright scenes run bluish, extreme
	// dark warm).
	AwbBiasR float64
	AwbBiasB float64
	// Ccm is the 3x3 color correction matrix, row-major. The default is the
	// fit against the board's YUV chroma (rows sum to 1.0).
	Ccm [9]float64
	// Gamma encoding exponent: output = pow(v, 1/gamma).
	Gamma float64
	// Saturation multiplies chroma distance from luma (1.0 = unchanged). The
	// fitted CCM deliberately compresses the red-cyan axis; the ~2.2 default
	// restores the board's "strong blue-yellow, weak red-cyan" chroma.
	Saturation float64
	// Contrast linearly stretches around 127.5 (1.0 = unchanged).
	Contrast float64
	// Brightness is an offset in the post-gamma 0..255 domain.
	Brightness float64
	// DigitalGain is the ISP digital gain (linear-domain multiplier, before
	// WB). The board dumps RAW before its ISP_D_Gain stage (up to 5x on dark
	// high-ISO frames); the viewer applies the frame's ISP_D_Gain_0/1024 from
	// the .txt sidecar.
	DigitalGain float64
	// Local tone mapping DRC (the board's strong local tone mapping: large
	// bright regions pushed down, shadows up, local texture kept). Decomposes
	// the CCM-output linear luma in the log2 domain into base (edge-aware
	// guided-filter blur) and detail:
	//
	//	log2 L' = pivot + (base - pivot)·compress + (log2 L - base)·detail
	//
	// and rescales RGB by L'/L. Compress=1 and detail=1 is a no-op.
	EnableDrc   bool
	DrcCompress float64
	DrcDetail   float64
	DrcRadius   int     // base blur radius in pixels
	DrcPivot    float64 // compression pivot, linear luma 0..1
	DrcEdge     float64 // base edge-preservation threshold (log2 luma)
	// LumaNr is the luma noise-reduction threshold σ (8-bit units, guided
	// filter); 0 disables. High-ISO single frames are extremely noisy and the
	// digital gain + DRC would amplify the noise into a grainy mess.
	LumaNr       float64
	LumaNrRadius int
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
	// (0 = disabled). Suppressed by shadow-chroma handling in the chroma map;
	// the tiers all carry 0.
	ChromaSuppLow float64
	// LumaTint is a per-luma additive channel calibration (15 values = 3
	// channels R/G/B x 5 nodes at Rec601 luma 0/64/128/192/255, linearly
	// interpolated), applied in the final 8-bit domain. UseLumaTint=false
	// disables. No tier uses it (kept for manual tuning parity with the
	// reference).
	UseLumaTint bool
	LumaTint    [15]float64
	// ChromaMap is the board's chroma mapping (final 8-bit YCbCr domain,
	// after lumaTint): trilinear interpolation over hue (CmapHues bins) x
	// chroma magnitude (CmapMag nodes, dense near 0) x luma (CmapLuma nodes)
	// yields a 2x2 rotate-scale (a, b):
	//
	//	Cb' = a·Cb - b·Cr + oCb(Y), Cr' = b·Cb + a·Cr + oCr(Y), Y' = Y + oY(Y)
	//
	// This expresses the board's nonlinear color handling — near-neutral
	// chroma (noise) pushed to gray while high-chroma objects stay vivid with
	// per-hue correction — which no global CCM/saturation can. Populated by
	// ApplyIsoTuning; UseChromaMap=false disables. Layout: a[h][m][l],
	// b[h][m][l], then oCb[l], oCr[l], oY[l].
	UseChromaMap bool
	ChromaMap    [975]float64
	// MaxVal is the raw data peak (e.g. 1023 for 10-bit; 4095 when 12-bit
	// data rides in a 16-bit container).
	MaxVal int
	// GrayMode skips the demosaic and displays the raw Bayer as grayscale.
	GrayMode bool
}

// Chroma map layout constants (see IspParams.ChromaMap).
const (
	CmapHues = 12
	CmapMag  = 8
	CmapLuma = 5
	CmapNG   = CmapHues * CmapMag * CmapLuma
	CmapSize = 2*CmapNG + 3*CmapLuma
)

// CmapMagNodes are the chroma-magnitude interpolation nodes (Cb/Cr 8-bit
// units, dense near 0 where the board's desaturation acts); CmapLumaNodes
// are the luma nodes (Rec601 8-bit).
var (
	CmapMagNodes  = [CmapMag]float32{0, 3, 6, 10, 16, 25, 40, 64}
	CmapLumaNodes = [CmapLuma]float32{0, 32, 64, 128, 255}
)

// Board-calibrated CCM fits (MiniIsp.cpp): ccmFit is the global default
// (reset), the per-tier matrices are the fit_tier results used before the
// chroma map stage.
var (
	CcmFit = [9]float64{
		0.7742, 0.4423, -0.2165,
		0.0864, 0.9833, -0.0697,
		-0.0356, 0.2751, 0.7605,
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

	ccm1500   = [9]float32{0.6042, 0.6973, -0.3015, 0.2464, 0.9258, -0.1722, 0.0450, 0.0775, 0.8775}
	ccm4651   = [9]float32{0.6942, 0.5773, -0.2715, 0.2789, 0.8883, -0.1672, -0.1231, 0.2751, 0.8480}
	ccm18305  = [9]float32{0.5867, 0.4773, -0.0640, 0.1939, 0.9583, -0.1522, -0.1831, 0.2751, 0.9080}
	ccm80512  = [9]float32{0.5417, 0.6223, -0.1640, 0.2639, 0.9333, -0.1972, -0.2406, 0.1226, 1.1180}
	ccm155629 = [9]float32{0.6017, 0.8373, -0.4390, 0.3164, 1.0158, -0.3322, -0.6356, -0.2924, 1.9280}
	ccm200668 = [9]float32{0.6042, 0.7473, -0.3515, 0.3689, 0.9208, -0.2897, -0.6356, -0.0899, 1.7255}
	ccm250554 = [9]float32{0.3542, 0.6748, -0.0290, 0.4814, 0.7933, -0.2747, -0.6356, 0.1276, 1.5080}
	ccm400945 = [9]float32{0.4917, 0.5423, -0.0340, 0.5114, 0.6083, -0.1197, -0.4581, 0.8226, 0.6355}
)

// CcmPresetNames labels the selectable CCM presets; index 0 is "manual"
// (edit the 3x3 matrix directly). Mirrors isp::CCM_PRESET_NAMES.
var CcmPresetNames = []string{"Manual", "Identity", "sRGB typical", "High saturation"}

// CcmPreset returns the preset matrix for index (1..len-1). Index 0 (manual)
// returns ok=false.
func CcmPreset(index int) (m [9]float64, ok bool) {
	switch index {
	case 1:
		return CcmIdentity, true
	case 2:
		return CcmSrgbTypical, true
	case 3:
		return CcmSaturated, true
	}
	return CcmIdentity, false
}

// IsoTuning is one ISO tier of the board's per-gain parameter table
// (isp::IsoTuning). The scalars interpolate (log2 ISO linear) between tiers;
// Ccm/ChromaMap/LumaTint are per-tier tables.
type IsoTuning struct {
	Iso                                         int64
	DrcCompress, DrcDetail, Sharpen, Saturation float32
	LumaNr                                      float32
	ChromaNrRadius                              int
	Brightness, AwbBiasR, AwbBiasB              float32
	ChromaSuppLow                               float32
	// Ccm is the tier's CCM (nil = keep the global reset matrix).
	Ccm *[9]float32
	// DrcRadius is the tier's DRC base radius (32; the ISO1500 session used
	// 96 — its WDR worked at a larger scale).
	DrcRadius int
	// LumaTint is the tier's per-luma tint (nil = off).
	LumaTint *[15]float32
	// ChromaMap is the tier's chroma map (nil = off).
	ChromaMap *[975]float32
}

// isoTuning is the per-ISO-tier fit (2026-10-31 refit; see MiniIsp.cpp's
// ISO_TUNING comment for the fitting methodology and residual errors).
var isoTuning = []IsoTuning{
	{Iso: 1500, DrcCompress: 0.891, DrcDetail: 1.130, Sharpen: 1.18, Saturation: 2.430, LumaNr: 0.1, ChromaNrRadius: 5, Brightness: -1.42, AwbBiasR: 0.996, AwbBiasB: 0.987, ChromaSuppLow: 0, Ccm: &ccm1500, DrcRadius: 96, ChromaMap: &chromaMap1500},
	{Iso: 4651, DrcCompress: 1.335, DrcDetail: 1.620, Sharpen: 0.78, Saturation: 2.504, LumaNr: 4.9, ChromaNrRadius: 4, Brightness: 0.96, AwbBiasR: 1.003, AwbBiasB: 1.184, ChromaSuppLow: 0, Ccm: &ccm4651, DrcRadius: 32, ChromaMap: &chromaMap4651},
	{Iso: 18305, DrcCompress: 1.217, DrcDetail: 1.588, Sharpen: 0.69, Saturation: 2.108, LumaNr: 10.5, ChromaNrRadius: 4, Brightness: 0.63, AwbBiasR: 1.018, AwbBiasB: 1.242, ChromaSuppLow: 0, Ccm: &ccm18305, DrcRadius: 32, ChromaMap: &chromaMap18305},
	{Iso: 80512, DrcCompress: 0.694, DrcDetail: 0.847, Sharpen: 0.98, Saturation: 1.086, LumaNr: 16.5, ChromaNrRadius: 5, Brightness: -9.24, AwbBiasR: 1.159, AwbBiasB: 1.166, ChromaSuppLow: 0, Ccm: &ccm80512, DrcRadius: 32, ChromaMap: &chromaMap80512},
	{Iso: 155629, DrcCompress: 0.646, DrcDetail: 0.727, Sharpen: 0.48, Saturation: 0.643, LumaNr: 19.2, ChromaNrRadius: 6, Brightness: -12.29, AwbBiasR: 1.163, AwbBiasB: 1.111, ChromaSuppLow: 0, Ccm: &ccm155629, DrcRadius: 32, ChromaMap: &chromaMap155629},
	{Iso: 200668, DrcCompress: 0.643, DrcDetail: 0.690, Sharpen: 0.32, Saturation: 0.607, LumaNr: 20.3, ChromaNrRadius: 6, Brightness: -12.28, AwbBiasR: 1.168, AwbBiasB: 1.064, ChromaSuppLow: 0, Ccm: &ccm200668, DrcRadius: 32, ChromaMap: &chromaMap200668},
	{Iso: 250554, DrcCompress: 0.605, DrcDetail: 0.605, Sharpen: 0.22, Saturation: 0.652, LumaNr: 21.2, ChromaNrRadius: 6, Brightness: -11.32, AwbBiasR: 1.148, AwbBiasB: 1.064, ChromaSuppLow: 0, Ccm: &ccm250554, DrcRadius: 32, ChromaMap: &chromaMap250554},
	{Iso: 400945, DrcCompress: 0.783, DrcDetail: 0.455, Sharpen: 0.07, Saturation: 0.779, LumaNr: 21.4, ChromaNrRadius: 6, Brightness: -3.72, AwbBiasR: 1.181, AwbBiasB: 1.103, ChromaSuppLow: 0, Ccm: &ccm400945, DrcRadius: 32, ChromaMap: &chromaMap400945},
}

// ApplyIsoTuning interpolates the ISO-tier table into p (isp::applyIsoTuning):
// the scalars interpolate linearly in log2 ISO between the surrounding tiers,
// clamped to the end tiers; iso <= 0 leaves p untouched. The per-tier CCM
// interpolates when both ends carry one, otherwise the nearer end's wins; the
// chroma map and luma tint treat a missing end as identity/zero and always
// overwrite (so no stale table survives a reload).
func ApplyIsoTuning(p *IspParams, iso int64) {
	if iso <= 0 {
		return
	}
	n := len(isoTuning)
	a, b := &isoTuning[0], &isoTuning[0]
	t := float32(0)
	if iso >= isoTuning[n-1].Iso {
		a, b = &isoTuning[n-1], &isoTuning[n-1]
	} else if iso > isoTuning[0].Iso {
		for k := 1; k < n; k++ {
			if iso <= isoTuning[k].Iso {
				a, b = &isoTuning[k-1], &isoTuning[k]
				t = float32((math.Log2(float64(iso)) - math.Log2(float64(a.Iso))) /
					(math.Log2(float64(b.Iso)) - math.Log2(float64(a.Iso))))
				break
			}
		}
	}
	lerp := func(x, y float32) float32 { return x + (y-x)*t }
	p.DrcCompress = float64(lerp(a.DrcCompress, b.DrcCompress))
	p.DrcDetail = float64(lerp(a.DrcDetail, b.DrcDetail))
	p.Sharpen = float64(lerp(a.Sharpen, b.Sharpen))
	p.Saturation = float64(lerp(a.Saturation, b.Saturation))
	p.LumaNr = float64(lerp(a.LumaNr, b.LumaNr))
	p.ChromaNrRadius = lroundF(lerp(float32(a.ChromaNrRadius), float32(b.ChromaNrRadius)))
	p.Brightness = float64(lerp(a.Brightness, b.Brightness))
	p.AwbBiasR = float64(lerp(a.AwbBiasR, b.AwbBiasR))
	p.AwbBiasB = float64(lerp(a.AwbBiasB, b.AwbBiasB))
	p.ChromaSuppLow = float64(lerp(a.ChromaSuppLow, b.ChromaSuppLow))
	p.DrcRadius = lroundF(lerp(float32(a.DrcRadius), float32(b.DrcRadius)))
	// Tier CCM: interpolate when both ends carry one, else the nearer end's
	// (exactly at the b endpoint, b's wins; if b has none the global matrix
	// stays).
	switch {
	case a.Ccm != nil && b.Ccm != nil:
		if a.Ccm == b.Ccm || t >= 1 {
			src := a.Ccm
			if t >= 1 && a.Ccm != b.Ccm {
				src = b.Ccm
			}
			for i := range p.Ccm {
				p.Ccm[i] = float64(src[i])
			}
		} else {
			for i := range p.Ccm {
				p.Ccm[i] = float64(lerp(a.Ccm[i], b.Ccm[i]))
			}
		}
	default:
		var src *[9]float32
		if t >= 1 {
			src = b.Ccm
		} else if t < 0.5 {
			if a.Ccm != nil {
				src = a.Ccm
			} else {
				src = b.Ccm
			}
		} else if b.Ccm != nil {
			src = b.Ccm
		} else {
			src = a.Ccm
		}
		if src != nil {
			for i := range p.Ccm {
				p.Ccm[i] = float64(src[i])
			}
		}
	}
	// Chroma map: a missing end is the identity (a=1, rest 0); always
	// overwrite so nothing survives from a previous file.
	p.UseChromaMap = a.ChromaMap != nil || b.ChromaMap != nil
	for i := 0; i < CmapSize; i++ {
		id := float32(0)
		if i < CmapNG {
			id = 1
		}
		x, y := id, id
		if a.ChromaMap != nil {
			x = a.ChromaMap[i]
		}
		if b.ChromaMap != nil {
			y = b.ChromaMap[i]
		}
		p.ChromaMap[i] = float64(lerp(x, y))
	}
	// Luma tint: a missing end is all zeros; always overwrite.
	p.UseLumaTint = a.LumaTint != nil || b.LumaTint != nil
	for i := 0; i < 15; i++ {
		var x, y float32
		if a.LumaTint != nil {
			x = a.LumaTint[i]
		}
		if b.LumaTint != nil {
			y = b.LumaTint[i]
		}
		p.LumaTint[i] = float64(lerp(x, y))
	}
}

// lroundF rounds half away from zero (C++ std::lround).
func lroundF(v float32) int {
	return int(math.Round(float64(v)))
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
// Params::reset) with MaxVal derived from the dump's bit depth: BLC 0 (the
// board dumps after BLC), gamma 1.43 + contrast 0.985 (joint fit with DRC),
// the fitted CCM, and the first ISO tier's compress/detail/sharpen/
// saturation/noise parameters.
func DefaultIspParams(bitDepth int) IspParams {
	t := &isoTuning[0]
	return IspParams{
		Blc:            [4]int{0, 0, 0, 0},
		GainR:          1.51,
		GainB:          1.69,
		AwbBiasR:       float64(t.AwbBiasR),
		AwbBiasB:       float64(t.AwbBiasB),
		Ccm:            CcmFit,
		Gamma:          1.43,
		Saturation:     float64(t.Saturation),
		Contrast:       0.985,
		Brightness:     float64(t.Brightness),
		DigitalGain:    1,
		EnableDrc:      true,
		DrcCompress:    float64(t.DrcCompress),
		DrcDetail:      float64(t.DrcDetail),
		DrcRadius:      t.DrcRadius,
		DrcPivot:       0.214,
		DrcEdge:        4.0,
		LumaNr:         float64(t.LumaNr),
		LumaNrRadius:   4,
		ChromaNrRadius: t.ChromaNrRadius,
		EnableSharpen:  true,
		Sharpen:        float64(t.Sharpen),
		SharpenRadius:  5,
		ChromaSuppLow:  float64(t.ChromaSuppLow),
		MaxVal:         DefaultIspMaxVal(bitDepth),
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
		lut[i] = min(255, max(0, int(math.Pow(float64(i)/4095.0, inv)*255.0+0.5)))
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
func ispSample(buf []byte, bitDepth int, i int) int {
	if bitDepth <= 8 {
		return int(buf[i])
	}
	return int(uint16(buf[i*2]) | uint16(buf[i*2+1])<<8)
}

// boxBlur is a separable box blur (edge-clamped) over a float32 plane, the
// low-pass base for the chroma NR, the guided filter and the sharpen bands.
// tmp must be at least len(plane); plane is blurred in place.
func boxBlur(plane, tmp []float32, w, h, radius int) {
	if radius <= 0 || w <= 0 || h <= 0 {
		return
	}
	n := float32(2*radius + 1)
	cl := func(v, hi int) int { return min(hi, max(0, v)) }
	// Horizontal.
	parallelFor(h, func(y0, y1 int) {
		for y := y0; y < y1; y++ {
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
	})
	// Vertical, processed in column bands (cache-friendly; each band's
	// running sums are identical to a pure column walk).
	const band = 64
	bands := (w + band - 1) / band
	parallelFor(bands, func(b0, b1 int) {
		var acc [band]float32
		for b := b0; b < b1; b++ {
			x0 := b * band
			xEnd := min(w, x0+band)
			bw := xEnd - x0
			for k := range acc {
				acc[k] = 0
			}
			for y := -radius; y <= radius; y++ {
				row := cl(y, h-1) * w
				for k := 0; k < bw; k++ {
					acc[k] += tmp[row+x0+k]
				}
			}
			for y := 0; y < h; y++ {
				dst := y*w + x0
				add := cl(y+radius+1, h-1)*w + x0
				sub := cl(y-radius, h-1)*w + x0
				for k := 0; k < bw; k++ {
					plane[dst+k] = acc[k] / n
					acc[k] += tmp[add+k] - tmp[sub+k]
				}
			}
		}
	})
}

// guidedSelf is an edge-preserving smoothing filter (He et al. guided filter
// with the guide being the input itself). Flat areas (local variance << eps)
// approach the radius-mean; strong edges (variance >> eps) are kept, so the
// blur does not bleed across them. Used for the DRC base (a plain box blur
// there leaves visible halos around bright objects) and the luma NR. mean,
// corr and tmp are scratch planes of len(plane).
func guidedSelf(plane []float32, w, h, radius int, eps float32, mean, corr, tmp []float32) {
	if radius <= 0 {
		return
	}
	copy(mean, plane)
	parallelFor(len(plane), func(lo, hi int) {
		for i := lo; i < hi; i++ {
			corr[i] = plane[i] * plane[i]
		}
	})
	boxBlur(mean, tmp, w, h, radius)
	boxBlur(corr, tmp, w, h, radius)
	// a = var/(var+eps), b = mean - a·mean; reuse mean/corr to hold a/b.
	parallelFor(len(mean), func(lo, hi int) {
		for i := lo; i < hi; i++ {
			m := mean[i]
			v := corr[i] - m*m
			if v < 0 {
				v = 0
			}
			a := v / (v + eps)
			mean[i] = a
			corr[i] = m - a*m
		}
	})
	boxBlur(mean, tmp, w, h, radius)
	boxBlur(corr, tmp, w, h, radius)
	parallelFor(len(plane), func(lo, hi int) {
		for i := lo; i < hi; i++ {
			plane[i] = mean[i]*plane[i] + corr[i]
		}
	})
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
	invMax := float32(1) / float32(maxVal)
	lutScale := float32(4095) / float32(maxVal)
	dgain := float32(1)
	if p.DigitalGain > 0 {
		dgain = float32(p.DigitalGain)
	}
	gR, gB := float32(p.GainR), float32(p.GainB)
	var m [9]float32
	for i, v := range p.Ccm {
		m[i] = float32(v)
	}
	sat, contrast, bright := float32(p.Saturation), float32(p.Contrast), float32(p.Brightness)

	if p.GrayMode {
		// Grayscale direct view of the raw Bayer: BLC + gamma + brightness.
		parallelFor(h, func(y0, y1 int) {
			for y := y0; y < y1; y++ {
				ph0 := (y & 1) * 2
				row := y * w
				for x := 0; x < w; x++ {
					v := ispSample(buf, bitDepth, row+x) - p.Blc[cfa.channel[ph0+(x&1)]]
					v = min(maxVal, max(0, v))
					idx := int(float32(v) * lutScale)
					if idx > 4095 {
						idx = 4095
					}
					g8 := int((float32(lut[idx])-127.5)*contrast + 127.5 + bright)
					g8 = min(255, max(0, g8))
					o := (row + x) * 4
					out[o] = byte(g8)
					out[o+1] = byte(g8)
					out[o+2] = byte(g8)
					out[o+3] = 255
				}
			}
		})
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
	// with no zipper). Borders use same-parity mirror extension so colors
	// stay correct (simple clamping would sample the wrong color at the
	// edges and tint them).
	mirx := func(v int) int {
		if v < 0 {
			v = -v
		} else if v >= w {
			v = 2*(w-1) - v
		}
		return min(w-1, max(0, v))
	}
	miry := func(v int) int {
		if v < 0 {
			v = -v
		} else if v >= h {
			v = 2*(h-1) - v
		}
		return min(h-1, max(0, v))
	}

	// Phase neighborhood color table: for each 2x2 phase, the color of the
	// horizontal axis neighbor.
	var horizCol [4]int
	for py := 0; py < 2; py++ {
		for px := 0; px < 2; px++ {
			ph := py*2 + px
			horizCol[ph] = cfa.color[py*2+(px^1)]
		}
	}

	// BLC + clamp → corrected sample plane pPlane (both interpolation passes
	// read the neighborhood repeatedly; precompute to avoid re-subtracting
	// BLC).
	pPlane := make([]float32, samples)
	parallelFor(h, func(y0, y1 int) {
		for y := y0; y < y1; y++ {
			ph0 := (y & 1) * 2
			row := y * w
			for x := 0; x < w; x++ {
				v := ispSample(buf, bitDepth, row+x) - p.Blc[cfa.channel[ph0+(x&1)]]
				pPlane[row+x] = float32(min(maxVal, max(0, v)))
			}
		}
	})

	// Pass 1: full green plane gp. G sites keep their value; R/B sites
	// interpolate directionally with second-order correction.
	gp := make([]float32, samples)
	parallelFor(h, func(y0, y1 int) {
		for y := y0; y < y1; y++ {
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
				gh := float32(0.5)*(gl+gr) + float32(0.5)*(c-float32(0.5)*(pPlane[row+xm2]+pPlane[row+xp2]))
				gv := float32(0.5)*(gu+gd) + float32(0.5)*(c-float32(0.5)*(pPlane[ym2*w+x]+pPlane[yp2*w+x]))
				dh := faAbs(gl - gr)
				dv := faAbs(gu - gd)
				switch {
				case dh < dv:
					gp[i] = gh
				case dv < dh:
					gp[i] = gv
				default:
					gp[i] = float32(0.5) * (gh + gv)
				}
			}
		}
	})

	// Pass 2a: R/B chroma-difference interpolation, stored as full-res
	// difference planes dr/db (= reconstructed value - G). The difference
	// field is naturally low-frequency, so the later low-pass removes chroma
	// noise without touching luma detail.
	dr := make([]float32, samples)
	db := make([]float32, samples)
	parallelFor(h, func(y0, y1 int) {
		for y := y0; y < y1; y++ {
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
					horizDiff := float32(0.5)*(pPlane[row+xm]+pPlane[row+xp]) - float32(0.5)*(gp[row+xm]+gp[row+xp])
					vertDiff := float32(0.5)*(pPlane[rowM+x]+pPlane[rowP+x]) - float32(0.5)*(gp[rowM+x]+gp[rowP+x])
					if horizCol[ph] == 0 {
						rp, bp = g+horizDiff, g+vertDiff
					} else {
						bp, rp = g+horizDiff, g+vertDiff
					}
				} else {
					// R/B site: own value kept; diagonal neighborhood is the
					// other color, recovered via chroma-difference interpolation.
					diagDiff := float32(0.25)*(pPlane[rowM+xm]+pPlane[rowM+xp]+pPlane[rowP+xm]+pPlane[rowP+xp]) -
						float32(0.25)*(gp[rowM+xm]+gp[rowM+xp]+gp[rowP+xm]+gp[rowP+xp])
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
	})

	// Pass 2b: chroma noise reduction (chroma-difference plane low-pass).
	tmp := make([]float32, samples)
	boxBlur(dr, tmp, w, h, p.ChromaNrRadius)
	boxBlur(db, tmp, w, h, p.ChromaNrRadius)

	// Reconstruct R/B + digital gain + WB + CCM → linear 0..1. The DRC
	// statistics and Pass 2c share this so the two stay consistent.
	nr := invMax * dgain * gR
	ng := invMax * dgain
	nb := invMax * dgain * gB
	linearRgb := func(i int) (cr, cg, cb float32) {
		g := gp[i]
		fr := (g + dr[i]) * nr
		fg := g * ng
		fb := (g + db[i]) * nb
		cr = m[0]*fr + m[1]*fg + m[2]*fb
		cg = m[3]*fr + m[4]*fg + m[5]*fb
		cb = m[6]*fr + m[7]*fg + m[8]*fb
		return
	}

	// DRC base: log2 linear luma smoothed with the edge-aware guided filter
	// (a plain box-blur base would darken a ring around every bright object).
	drc := p.EnableDrc && (p.DrcCompress != 1 || p.DrcDetail != 1)
	drcPivotL := log2F32(float32(max(1e-4, p.DrcPivot)))
	var drcL, drcB []float32
	if drc {
		drcL = make([]float32, samples)
		parallelFor(samples, func(lo, hi int) {
			for i := lo; i < hi; i++ {
				cr, cg, cb := linearRgb(i)
				l := float32(0.299)*cr + float32(0.587)*cg + float32(0.114)*cb
				if l < 1e-4 {
					l = 1e-4
				}
				drcL[i] = log2F32(l)
			}
		})
		drcB = make([]float32, samples)
		copy(drcB, drcL)
		rad := p.DrcRadius
		if rad <= 0 {
			rad = 1
		}
		e := float32(1e-3)
		if p.DrcEdge > 0 {
			e = float32(p.DrcEdge)
		}
		gfA := make([]float32, samples)
		gfB := make([]float32, samples)
		guidedSelf(drcB, w, h, rad, e*e, gfA, gfB, tmp)
	}

	// Pass 2c: reconstruct R/B + WB + CCM + DRC + gamma + saturation/
	// contrast/brightness (per pixel).
	drcCompress, drcDetail := float32(p.DrcCompress), float32(p.DrcDetail)
	chromaSuppLow := float32(p.ChromaSuppLow)
	parallelFor(h, func(y0, y1 int) {
		for y := y0; y < y1; y++ {
			row := y * w
			for x := 0; x < w; x++ {
				i := row + x
				cr, cg, cb := linearRgb(i)

				if drc {
					l, b := drcL[i], drcB[i]
					lo := drcPivotL + (b-drcPivotL)*drcCompress + (l-b)*drcDetail
					k := exp2F32(lo - l)
					cr *= k
					cg *= k
					cb *= k
				}

				// Out-of-gamut handling that preserves luma: when any channel
				// falls below 0 (or above 1), shrink toward the pixel's luma
				// until it lands back in range instead of clamping each channel
				// independently. The chroma low-pass pushes dark-edge R/B
				// negative (RAW dark noise is cut at 0); per-channel clamping
				// would leave only G — single-pixel green dots (very visible
				// once DRC lifts the shadows). Same for clipped highlights.
				{
					lum := float32(0.299)*cr + float32(0.587)*cg + float32(0.114)*cb
					mn := min(cr, min(cg, cb))
					mx := max(cr, max(cg, cb))
					if lum <= 0 {
						cr, cg, cb = 0, 0, 0
					} else if lum >= 1 {
						cr, cg, cb = 1, 1, 1
					} else if mn < 0 || mx > 1 {
						t := float32(1)
						if mn < 0 {
							t = min(t, lum/(lum-mn))
						}
						if mx > 1 {
							t = min(t, (1-lum)/(mx-lum))
						}
						cr = lum + (cr-lum)*t
						cg = lum + (cg-lum)*t
						cb = lum + (cb-lum)*t
					}
				}

				// Clamp 0..1 → gamma LUT.
				r8 := lut[lutIdx(cr)]
				g8 := lut[lutIdx(cg)]
				b8 := lut[lutIdx(cb)]

				// Saturation (around Rec.601 luma) + contrast (stretch around
				// 127.5) + brightness offset.
				if sat != 1 || contrast != 1 || bright != 0 {
					luma := float32(0.299)*float32(r8) + float32(0.587)*float32(g8) + float32(0.114)*float32(b8)
					r8 = clamp255(int((luma+(float32(r8)-luma)*sat-127.5)*contrast + 127.5 + bright))
					g8 = clamp255(int((luma+(float32(g8)-luma)*sat-127.5)*contrast + 127.5 + bright))
					b8 = clamp255(int((luma+(float32(b8)-luma)*sat-127.5)*contrast + 127.5 + bright))
				}

				// Shadow chroma suppression: the lower the luma, the more color
				// collapses toward neutral gray.
				if chromaSuppLow > 0 {
					luma := float32(0.299)*float32(r8) + float32(0.587)*float32(g8) + float32(0.114)*float32(b8)
					if luma < chromaSuppLow {
						k := luma / chromaSuppLow
						r8 = int(luma + (float32(r8)-luma)*k)
						g8 = int(luma + (float32(g8)-luma)*k)
						b8 = int(luma + (float32(b8)-luma)*k)
					}
				}

				o := i * 4
				out[o] = byte(clamp255(r8))
				out[o+1] = byte(clamp255(g8))
				out[o+2] = byte(clamp255(b8))
				out[o+3] = 255
			}
		}
	})

	// Pass 3a: luma noise reduction (guided filter on the 8-bit output
	// luma; after gamma the noise is roughly luma-independent, so eps is the
	// 8-bit σ²). Only the luma component changes (RGB all shift by ΔL) —
	// chroma was Pass 2b's job.
	if p.LumaNr > 0 && w >= 3 && h >= 3 {
		lumaPlane := ispOutLuma(out)
		smoothed := make([]float32, samples)
		copy(smoothed, lumaPlane)
		rad := p.LumaNrRadius
		if rad <= 0 {
			rad = 2
		}
		sigma := float32(p.LumaNr)
		gfA := make([]float32, samples)
		gfB := make([]float32, samples)
		guidedSelf(smoothed, w, h, rad, sigma*sigma, gfA, gfB, tmp)
		parallelFor(samples, func(lo, hi int) {
			for i := lo; i < hi; i++ {
				d := smoothed[i] - lumaPlane[i]
				o := i * 4
				r8 := clamp255(int(float32(out[o]) + d + float32(0.5)))
				g8 := clamp255(int(float32(out[o+1]) + d + float32(0.5)))
				b8 := clamp255(int(float32(out[o+2]) + d + float32(0.5)))
				out[o] = byte(r8)
				out[o+1] = byte(g8)
				out[o+2] = byte(b8)
			}
		})
	}

	// Pass 3: luma sharpening (multi-band unsharp mask + piecewise
	// luma-dependent gain, approximating the board's EE edge enhancement).
	ispSharpenRGBA(out, w, h, p)

	// Pass 4: per-luma channel tint (final 8-bit domain).
	if p.UseLumaTint {
		kx := [5]int{0, 64, 128, 192, 255}
		cR := p.LumaTint[0:5]
		cG := p.LumaTint[5:10]
		cB := p.LumaTint[10:15]
		parallelFor(samples, func(lo, hi int) {
			for i := lo; i < hi; i++ {
				o := i * 4
				r8, g8, b8 := int(out[o]), int(out[o+1]), int(out[o+2])
				luma := float32(0.299)*float32(r8) + float32(0.587)*float32(g8) + float32(0.114)*float32(b8)
				k := 0
				for k < 3 && luma > float32(kx[k+1]) {
					k++
				}
				t := (luma - float32(kx[k])) / float32(kx[k+1]-kx[k])
				t = min(float32(1), max(float32(0), t))
				r8 = clamp255(int(float32(r8) + float32(cR[k]) + (float32(cR[k+1])-float32(cR[k]))*t + float32(0.5)))
				g8 = clamp255(int(float32(g8) + float32(cG[k]) + (float32(cG[k+1])-float32(cG[k]))*t + float32(0.5)))
				b8 = clamp255(int(float32(b8) + float32(cB[k]) + (float32(cB[k+1])-float32(cB[k]))*t + float32(0.5)))
				out[o] = byte(r8)
				out[o+1] = byte(g8)
				out[o+2] = byte(b8)
			}
		})
	}

	// Pass 5: board chroma map (final 8-bit YCbCr domain).
	if p.UseChromaMap {
		applyChromaMap(out, &p.ChromaMap)
	}
	return out
}

// lutIdx maps a clamped 0..1 linear value onto the gamma LUT index exactly
// as the reference does (branch, then a float32 multiply and truncation).
func lutIdx(v float32) int {
	if v <= 0 {
		return 0
	}
	if v >= 1 {
		return 4095
	}
	return int(v * 4095.0)
}

// clamp255 clamps an int to the 0..255 byte range.
func clamp255(v int) int {
	return min(255, max(0, v))
}

// faAbs is float32 absolute value.
func faAbs(v float32) float32 {
	return float32(math.Abs(float64(v)))
}

// log2F32/exp2F32 compute log2/exp2 on float32 values (the reference calls
// the C float overloads on float data).
func log2F32(v float32) float32 {
	return float32(math.Log2(float64(v)))
}

func exp2F32(v float32) float32 {
	return float32(math.Exp2(float64(v)))
}

// ispOutLuma extracts the Rec.601 luma plane from an RGBA buffer.
func ispOutLuma(rgba []byte) []float32 {
	l := make([]float32, len(rgba)/4)
	parallelFor(len(l), func(lo, hi int) {
		for i := lo; i < hi; i++ {
			o := i * 4
			l[i] = float32(0.299)*float32(rgba[o]) + float32(0.587)*float32(rgba[o+1]) + float32(0.114)*float32(rgba[o+2])
		}
	})
	return l
}

// applyChromaMap applies the board chroma map (isp::Params::chromaMap) to an
// 8-bit RGBA buffer: per pixel, convert to YCbCr (Rec.601 full-range factor
// form used by the reference), trilinearly interpolate the (a, b) rotator-
// scaler over hue x chroma magnitude x luma plus the per-luma offsets, then
// convert back. Hue is interpolated around the ring; magnitude and luma use
// piecewise-linear segments clamped at the end nodes. At zero chroma the hue
// is undefined but (a, b) only ever multiply the chroma itself, so the
// result is unaffected.
func applyChromaMap(rgba []byte, table *[975]float64) {
	tA := table[0:CmapNG]
	tB := table[CmapNG : 2*CmapNG]
	oCb := table[2*CmapNG : 2*CmapNG+CmapLuma]
	oCr := table[2*CmapNG+CmapLuma : 2*CmapNG+2*CmapLuma]
	oY := table[2*CmapNG+2*CmapLuma : CmapSize]
	segM := func(v float32) (int, float32) {
		nodes := CmapMagNodes
		if v <= nodes[0] {
			return 0, 0
		}
		if v >= nodes[len(nodes)-1] {
			return len(nodes) - 2, 1
		}
		j := 0
		for j < len(nodes)-2 && v >= nodes[j+1] {
			j++
		}
		return j, (v - nodes[j]) / (nodes[j+1] - nodes[j])
	}
	segL := func(v float32) (int, float32) {
		nodes := CmapLumaNodes
		if v <= nodes[0] {
			return 0, 0
		}
		if v >= nodes[len(nodes)-1] {
			return len(nodes) - 2, 1
		}
		j := 0
		for j < len(nodes)-2 && v >= nodes[j+1] {
			j++
		}
		return j, (v - nodes[j]) / (nodes[j+1] - nodes[j])
	}
	const twoPi = float32(6.28318530718)
	np := len(rgba) / 4
	parallelFor(np, func(p0, p1 int) {
		for p := p0; p < p1; p++ {
			o := p * 4
			r := float32(rgba[o])
			g := float32(rgba[o+1])
			b := float32(rgba[o+2])
			y := float32(0.299)*r + float32(0.587)*g + float32(0.114)*b
			cb := (b - y) * float32(0.564)
			cr := (r - y) * float32(0.713)
			mag := sqrtF32(cb*cb + cr*cr)
			th := atan2F32(cr, cb)
			if th < 0 {
				th += twoPi
			}
			hh := th / twoPi * CmapHues
			h0 := int(hh)
			fh := hh - float32(h0)
			h0 %= CmapHues
			h1 := (h0 + 1) % CmapHues
			jm, fm := segM(mag)
			jl, fl := segL(y)
			var ga, gb float32
			for dh := 0; dh < 2; dh++ {
				for dm := 0; dm < 2; dm++ {
					for dl := 0; dl < 2; dl++ {
						wt := pickf(fh, dh) * pickf(fm, dm) * pickf(fl, dl)
						idx := ((picki(h1, h0, dh)*CmapMag + jm + dm) * CmapLuma) + jl + dl
						ga += wt * float32(tA[idx])
						gb += wt * float32(tB[idx])
					}
				}
			}
			ocb := float32(oCb[jl]) + (float32(oCb[jl+1])-float32(oCb[jl]))*fl
			ocr := float32(oCr[jl]) + (float32(oCr[jl+1])-float32(oCr[jl]))*fl
			yo := y + float32(oY[jl]) + (float32(oY[jl+1])-float32(oY[jl]))*fl
			cb2 := ga*cb - gb*cr + ocb
			cr2 := gb*cb + ga*cr + ocr
			ro := yo + cr2/float32(0.713)
			bo := yo + cb2/float32(0.564)
			gv := (yo - float32(0.299)*ro - float32(0.114)*bo) / float32(0.587)
			rgba[o] = byte(clamp255(lroundF32(ro)))
			rgba[o+1] = byte(clamp255(lroundF32(gv)))
			rgba[o+2] = byte(clamp255(lroundF32(bo)))
		}
	})
}

// pickf returns the interpolation weight for one trilinear axis slot.
func pickf(f float32, hi int) float32 {
	if hi != 0 {
		return f
	}
	return 1 - f
}

// picki returns the hue-bin index for one trilinear axis slot.
func picki(hi, lo int, which int) int {
	if which != 0 {
		return hi
	}
	return lo
}

// lroundF32 rounds half away from zero (C++ std::lround on float).
func lroundF32(v float32) int {
	return int(math.Round(float64(v)))
}

// sqrtF32 computes the float32 square root.
func sqrtF32(v float32) float32 {
	return float32(math.Sqrt(float64(v)))
}

// atan2F32 computes the float32 atan2. The exact libm used by the reference
// build may differ from Go's by an ulp; the only consumer is the chroma
// map's hue bin, where an ulp is far below the bin width.
func atan2F32(y, x float32) float32 {
	return float32(math.Atan2(float64(y), float64(x)))
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
	l := ispOutLuma(rgba)
	acc := make([]float32, n)
	band := make([]float32, n)
	tmp := make([]float32, n)
	// High band (3x3).
	copy(band, l)
	boxBlur(band, tmp, w, h, 1)
	parallelFor(n, func(lo, hi int) {
		for i := lo; i < hi; i++ {
			acc[i] = (l[i] - band[i]) * sharpenGainAt(&sharpenG1X, &sharpenG1Y, l[i])
		}
	})
	// Mid band (configurable radius) + low-mid band (radius 25, weight 0.3).
	radius := p.SharpenRadius
	if radius <= 0 {
		radius = 1
	}
	copy(band, l)
	boxBlur(band, tmp, w, h, radius)
	parallelFor(n, func(lo, hi int) {
		for i := lo; i < hi; i++ {
			acc[i] += (l[i] - band[i]) * sharpenGainAt(&sharpenG5X, &sharpenG5Y, l[i])
		}
	})
	copy(band, l)
	boxBlur(band, tmp, w, h, 25)
	sharpen := float32(p.Sharpen)
	parallelFor(n, func(lo, hi int) {
		for i := lo; i < hi; i++ {
			acc[i] += float32(0.3) * (l[i] - band[i]) * sharpenGainAt(&sharpenG5X, &sharpenG5Y, l[i])
			d := acc[i] * sharpen
			if d == 0 {
				continue
			}
			o := i * 4
			rgba[o] = byte(clamp255(int(float32(rgba[o]) + d)))
			rgba[o+1] = byte(clamp255(int(float32(rgba[o+1]) + d)))
			rgba[o+2] = byte(clamp255(int(float32(rgba[o+2]) + d)))
		}
	})
}

// ApplyIspParams is the RGB-domain ISP subset for already-decoded frames
// (the YUV viewer path): white balance gains → 3x3 CCM → pow gamma →
// saturation/contrast/brightness → shadow chroma suppression → multi-band
// luma sharpen. Blc, GrayMode, the DRC and the Bayer-domain stages are
// ignored here. The buffer is transformed in place.
func ApplyIspParams(rgba []byte, w, h int, p IspParams) {
	n := len(rgba) / 4
	if n == 0 {
		return
	}
	lut := ispGammaLUT(p.Gamma)
	m := &p.Ccm
	gR, gB := float64(p.GainR), float64(p.GainB)
	sat, contrast, bright := p.Saturation, p.Contrast, p.Brightness
	for i := 0; i < n; i++ {
		o := i * 4
		// Normalize 0..1 + white balance gains.
		fr := float64(rgba[o]) / 255 * gR
		fg := float64(rgba[o+1]) / 255
		fb := float64(rgba[o+2]) / 255 * gB

		// 3x3 CCM.
		cr := m[0]*fr + m[1]*fg + m[2]*fb
		cg := m[3]*fr + m[4]*fg + m[5]*fb
		cb := m[6]*fr + m[7]*fg + m[8]*fb

		r8 := lut[int(min(1, max(0, float32(cr)))*4095)]
		g8 := lut[int(min(1, max(0, float32(cg)))*4095)]
		b8 := lut[int(min(1, max(0, float32(cb)))*4095)]

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

		rgba[o] = byte(min(255, max(0, r8)))
		rgba[o+1] = byte(min(255, max(0, g8)))
		rgba[o+2] = byte(min(255, max(0, b8)))
	}

	ispSharpenRGBA(rgba, w, h, p)
}

// ComputeGrayWorldGains runs gray-world auto white balance over the raw
// Bayer frame: average the BLC-corrected R/G/B sites and return
// gainR=avgG/avgR, gainB=avgG/avgB (G gain fixed at 1.0), clamped to 0.1..8.
// Sampling steps over aligned 2x2 blocks (whole CFA tiles, so all four
// phases contribute) and grows the stride until ~2M samples. Sums are exact
// integers, matching the reference's int64 accumulation.
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
	var sumR, sumG, sumB int64
	var nR, nG, nB int64
	for y := 0; y+1 < h; y += step {
		for x := 0; x+1 < w; x += step {
			for dy := 0; dy < 2; dy++ {
				row := (y + dy) * w
				for dx := 0; dx < 2; dx++ {
					xx, yy := x+dx, y+dy
					v := ispSample(buf, bitDepth, row+xx) - p.Blc[cfa.channel[(yy&1)*2+(xx&1)]]
					v = min(maxVal, max(0, v))
					switch cfa.color[(yy&1)*2+(xx&1)] {
					case 0:
						sumR += int64(v)
						nR++
					case 1:
						sumG += int64(v)
						nG++
					default:
						sumB += int64(v)
						nB++
					}
				}
			}
		}
	}
	if nR == 0 || nG == 0 || nB == 0 {
		return
	}
	avgR := float64(sumR) / float64(nR)
	avgG := float64(sumG) / float64(nG)
	avgB := float64(sumB) / float64(nB)
	gr := avgG / math.Max(avgR, 1e-3)
	gb := avgG / math.Max(avgB, 1e-3)
	return min(8, max(0.1, gr)), min(8, max(0.1, gb))
}

// AutoWhiteBalance computes the gray-world gains for the frame and writes
// them into p, multiplied by the board's AWB preference biases (per ISO
// tier) — approximating the board's own AWB rendering (isp::autoWhiteBalance).
func AutoWhiteBalance(buf []byte, w, h, bitDepth int, patternKey string, p *IspParams) {
	p.GainR, p.GainB = ComputeGrayWorldGains(buf, w, h, bitDepth, patternKey, *p)
	p.GainR *= p.AwbBiasR
	p.GainB *= p.AwbBiasB
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
	return min(8, max(0.1, avgG/math.Max(avgR, 1e-3))), min(8, max(0.1, avgG/math.Max(avgB, 1e-3)))
}
