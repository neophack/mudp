package raster

import (
	"reflect"
	"testing"
)

// TestBayerDecodeFlatField: a flat field (each CFA color constant across the
// whole frame) is the simplest correctness check for the bilinear demosaic.
// Interior pixels (at least one sample away from every edge) reconstruct
// exactly, since every neighbor they average is a real same-color sample; the
// outermost ring instead averages in edge-clamped duplicates of the wrong
// color, so this test deliberately only checks the interior. Ported from
// web/tests/unit/raw.test.js.
func TestBayerDecodeFlatField(t *testing.T) {
	// 4x4 RGGB, 8-bit, one byte per sample:
	//   R G R G      200 100 200 100
	//   G B G B  =   100  50 100  50
	//   R G R G      200 100 200 100
	//   G B G B      100  50 100  50
	buf := []byte{
		200, 100, 200, 100,
		100, 50, 100, 50,
		200, 100, 200, 100,
		100, 50, 100, 50,
	}
	at := func(rgba []byte, w, x, y int) []byte {
		o := (y*w + x) * 4
		return rgba[o : o+4]
	}

	t.Run("reconstructs a flat field to the same RGB at every interior pixel", func(t *testing.T) {
		rgba := BayerDecode(buf, 4, 4, 8, "RGGB", 0, 4)
		want := []byte{200, 100, 50, 255}
		for _, y := range []int{1, 2} {
			for _, x := range []int{1, 2} {
				if got := at(rgba, 4, x, y); !reflect.DeepEqual(got, want) {
					t.Errorf("(%d,%d) = %v, want %v", x, y, got, want)
				}
			}
		}
	})

	t.Run("swaps R/B reconstruction for BGGR", func(t *testing.T) {
		// Same buffer, reinterpreted as BGGR: the CFA sites that were R
		// become B and vice versa, so the flat-field R/B constants swap; G is
		// unaffected.
		rgba := BayerDecode(buf, 4, 4, 8, "BGGR", 0, 4)
		want := []byte{50, 100, 200, 255}
		for _, y := range []int{1, 2} {
			for _, x := range []int{1, 2} {
				if got := at(rgba, 4, x, y); !reflect.DeepEqual(got, want) {
					t.Errorf("(%d,%d) = %v, want %v", x, y, got, want)
				}
			}
		}
	})
}

func TestBayerDecode16BitLittleEndian(t *testing.T) {
	// 2x2 RGGB at 16-bit: R=1000 (0,0), G=500 (0,1)/(1,0), B=100 (1,1).
	buf := []byte{
		0xe8, 0x03, // 1000 (R)
		0xf4, 0x01, // 500 (G)
		0xf4, 0x01, // 500 (G)
		0x64, 0x00, // 100 (B)
	}
	rgba := BayerDecode(buf, 2, 2, 16, "RGGB", 0, 2)
	const maxVal = (1 << 16) - 1
	// Only the CFA site's own channel is read directly (unaveraged), so it's
	// the one value exact regardless of edge-clamping at the other channels.
	wantR := clampByte(1000.0 / maxVal * 255)
	wantB := clampByte(100.0 / maxVal * 255)
	if rgba[0] != wantR { // R at (0,0)
		t.Errorf("R at (0,0) = %d, want %d", rgba[0], wantR)
	}
	if got := rgba[4*3+2]; got != wantB { // B at (1,1)
		t.Errorf("B at (1,1) = %d, want %d", got, wantB)
	}
}

// TestBayerDecodeRowBandsMatchFullDecode: a caller decoding row bands (one
// call per worker in the browser's worker pool) must get byte-identical
// output to decoding the whole frame at once. Split lands on an odd row (3)
// so an asymmetric, non-2-aligned band boundary is actually exercised. Ported
// from web/tests/unit/raw.test.js.
func TestBayerDecodeRowBandsMatchFullDecode(t *testing.T) {
	const width, height = 6, 6
	// Deterministic non-flat data so a band-boundary bug (e.g. using a
	// relative instead of absolute y) would change pixel values, not just
	// get masked by every row looking the same.
	full := make([]byte, width*height)
	for i := range full {
		full[i] = byte((i*17 + 5) % 256)
	}

	whole := BayerDecode(full, width, height, 8, "RGGB", 0, height)
	bandA := BayerDecode(full, width, height, 8, "RGGB", 0, 3)
	bandB := BayerDecode(full, width, height, 8, "RGGB", 3, 6)

	stitched := make([]byte, width*height*4)
	copy(stitched, bandA)
	copy(stitched[3*width*4:], bandB)

	if !reflect.DeepEqual(stitched, whole) {
		t.Errorf("stitched row bands != whole-frame decode")
	}
}
