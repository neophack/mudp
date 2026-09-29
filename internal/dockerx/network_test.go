package dockerx

import (
	"strings"
	"testing"

	"github.com/docker/docker/api/types"
)

// networkEndpoint builds an endpoint resource the way Docker reports it inside
// a network inspect: the name carries a leading slash.
func networkEndpoint(name string) types.EndpointResource {
	return types.EndpointResource{Name: "/" + name}
}

// managedContainer builds a mudp-managed container summary with owner and
// display-name labels, as ContainerList reports it.
func managedContainer(id, owner, displayName string) types.Container {
	return types.Container{
		ID:     id,
		Names:  []string{"/" + FullContainerName(owner, displayName)},
		Labels: map[string]string{ManagedLabel: "true", UserLabel: owner, NameLabel: displayName},
	}
}

// TestResolveNetworkEndpoints pins the detail view's visibility rule: a
// non-admin caller sees only their own mudp-managed containers on the network
// (with display names resolved), while another user's containers and unmanaged
// host containers must not leak their names, IDs, or addresses. Admins see
// every endpoint, managed ones under their display names.
func TestResolveNetworkEndpoints(t *testing.T) {
	endpoints := map[string]types.EndpointResource{
		"id-mine":     networkEndpoint(FullContainerName("bob", "web")),
		"id-foreign":  networkEndpoint(FullContainerName("alice", "pg")),
		"id-hostapp":  networkEndpoint("thirdparty-cache"),
		"id-mine-old": networkEndpoint(FullContainerName("bob", "worker")),
	}
	managed := []types.Container{
		managedContainer("id-mine", "bob", "web"),
		managedContainer("id-foreign", "alice", "pg"),
		managedContainer("id-mine-old", "bob", "worker"),
	}

	userView := resolveNetworkEndpoints(endpoints, managed, "bob", false)
	if len(userView) != 2 {
		t.Fatalf("non-admin: got %d containers, want 2 (only their own): %+v", len(userView), userView)
	}
	for _, c := range userView {
		if c.ID == "id-foreign" || c.ID == "id-hostapp" {
			t.Errorf("non-admin view leaks container %s (%s)", c.ID, c.Name)
		}
		if c.Name == FullContainerName("bob", "web") || c.Name == FullContainerName("bob", "worker") {
			t.Errorf("non-admin view shows the raw full name %q instead of the display name", c.Name)
		}
	}
	byID := map[string]NetworkContainer{}
	for _, c := range userView {
		byID[c.ID] = c
	}
	if c := byID["id-mine"]; c.Name != "web" || c.IPv4 != "" {
		t.Errorf("own container resolved wrong: %+v", c)
	}
	if c := byID["id-mine-old"]; c.Name != "worker" {
		t.Errorf("own container resolved wrong: %+v", c)
	}

	adminView := resolveNetworkEndpoints(endpoints, managed, "bob", true)
	if len(adminView) != 4 {
		t.Fatalf("admin: got %d containers, want 4: %+v", len(adminView), adminView)
	}
	byID = map[string]NetworkContainer{}
	for _, c := range adminView {
		byID[c.ID] = c
	}
	if c := byID["id-foreign"]; c.Name != "pg" {
		t.Errorf("admin view should resolve the other user's container to its display name: %+v", c)
	}
	if c := byID["id-hostapp"]; c.Name != "thirdparty-cache" {
		t.Errorf("admin view should keep the unmanaged container's real name: %+v", c)
	}
}

// TestVisibleEndpointCount pins the Networks list rule behind the detail view:
// admins count every endpoint, everyone else counts only their own
// mudp-managed containers, so a shared network's row does not advertise other
// users' containers.
func TestVisibleEndpointCount(t *testing.T) {
	endpoints := map[string]types.EndpointResource{
		"a": networkEndpoint(FullContainerName("bob", "web")),
		"b": networkEndpoint(FullContainerName("alice", "pg")),
		"c": networkEndpoint("thirdparty-cache"),
	}
	if got := visibleEndpointCount(endpoints, "bob", true); got != 3 {
		t.Errorf("admin count = %d, want 3", got)
	}
	if got := visibleEndpointCount(endpoints, "bob", false); got != 1 {
		t.Errorf("non-admin count = %d, want 1 (only their own container)", got)
	}
	if got := visibleEndpointCount(endpoints, "nobody", false); got != 0 {
		t.Errorf("non-member count = %d, want 0", got)
	}
	if got := visibleEndpointCount(nil, "bob", true); got != 0 {
		t.Errorf("empty count = %d, want 0", got)
	}
}

// TestSortNetworkContainers checks the display ordering stays stable after
// display-name resolution (which can reorder the raw names).
func TestSortNetworkContainers(t *testing.T) {
	cs := []NetworkContainer{
		{Name: "worker"}, {Name: "api"}, {Name: "db"},
	}
	sortNetworkContainers(cs)
	if got := strings.Join([]string{cs[0].Name, cs[1].Name, cs[2].Name}, " "); got != "api db worker" {
		t.Errorf("sort order = %q", got)
	}
}
