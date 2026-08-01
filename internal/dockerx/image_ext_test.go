package dockerx

import "testing"

func TestIsRawImageID(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		// Raw Docker image IDs.
		{"97dccc7c4fd8", true},                                                   // 12-char short ID
		{"sha256:97dccc7c4fd8abcdef0123456789abcdef0123456789abcdef0123456789ab", true}, // full id w/ digest prefix
		{"97dccc7c4fd8abcdef0123456789abcdef0123456789abcdef0123456789ab", true}, // 64-char full ID
		{"ABCDEF0123456789", true}, // uppercase hex, 16 chars

		// Curated display names — must NOT be flagged.
		{"asr2pass", false},
		{"neo-ml2.3.latest", false},
		{"my-app", false},
		{"ubuntu", false},
		{"mudp-ubuntu:latest", false},
		{"registry.example.com/repo:tag", false}, // registry ref: colon but has a slash host
		{"ubuntu:22.04", false},                  // name:tag (no slash, but non-hex tail)
		{"", false},
		{"deadbeef", false},   // 8 hex chars — too short to be a short ID
		{"123456789012", true}, // 12 hex chars — lower bound of a short ID
		{"12345678901", false}, // 11 hex chars — below short-ID threshold
		{"g7dccc7c4fd8", false}, // 12 chars but contains a non-hex 'g'
	}
	for _, c := range cases {
		got := IsRawImageID(c.name)
		if got != c.want {
			t.Errorf("IsRawImageID(%q) = %v, want %v", c.name, got, c.want)
		}
	}
}
