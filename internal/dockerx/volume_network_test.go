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

// TestDequalifyVolumeName is a regression test: dequalifyVolumeName is
// documented to return "" when full doesn't belong to username's volume
// namespace, but strings.TrimPrefix alone returns its input unchanged on a
// non-match instead of "". DuplicateContainer relies on the empty string to
// skip a volume outside the target user's namespace (e.g. an admin
// duplicating another user's container) -- without the fix, such a volume's
// raw qualified name would be fed back into CreateOptions.Mounts as if it
// were a valid display name, and fail downstream with a confusing "volume
// not found" once re-qualified under the wrong user.
func TestDequalifyVolumeName(t *testing.T) {
	cases := []struct{ full, user, want string }{
		{"mudp-alice-vol-data", "alice", "data"},
		{"mudp-alice-vol-my-data", "alice", "my-data"},
		// Belongs to a different user's namespace: must not pass through.
		{"mudp-alice-vol-data", "admin", ""},
		{"mudp-bob-vol-pg", "alice", ""},
		{"notprefixed", "alice", ""},
		{"", "alice", ""},
	}
	for _, c := range cases {
		if got := dequalifyVolumeName(c.full, c.user); got != c.want {
			t.Errorf("dequalifyVolumeName(%q,%q) = %q, want %q", c.full, c.user, got, c.want)
		}
	}
}

// TestDequalifyNetworkName mirrors TestDequalifyVolumeName for networks.
func TestDequalifyNetworkName(t *testing.T) {
	cases := []struct{ full, user, want string }{
		{"mudp-alice-net-frontend", "alice", "frontend"},
		{"mudp-alice-net-frontend", "admin", ""},
		{"mudp-bob-net-backend", "alice", ""},
		{"bridge", "alice", ""},
		{"", "alice", ""},
	}
	for _, c := range cases {
		if got := dequalifyNetworkName(c.full, c.user); got != c.want {
			t.Errorf("dequalifyNetworkName(%q,%q) = %q, want %q", c.full, c.user, got, c.want)
		}
	}
}

// NetworkNameMatches gates external MCP access, so both a false negative (a
// correctly-placed container refused) and a false positive (a container on some
// other network reachable from the internet) matter.
func TestNetworkNameMatches(t *testing.T) {
	cases := []struct {
		attached, want string
		match          bool
	}{
		// A host network an operator created: the name is what it is.
		{"openwrt-lan", "openwrt-lan", true},
		{"OpenWRT-LAN", "openwrt-lan", true},
		// A mudp-managed network is namespaced on the host but admins type the
		// display name they see in the Networks view.
		{"mudp-alice-net-openwrt-lan", "openwrt-lan", true},
		{"mudp-alice-net-openwrt-lan", "lan", false},
		// Near-misses must not pass.
		{"openwrt-lan2", "openwrt-lan", false},
		{"bridge", "openwrt-lan", false},
		{"", "openwrt-lan", false},
		{"openwrt-lan", "", false},
		{"", "", false},
	}
	for _, c := range cases {
		if got := NetworkNameMatches(c.attached, c.want); got != c.match {
			t.Errorf("NetworkNameMatches(%q, %q) = %v, want %v", c.attached, c.want, got, c.match)
		}
	}
}
