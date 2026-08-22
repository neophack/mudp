package version

import "testing"

func TestCompare(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"v1.0", "v1.0", 0},
		{"1.0", "v1.0.0", 0},
		{"v1.0.1", "v1.0", 1},
		{"v1.0", "v1.0.1", -1},
		{"v1.10.0", "v1.9.0", 1},
		{"v2.0", "v10.0", -1},
		// git-describe suffix means commits on top of the tag, so newer.
		{"v1.0-5-gabc", "v1.0", 1},
		{"v1.0-3-gabc", "v1.0-20-gdef", -1},
		// dev builds are older than any tag.
		{"dev", "v0.1", -1},
		{"v0.1", "dev", 1},
		{"dev", "dev", 0},
	}
	for _, c := range cases {
		if got := Compare(c.a, c.b); got != c.want {
			t.Errorf("Compare(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}
