package dockerx

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/network"
)

// Network is the UI-facing network record.
type Network struct {
	Name       string            `json:"name"`
	FullName   string            `json:"fullName"`
	ID         string            `json:"id"`
	Driver     string            `json:"driver"`
	Scope      string            `json:"scope"`
	Subnet     string            `json:"subnet"`
	Gateway    string            `json:"gateway,omitempty"`
	IPRange    string            `json:"ipRange,omitempty"`
	IPv6       bool              `json:"ipv6,omitempty"`
	Containers int               `json:"containers"`
	Labels     map[string]string `json:"labels"`
	Owner      string            `json:"owner,omitempty"`
	CreatedAt  string            `json:"createdAt"`
	// System marks Docker's built-in networks (bridge, host, none, etc.) which
	// are shown read-only alongside the user's mudp-managed networks.
	System bool `json:"system,omitempty"`
	// Managed marks a network mudp created (and therefore knows the owner of).
	Managed bool `json:"managed,omitempty"`
	// External marks a network that already exists on the host but was not
	// created by mudp — e.g. one an operator made with `docker network create`
	// or that came with a third-party compose project. Admins always see these;
	// other users see one only when it has been granted to them.
	External bool `json:"external,omitempty"`
	// Shared marks a network the caller can see only because an administrator
	// granted it to them, rather than because they own it.
	Shared bool `json:"shared,omitempty"`
	// Attachable reports whether the caller may attach a container to this
	// network. host/none are never attachable; "bridge" always is.
	Attachable bool `json:"attachable,omitempty"`
	// CanDelete reports whether the caller may delete this network. Only the
	// mudp-managed networks they created (any managed network, for an admin).
	CanDelete bool `json:"canDelete,omitempty"`
	// Groups lists the user groups an administrator has granted this network to.
	// Filled in by the server for admins only; empty for everyone else.
	Groups []string `json:"groups,omitempty"`
	// Forward marks a network whose containers get their host ports relayed by
	// mudp instead of published by Docker (see forward.go). Filled in by the
	// server from the administrator's setting; it is a property of the host's
	// networking, not something stored on the network itself.
	Forward bool `json:"forward,omitempty"`
	// Internal marks a network whose containers are isolated from the host's
	// external networks — they can reach each other, but not the host or the
	// internet. Mirrors Docker's `--internal` flag.
	Internal bool `json:"internal,omitempty"`
}

// NetworkContainer is a container endpoint attached to a network, for the
// network detail view.
type NetworkContainer struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	IPv4    string `json:"ipv4"`
	IPv6    string `json:"ipv6"`
	MacAddr string `json:"macAddress"`
}

// NetworkDetail is the network record enriched with its attached containers,
// used by the network detail modal.
type NetworkDetail struct {
	Network
	Containers []NetworkContainer `json:"containers"`
}

// CreateNetworkOptions describes a network to create.
type CreateNetworkOptions struct {
	Username string
	Name     string
	Driver   string
	Subnet   string
	// Advanced IPAM fields (all optional; empty → Docker auto-assigns).
	Gateway      string
	IPRange      string
	AuxAddresses map[string]string
	IPv6         bool
	// Internal isolates the network from the host's external networks.
	Internal bool
	Labels   map[string]string
}

// NetworkFullName builds the mudp-namespaced network name.
func NetworkFullName(username, name string) string {
	return Prefix + Slug(username) + "-net-" + Slug(name)
}

// NetworkAccess carries the group-grant picture for one caller. Granted holds
// the full Docker names granted to a group the caller belongs to; Restricted
// holds every name that carries at least one grant, for anybody.
//
// The two are needed together because a grant means different things depending
// on the network. For a network mudp did not hand out by default — another
// user's, or one already on the host — Granted alone decides. For Docker's
// "bridge", which every user could always attach to, the first grant is what
// turns it from open-to-everyone into restricted: with no grants at all it stays
// open (Restricted says so), and once an admin picks groups only those groups
// keep it.
type NetworkAccess struct {
	Granted    map[string]bool
	Restricted map[string]bool
}

// MayUseSystem reports whether the caller may attach to one of Docker's
// built-in networks. Only "bridge" is ever attachable: "host" would hand the
// container every host interface and "none" cannot be joined at all.
func (a NetworkAccess) MayUseSystem(name string, admin bool) bool {
	if !IsShareableSystemNetwork(name) {
		return false
	}
	return admin || a.Granted[name] || !a.Restricted[name]
}

// ListNetworks returns the networks visible to the caller: the mudp-managed
// ones they own, any network granted to one of their groups, and Docker's
// built-in networks (bridge, host, none) shown read-only so the Networks view is
// never empty on a fresh host. Admins additionally see every mudp-managed
// network and every pre-existing host network, which is what makes those
// grantable in the first place.
//
// access holds the caller's group grants (see NetworkAccess); the zero value
// means "no grants anywhere", which leaves bridge open to everyone.
func (d *Client) ListNetworks(ctx context.Context, username string, admin bool, access NetworkAccess) ([]Network, error) {
	granted := access.Granted
	// Fetch every network once, then partition into managed / external / system.
	nets, err := d.c.NetworkList(ctx, types.NetworkListOptions{})
	if err != nil {
		return nil, err
	}
	var managed, external, system []Network
	for _, n := range nets {
		isManaged := n.Labels[ManagedLabel] == "true"
		owner := n.Labels[UserLabel]
		net := Network{
			Name:       n.Name,
			FullName:   n.Name,
			ID:         n.ID,
			Driver:     n.Driver,
			Scope:      n.Scope,
			Labels:     n.Labels,
			Owner:      owner,
			Containers: len(n.Containers),
			Internal:   n.Internal,
		}
		// Surface the first IPv4 IPAM config (subnet/gateway/range). IPv6-aware
		// networks carry a second config entry; flag IPv6 when one is present.
		for _, cfg := range n.IPAM.Config {
			if cfg.Subnet != "" && !strings.Contains(cfg.Subnet, ":") {
				net.Subnet = cfg.Subnet
				net.Gateway = cfg.Gateway
				net.IPRange = cfg.IPRange
				break
			}
		}
		for _, cfg := range n.IPAM.Config {
			if strings.Contains(cfg.Subnet, ":") {
				net.IPv6 = true
				break
			}
		}
		switch {
		case isManaged:
			if owner == "" {
				continue
			}
			isOwner := owner == username
			if !admin && !isOwner && !granted[n.Name] {
				continue
			}
			net.Managed = true
			net.Attachable = true
			// Deletion follows ownership, not visibility: a network shared with
			// your group is yours to use, not to remove. RemoveNetwork enforces
			// the same rule server-side.
			net.CanDelete = isOwner || admin
			net.Shared = !isOwner && !admin
			net.Name = netLabelToDisplay(n.Name)
			if c, ok := n.Labels["mudp.createdAt"]; ok {
				net.CreatedAt = c
			}
			managed = append(managed, net)
		case IsSystemNetworkName(n.Name):
			// Docker's built-ins are read-only: mudp neither created nor removes
			// them. Everyone still sees the rows — hiding "bridge" from a user
			// who may not join it would leave a fresh host's Networks view empty
			// and say nothing about why. Whether they can attach is the grant
			// question, answered by MayUseSystem.
			net.System = true
			net.Attachable = access.MayUseSystem(n.Name, admin)
			system = append(system, net)
		case admin || granted[n.Name]:
			// A network that already exists on the host but that mudp did not
			// create — an operator's `docker network create`, or one a
			// third-party compose project left behind. Admins see every such
			// network so they can grant it; everyone else sees only the granted
			// ones.
			net.External = true
			net.Attachable = true
			net.Shared = !admin
			if net.Owner == "" {
				net.Owner = "host"
			}
			external = append(external, net)
		}
	}
	// Managed networks first (the user's own), then host networks, then defaults.
	return append(append(managed, external...), system...), nil
}

// IsSystemNetworkName reports whether a network name is one of Docker's
// built-in defaults (bridge, host, none). Shared by the Networks view, the
// dashboard tile, and the server package's inspect-name resolution so all
// three agree on what counts as a system network.
func IsSystemNetworkName(name string) bool {
	switch name {
	case "bridge", "host", "none":
		return true
	}
	return false
}

// IsShareableSystemNetwork reports whether a built-in network can be restricted
// to user groups. Only "bridge" can: it is the one built-in a container may join
// without gaining the host's interfaces, so it is also the only one where
// choosing who gets to join means anything.
func IsShareableSystemNetwork(name string) bool {
	return name == "bridge"
}

// CreateNetwork creates a mudp-managed network owned by username.
func (d *Client) CreateNetwork(ctx context.Context, opts CreateNetworkOptions) (string, error) {
	name := strings.TrimSpace(opts.Name)
	if name == "" {
		return "", fmt.Errorf("network name is required")
	}
	driver := opts.Driver
	if driver == "" {
		driver = "bridge"
	}
	full := NetworkFullName(opts.Username, name)
	labels := map[string]string{
		ManagedLabel:     "true",
		UserLabel:        opts.Username,
		NameLabel:        name,
		"mudp.createdAt": time.Now().Format(time.RFC3339),
	}
	for k, v := range opts.Labels {
		labels[k] = v
	}
	ipam := &network.IPAM{}
	// Build the IPv4 IPAM config (subnet + optional gateway/range/aux). When a
	// subnet is supplied the advanced fields are honored; without a subnet the
	// whole config is omitted so Docker auto-assigns (preserving prior behavior).
	var configs []network.IPAMConfig
	if opts.Subnet != "" {
		cfg := network.IPAMConfig{Subnet: opts.Subnet, Gateway: opts.Gateway, IPRange: opts.IPRange}
		if len(opts.AuxAddresses) > 0 {
			cfg.AuxAddress = opts.AuxAddresses
		}
		configs = append(configs, cfg)
	}
	// IPv6: when requested, enable IPv6 on the network (Docker auto-assigns a
	// v6 subnet from the daemon's default IPv6 pool). We don't add an empty
	// IPAM config entry because ValidateIPAM rejects zero-subnet configs.
	if len(configs) > 0 {
		ipam.Config = configs
	}
	createOpts := types.NetworkCreate{
		Driver:     driver,
		IPAM:       ipam,
		Labels:     labels,
		Attachable: true,
	}
	if opts.IPv6 {
		createOpts.EnableIPv6 = true
	}
	if opts.Internal {
		createOpts.Internal = true
	}
	resp, err := d.c.NetworkCreate(ctx, full, createOpts)
	if err != nil {
		return "", err
	}
	return resp.ID, nil
}

// InspectNetwork returns a network's summary plus its attached containers.
// Visibility mirrors ListNetworks: a non-admin caller may inspect the
// mudp-managed networks they own, anything granted to one of their groups, and
// Docker's built-in networks. access may be the zero value.
func (d *Client) InspectNetwork(ctx context.Context, full, username string, admin bool, access NetworkAccess) (NetworkDetail, error) {
	granted := access.Granted
	info, err := d.c.NetworkInspect(ctx, full, types.NetworkInspectOptions{})
	if err != nil {
		return NetworkDetail{}, err
	}
	nd := NetworkDetail{Network: Network{
		Name:       info.Name,
		FullName:   info.Name,
		ID:         info.ID,
		Driver:     info.Driver,
		Scope:      info.Scope,
		Labels:     info.Labels,
		Owner:      info.Labels[UserLabel],
		Containers: len(info.Containers),
		Internal:   info.Internal,
	}}
	switch {
	case info.Labels[ManagedLabel] == "true":
		if info.Labels[UserLabel] == "" {
			return NetworkDetail{}, fmt.Errorf("network %q is not managed by mudp", full)
		}
		if !admin && info.Labels[UserLabel] != username && !granted[info.Name] {
			return NetworkDetail{}, fmt.Errorf("network %q is not yours", full)
		}
		nd.Managed = true
		nd.Name = netLabelToDisplay(info.Name)
		if c, ok := info.Labels["mudp.createdAt"]; ok {
			nd.CreatedAt = c
		}
	case IsSystemNetworkName(info.Name):
		nd.System = true
		nd.Attachable = access.MayUseSystem(info.Name, admin)
	case admin || granted[info.Name]:
		// A pre-existing host network the admin has shared (or any host network,
		// for an admin). Read-only: mudp did not create it and won't remove it.
		nd.External = true
	default:
		// Non-managed, non-granted network: refuse (don't leak arbitrary host nets).
		return NetworkDetail{}, fmt.Errorf("network %q is not managed by mudp", full)
	}
	for _, cfg := range info.IPAM.Config {
		if cfg.Subnet != "" && !strings.Contains(cfg.Subnet, ":") {
			nd.Subnet = cfg.Subnet
			nd.Gateway = cfg.Gateway
			nd.IPRange = cfg.IPRange
			break
		}
	}
	for _, cfg := range info.IPAM.Config {
		if strings.Contains(cfg.Subnet, ":") {
			nd.IPv6 = true
			break
		}
	}
	// Expand the Containers map (id → endpoint JSON) into a sorted slice.
	for cid, ep := range info.Containers {
		nd.Containers = append(nd.Containers, NetworkContainer{
			ID:      cid,
			Name:    strings.TrimPrefix(ep.Name, "/"),
			IPv4:    ep.IPv4Address,
			IPv6:    ep.IPv6Address,
			MacAddr: ep.MacAddress,
		})
	}
	sortNetworkContainers(nd.Containers)
	return nd, nil
}

// NetworkConnectContainer attaches a container to a network the user may use,
// with a guard on both the network and the container.
func (d *Client) NetworkConnectContainer(ctx context.Context, full, containerID, username string, admin bool, access NetworkAccess) error {
	if err := d.guardUsableNetwork(ctx, full, username, admin, access); err != nil {
		return err
	}
	return d.c.NetworkConnect(ctx, full, containerID, nil)
}

// NetworkDisconnectContainer detaches a container from a network.
func (d *Client) NetworkDisconnectContainer(ctx context.Context, full, containerID, username string, admin bool, access NetworkAccess, force bool) error {
	if err := d.guardUsableNetwork(ctx, full, username, admin, access); err != nil {
		return err
	}
	return d.c.NetworkDisconnect(ctx, full, containerID, force)
}

// guardUsableNetwork verifies the caller may attach to a network: it is
// mudp-managed and theirs, it was granted to one of their groups (Granted holds
// full Docker names, so a shared host network qualifies), or it is a built-in
// they may join. This is what keeps connect/disconnect off a network nobody
// shared with them.
func (d *Client) guardUsableNetwork(ctx context.Context, full, username string, admin bool, access NetworkAccess) error {
	info, err := d.c.NetworkInspect(ctx, full, types.NetworkInspectOptions{})
	if err != nil {
		return err
	}
	if IsSystemNetworkName(info.Name) {
		// Built-ins carry no mudp labels, so the ownership checks below would
		// reject even "bridge". Grants decide instead.
		if access.MayUseSystem(info.Name, admin) {
			return nil
		}
		return fmt.Errorf("network %q is not available to you", info.Name)
	}
	if access.Granted[full] || admin {
		return nil
	}
	if info.Labels[ManagedLabel] != "true" {
		return fmt.Errorf("network %q is not managed by mudp", full)
	}
	if info.Labels[UserLabel] != username {
		return fmt.Errorf("network %q is not yours", full)
	}
	return nil
}

// sortNetworkContainers sorts attached containers by name for stable display.
func sortNetworkContainers(cs []NetworkContainer) {
	for i := 1; i < len(cs); i++ {
		for j := i; j > 0 && cs[j-1].Name > cs[j].Name; j-- {
			cs[j-1], cs[j] = cs[j], cs[j-1]
		}
	}
}

// RemoveNetwork removes a mudp-managed network with an ownership guard.
func (d *Client) RemoveNetwork(ctx context.Context, name string, username string, admin bool) error {
	info, err := d.c.NetworkInspect(ctx, name, types.NetworkInspectOptions{})
	if err != nil {
		return err
	}
	if info.Labels[ManagedLabel] != "true" {
		return fmt.Errorf("network %q is not managed by mudp", name)
	}
	if !admin && info.Labels[UserLabel] != username {
		return fmt.Errorf("network %q is not yours", name)
	}
	if len(info.Containers) > 0 {
		return fmt.Errorf("network %q has %d attached containers; disconnect them first", name, len(info.Containers))
	}
	return d.c.NetworkRemove(ctx, name)
}

// netLabelToDisplay strips the mudp prefix and user segment from a network name.
func netLabelToDisplay(full string) string {
	s := strings.TrimPrefix(full, Prefix)
	if i := strings.Index(s, "-net-"); i >= 0 {
		return s[i+len("-net-"):]
	}
	return full
}

// ContainerNetworkNames returns the Docker network names a container is
// currently attached to. Unlike Inspect it does no other work, because the
// external MCP listener calls it on every request to decide whether the target
// container sits on the administrator's designated safe network.
func (d *Client) ContainerNetworkNames(ctx context.Context, id string) ([]string, error) {
	inspect, err := d.c.ContainerInspect(ctx, id)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(inspect.NetworkSettings.Networks))
	for name := range inspect.NetworkSettings.Networks {
		out = append(out, name)
	}
	return out, nil
}

// NetworkNameMatches reports whether an attached Docker network name refers to
// the network an administrator named. Admins type the name they read off the
// Networks view, which for a mudp-managed network is the display name ("lan"),
// not the namespaced Docker name ("mudp-alice-net-lan") — so both forms have to
// resolve to the same network, or a correctly-configured safe network would
// silently never match.
func NetworkNameMatches(attached, want string) bool {
	attached = strings.TrimSpace(attached)
	want = strings.TrimSpace(want)
	if attached == "" || want == "" {
		return false
	}
	if strings.EqualFold(attached, want) {
		return true
	}
	return strings.EqualFold(netLabelToDisplay(attached), want)
}
