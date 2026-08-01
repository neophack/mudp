package server

import (
	"reflect"
	"testing"

	"mudp/internal/dockerx"
	"mudp/internal/store"
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

// TestAllowedNetworksForImage covers the create/edit narrowing: a preset's
// SelectableNetworks pool intersects with the user's attachable networks, an
// empty pool leaves the list untouched, and pool names are trimmed so a stray
// space can't hide an otherwise-valid network.
func TestAllowedNetworksForImage(t *testing.T) {
	attachable := []string{"mudp_alice_net", "mudp_bob_net", "bridge"}
	cases := []struct {
		name   string
		preset *store.ImagePreset
		want   []string
	}{
		{
			name:   "nil preset leaves list unchanged",
			preset: nil,
			want:   attachable,
		},
		{
			name:   "empty pool leaves list unchanged",
			preset: &store.ImagePreset{},
			want:   attachable,
		},
		{
			name:   "pool intersects attachable",
			preset: &store.ImagePreset{SelectableNetworks: []string{"mudp_alice_net", "bridge"}},
			want:   []string{"mudp_alice_net", "bridge"},
		},
		{
			name:   "pool excludes non-attachable networks",
			preset: &store.ImagePreset{SelectableNetworks: []string{"mudp_alice_net", "mudp_carol_net"}},
			want:   []string{"mudp_alice_net"},
		},
		{
			name:   "whitespace in pool is trimmed",
			preset: &store.ImagePreset{SelectableNetworks: []string{"  mudp_bob_net  "}},
			want:   []string{"mudp_bob_net"},
		},
		{
			name:   "pool with no overlap yields empty",
			preset: &store.ImagePreset{SelectableNetworks: []string{"mudp_zoe_net"}},
			want:   []string{},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := allowedNetworksForImage(c.preset, attachable)
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("allowedNetworksForImage = %v, want %v", got, c.want)
			}
		})
	}
}
