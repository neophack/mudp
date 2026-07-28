package dockerx

// mudp-side port forwarding. On a host where Docker cannot publish a port —
// an OpenWrt box whose firewall owns the NAT tables, where `-p 10001:8080`
// either fails to bind or is silently bypassed — a container on the LAN network
// is still reachable at its own address (`curl 10.210.1.3:8080`). What is
// missing is only the host port, so mudp provides it itself: it allocates the
// same port out of the owner's range and relays it to the container's address
// in-process (see internal/portfwd).
//
// Which networks work that way is an administrator's decision, because it is a
// property of the host's networking, not of the container. For a container on a
// forwarding network mudp records the intended mapping in a label instead of
// asking Docker to publish it, and the label is the source of truth from then
// on: it survives restarts, it is visible in `docker inspect`, and the
// reconciler re-reads it (and the container's current address) on every sweep,
// so a container that comes back on a new IP keeps the same host port.

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"

	"mudp/internal/portfwd"
)

const (
	// ForwardPortsLabel records the host→container mappings mudp relays itself,
	// as a comma-separated list of "hostPort:containerPort/proto".
	ForwardPortsLabel = "mudp.forward.ports"
	// ForwardNetLabel records the Docker network whose address the forward
	// targets. A container is usually on more than one network; this says which
	// one's IP the relay must follow.
	ForwardNetLabel = "mudp.forward.net"
)

// ForwardSpec is one host→container mapping served by mudp rather than Docker.
type ForwardSpec struct {
	HostPort      int
	ContainerPort int
	Proto         string
}

// String renders a spec the way it is stored in the label and shown in the UI:
// "10001:8080/tcp".
func (s ForwardSpec) String() string {
	proto := s.Proto
	if proto == "" {
		proto = "tcp"
	}
	return fmt.Sprintf("%d:%d/%s", s.HostPort, s.ContainerPort, proto)
}

// FormatForwardSpecs renders specs into the label value.
func FormatForwardSpecs(specs []ForwardSpec) string {
	parts := make([]string, 0, len(specs))
	for _, s := range specs {
		parts = append(parts, s.String())
	}
	return strings.Join(parts, ",")
}

// ParseForwardSpecs reads the label value back. Entries that do not parse are
// skipped rather than failing the whole container: a hand-edited label must not
// be able to hide every other forward the container has.
func ParseForwardSpecs(label string) []ForwardSpec {
	var out []ForwardSpec
	for _, raw := range strings.Split(label, ",") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		proto := "tcp"
		if i := strings.LastIndex(raw, "/"); i >= 0 {
			proto = strings.ToLower(strings.TrimSpace(raw[i+1:]))
			raw = raw[:i]
		}
		if proto != "tcp" && proto != "udp" {
			continue
		}
		host, containerPort, ok := strings.Cut(raw, ":")
		if !ok {
			continue
		}
		hp, err := strconv.Atoi(strings.TrimSpace(host))
		if err != nil || hp < 1 || hp > 65535 {
			continue
		}
		cp, err := strconv.Atoi(strings.TrimSpace(containerPort))
		if err != nil || cp < 1 || cp > 65535 {
			continue
		}
		out = append(out, ForwardSpec{HostPort: hp, ContainerPort: cp, Proto: proto})
	}
	return out
}

// portSpec is one parsed line of the create form's port field.
type portSpec struct {
	// hostPort is 0 when the user left the host side out, meaning "allocate one
	// from my range".
	hostPort      int
	containerPort int
	proto         string
	// skip marks a blank line, which is not an error — the field is a textarea.
	skip bool
}

// parsePortSpec accepts "host:container", ":container" and "container", each
// with an optional "/tcp" or "/udp" suffix (default tcp).
//
// The protocol suffix matters more than it used to: a mudp forward relays UDP
// as readily as TCP, so a container's DNS, QUIC, or VPN port is now something
// the create form can express instead of something arranged outside mudp.
func parsePortSpec(spec string) (portSpec, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return portSpec{skip: true}, nil
	}
	proto := "tcp"
	if i := strings.LastIndex(spec, "/"); i >= 0 {
		proto = strings.ToLower(strings.TrimSpace(spec[i+1:]))
		spec = strings.TrimSpace(spec[:i])
		if proto != "tcp" && proto != "udp" {
			return portSpec{}, fmt.Errorf("port protocol must be tcp or udp, got %q", proto)
		}
	}
	hostRaw, containerRaw := "", spec
	if h, c, ok := strings.Cut(spec, ":"); ok {
		hostRaw, containerRaw = strings.TrimSpace(h), strings.TrimSpace(c)
	}
	containerPort, err := strconv.Atoi(containerRaw)
	if err != nil || containerPort < 1 || containerPort > 65535 {
		return portSpec{}, fmt.Errorf("port %q must be host:container, :container, or container", spec)
	}
	out := portSpec{containerPort: containerPort, proto: proto}
	if hostRaw == "" {
		return out, nil
	}
	hostPort, err := strconv.Atoi(hostRaw)
	if err != nil || hostPort < 1 || hostPort > 65535 {
		return portSpec{}, fmt.Errorf("invalid host port %q", hostRaw)
	}
	out.hostPort = hostPort
	return out, nil
}

// ForwardNetworkFor picks the network a container should be forwarded on: one
// of its attached networks that the administrator nominated for forwarding,
// but only when *every* attached network is one — a container also joined to
// bridge (or any other ordinary network) can have Docker publish its ports
// there just fine, so forwarding it too would fight the same host port two
// ways. Empty means Docker publishing applies, either because nothing here is
// nominated or because the container is not forwarding-only.
//
// Matching goes through NetworkNameMatches, so an admin can nominate either the
// display name they read off the Networks view ("openwrt-lan") or the full
// Docker name ("mudp-alice-net-openwrt-lan").
func ForwardNetworkFor(attached, forwardNetworks []string) string {
	if len(attached) == 0 || len(forwardNetworks) == 0 {
		return ""
	}
	match := ""
	for _, name := range attached {
		if !NetworkNameMatchesAny(name, forwardNetworks) {
			return ""
		}
		if match == "" {
			match = name
		}
	}
	return match
}

// NetworkNameMatchesAny reports whether a network name matches any of the
// administrator's nominated names, in either the display or the full form.
func NetworkNameMatchesAny(name string, want []string) bool {
	for _, w := range want {
		if NetworkNameMatches(name, w) {
			return true
		}
	}
	return false
}

// EffectiveForwardSpecs resolves what mudp actually relays for a container:
// the mapping recorded in its forward label when it was created on a
// forwarding network, or — for a container that reached that network some
// other way, such as being switched onto it after creation via network
// settings — its current Docker port bindings adopted as forward specs. Both
// ForwardRules (what is actually relayed) and the container list (what the UI
// shows) must agree on this, or a container moved onto a forwarding network
// after the fact keeps showing its stale bridge-style binding, or none at all,
// even though mudp is in fact relaying it.
func EffectiveForwardSpecs(c types.Container, forwardNetworks []string) (network string, specs []ForwardSpec) {
	network = c.Labels[ForwardNetLabel]
	specs = ParseForwardSpecs(c.Labels[ForwardPortsLabel])
	if len(specs) > 0 {
		return network, specs
	}
	network = ForwardNetworkFor(containerNetworkNames(c), forwardNetworks)
	if network == "" {
		return "", nil
	}
	return network, publishedForwardSpecs(c)
}

// ForwardRules builds the live forwarding rules from container state: every
// running managed container that should be forwarded, resolved to the address
// it currently holds on its forwarding network.
//
// Two kinds of container qualify. One carries a forward label, written when it
// was created on a forwarding network. The other was created *before* the
// administrator marked its network — it holds ordinary Docker port bindings
// that this host cannot make work — and is adopted: the same host ports are
// relayed instead. Without adoption, turning the setting on would do nothing
// until every existing container had been recreated, which on the hosts this
// feature exists for means every container is broken until then. If Docker's
// own proxy does hold the port, the bind fails and is reported; nothing is
// taken over silently.
//
// Stopped containers produce no rules — they have no address to relay to — so a
// stopped container releases its host port and reclaims it when it starts
// again. The host port itself stays reserved for it either way, because
// managedHostPorts reads the same label off stopped containers.
func (d *Client) ForwardRules(ctx context.Context, forwardNetworks []string) ([]portfwd.Rule, error) {
	args := filters.NewArgs(filters.Arg("label", ManagedLabel+"=true"))
	list, err := d.c.ContainerList(ctx, container.ListOptions{Filters: args})
	if err != nil {
		return nil, err
	}
	var out []portfwd.Rule
	for _, c := range list {
		if c.Labels[ManagedLabel] != "true" {
			continue
		}
		network, specs := EffectiveForwardSpecs(c, forwardNetworks)
		if len(specs) == 0 {
			continue
		}
		ip := forwardIP(c, network)
		name := c.Labels[NameLabel]
		if name == "" && len(c.Names) > 0 {
			name = strings.TrimPrefix(c.Names[0], "/")
		}
		for _, s := range specs {
			out = append(out, portfwd.Rule{
				HostPort:   s.HostPort,
				Proto:      s.Proto,
				TargetIP:   ip,
				TargetPort: s.ContainerPort,
				Container:  c.ID,
				Owner:      c.Labels[UserLabel],
				Name:       name,
				Source:     "container",
			})
		}
	}
	return out, nil
}

// ContainerTarget is a managed container an administrator can point a manual
// forward at: its current address, and enough identity to choose it from a list.
type ContainerTarget struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	FullName string   `json:"fullName"`
	Owner    string   `json:"owner"`
	IP       string   `json:"ip"`
	State    string   `json:"state"`
	Networks []string `json:"networks,omitempty"`
	// Ports are the container ports the image exposes or the container
	// publishes, offered as suggestions when picking what to forward to.
	Ports []string `json:"ports,omitempty"`
}

// ContainerTargets lists the managed containers a manual forward can name, with
// the address each one currently holds. Stopped containers are included (with
// no address) so a forward can be prepared for one before it is started; the
// relay picks up the address when it appears.
func (d *Client) ContainerTargets(ctx context.Context) ([]ContainerTarget, error) {
	args := filters.NewArgs(filters.Arg("label", ManagedLabel+"=true"))
	list, err := d.c.ContainerList(ctx, container.ListOptions{All: true, Filters: args})
	if err != nil {
		return nil, err
	}
	out := make([]ContainerTarget, 0, len(list))
	for _, c := range list {
		full := ""
		if len(c.Names) > 0 {
			full = strings.TrimPrefix(c.Names[0], "/")
		}
		name := c.Labels[NameLabel]
		if name == "" {
			name = full
		}
		t := ContainerTarget{
			ID: c.ID, Name: name, FullName: full, Owner: c.Labels[UserLabel],
			IP: forwardIP(c, c.Labels[ForwardNetLabel]), State: c.State,
			Networks: containerNetworkNames(c),
		}
		seen := map[string]bool{}
		for _, p := range c.Ports {
			if p.PrivatePort == 0 {
				continue
			}
			proto := p.Type
			if proto == "" {
				proto = "tcp"
			}
			spec := fmt.Sprintf("%d/%s", p.PrivatePort, proto)
			if !seen[spec] {
				seen[spec] = true
				t.Ports = append(t.Ports, spec)
			}
		}
		out = append(out, t)
	}
	return out, nil
}

// containerNetworkNames lists the Docker networks a listed container is on.
func containerNetworkNames(c types.Container) []string {
	if c.NetworkSettings == nil {
		return nil
	}
	out := make([]string, 0, len(c.NetworkSettings.Networks))
	for name := range c.NetworkSettings.Networks {
		out = append(out, name)
	}
	return out
}

// publishedForwardSpecs turns a container's Docker port bindings into forward
// specs, so a container that predates the forwarding setting keeps the host
// ports it was already assigned. Docker reports one entry per binding (IPv4 and
// IPv6 for the same mapping), so identical mappings are collapsed.
func publishedForwardSpecs(c types.Container) []ForwardSpec {
	var out []ForwardSpec
	seen := map[ForwardSpec]bool{}
	for _, p := range c.Ports {
		if p.PublicPort == 0 || p.PrivatePort == 0 {
			continue
		}
		proto := p.Type
		if proto == "" {
			proto = "tcp"
		}
		if proto != "tcp" && proto != "udp" {
			continue
		}
		s := ForwardSpec{HostPort: int(p.PublicPort), ContainerPort: int(p.PrivatePort), Proto: proto}
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

// forwardIP resolves the address to relay to: the container's IP on the network
// the forward was created for, or — when that network is gone or was renamed —
// any address it does have, so a forward degrades to "still reachable" instead
// of breaking outright.
func forwardIP(c types.Container, network string) string {
	if c.NetworkSettings == nil {
		return ""
	}
	if network != "" {
		if ep, ok := c.NetworkSettings.Networks[network]; ok && ep != nil && ep.IPAddress != "" {
			return ep.IPAddress
		}
		for name, ep := range c.NetworkSettings.Networks {
			if ep != nil && ep.IPAddress != "" && NetworkNameMatches(name, network) {
				return ep.IPAddress
			}
		}
	}
	for _, ep := range c.NetworkSettings.Networks {
		if ep != nil && ep.IPAddress != "" {
			return ep.IPAddress
		}
	}
	return ""
}

// forwardHostPort returns the host port a container's label assigns to one
// container port, or 0 when that port is not forwarded. Used to render the
// mapping in the container list and detail views, where Docker itself reports
// the port as merely exposed.
func forwardHostPort(specs []ForwardSpec, containerPort int, proto string) int {
	if proto == "" {
		proto = "tcp"
	}
	for _, s := range specs {
		if s.ContainerPort == containerPort && s.Proto == proto {
			return s.HostPort
		}
	}
	return 0
}
