package raster

import "testing"

// TestApplyIspNeutralGrayUnchangedByWhiteBalance: a perfectly neutral gray
// frame has equal R/G/B channel means, so gray-world white balance should
// apply unit gain (no color shift) — only gamma changes the value.
func TestApplyIspNeutralGrayUnchangedByWhiteBalance(t *testing.T) {
	rgba := []byte{128, 128, 128, 255, 128, 128, 128, 255}
	ApplyIsp(rgba, 1) // saturation=1: skip the saturation pass to isolate WB+gamma
	// Literal, hand-derived from isp.go's LUT formula (standard sRGB):
	// 128/255 = 0.50196 → 1.055·0.50196^(1/2.4) − 0.055 = 0.73665 →
	// round(0.73665·255) = 188. Referencing srgbGammaLUT here instead would
	// make the assertion a tautology: a wrong LUT would be compared to itself.
	const want = 188
	for _, p := range []int{0, 1, 2, 4, 5, 6} {
		if rgba[p] != want {
			t.Errorf("channel[%d] = %d, want %d (gamma of neutral 128)", p, rgba[p], want)
		}
	}
	if rgba[3] != 255 || rgba[7] != 255 {
		t.Errorf("alpha channel was modified")
	}
}

// TestApplyIspColorCastWhiteBalance: a red-cast two-pixel frame (channel
// means R160/G80/B48) must get the gray-world gains gainR = 96/160 = 0.6,
// gainG = 96/80 = 1.2, gainB = 96/48 = 2.0 — hand-derived from isp.go's Pass
// 1 (avg = (320+160+96)/6 = 96). Both pixels share the same 200:100:60
// channel ratio, so correct white balance lands them on exact neutral grays
// (120 and 72) and sRGB gamma maps those to round(1.055·(120/255)^(1/2.4)·255
// − 0.055·255 …) = 182 and 145. Hardcoding those literals catches a gain
// applied to the wrong channel (200·1.2 = 240 would encode as 248, not 182),
// which the neutral-gray test above cannot see.
func TestApplyIspColorCastWhiteBalance(t *testing.T) {
	rgba := []byte{
		200, 100, 60, 255,
		120, 60, 36, 255,
	}
	ApplyIsp(rgba, 1)
	want := []byte{
		182, 182, 182, 255,
		145, 145, 145, 255,
	}
	for i := range want {
		if rgba[i] != want[i] {
			t.Errorf("byte[%d] = %d, want %d", i, rgba[i], want[i])
		}
	}
}

// TestApplyIspEmptyBufferNoPanic guards the n==0 short-circuit.
func TestApplyIspEmptyBufferNoPanic(t *testing.T) {
	ApplyIsp(nil, 1.4)
	ApplyIsp([]byte{}, 1.4)
}
