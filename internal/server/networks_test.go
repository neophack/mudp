package server

import (
	"testing"

	"mudp/internal/dockerx"
)

// TestResolveNetworkNameKeepsRealNames covers the identifiers that already name
// a network on the host. Namespacing any of them produces a "mudp-<user>-net-x
// not found" error on what should be a plain detail view — the bug this guards.
func TestResolveNetworkNameKeepsRealNames(t *testing.T) {
	granted := dockerx.NetworkAccess{Granted: map[string]bool{"pubtest-dockwrt": true}}
	cases := []struct {
		name   string
		raw    string
		user   string
		admin  bool
		access dockerx.NetworkAccess
		want   string
	}{
		{"own managed network by full name", "mudp-bob-net-web", "bob", false, dockerx.NetworkAccess{}, "mudp-bob-net-web"},
		{"own managed network by display name", "web", "bob", false, dockerx.NetworkAccess{}, "mudp-bob-net-web"},
		{"built-in bridge", "bridge", "bob", false, dockerx.NetworkAccess{}, "bridge"},
		{"granted host network", "pubtest-dockwrt", "bob", false, granted, "pubtest-dockwrt"},
		{"ungranted host network is namespaced for a plain user", "pubtest-dockwrt", "bob", false, dockerx.NetworkAccess{}, "mudp-bob-net-pubtest-dockwrt"},
		{"admin sees host networks under their real name", "pubtest-dockwrt", "admin", true, dockerx.NetworkAccess{}, "pubtest-dockwrt"},
		{"admin managed network is untouched", "mudp-bob-net-web", "admin", true, dockerx.NetworkAccess{}, "mudp-bob-net-web"},
		{"blank stays blank", "  ", "bob", false, dockerx.NetworkAccess{}, ""},
	}
	for _, c := range cases {
		if got := resolveNetworkName(c.raw, c.user, c.admin, c.access); got != c.want {
			t.Errorf("%s: resolveNetworkName(%q, %q, admin=%v) = %q, want %q", c.name, c.raw, c.user, c.admin, got, c.want)
		}
	}
}
