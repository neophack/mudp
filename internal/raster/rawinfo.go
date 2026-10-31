package raster

import (
	"os"
	"strconv"
	"strings"
)

// RAW sidecar metadata (.txt), ported from the reference capture tool's
// RawInfoFile.cpp. The capture pipeline drops an INI file next to every dump
// carrying the sensor's per-frame state; the viewer uses it to resolve the
// ISP parameters that vary with gain (ISO tier, ISP digital gain, black
// level) exactly like the reference viewer does when it opens a file:
//
//	[common]
//	Width=1936  Height=1552  Bits=12  Bayer=RGGB
//	BlackLevel_R=0  BlackLevel_Gr=0  BlackLevel_Gb=0  BlackLevel_B=0
//	[Frame0]
//	ISO=4651  Exposure_Time_0=...  A_Gain_0=...  D_Gain_0=...
//	ISP_D_Gain_0=1024  Ratio_0=...

// RawFrameInfo is one [FrameN] section of the sidecar. Only the fields the
// viewer consumes are kept.
type RawFrameInfo struct {
	Iso       int64
	IspDGain0 int64
}

// RawInfo is the parsed sidecar. Frames is keyed by frame index; HasBlackLevel
// is true when any BlackLevel_* key was present.
type RawInfo struct {
	Width, Height, Bits int
	Bayer               string
	HasBayer            bool
	BlackLevel          [4]int // R, Gr, Gb, B
	HasBlackLevel       bool
	Frames              map[int]RawFrameInfo
}

// SidecarPath returns path with its extension replaced by ".txt" — the
// capture tool's naming convention (RawDocument::open).
func SidecarPath(path string) string {
	dot := strings.LastIndexByte(path, '.')
	slash := strings.LastIndexAny(path, `/\`)
	if dot > slash+1 {
		return path[:dot] + ".txt"
	}
	return path + ".txt"
}

// ParseRawInfoFile reads and parses the .txt sidecar for a raw dump. A missing
// or unparsable file returns ok=false (the caller then falls back to the
// no-sidecar defaults, exactly like the reference's hasInfo=false path).
func ParseRawInfoFile(path string) (info RawInfo, ok bool) {
	data, err := os.ReadFile(SidecarPath(path))
	if err != nil {
		return RawInfo{}, false
	}
	return ParseRawInfoBytes(data)
}

// ParseRawInfoBytes parses sidecar bytes (the server reads them through its
// symlink-checked file opener and hands the bytes here).
func ParseRawInfoBytes(data []byte) (RawInfo, bool) {
	return parseRawInfo(string(data))
}

// parseRawInfo parses the sidecar text into sections. ok is false when the
// text contains no section at all (treated as "no sidecar").
func parseRawInfo(text string) (RawInfo, bool) {
	sections := map[string]map[string]string{}
	section := ""
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if line == "" || line[0] == ';' || line[0] == '#' {
			continue
		}
		if line[0] == '[' && line[len(line)-1] == ']' {
			section = strings.TrimSpace(line[1 : len(line)-1])
			if sections[section] == nil {
				sections[section] = map[string]string{}
			}
			continue
		}
		eq := strings.IndexByte(line, '=')
		if eq <= 0 {
			continue
		}
		key := strings.TrimSpace(line[:eq])
		val := strings.TrimSpace(line[eq+1:])
		if sc := strings.IndexByte(val, ';'); sc >= 0 { // trailing comment
			val = strings.TrimSpace(val[:sc])
		}
		if sections[section] == nil {
			sections[section] = map[string]string{}
		}
		sections[section][key] = val
	}
	if len(sections) == 0 {
		return RawInfo{}, false
	}

	getInt := func(m map[string]string, key string, def int) int {
		if v, err := strconv.Atoi(strings.TrimSpace(m[key])); err == nil {
			return v
		}
		return def
	}
	var info RawInfo
	if common := sections["common"]; common != nil {
		info.Width = getInt(common, "Width", -1)
		info.Height = getInt(common, "Height", -1)
		info.Bits = getInt(common, "Bits", -1)
		switch bayer := strings.ToUpper(strings.TrimSpace(common["Bayer"])); bayer {
		case "RGGB", "BGGR", "GRBG", "GBRG":
			info.Bayer = bayer
			info.HasBayer = true
		}
		if _, ok := common["BlackLevel_R"]; ok {
			info.HasBlackLevel = true
		}
		if _, ok := common["BlackLevel_Gr"]; ok {
			info.HasBlackLevel = true
		}
		if _, ok := common["BlackLevel_Gb"]; ok {
			info.HasBlackLevel = true
		}
		if _, ok := common["BlackLevel_B"]; ok {
			info.HasBlackLevel = true
		}
		if info.HasBlackLevel {
			info.BlackLevel = [4]int{
				getInt(common, "BlackLevel_R", 0),
				getInt(common, "BlackLevel_Gr", 0),
				getInt(common, "BlackLevel_Gb", 0),
				getInt(common, "BlackLevel_B", 0),
			}
		}
	}
	for name, m := range sections {
		if !strings.HasPrefix(name, "Frame") {
			continue
		}
		idx, err := strconv.Atoi(strings.TrimSpace(name[len("Frame"):]))
		if err != nil {
			continue
		}
		iso := int64(getInt(m, "ISO", -1))
		dgain := int64(-1)
		if v, err := strconv.ParseInt(strings.TrimSpace(m["ISP_D_Gain_0"]), 10, 64); err == nil {
			dgain = v
		}
		if info.Frames == nil {
			info.Frames = map[int]RawFrameInfo{}
		}
		info.Frames[idx] = RawFrameInfo{Iso: iso, IspDGain0: dgain}
	}
	return info, true
}
