package raster

import "testing"

// TestApplyIspNeutralGrayUnchangedByWhiteBalance: a perfectly neutral gray
// frame has equal R/G/B channel means, so gray-world white balance should
// apply unit gain (no color shift) — only gamma changes the value.
func TestApplyIspNeutralGrayUnchangedByWhiteBalance(t *testing.T) {
	rgba := []byte{128, 128, 128, 255, 128, 128, 128, 255}
	ApplyIsp(rgba, 1) // saturation=1: skip the saturation pass to isolate WB+gamma
	want := srgbGammaLUT[128]
	for _, p := range []int{0, 1, 2, 4, 5, 6} {
		if rgba[p] != want {
			t.Errorf("channel[%d] = %d, want %d (gamma of neutral 128)", p, rgba[p], want)
		}
	}
	if rgba[3] != 255 || rgba[7] != 255 {
		t.Errorf("alpha channel was modified")
	}
}

// TestApplyIspEmptyBufferNoPanic guards the n==0 short-circuit.
func TestApplyIspEmptyBufferNoPanic(t *testing.T) {
	ApplyIsp(nil, 1.4)
	ApplyIsp([]byte{}, 1.4)
}
