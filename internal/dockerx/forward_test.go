package dockerx

import (
	"testing"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/network"
)

func TestParsePortSpec(t *testing.T) {
	cases := []struct {
		in    string
		host  int
		guest int
		proto string
	}{
		{"10001:8080", 10001, 8080, "tcp"},
		{":8080", 0, 8080, "tcp"},
		{"8080", 0, 8080, "tcp"},
		{"10002:53/udp", 10002, 53, "udp"},
		{":51820/udp", 0, 51820, "udp"},
		{"  10003 : 22 ", 10003, 22, "tcp"},
		{"10004:5000/TCP", 10004, 5000, "tcp"},
	}
	for _, c := range cases {
		got, err := parsePortSpec(c.in)
		if err != nil {
			t.Errorf("parsePortSpec(%q) errored: %v", c.in, err)
			continue
		}
		if got.hostPort != c.host || got.containerPort != c.guest || got.proto != c.proto {
			t.Errorf("parsePortSpec(%q) = %d:%d/%s, want %d:%d/%s", c.in, got.hostPort, got.containerPort, got.proto, c.host, c.guest, c.proto)
		}
	}
}

func TestParsePortSpecRejectsGarbage(t *testing.T) {
	for _, in := range []string{"8080/sctp", "abc", "10001:", ":", "10001:70000", "70000:80", "1.2.3.4:10001:80"} {
		if _, err := parsePortSpec(in); err == nil {
			t.Errorf("parsePortSpec(%q) = nil error, want a rejection", in)
		}
	}
}

func TestParsePortSpecSkipsBlank(t *testing.T) {
	got, err := parsePortSpec("   ")
	if err != nil || !got.skip {
		t.Fatalf("parsePortSpec(blank) = %+v, %v; want skip with no error", got, err)
	}
}

func TestForwardSpecRoundTrip(t *testing.T) {
	specs := []ForwardSpec{
		{HostPort: 10001, ContainerPort: 8080, Proto: "tcp"},
		{HostPort: 10002, ContainerPort: 53, Proto: "udp"},
	}
	label := FormatForwardSpecs(specs)
	if want := "10001:8080/tcp,10002:53/udp"; label != want {
		t.Fatalf("FormatForwardSpecs = %q, want %q", label, want)
	}
	got := ParseForwardSpecs(label)
	if len(got) != 2 || got[0] != specs[0] || got[1] != specs[1] {
		t.Fatalf("ParseForwardSpecs(%q) = %+v, want %+v", label, got, specs)
	}
}

// A label that was hand-edited must not be able to hide the container's other
// forwards: the broken entry is dropped, the rest survive.
func TestParseForwardSpecsSkipsBadEntries(t *testing.T) {
	got := ParseForwardSpecs("10001:8080/tcp,nonsense,0:80/tcp,10002:99999/tcp,10003:22/sctp,10004:22/udp")
	want := []ForwardSpec{
		{HostPort: 10001, ContainerPort: 8080, Proto: "tcp"},
		{HostPort: 10004, ContainerPort: 22, Proto: "udp"},
	}
	if len(got) != len(want) {
		t.Fatalf("ParseForwardSpecs = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ParseForwardSpecs[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestParseForwardSpecsEmpty(t *testing.T) {
	if got := ParseForwardSpecs(""); len(got) != 0 {
		t.Fatalf("ParseForwardSpecs(\"\") = %+v, want none", got)
	}
}

// An admin names the network the way they read it off the Networks view, which
// for a mudp-managed network is the display name. Both forms must select it, or
// a correctly-configured forward would silently never apply.
func TestForwardNetworkFor(t *testing.T) {
	attached := []string{"mudp-alice-net-openwrt-lan"}
	cases := map[string]string{
		"openwrt-lan":                "mudp-alice-net-openwrt-lan",
		"mudp-alice-net-openwrt-lan": "mudp-alice-net-openwrt-lan",
		"other-lan":                  "",
	}
	for want, expected := range cases {
		if got := ForwardNetworkFor(attached, []string{want}); got != expected {
			t.Errorf("ForwardNetworkFor(%q) = %q, want %q", want, got, expected)
		}
	}
}

func TestForwardNetworkForNoSetting(t *testing.T) {
	if got := ForwardNetworkFor([]string{"mudp-alice-net-openwrt-lan"}, nil); got != "" {
		t.Fatalf("ForwardNetworkFor with no configured networks = %q, want empty", got)
	}
}

// A container also joined to an ordinary network (bridge, say) can have Docker
// publish its ports there normally, so mixing a forwarding network with
// anything else must fall back to Docker's own publishing rather than
// forwarding — forwarding it too would fight Docker for the same host port.
func TestForwardNetworkForMixedNetworksFallsBackToBind(t *testing.T) {
	attached := []string{"bridge", "mudp-alice-net-openwrt-lan"}
	if got := ForwardNetworkFor(attached, []string{"openwrt-lan"}); got != "" {
		t.Fatalf("ForwardNetworkFor(mixed with bridge) = %q, want empty (bind instead)", got)
	}
	// Even naming bridge itself does not help once another, unnominated network
	// is also attached — every attached network must be nominated.
	if got := ForwardNetworkFor(attached, []string{"bridge"}); got != "" {
		t.Fatalf("ForwardNetworkFor(bridge nominated, openwrt-lan not) = %q, want empty", got)
	}
}

func TestForwardHostPort(t *testing.T) {
	specs := []ForwardSpec{
		{HostPort: 10001, ContainerPort: 8080, Proto: "tcp"},
		{HostPort: 10002, ContainerPort: 8080, Proto: "udp"},
	}
	if got := forwardHostPort(specs, 8080, "tcp"); got != 10001 {
		t.Errorf("forwardHostPort(8080/tcp) = %d, want 10001", got)
	}
	if got := forwardHostPort(specs, 8080, "udp"); got != 10002 {
		t.Errorf("forwardHostPort(8080/udp) = %d, want 10002", got)
	}
	// An empty protocol means tcp, matching how Docker reports a port.
	if got := forwardHostPort(specs, 8080, ""); got != 10001 {
		t.Errorf("forwardHostPort(8080, unspecified) = %d, want 10001", got)
	}
	if got := forwardHostPort(specs, 22, "tcp"); got != 0 {
		t.Errorf("forwardHostPort(22/tcp) = %d, want 0", got)
	}
}

// The relay has to follow the address on the network the forward was made for,
// which is rarely the container's only network.
func TestForwardIPPrefersTheForwardingNetwork(t *testing.T) {
	c := types.Container{NetworkSettings: &types.SummaryNetworkSettings{Networks: map[string]*network.EndpointSettings{
		"bridge":                     {IPAddress: "172.17.0.4"},
		"mudp-alice-net-openwrt-lan": {IPAddress: "10.210.1.3"},
	}}}
	if got := forwardIP(c, "mudp-alice-net-openwrt-lan"); got != "10.210.1.3" {
		t.Fatalf("forwardIP = %q, want the openwrt-lan address", got)
	}
	// The display name resolves to the same network, so a label written before a
	// rename still finds it.
	if got := forwardIP(c, "openwrt-lan"); got != "10.210.1.3" {
		t.Fatalf("forwardIP by display name = %q, want the openwrt-lan address", got)
	}
}

// A forward whose network is gone degrades to whatever address the container
// does hold, rather than dropping the rule entirely.
func TestForwardIPFallsBackToAnyAddress(t *testing.T) {
	c := types.Container{NetworkSettings: &types.SummaryNetworkSettings{Networks: map[string]*network.EndpointSettings{
		"bridge": {IPAddress: "172.17.0.4"},
	}}}
	if got := forwardIP(c, "mudp-alice-net-openwrt-lan"); got != "172.17.0.4" {
		t.Fatalf("forwardIP = %q, want the bridge address as a fallback", got)
	}
}

// A container with no addresses at all (created but never started) yields no
// address, which the portfwd Manager reports rather than binding a dead relay.
func TestForwardIPWithoutNetworks(t *testing.T) {
	if got := forwardIP(types.Container{}, "openwrt-lan"); got != "" {
		t.Fatalf("forwardIP = %q, want empty", got)
	}
	c := types.Container{NetworkSettings: &types.SummaryNetworkSettings{Networks: map[string]*network.EndpointSettings{
		"bridge": {IPAddress: ""},
	}}}
	if got := forwardIP(c, ""); got != "" {
		t.Fatalf("forwardIP with an address-less endpoint = %q, want empty", got)
	}
}

// A container created before its network was marked for forwarding has Docker
// bindings and no label. Those bindings are what gets relayed instead, so
// turning the setting on fixes the containers that are already there.
func TestPublishedForwardSpecs(t *testing.T) {
	c := types.Container{Ports: []types.Port{
		// Docker reports one published port twice: once per address family.
		{IP: "0.0.0.0", PublicPort: 10503, PrivatePort: 80, Type: "tcp"},
		{IP: "::", PublicPort: 10503, PrivatePort: 80, Type: "tcp"},
		{IP: "0.0.0.0", PublicPort: 10504, PrivatePort: 53, Type: "udp"},
		// Exposed but not published: nothing to relay.
		{PrivatePort: 9000, Type: "tcp"},
	}}
	got := publishedForwardSpecs(c)
	want := []ForwardSpec{
		{HostPort: 10503, ContainerPort: 80, Proto: "tcp"},
		{HostPort: 10504, ContainerPort: 53, Proto: "udp"},
	}
	if len(got) != len(want) {
		t.Fatalf("publishedForwardSpecs = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("publishedForwardSpecs[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestPublishedForwardSpecsWithoutPublishedPorts(t *testing.T) {
	c := types.Container{Ports: []types.Port{{PrivatePort: 80, Type: "tcp"}}}
	if got := publishedForwardSpecs(c); len(got) != 0 {
		t.Fatalf("publishedForwardSpecs = %+v, want none", got)
	}
}

func TestContainerNetworkNames(t *testing.T) {
	c := types.Container{NetworkSettings: &types.SummaryNetworkSettings{Networks: map[string]*network.EndpointSettings{
		"bridge":                     {IPAddress: "172.17.0.4"},
		"mudp-alice-net-openwrt-lan": {IPAddress: "10.210.1.3"},
	}}}
	names := containerNetworkNames(c)
	if len(names) != 2 {
		t.Fatalf("containerNetworkNames = %v, want two entries", names)
	}
	// The adoption path feeds these straight into ForwardNetworkFor, and a
	// container also on bridge is mixed, not forwarding-only, so it falls back
	// to Docker's own publishing.
	if got := ForwardNetworkFor(names, []string{"openwrt-lan"}); got != "" {
		t.Fatalf("ForwardNetworkFor(containerNetworkNames, mixed with bridge) = %q, want empty", got)
	}
	// Drop bridge and adoption finds the forwarding network as usual.
	soloNames := containerNetworkNames(types.Container{NetworkSettings: &types.SummaryNetworkSettings{Networks: map[string]*network.EndpointSettings{
		"mudp-alice-net-openwrt-lan": {IPAddress: "10.210.1.3"},
	}}})
	if got := ForwardNetworkFor(soloNames, []string{"openwrt-lan"}); got != "mudp-alice-net-openwrt-lan" {
		t.Fatalf("ForwardNetworkFor(containerNetworkNames, forwarding-only) = %q, want the openwrt-lan network", got)
	}
	if len(containerNetworkNames(types.Container{})) != 0 {
		t.Fatal("a container with no network settings reported networks")
	}
}

func TestNetworkNameMatchesAny(t *testing.T) {
	if !NetworkNameMatchesAny("mudp-bob-net-lan", []string{"other", "lan"}) {
		t.Error("display name in the list did not match")
	}
	if NetworkNameMatchesAny("bridge", []string{"lan", "openwrt-lan"}) {
		t.Error("bridge matched a list it is not in")
	}
	if NetworkNameMatchesAny("bridge", nil) {
		t.Error("an empty list matched")
	}
}
