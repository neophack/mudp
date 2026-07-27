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

// TestNetworkAccessMayUseSystem pins the rule for Docker's built-ins: only
// bridge is ever attachable, it stays open while nobody has claimed it with a
// grant, and once grants exist it belongs to those groups (plus admins).
func TestNetworkAccessMayUseSystem(t *testing.T) {
	open := NetworkAccess{}
	restricted := NetworkAccess{Restricted: map[string]bool{"bridge": true}}
	member := NetworkAccess{
		Granted:    map[string]bool{"bridge": true},
		Restricted: map[string]bool{"bridge": true},
	}
	cases := []struct {
		name   string
		access NetworkAccess
		net    string
		admin  bool
		want   bool
	}{
		{"bridge is open with no grants", open, "bridge", false, true},
		{"bridge closes once granted to a group", restricted, "bridge", false, false},
		{"a granted member keeps bridge", member, "bridge", false, true},
		{"admins keep bridge regardless", restricted, "bridge", true, true},
		{"host is never attachable", open, "host", true, false},
		{"none is never attachable", open, "none", true, false},
		{"non-system names are not decided here", open, "mudp-bob-net-web", true, false},
	}
	for _, c := range cases {
		if got := c.access.MayUseSystem(c.net, c.admin); got != c.want {
			t.Errorf("%s: MayUseSystem(%q, admin=%v) = %v, want %v", c.name, c.net, c.admin, got, c.want)
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
