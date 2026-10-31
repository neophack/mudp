package raster

import (
	"math"
	"testing"
)

// neutralParams returns an ISP parameter set whose stages are all pass-through
// (unit gains, identity CCM, gamma 1, no sat/contrast/brightness/sharpen/DRC/
// chroma map), so tests can enable one stage at a time.
func neutralParams(maxVal int) IspParams {
	return IspParams{
		GainR:      1,
		GainB:      1,
		Ccm:        CcmIdentity,
		Gamma:      1,
		Saturation: 1,
		Contrast:   1,
		MaxVal:     maxVal,
	}
}

// uniformRaw builds a w*h Bayer dump where every site holds the same value.
func uniformRaw(w, h int, v int) []byte {
	buf := make([]byte, 2*w*h)
	for i := 0; i < w*h; i++ {
		buf[i*2] = byte(v & 0xFF)
		buf[i*2+1] = byte(v >> 8)
	}
	return buf
}

// TestDefaultIspParamsBoardValues pins the board-calibrated defaults ported
// from MiniIsp.cpp Params::reset (which seeds from the first ISO tier) — a
// silent change to any of these shifts the default preview away from the
// board's YUV output.
func TestDefaultIspParamsBoardValues(t *testing.T) {
	p := DefaultIspParams(12)
	if p.Blc != [4]int{0, 0, 0, 0} {
		t.Errorf("Blc = %v, want zeros (board dumps after BLC)", p.Blc)
	}
	if p.GainR != 1.51 || p.GainB != 1.69 {
		t.Errorf("gains = %v/%v, want 1.51/1.69", p.GainR, p.GainB)
	}
	// The reset CCM transcribed as a literal (CCM_FIT in MiniIsp.cpp).
	wantCcm := [9]float64{
		0.7742, 0.4423, -0.2165,
		0.0864, 0.9833, -0.0697,
		-0.0356, 0.2751, 0.7605,
	}
	if p.Ccm != wantCcm {
		t.Errorf("Ccm = %v, want fit %v", p.Ccm, wantCcm)
	}
	if p.Gamma != 1.43 || p.Contrast != 0.985 {
		t.Errorf("tone = gamma %v contrast %v", p.Gamma, p.Contrast)
	}
	// Tier-sourced scalars are float32 values widened to float64 (the
	// reference's Params fields are C float); compare against the same
	// widening of the literal.
	f32 := func(v float64) float64 { return float64(float32(v)) }
	if p.Saturation != f32(2.43) || p.Brightness != f32(-1.42) {
		t.Errorf("tone = sat %v bright %v", p.Saturation, p.Brightness)
	}
	if !p.EnableDrc || p.DrcCompress != f32(0.891) || p.DrcDetail != f32(1.130) || p.DrcRadius != 96 ||
		p.DrcPivot != 0.214 || p.DrcEdge != 4.0 {
		t.Errorf("DRC defaults changed: %+v", p)
	}
	if p.LumaNr != f32(0.1) || p.LumaNrRadius != 4 || p.ChromaNrRadius != 5 {
		t.Errorf("NR defaults changed: %+v", p)
	}
	if !p.EnableSharpen || p.Sharpen != f32(1.18) || p.SharpenRadius != 5 {
		t.Errorf("sharpen defaults changed: %+v", p)
	}
	if p.ChromaSuppLow != 0 || p.DigitalGain != 1 {
		t.Errorf("chromaSupp/dgain defaults changed: %+v", p)
	}
	if p.UseChromaMap || p.UseLumaTint {
		t.Errorf("reset must not enable the tier tables (applyIsoTuning owns them)")
	}
	if p.AwbBiasR != f32(0.996) || p.AwbBiasB != f32(0.987) {
		t.Errorf("AWB bias defaults changed: %+v", p)
	}
	if p.MaxVal != 4095 {
		t.Errorf("MaxVal(12bit) = %d, want 4095", p.MaxVal)
	}
	if DefaultIspParams(16).MaxVal != 65535 {
		t.Errorf("MaxVal(16bit) = %d, want 65535", DefaultIspParams(16).MaxVal)
	}
}

// TestCcmPresetMatrixes verifies the preset table: index 0 is manual
// (not a preset), the rest return their matrices (isp::CCM_PRESET_NAMES).
func TestCcmPresetMatrixes(t *testing.T) {
	if _, ok := CcmPreset(0); ok {
		t.Error("preset 0 (manual) should return ok=false")
	}
	if len(CcmPresetNames) != 4 {
		t.Errorf("preset names = %v, want 4 entries", CcmPresetNames)
	}
	for idx, want := range map[int][9]float64{
		1: {1, 0, 0, 0, 1, 0, 0, 0, 1},
		2: {1.66, -0.55, -0.11, -0.25, 1.45, -0.20, -0.07, -0.28, 1.35},
		3: {1.85, -0.70, -0.15, -0.32, 1.62, -0.30, -0.10, -0.38, 1.48},
	} {
		got, ok := CcmPreset(idx)
		if !ok {
			t.Errorf("preset %d should exist", idx)
			continue
		}
		if got != want {
			t.Errorf("preset %d = %v, want %v", idx, got, want)
		}
	}
	if _, ok := CcmPreset(4); ok {
		t.Error("preset 4 should not exist")
	}
}

// TestIspCfaChannelMapping checks the 2x2 phase tables for all four layouts:
// the Blc channel index (R/Gr/Gb/B) and the color index (R/G/B).
func TestIspCfaChannelMapping(t *testing.T) {
	cases := map[string][4][2]int{ // per phase (y*2+x): {channel, color}
		"RGGB": {{0, 0}, {1, 1}, {2, 1}, {3, 2}},
		"BGGR": {{3, 2}, {2, 1}, {1, 1}, {0, 0}},
		"GRBG": {{1, 1}, {0, 0}, {3, 2}, {2, 1}},
		"GBRG": {{2, 1}, {3, 2}, {0, 0}, {1, 1}},
	}
	for key, want := range cases {
		got := ispCfa(key)
		for ph := 0; ph < 4; ph++ {
			if got.channel[ph] != want[ph][0] || got.color[ph] != want[ph][1] {
				t.Errorf("%s phase %d: channel/color = %d/%d, want %d/%d",
					key, ph, got.channel[ph], got.color[ph], want[ph][0], want[ph][1])
			}
		}
	}
	// Unknown pattern falls back to RGGB.
	if ispCfa("nope").channel != ispCfa("RGGB").channel {
		t.Error("unknown pattern must fall back to RGGB")
	}
}

// TestProcessBayerFlatFrameUniformGray: a uniform field through the neutral
// pipeline must come out as one uniform gray value end to end (directional
// interpolation preserves constants, gamma(1) is identity).
func TestProcessBayerFlatFrameUniformGray(t *testing.T) {
	w, h := 16, 12
	rgba := ProcessBayer(uniformRaw(w, h, 1000), w, h, 16, "RGGB", neutralParams(4095))
	if rgba == nil {
		t.Fatal("nil output")
	}
	// gamma 1 LUT at index 1000 (v/maxVal*4095 with maxVal 4095): round(1000/4095*255).
	wantLut := min(255, max(0, int(math.Pow(float64(1000)/4095.0, 1.0)*255.0+0.5)))
	for i := 0; i < w*h; i++ {
		r, g, b, a := rgba[4*i], rgba[4*i+1], rgba[4*i+2], rgba[4*i+3]
		if r != g || g != b {
			t.Fatalf("pixel %d not gray: %d %d %d", i, r, g, b)
		}
		if r != byte(wantLut) {
			t.Fatalf("pixel %d = %d, want %d", i, r, wantLut)
		}
		if a != 255 {
			t.Fatalf("alpha %d != 255", a)
		}
	}
}

// TestProcessBayerBlcSubtractsBlackPoint: a field at the black level must
// decode to black.
func TestProcessBayerBlcSubtractsBlackPoint(t *testing.T) {
	w, h := 12, 8
	p := neutralParams(4095)
	p.Blc = [4]int{100, 100, 100, 100}
	rgba := ProcessBayer(uniformRaw(w, h, 100), w, h, 16, "RGGB", p)
	for i := 0; i < w*h; i++ {
		if rgba[4*i] != 0 || rgba[4*i+1] != 0 || rgba[4*i+2] != 0 {
			t.Fatalf("black-level field is not black: pixel 0 = %d %d %d", rgba[0], rgba[1], rgba[2])
		}
	}
}

// TestProcessBayerGrayModeValues checks the gray-mode pixel formula against an
// independent recomputation: v = clamp(raw - blc) → lut index → contrast
// stretch around 127.5 + brightness.
func TestProcessBayerGrayModeValues(t *testing.T) {
	w, h := 8, 6
	p := neutralParams(4095)
	p.Contrast = 1.1
	p.Brightness = 3
	p.Blc = [4]int{10, 10, 10, 10}
	raw := uniformRaw(w, h, 2000)
	rgba := ProcessBayer(raw, w, h, 16, "BGGR", p)
	lut := ispGammaLUT(1.0)
	v := 2000 - 10
	idx := int(float32(v) * (float32(4095) / float32(4095)))
	want := min(255, max(0, int((float32(lut[idx])-127.5)*1.1+127.5+3)))
	for i := 0; i < w*h; i++ {
		if rgba[4*i] != byte(want) || rgba[4*i+1] != byte(want) || rgba[4*i+2] != byte(want) {
			t.Fatalf("gray pixel %d = %d, want %d", i, rgba[4*i], want)
		}
	}
}

// TestProcessBayerGrayModeIsGrayscale: with per-channel black levels the gray
// view still shows the raw values per CFA phase (no color tint).
func TestProcessBayerGrayModeIsGrayscale(t *testing.T) {
	w, h := 10, 10
	p := neutralParams(4095)
	p.GrayMode = true
	p.Blc = [4]int{0, 5, 7, 9}
	rgba := ProcessBayer(uniformRaw(w, h, 1234), w, h, 16, "GRBG", p)
	for i := 0; i < w*h; i++ {
		if rgba[4*i] != rgba[4*i+1] || rgba[4*i+1] != rgba[4*i+2] {
			t.Fatalf("pixel %d is not grayscale", i)
		}
	}
}

// TestProcessBayerTinySizesNoPanic: mirror-extension border handling must not
// index out of bounds on tiny frames.
func TestProcessBayerTinySizesNoPanic(t *testing.T) {
	for _, sz := range [][2]int{{1, 1}, {2, 2}, {3, 3}, {5, 3}, {3, 5}} {
		w, h := sz[0], sz[1]
		for _, pat := range []string{"RGGB", "BGGR", "GRBG", "GBRG"} {
			p := DefaultIspParams(12)
			rgba := ProcessBayer(uniformRaw(w, h, 500), w, h, 16, pat, p)
			if len(rgba) != 4*w*h {
				t.Fatalf("%dx%d %s: got %d bytes, want %d", w, h, pat, len(rgba), 4*w*h)
			}
		}
	}
}

// TestProcessBayerEightBitPacksOneBytePerSample: 8-bit dumps read one byte
// per sample.
func TestProcessBayerEightBitPacksOneBytePerSample(t *testing.T) {
	w, h := 6, 4
	buf := make([]byte, w*h)
	for i := range buf {
		buf[i] = byte(200 + i%7)
	}
	p := neutralParams(255)
	rgba := ProcessBayer(buf, w, h, 8, "RGGB", p)
	if len(rgba) != 4*w*h {
		t.Fatalf("got %d bytes, want %d", len(rgba), 4*w*h)
	}
}

// TestProcessBayerShortBufferReturnsNil: truncated buffers return nil instead
// of panicking.
func TestProcessBayerShortBufferReturnsNil(t *testing.T) {
	if got := ProcessBayer(make([]byte, 10), 8, 8, 16, "RGGB", neutralParams(4095)); got != nil {
		t.Errorf("short 16-bit buffer: got %d bytes, want nil", len(got))
	}
	if got := ProcessBayer(make([]byte, 10), 8, 8, 8, "RGGB", neutralParams(255)); got != nil {
		t.Errorf("short 8-bit buffer: got %d bytes, want nil", len(got))
	}
}

// TestApplyIsoTuningEndpointsAndClamp: at a tier's exact ISO the scalars land
// on that tier (within the lerp's float32 rounding — the interpolation runs
// even at t=1); below/above the table they clamp to the end tiers; iso <= 0
// leaves the parameters untouched.
func TestApplyIsoTuningEndpointsAndClamp(t *testing.T) {
	near := func(a, b float64) bool { return math.Abs(a-b) < 1e-5 }
	check := func(iso int64, want IsoTuning) {
		t.Helper()
		p := DefaultIspParams(12)
		ApplyIsoTuning(&p, iso)
		if !near(p.DrcCompress, float64(want.DrcCompress)) || !near(p.DrcDetail, float64(want.DrcDetail)) ||
			!near(p.Sharpen, float64(want.Sharpen)) || !near(p.Saturation, float64(want.Saturation)) ||
			!near(p.LumaNr, float64(want.LumaNr)) || p.ChromaNrRadius != want.ChromaNrRadius ||
			!near(p.Brightness, float64(want.Brightness)) || p.DrcRadius != want.DrcRadius ||
			!near(p.ChromaSuppLow, float64(want.ChromaSuppLow)) {
			t.Errorf("iso %d: scalars = %+v, want tier %+v", iso, p, want)
		}
		if !p.UseChromaMap {
			t.Errorf("iso %d: chroma map not enabled", iso)
		}
		if want.Ccm != nil {
			for i := 0; i < 9; i++ {
				if !near(p.Ccm[i], float64(want.Ccm[i])) {
					t.Errorf("iso %d: Ccm[%d] = %v, want %v", iso, i, p.Ccm[i], want.Ccm[i])
				}
			}
		}
	}
	check(1500, isoTuning[0])
	check(4651, isoTuning[1])
	check(400945, isoTuning[len(isoTuning)-1])
	check(1, isoTuning[0])                       // below the first tier clamps up
	check(99000000, isoTuning[len(isoTuning)-1]) // above the last clamps down
}

// TestApplyIsoTuningIsoZeroUntouched: non-positive ISO changes nothing.
func TestApplyIsoTuningIsoZeroUntouched(t *testing.T) {
	p := DefaultIspParams(12)
	before := p
	ApplyIsoTuning(&p, -5)
	if p != before {
		t.Error("iso <= 0 must leave the parameters untouched")
	}
}

// TestApplyIsoTuningInterpolatesInLogIso: between two tiers the scalars lie
// on the log2-ISO-linear segment.
func TestApplyIsoTuningInterpolatesInLogIso(t *testing.T) {
	a, b := &isoTuning[3], &isoTuning[4] // 80512 / 155629
	iso := int64(math.Sqrt(float64(a.Iso) * float64(b.Iso)))
	p := DefaultIspParams(12)
	ApplyIsoTuning(&p, iso)
	t2 := (math.Log2(float64(iso)) - math.Log2(float64(a.Iso))) /
		(math.Log2(float64(b.Iso)) - math.Log2(float64(a.Iso)))
	wantLumaNr := float64(a.LumaNr) + (float64(b.LumaNr)-float64(a.LumaNr))*t2
	if math.Abs(p.LumaNr-wantLumaNr) > 1e-4 {
		t.Errorf("LumaNr = %v, want %v (t=%v)", p.LumaNr, wantLumaNr, t2)
	}
	for i := 0; i < 9; i++ {
		want := float64(a.Ccm[i]) + (float64(b.Ccm[i])-float64(a.Ccm[i]))*t2
		if math.Abs(p.Ccm[i]-want) > 1e-4 {
			t.Errorf("Ccm[%d] = %v, want %v", i, p.Ccm[i], want)
		}
	}
	// The interpolated chroma map must be populated from both tiers (not the
	// identity, not stale zeros).
	nonIdentity := false
	for i := 0; i < CmapNG; i++ {
		if p.ChromaMap[i] != 1 {
			nonIdentity = true
			break
		}
	}
	if !nonIdentity {
		t.Error("interpolated chroma map looks like the identity table")
	}
	nonZero := false
	for _, v := range p.ChromaMap {
		if v != 0 {
			nonZero = true
			break
		}
	}
	if !nonZero {
		t.Error("interpolated chroma map is all zeros")
	}
}

// TestProcessBayerDrcLiftsDarkUniform: with detail=1 and compress<1, a
// uniform bright field (above the pivot) must come out darker than with DRC
// off; a uniform dark field (below the pivot) must come out brighter.
func TestProcessBayerDrcLiftsDarkUniform(t *testing.T) {
	w, h := 32, 32
	mk := func(level int, drc bool) byte {
		p := neutralParams(4095)
		p.EnableDrc = drc
		p.DrcCompress = 0.5
		p.DrcDetail = 1
		p.DrcRadius = 8
		p.DrcPivot = 0.214
		p.DrcEdge = 4
		rgba := ProcessBayer(uniformRaw(w, h, level), w, h, 16, "RGGB", p)
		return rgba[4*(w*h/2)]
	}
	brightOff, brightOn := mk(4000, false), mk(4000, true)
	darkOff, darkOn := mk(100, false), mk(100, true)
	if brightOn >= brightOff {
		t.Errorf("bright uniform: DRC on %d >= off %d", brightOn, brightOff)
	}
	if darkOn <= darkOff {
		t.Errorf("dark uniform: DRC on %d <= off %d", darkOn, darkOff)
	}
}

// TestProcessBayerChromaMapRuns: the tier chroma map must change the output
// on a colored field (near-neutral desaturation).
func TestProcessBayerChromaMapRuns(t *testing.T) {
	w, h := 32, 32
	buf := make([]byte, 2*w*h)
	for i := 0; i < w*h; i++ {
		v := 2000 + (i*7)%23
		buf[2*i] = byte(v & 0xFF)
		buf[2*i+1] = byte(v >> 8)
	}
	run := func(mapOn bool) float64 {
		p := DefaultIspParams(12)
		ApplyIsoTuning(&p, 4651)
		p.UseChromaMap = mapOn
		rgba := ProcessBayer(buf, w, h, 16, "RGGB", p)
		var sum float64
		for i := 0; i < w*h; i++ {
			r, g, b := float64(rgba[4*i]), float64(rgba[4*i+1]), float64(rgba[4*i+2])
			l := 0.299*r + 0.587*g + 0.114*b
			sum += math.Abs(r-l) + math.Abs(g-l) + math.Abs(b-l)
		}
		return sum / float64(w*h)
	}
	if !(run(true) <= run(false)) {
		t.Errorf("chroma distance map on %v > off %v", run(true), run(false))
	}
}

// TestComputeGrayWorldGainsSynthetic: a field with known per-color means
// yields gains = meanG/meanR and meanG/meanB.
func TestComputeGrayWorldGainsSynthetic(t *testing.T) {
	w, h := 32, 32
	buf := make([]byte, 2*w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var v int
			switch (y&1)*2 + (x & 1) { // RGGB
			case 0:
				v = 800 // R sites
			case 3:
				v = 400 // B sites
			default:
				v = 1000 // G sites
			}
			i := y*w + x
			buf[2*i] = byte(v & 0xFF)
			buf[2*i+1] = byte(v >> 8)
		}
	}
	gainR, gainB := ComputeGrayWorldGains(buf, w, h, 16, "RGGB", neutralParams(4095))
	if math.Abs(gainR-1.25) > 1e-6 || math.Abs(gainB-2.5) > 1e-6 {
		t.Errorf("gains = %v/%v, want 1.25/2.5", gainR, gainB)
	}
}

// TestComputeGrayWorldGainsClampsAndGuards: gains clamp to 0.1..8 and empty
// input keeps 1/1.
func TestComputeGrayWorldGainsClampsAndGuards(t *testing.T) {
	if gR, gB := ComputeGrayWorldGains(nil, 0, 0, 16, "RGGB", neutralParams(4095)); gR != 1 || gB != 1 {
		t.Errorf("empty frame gains = %v/%v, want 1/1", gR, gB)
	}
	// Extreme channel imbalance clamps: R at black, G bright.
	w, h := 8, 8
	buf := make([]byte, 2*w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			v := 1000
			if (y&1)*2+(x&1) == 0 {
				v = 0 // R sites
			}
			i := y*w + x
			buf[2*i] = byte(v & 0xFF)
			buf[2*i+1] = byte(v >> 8)
		}
	}
	gainR, _ := ComputeGrayWorldGains(buf, w, h, 16, "RGGB", neutralParams(4095))
	if gainR != 8 {
		t.Errorf("gainR = %v, want clamp at 8", gainR)
	}
}

// TestAutoWhiteBalanceAppliesBias: AutoWhiteBalance multiplies the gray-world
// gains by the AWB biases.
func TestAutoWhiteBalanceAppliesBias(t *testing.T) {
	w, h := 32, 32
	buf := make([]byte, 2*w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var v int
			switch (y&1)*2 + (x & 1) {
			case 0:
				v = 800
			case 3:
				v = 400
			default:
				v = 1000
			}
			i := y*w + x
			buf[2*i] = byte(v & 0xFF)
			buf[2*i+1] = byte(v >> 8)
		}
	}
	p := neutralParams(4095)
	p.AwbBiasR = 1.1
	p.AwbBiasB = 0.9
	AutoWhiteBalance(buf, w, h, 16, "RGGB", &p)
	if math.Abs(p.GainR-1.25*1.1) > 1e-6 || math.Abs(p.GainB-2.5*0.9) > 1e-6 {
		t.Errorf("biased gains = %v/%v, want %v/%v", p.GainR, p.GainB, 1.25*1.1, 2.5*0.9)
	}
}

// TestApplyIspParamsNeutralIsIdentity: the RGB-domain subset with neutral
// parameters leaves an RGBA buffer untouched.
func TestApplyIspParamsNeutralIsIdentity(t *testing.T) {
	rgba := make([]byte, 4*8*8)
	for i := range rgba {
		rgba[i] = byte(i * 7 % 256)
	}
	want := append([]byte(nil), rgba...)
	ApplyIspParams(rgba, 8, 8, neutralParams(255))
	for i := range rgba {
		if rgba[i] != want[i] {
			t.Fatalf("byte %d changed: %d → %d", i, want[i], rgba[i])
		}
	}
}

// TestApplyIspParamsGammaBrightensMids: gamma 2.2 must lift mid-tones.
func TestApplyIspParamsGammaBrightensMids(t *testing.T) {
	rgba := make([]byte, 4*4)
	for c := 0; c < 3; c++ {
		rgba[c] = 128
	}
	rgba[3] = 255
	p := neutralParams(255)
	p.Gamma = 2.2
	ApplyIspParams(rgba, 2, 2, p)
	if rgba[0] <= 128 {
		t.Errorf("mid-tone not lifted: %d", rgba[0])
	}
}

// TestApplyIspParamsEmptyBufferNoPanic: zero-length input is a no-op.
func TestApplyIspParamsEmptyBufferNoPanic(t *testing.T) {
	ApplyIspParams(nil, 0, 0, neutralParams(255))
}

// TestBoxBlurMatchesDirectSum: the sliding-window box blur must equal a
// direct (slow) edge-clamped sum on a small random plane.
func TestBoxBlurMatchesDirectSum(t *testing.T) {
	w, h, radius := 19, 13, 4
	plane := make([]float32, w*h)
	seed := uint32(12345)
	for i := range plane {
		seed = seed*1103515245 + 12345
		plane[i] = float32(seed%1000) / 97
	}
	got := append([]float32(nil), plane...)
	tmp := make([]float32, len(plane))
	boxBlur(got, tmp, w, h, radius)
	cl := func(v, hi int) int { return min(hi, max(0, v)) }
	// The separable blur divides by the window length once per pass, so the
	// 2D result is the window sum over (2r+1)² samples divided by (2r+1)².
	side := 2*radius + 1
	n := float32(side * side)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var sum float64
			for dy := -radius; dy <= radius; dy++ {
				for dx := -radius; dx <= radius; dx++ {
					sum += float64(plane[cl(y+dy, h-1)*w+cl(x+dx, w-1)])
				}
			}
			want := float32(sum / float64(n))
			if math.Abs(float64(got[y*w+x]-want)) > 1e-3 {
				t.Fatalf("(%d,%d): sliding %v vs direct %v", x, y, got[y*w+x], want)
			}
		}
	}
}

// TestGuidedSelfSmoothsFlatKeepsSteps: on a flat-plus-noise plane the filter
// approaches the mean; across a large step it keeps the two levels apart.
func TestGuidedSelfSmoothsFlatKeepsSteps(t *testing.T) {
	w, h, radius := 40, 20, 3
	flat := make([]float32, w*h)
	for i := range flat {
		flat[i] = 100
		if i%3 == 0 {
			flat[i] = 101 // small noise, variance << eps
		}
	}
	mean := make([]float32, len(flat))
	corr := make([]float32, len(flat))
	tmp := make([]float32, len(flat))
	guidedSelf(flat, w, h, radius, 5000, mean, corr, tmp)
	for y := radius; y < h-radius; y++ { // interior only: the border windows
		for x := radius; x < w-radius; x++ { // clamp columns and skew the mean
			v := float64(flat[y*w+x])
			if math.Abs(v-100.33) > 0.2 {
				t.Fatalf("flat region (%d,%d) not smoothed to the mean: %v", x, y, v)
			}
		}
	}

	step := make([]float32, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if x < w/2 {
				step[y*w+x] = 0
			} else {
				step[y*w+x] = 1000
			}
		}
	}
	guidedSelf(step, w, h, radius, 4, mean, corr, tmp) // tiny eps: keep edges
	for y := 0; y < h; y++ {
		l, r := step[y*w+w/4], step[y*w+3*w/4]
		if l > 10 || r < 990 {
			t.Fatalf("step edges bled across: %v / %v", l, r)
		}
	}
}
