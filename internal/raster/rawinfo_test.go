package raster

import (
	"os"
	"path/filepath"
	"testing"
)

const sampleSidecar = "[common]\r\n" +
	"Width=1936\r\n" +
	"Height=1552\r\n" +
	"Bits=12\r\n" +
	"Bayer=RGGB\r\n" +
	"; board dumps after BLC\r\n" +
	"BlackLevel_R=0\r\n" +
	"BlackLevel_Gr=0\r\n" +
	"BlackLevel_Gb=0\r\n" +
	"BlackLevel_B=0\r\n" +
	"\r\n" +
	"[Frame0]\r\n" +
	"ISO=4651\r\n" +
	"Exposure_Time_0=15625\r\n" +
	"A_Gain_0=1024\r\n" +
	"D_Gain_0=1024\r\n" +
	"ISP_D_Gain_0=1024\r\n" +
	"\r\n" +
	"[Frame1]\r\n" +
	"ISO=80512\r\n" +
	"ISP_D_Gain_0=5120 ; trailing comment\r\n"

func TestParseRawInfoSidecar(t *testing.T) {
	info, ok := ParseRawInfoBytes([]byte(sampleSidecar))
	if !ok {
		t.Fatal("sidecar should parse")
	}
	if info.Width != 1936 || info.Height != 1552 || info.Bits != 12 {
		t.Errorf("dims = %d %d %d, want 1936 1552 12", info.Width, info.Height, info.Bits)
	}
	if !info.HasBayer || info.Bayer != "RGGB" {
		t.Errorf("bayer = %q (has %v)", info.Bayer, info.HasBayer)
	}
	if !info.HasBlackLevel || info.BlackLevel != [4]int{0, 0, 0, 0} {
		t.Errorf("black level = %v (has %v)", info.BlackLevel, info.HasBlackLevel)
	}
	f0, has := info.Frames[0]
	if !has {
		t.Fatal("Frame0 missing")
	}
	if f0.Iso != 4651 || f0.IspDGain0 != 1024 {
		t.Errorf("frame0 = %+v", f0)
	}
	f1 := info.Frames[1]
	if f1.Iso != 80512 || f1.IspDGain0 != 5120 {
		t.Errorf("frame1 = %+v (trailing comment not stripped?)", f1)
	}
}

func TestParseRawInfoNonzeroBlackLevel(t *testing.T) {
	info, ok := ParseRawInfoBytes([]byte(
		"[common]\nBlackLevel_R=64\nBlackLevel_Gr=68\nBlackLevel_Gb=68\nBlackLevel_B=60\n"))
	if !ok {
		t.Fatal("sidecar should parse")
	}
	if info.BlackLevel != [4]int{64, 68, 68, 60} {
		t.Errorf("black level = %v", info.BlackLevel)
	}
}

func TestParseRawInfoGarbageAndEmpty(t *testing.T) {
	if _, ok := ParseRawInfoBytes([]byte("")); ok {
		t.Error("empty text must not parse")
	}
	if _, ok := ParseRawInfoBytes([]byte("no sections here\njust text\n")); ok {
		t.Error("section-less text must not parse")
	}
	// A section that is not common/FrameN is tolerated.
	if _, ok := ParseRawInfoBytes([]byte("[whatever]\nx=1\n")); !ok {
		t.Error("unknown-only sections still count as a parsed sidecar")
	}
}

func TestSidecarPathAndFileRead(t *testing.T) {
	dir := t.TempDir()
	raw := filepath.Join(dir, "dump_1936x1552_12bits_RGGB.raw")
	if err := os.WriteFile(raw, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if got := SidecarPath(raw); got != filepath.Join(dir, "dump_1936x1552_12bits_RGGB.txt") {
		t.Errorf("sidecar of .raw = %q", got)
	}
	if got := SidecarPath(filepath.Join(dir, "noext")); got != filepath.Join(dir, "noext.txt") {
		t.Errorf("sidecar of extensionless = %q", got)
	}
	// Missing sidecar reports ok=false.
	if _, ok := ParseRawInfoFile(raw); ok {
		t.Error("missing sidecar must return ok=false")
	}
	// Present sidecar round-trips through the file reader.
	txt := SidecarPath(raw)
	if err := os.WriteFile(txt, []byte(sampleSidecar), 0o644); err != nil {
		t.Fatal(err)
	}
	info, ok := ParseRawInfoFile(raw)
	if !ok || info.Frames[0].Iso != 4651 {
		t.Errorf("file-read sidecar = %+v ok=%v", info, ok)
	}
}
