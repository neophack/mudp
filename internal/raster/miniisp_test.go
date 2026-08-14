package raster

import (
	"math"
	"testing"
)

// neutralParams returns an ISP parameter set whose stages are all pass-through
// (unit gains, identity CCM, gamma 1, no sat/contrast/brightness/sharpen), so
// tests can enable one stage at a time.
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
// from MiniIsp.cpp Params::reset — a silent change to any of these shifts the
// default preview away from the board's YUV output.
func TestDefaultIspParamsBoardValues(t *testing.T) {
	p := DefaultIspParams(12)
	if p.Blc != [4]int{39, 225, 225, 48} {
		t.Errorf("Blc = %v, want {39 225 225 48}", p.Blc)
	}
	if p.GainR != 1.07 || p.GainB != 1.12 {
		t.Errorf("gains = %v/%v, want 1.07/1.12", p.GainR, p.GainB)
	}
	if p.Ccm != CcmBoard4032K {
		t.Errorf("Ccm = %v, want board 4032K", p.Ccm)
	}
	if p.Gamma != 2.27 || p.Saturation != 1.15 || p.Contrast != 1.22 || p.Brightness != 0 {
		t.Errorf("tone = gamma %v sat %v contrast %v bright %v", p.Gamma, p.Saturation, p.Contrast, p.Brightness)
	}
	if p.ChromaNrRadius != 3 || !p.EnableSharpen || p.Sharpen != 1.0 || p.SharpenRadius != 5 || p.ChromaSuppLow != 60 {
		t.Errorf("nr/sharpen defaults changed: %+v", p)
	}
	if p.MaxVal != 4095 {
		t.Errorf("MaxVal(12bit) = %d, want 4095", p.MaxVal)
	}
	if DefaultIspParams(16).MaxVal != 65535 {
		t.Errorf("MaxVal(16bit) = %d, want 65535", DefaultIspParams(16).MaxVal)
	}
}

// TestCcmPresetMatrixes verifies the preset table: index 0 is manual
// (not a preset), the rest return their matrices.
func TestCcmPresetMatrixes(t *testing.T) {
	if _, ok := CcmPreset(0); ok {
		t.Error("preset 0 (manual) should return ok=false")
	}
	for idx, want := range map[int][9]float64{1: CcmIdentity, 2: CcmBoard4032K, 3: CcmSrgbTypical, 4: CcmSaturated} {
		got, ok := CcmPreset(idx)
		if !ok {
			t.Fatalf("preset %d unexpectedly unavailable", idx)
		}
		if got != want {
			t.Errorf("preset %d matrix mismatch", idx)
		}
	}
	if _, ok := CcmPreset(5); ok {
		t.Error("out-of-range preset should return ok=false")
	}
	if len(CcmPresetNames) != 5 {
		t.Errorf("CcmPresetNames has %d entries, want 5", len(CcmPresetNames))
	}
}

// TestIspCfaChannelMapping: the 2x2-phase tables must assign Blc channel
// indices 0=R 1=Gr 2=Gb 3=B for every Bayer layout (phase = (y&1)*2+(x&1)).
func TestIspCfaChannelMapping(t *testing.T) {
	cases := map[string][4]int{
		// (0,0) (0,1) (1,0) (1,1)
		"RGGB": {0, 1, 2, 3},
		"BGGR": {3, 2, 1, 0},
		"GRBG": {1, 0, 3, 2},
		"GBRG": {2, 3, 0, 1},
	}
	for key, want := range cases {
		got := ispCfa(key)
		if got.channel != want {
			t.Errorf("%s channel = %v, want %v", key, got.channel, want)
		}
	}
	// Unknown keys fall back to RGGB.
	if ispCfa("nope").channel != cases["RGGB"] {
		t.Error("unknown pattern should fall back to RGGB")
	}
}

// TestProcessBayerFlatFrameUniformGray: a flat raw frame through neutral
// params (unit gains, identity CCM, no chroma NR needed) must come out as one
// uniform gray whose value is pow(v/maxVal, 1/gamma)*255.
func TestProcessBayerFlatFrameUniformGray(t *testing.T) {
	const w, h = 8, 6
	raw := 2048
	p := neutralParams(4095)
	p.Gamma = 2.2
	out := ProcessBayer(uniformRaw(w, h, raw), w, h, 12, "RGGB", p)
	if len(out) != w*h*4 {
		t.Fatalf("output size %d, want %d", len(out), w*h*4)
	}
	want := int(math.Pow(2048.0/4095.0, 1/2.2)*255 + 0.5)
	for i := 0; i < w*h; i++ {
		o := i * 4
		if out[o] != out[o+1] || out[o+1] != out[o+2] {
			t.Fatalf("pixel %d not gray: %d %d %d", i, out[o], out[o+1], out[o+2])
		}
		if int(out[o]) != want {
			t.Fatalf("pixel %d = %d, want %d", i, out[o], want)
		}
		if out[o+3] != 255 {
			t.Fatalf("alpha = %d", out[o+3])
		}
	}
}

// TestProcessBayerBlcSubtractsBlackPoint: samples at or below the black level
// must clamp to black, and the flat output must stay uniform.
func TestProcessBayerBlcSubtractsBlackPoint(t *testing.T) {
	const w, h = 4, 4
	p := neutralParams(4095)
	p.Blc = [4]int{50, 50, 50, 50}
	out := ProcessBayer(uniformRaw(w, h, 50), w, h, 12, "RGGB", p)
	for i := 0; i < w*h; i++ {
		if out[i*4] != 0 || out[i*4+1] != 0 || out[i*4+2] != 0 {
			t.Fatalf("pixel %d not black: %d %d %d", i, out[i*4], out[i*4+1], out[i*4+2])
		}
	}
}

// TestProcessBayerGrayModeIsGrayscale: gray mode bypasses the demosaic, so
// every pixel is R==G==B even on a strongly colored (non-uniform) frame.
func TestProcessBayerGrayModeIsGrayscale(t *testing.T) {
	const w, h = 8, 8
	// Checker pattern of very different raw values.
	buf := make([]byte, 2*w*h)
	for i, v := 0, 0; i < w*h; i, v = i+1, v+97 {
		buf[i*2] = byte(v % 3000 & 0xFF)
		buf[i*2+1] = byte(v % 3000 >> 8)
	}
	p := DefaultIspParams(12)
	p.GrayMode = true
	out := ProcessBayer(buf, w, h, 12, "RGGB", p)
	for i := 0; i < w*h; i++ {
		o := i * 4
		if out[o] != out[o+1] || out[o+1] != out[o+2] {
			t.Fatalf("gray mode pixel %d colored: %d %d %d", i, out[o], out[o+1], out[o+2])
		}
	}
}

// TestProcessBayerTinySizesNoPanic guards the mirror/clamp indexing at and
// below the minimum neighborhood sizes the interpolation reads.
func TestProcessBayerTinySizesNoPanic(t *testing.T) {
	for _, wh := range [][2]int{{2, 2}, {3, 3}, {5, 4}, {4, 5}, {1, 1}} {
		w, h := wh[0], wh[1]
		buf := uniformRaw(w, h, 1000)
		for _, gray := range []bool{false, true} {
			p := DefaultIspParams(12)
			p.GrayMode = gray
			out := ProcessBayer(buf, w, h, 12, "GRBG", p)
			if len(out) != w*h*4 {
				t.Fatalf("%dx%d gray=%v: output size %d", w, h, gray, len(out))
			}
		}
	}
}

// TestProcessBayerEightBitPacksOneBytePerSample: the ≤8-bit container path
// must read one byte per site (not LE16) and respect its own maxVal.
func TestProcessBayerEightBitPacksOneBytePerSample(t *testing.T) {
	const w, h = 4, 4
	buf := make([]byte, w*h) // 16 bytes for 16 sites
	for i := range buf {
		buf[i] = 200
	}
	p := neutralParams(255)
	p.Gamma = 2.2
	out := ProcessBayer(buf, w, h, 8, "RGGB", p)
	want := int(math.Pow(200.0/255.0, 1/2.2)*255 + 0.5)
	if int(out[0]) != want || int(out[1]) != want || int(out[2]) != want {
		t.Errorf("8-bit output = %d %d %d, want uniform %d", out[0], out[1], out[2], want)
	}
}

// TestProcessBayerShortBufferReturnsNil: truncated input must not panic.
func TestProcessBayerShortBufferReturnsNil(t *testing.T) {
	p := DefaultIspParams(12)
	if got := ProcessBayer(nil, 4, 4, 12, "RGGB", p); got != nil {
		t.Errorf("nil buffer should return nil, got len %d", len(got))
	}
	if got := ProcessBayer(make([]byte, 5), 4, 4, 12, "RGGB", p); got != nil {
		t.Errorf("short buffer should return nil, got len %d", len(got))
	}
	if got := ProcessBayer(make([]byte, 15), 4, 4, 8, "RGGB", p); got != nil {
		t.Errorf("short 8-bit buffer should return nil, got len %d", len(got))
	}
}

// TestComputeGrayWorldGainsSynthetic: an RGGB frame whose R sites sit at half
// the G/B level needs gainR = avgG/avgR = 2 and gainB = 1.
func TestComputeGrayWorldGainsSynthetic(t *testing.T) {
	const w, h = 8, 8
	buf := make([]byte, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			switch (y&1)*2 + (x & 1) {
			case 0: // R
				buf[y*w+x] = 100
			default: // Gr/Gb/B
				buf[y*w+x] = 200
			}
		}
	}
	p := neutralParams(255)
	gainR, gainB := ComputeGrayWorldGains(buf, w, h, 8, "RGGB", p)
	if math.Abs(gainR-2.0) > 1e-6 {
		t.Errorf("gainR = %v, want 2.0", gainR)
	}
	if math.Abs(gainB-1.0) > 1e-6 {
		t.Errorf("gainB = %v, want 1.0", gainB)
	}
}

// TestComputeGrayWorldGainsClampsAndGuards: gains clamp to 0.1..8 and broken
// inputs return the 1.0/1.0 no-op pair.
func TestComputeGrayWorldGainsClampsAndGuards(t *testing.T) {
	// Fully black frame: all means are 0, so the ratios are 0 and the gains
	// clamp at the 0.1 floor (same as the reference implementation).
	p := neutralParams(255)
	gainR, gainB := ComputeGrayWorldGains(make([]byte, 64), 8, 8, 8, "RGGB", p)
	if gainR != 0.1 || gainB != 0.1 {
		t.Errorf("black frame gains = %v/%v, want 0.1/0.1", gainR, gainB)
	}
	if gainR, gainB = ComputeGrayWorldGains(nil, 8, 8, 8, "RGGB", p); gainR != 1 || gainB != 1 {
		t.Errorf("nil buffer gains = %v/%v, want 1/1", gainR, gainB)
	}
	if gainR, gainB = ComputeGrayWorldGains(make([]byte, 3), 8, 8, 8, "RGGB", p); gainR != 1 || gainB != 1 {
		t.Errorf("short buffer gains = %v/%v, want 1/1", gainR, gainB)
	}
}

// TestApplyIspParamsNeutralIsIdentity: with unit gains, identity CCM, gamma 1
// and no tone/sharpen stages, the pass must leave the pixels untouched.
func TestApplyIspParamsNeutralIsIdentity(t *testing.T) {
	rgba := make([]byte, 16*4)
	for i := 0; i < 16; i++ {
		rgba[i*4] = byte(i * 17)
		rgba[i*4+1] = byte(255 - i*17)
		rgba[i*4+2] = byte(i * 7)
		rgba[i*4+3] = 255
	}
	orig := append([]byte(nil), rgba...)
	ApplyIspParams(rgba, 4, 4, neutralParams(255))
	for i := 0; i < len(rgba); i++ {
		if rgba[i] != orig[i] {
			t.Fatalf("byte %d changed: %d → %d", i, orig[i], rgba[i])
		}
	}
}

// TestApplyIspParamsGammaBrightensMids: gamma > 1 lifts mid-tones.
func TestApplyIspParamsGammaBrightensMids(t *testing.T) {
	rgba := []byte{64, 64, 64, 255}
	p := neutralParams(255)
	p.Gamma = 2.2
	ApplyIspParams(rgba, 1, 1, p)
	if rgba[0] <= 64 || rgba[0] != rgba[1] || rgba[1] != rgba[2] {
		t.Errorf("gamma 2.2 of 64 = %d %d %d, want equal and > 64", rgba[0], rgba[1], rgba[2])
	}
}

// TestApplyIspParamsSharpenBoostsEdgeContrast: a hard vertical edge through a
// flat field must gain local contrast (dark side darker, light side lighter)
// when the multi-band unsharp mask runs.
func TestApplyIspParamsSharpenBoostsEdgeContrast(t *testing.T) {
	const w, h = 16, 16
	rgba := make([]byte, w*h*4)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			v := byte(80)
			if x >= w/2 {
				v = 160
			}
			o := (y*w + x) * 4
			rgba[o], rgba[o+1], rgba[o+2], rgba[o+3] = v, v, v, 255
		}
	}
	orig := append([]byte(nil), rgba...)
	p := neutralParams(255)
	p.EnableSharpen = true
	p.Sharpen = 1.0
	p.SharpenRadius = 5
	ApplyIspParams(rgba, w, h, p)
	darker, lighter := false, false
	for i := 0; i < w*h; i++ {
		o := i * 4
		if rgba[o] < orig[o] {
			darker = true
		}
		if rgba[o] > orig[o] {
			lighter = true
		}
	}
	if !darker || !lighter {
		t.Errorf("sharpen changed nothing (darker=%v lighter=%v)", darker, lighter)
	}
}

// TestApplyIspParamsEmptyBufferNoPanic guards the n==0 short-circuit.
func TestApplyIspParamsEmptyBufferNoPanic(t *testing.T) {
	ApplyIspParams(nil, 0, 0, DefaultIspParams(12))
	ApplyIspParams([]byte{}, 0, 0, DefaultIspParams(12))
}
