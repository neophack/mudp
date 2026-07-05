package dockerx

import (
	"testing"
)

func TestVolumeFullName(t *testing.T) {
	cases := []struct{ user, name, want string }{
		{"alice", "data", "mudp-alice-vol-data"},
		{"Alice", "My Data!", "mudp-alice-vol-my-data"},
		{"bob", "pg", "mudp-bob-vol-pg"},
	}
	for _, c := range cases {
		if got := VolumeFullName(c.user, c.name); got != c.want {
			t.Errorf("VolumeFullName(%q,%q) = %q, want %q", c.user, c.name, got, c.want)
		}
	}
}

func TestNetworkFullName(t *testing.T) {
	cases := []struct{ user, name, want string }{
		{"alice", "frontend", "mudp-alice-net-frontend"},
		{"Bob", "Back End", "mudp-bob-net-back-end"},
	}
	for _, c := range cases {
		if got := NetworkFullName(c.user, c.name); got != c.want {
			t.Errorf("NetworkFullName(%q,%q) = %q, want %q", c.user, c.name, got, c.want)
		}
	}
}

func TestLabelToDisplay(t *testing.T) {
	cases := []struct{ in, want string }{
		{"mudp-alice-vol-data", "data"},
		{"mudp-bob-vol-pg", "pg"},
		{"notprefixed", "notprefixed"},
		{"", ""},
	}
	for _, c := range cases {
		if got := labelToDisplay(c.in); got != c.want {
			t.Errorf("labelToDisplay(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestNetLabelToDisplay(t *testing.T) {
	cases := []struct{ in, want string }{
		{"mudp-alice-net-frontend", "frontend"},
		{"mudp-bob-net-back-end", "back-end"},
		{"custom", "custom"},
	}
	for _, c := range cases {
		if got := netLabelToDisplay(c.in); got != c.want {
			t.Errorf("netLabelToDisplay(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
