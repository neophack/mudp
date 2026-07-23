package dockerx

import (
	"context"
	"fmt"
	"log"
	"net"
	"strings"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/network"
)

// System (platform-level) network names used for container egress isolation.
// Every mudp-created user container is forced onto the mesh network; the mesh
// itself has no route to the outside world (Internal:true). A single privileged
// egress-gateway container bridges mesh → egress, performing NAT for outbound
// traffic while DROPping anything destined for RFC1918 / the docker bridge /
// loopback. The result: user containers can reach the public Internet but not
// the host, the LAN, or the Docker daemon.
const (
	MeshNetworkName = "mudp-mesh"
	WANNetworkName  = "mudp-wrt-wan"

	// SystemLabel marks platform-owned networks/containers that must never
	// appear in a user's view nor be mutated through the user-facing API.
	SystemLabel = "mudp.system"
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
	Labels       map[string]string
}

// RawNetworkList returns Docker's full network list (unfiltered). Used by
// callers that need container counts on platform networks that ListNetworks
// intentionally hides (e.g. mudp-wrt-wan) or surfaces specially (mudp-mesh).
func (d *Client) RawNetworkList(ctx context.Context) ([]types.NetworkResource, error) {
	return d.c.NetworkList(ctx, types.NetworkListOptions{})
}

// NetworkFullName builds the mudp-namespaced network name.
func NetworkFullName(username, name string) string {
	return Prefix + Slug(username) + "-net-" + Slug(name)
}

// ListNetworks returns mudp-managed networks visible to the caller, followed by
// Docker's built-in/system networks (bridge, host, none, …) shown read-only so
// the Networks view is never empty on a fresh host.
func (d *Client) ListNetworks(ctx context.Context, username string, admin bool) ([]Network, error) {
	// Fetch every network once, then partition into managed vs. system.
	nets, err := d.c.NetworkList(ctx, types.NetworkListOptions{})
	if err != nil {
		return nil, err
	}
	var managed, system []Network
	for _, n := range nets {
		// mudp-mesh is the LAN side of the WRT gateway — every user container
		// lives on it, so surface it read-only so users can see (and inspect)
		// where their containers are attached. mudp-wrt-wan (the WAN side)
		// stays hidden: it's pure infrastructure users never touch.
		if n.Name == MeshNetworkName {
			lan := Network{
				Name:       MeshNetworkName,
				FullName:   MeshNetworkName,
				ID:         n.ID,
				Driver:     n.Driver,
				Scope:      n.Scope,
				Labels:     n.Labels,
				Containers: len(n.Containers),
				System:     true,
			}
			for _, cfg := range n.IPAM.Config {
				if cfg.Subnet != "" && !strings.Contains(cfg.Subnet, ":") {
					lan.Subnet = cfg.Subnet
					lan.Gateway = cfg.Gateway
					break
				}
			}
			system = append(system, lan)
			continue
		}
		// mudp-wrt-wan (WAN) and any other platform-owned network stay hidden.
		if n.Labels[SystemLabel] == "true" {
			continue
		}
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
		if isManaged {
			if owner == "" {
				continue
			}
			if !admin && owner != username {
				continue
			}
			net.Name = netLabelToDisplay(n.Name)
			if c, ok := n.Labels["mudp.createdAt"]; ok {
				net.CreatedAt = c
			}
			managed = append(managed, net)
		} else if isSystemNetwork(n.Name, n.Driver) {
			// Surface Docker's built-in networks read-only.
			net.System = true
			system = append(system, net)
		}
	}
	// Managed networks first (the user's own), then system defaults.
	return append(managed, system...), nil
}

// isSystemNetwork reports whether a network is one of Docker's built-in
// defaults worth surfacing in the UI. These cannot be deleted via mudp.
func isSystemNetwork(name, driver string) bool {
	return isSystemNetworkName(name)
}

// isSystemNetworkName reports whether a network name is one of Docker's
// built-in defaults (bridge, host, none). Shared by the Networks view and the
// dashboard tile so the two counts agree.
func isSystemNetworkName(name string) bool {
	switch name {
	case "bridge", "host", "none":
		return true
	}
	return false
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
	resp, err := d.c.NetworkCreate(ctx, full, createOpts)
	if err != nil {
		return "", err
	}
	return resp.ID, nil
}

// InspectNetwork returns a network's summary plus its attached containers.
// Ownership is enforced: non-admin callers may only inspect their own
// mudp-managed networks; system networks are readable by all.
func (d *Client) InspectNetwork(ctx context.Context, full, username string, admin bool) (NetworkDetail, error) {
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
	}}
	isManaged := info.Labels[ManagedLabel] == "true"
	if isManaged {
		if info.Labels[UserLabel] == "" {
			return NetworkDetail{}, fmt.Errorf("network %q is not managed by mudp", full)
		}
		if !admin && info.Labels[UserLabel] != username {
			return NetworkDetail{}, fmt.Errorf("network %q is not yours", full)
		}
		nd.Name = netLabelToDisplay(info.Name)
		if c, ok := info.Labels["mudp.createdAt"]; ok {
			nd.CreatedAt = c
		}
	} else if info.Name == MeshNetworkName {
		// mudp-mesh is the egress gateway's LAN: read-only for every user so
		// they can see which containers share their isolation network. It's
		// never editable or deletable from the UI.
		nd.System = true
	} else if isSystemNetwork(info.Name, info.Driver) {
		nd.System = true
	} else {
		// Non-managed, non-system network: refuse (don't leak arbitrary host nets).
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

// NetworkConnectContainer attaches a container to a mudp-managed network with
// an ownership guard on both the network and the container.
func (d *Client) NetworkConnectContainer(ctx context.Context, full, containerID, username string, admin bool) error {
	if err := d.guardManagedNetwork(ctx, full, username, admin); err != nil {
		return err
	}
	return d.c.NetworkConnect(ctx, full, containerID, nil)
}

// NetworkDisconnectContainer detaches a container from a mudp-managed network.
func (d *Client) NetworkDisconnectContainer(ctx context.Context, full, containerID, username string, admin bool, force bool) error {
	if err := d.guardManagedNetwork(ctx, full, username, admin); err != nil {
		return err
	}
	return d.c.NetworkDisconnect(ctx, full, containerID, force)
}

// guardManagedNetwork verifies a network is mudp-managed and owned by username
// (unless admin), so connect/disconnect can't touch another user's network.
// mudp-mesh (the egress gateway's LAN) is a shared platform network with no
// per-user owner, so it's allowed for every authenticated user — that's how a
// user manually (re)attaches a container to the gateway path. mudp-wrt-wan (the
// WAN side) is NOT allowed here: it's pure infrastructure.
func (d *Client) guardManagedNetwork(ctx context.Context, full, username string, admin bool) error {
	if full == MeshNetworkName {
		return nil // shared LAN: any authenticated user may attach/detach
	}
	info, err := d.c.NetworkInspect(ctx, full, types.NetworkInspectOptions{})
	if err != nil {
		return err
	}
	if info.Labels[ManagedLabel] != "true" {
		return fmt.Errorf("network %q is not managed by mudp", full)
	}
	if !admin && info.Labels[UserLabel] != username {
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
	// Platform isolation networks are never deletable via the UI.
	if IsSystemNetworkName(name) {
		return fmt.Errorf("network %q is a platform network and cannot be deleted (it's part of the egress isolation topology)", name)
	}
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

// EnsureSystemNetworks idempotently creates the platform isolation networks:
//
//   - mudp-mesh:    an Internal bridge. Every user container is forced onto it.
//     Internal networks have no NAT/forwarding rules, so by itself
//     it gives "no LAN, no host, no Internet". The WRT gateway
//     provides the controlled outbound path.
//   - mudp-wrt-wan: a normal bridge attached to the WRT gateway. The gateway
//     MASQUERADEs mesh → WAN traffic so containers can reach the
//     public Internet, while DROPping RFC1918/docker/loopback
//     destinations.
//
// Both networks carry the mudp.system label so mudp-wrt-wan stays hidden from
// user views and rejected by the ownership guards. (mudp-mesh is surfaced
// read-only — see ListNetworks.) Safe to call on every boot.
//
// NOTE: a network's IPAM subnet/gateway is immutable after creation, so changes
// to the policy's subnet fields only take effect if the admin first removes the
// old network (docker network rm mudp-mesh mudp-wrt-wan) and reboots. The
// Networks → WRT card surfaces this caveat.
func (d *Client) EnsureSystemNetworks(ctx context.Context) error {
	return d.EnsureSystemNetworksWithPolicy(ctx, DefaultWRTPolicy())
}

// EnsureSystemNetworksWithPolicy is the policy-driven form: the mesh/WAN
// subnets and gateways come from pol instead of the hardcoded constants.
func (d *Client) EnsureSystemNetworksWithPolicy(ctx context.Context, pol WRTPolicy) error {
	// First, sweep away legacy platform networks left over from earlier mudp
	// versions (e.g. the pre-rename "mudp-egress" WAN network). These carry the
	// mudp.system label but use names we no longer create, and because Docker
	// IPAM subnets can't overlap, a stale network squatting on the WAN subnet
	// would otherwise make the NetworkCreate below fail with "Pool overlaps with
	// other one on this address space". We only remove networks with no live
	// containers attached — a still-connected one is left alone and logged so
	// the admin can drain and remove it manually.
	if err := d.removeLegacySystemNetworks(ctx); err != nil {
		// Best-effort: a failure here shouldn't block the whole ensure. The
		// NetworkCreate below will surface a real conflict if one remains.
		log.Printf("wrt: legacy system network cleanup failed (continuing): %v", err)
	}
	lanGateway := pol.LANGateway
	if lanGateway == "" {
		lanGateway = MeshGatewayIP
	}
	// The IPAM gateway (meshBridgeGW) is set to pol.LANGateway (.2 = WRT’s LAN
	// IP) so Docker automatically injects .2 as the default route for every
	// container on mudp-mesh. No manual `ip route replace` is needed.
	// Docker assigns the gateway IP to the host bridge interface (br-mudp-mesh),
	// but WRT’s netifd also claims .2 on br-lan inside the container. WRT sends
	// a gratuitous ARP for .2 when br-lan comes up, updating containers’ ARP
	// caches so that “default via .2” resolves to WRT’s MAC and traffic transits
	// the gateway. For any container that misses the gratuitous ARP, mudp runs a
	// privileged `ip route replace default via .2` post-start (see CreateContainer).
	meshBridgeGW := lanGateway // pol.LANGateway = WRTMeshIP = 172.31.252.2
	wanGateway := pol.WANGateway
	if wanGateway == "" {
		wanGateway = WANBridgeGW
	}
	sysLabels := map[string]string{
		SystemLabel:      "true",
		ManagedLabel:     "true",
		"mudp.createdAt": time.Now().Format(time.RFC3339),
	}
	// mesh: the WRT gateway’s LAN side.
	//
	// Internal: false — Docker injects meshBridgeGW (= pol.LANGateway = WRT’s LAN
	// IP) as the default route into every container on this network. With
	// Internal: true the default route is suppressed and containers get
	// “Gateway: ”” — they cannot reach WRT or the Internet at all.
	//
	// AuxAddress reserves low addresses. Exclude the bridge gateway (.2 = lanGateway)
	// because Docker auto-reserves it and rejects a duplicate AuxAddress entry.
	// We do NOT request .2 for WRT via IPAMConfig.IPv4Address; WRT gets an
	// auto-allocated IP from the pool and netifd re-assigns br-lan to .2 via UCI.
	meshAux := auxAddrsExcluding(lanReservedAddrs(pol.LANSubnet), meshBridgeGW)
	meshCreate := types.NetworkCreate{
		Driver:     "bridge",
		Internal:   false,
		Attachable: true,
		Labels:     sysLabels,
		IPAM: &network.IPAM{
			Config: []network.IPAMConfig{
				{
					Subnet:     pol.LANSubnet,
					Gateway:    meshBridgeGW,
					AuxAddress: meshAux,
				},
			},
		},
	}
	if _, err := d.c.NetworkCreate(ctx, MeshNetworkName, meshCreate); err != nil {
		if !isAlreadyExistsErr(err) {
			return fmt.Errorf("create mesh network: %w", err)
		}
		// Network already exists — check whether its subnet still matches the
		// policy. If not (e.g. admin changed LANSubnet), remove and recreate.
		if err2 := d.reconcileNetworkIPAM(ctx, MeshNetworkName, pol.LANSubnet, meshCreate); err2 != nil {
			return fmt.Errorf("reconcile mesh network: %w", err2)
		}
	}
	// WAN: ordinary bridge (gets the daemon’s default NAT path to the host NIC,
	// which is what makes real outbound Internet work once the gateway
	// MASQUERADEs into it). The bridge gateway is the gateway container’s WAN
	// next hop (pol.WANGateway). AuxAddress keeps the gateway’s fixed WAN
	// address (pol.WANIP) out of the auto-allocation pool, with the gateway IP
	// itself excluded to avoid the "Address already in use" IPAM error.
	// Also exclude pol.WANIP (.2) from WAN AuxAddress for the same reason:
	// AuxAddress entries are configured on the bridge interface; if .2 is in
	// AuxAddress, the bridge owns that IP and WRT's veth can't claim it.
	wanWRTIP := pol.WANIP
	if wanWRTIP == "" {
		wanWRTIP = WRTWANIP
	}
	wanCreate := types.NetworkCreate{
		Driver:     "bridge",
		Attachable: true,
		Labels:     sysLabels,
		IPAM: &network.IPAM{
			Config: []network.IPAMConfig{
				{
					Subnet:  pol.WANSubnet,
					Gateway: wanGateway,
					AuxAddress: auxAddrsExcluding(
						auxAddrsExcluding(wanReservedAddrs(pol.WANSubnet), wanGateway),
						wanWRTIP,
					),
				},
			},
		},
	}
	if _, err := d.c.NetworkCreate(ctx, WANNetworkName, wanCreate); err != nil {
		if !isAlreadyExistsErr(err) {
			return fmt.Errorf("create wrt-wan network: %w", err)
		}
		if err2 := d.reconcileNetworkIPAM(ctx, WANNetworkName, pol.WANSubnet, wanCreate); err2 != nil {
			return fmt.Errorf("reconcile wrt-wan network: %w", err2)
		}
	}
	return nil
}

// reconcileNetworkIPAM checks whether an existing Docker network’s IPAM subnet
// matches wantSubnet. If it does, it’s a no-op. If it doesn’t (the policy
// changed), the network is removed and recreated — provided no containers are
// currently attached. A non-empty network returns an actionable error so the
// admin knows to drain it manually before the change can land.
func (d *Client) reconcileNetworkIPAM(ctx context.Context, name, wantSubnet string, create types.NetworkCreate) error {
	info, err := d.c.NetworkInspect(ctx, name, types.NetworkInspectOptions{})
	if err != nil {
		return err
	}
	for _, cfg := range info.IPAM.Config {
		if cfg.Subnet == wantSubnet {
			return nil // subnet already matches — nothing to do
		}
	}
	// Subnet mismatch. Refuse to remove a network that still has containers.
	if len(info.Containers) > 0 {
		return fmt.Errorf(
			"network %q has subnet %v but policy wants %s; %d container(s) still attached — "+
				"drain them first: docker network disconnect %s <container>, then redeploy",
			name, info.IPAM.Config, wantSubnet, len(info.Containers), name)
	}
	log.Printf("wrt: network %q has stale subnet — removing and recreating with %s", name, wantSubnet)
	if err := d.c.NetworkRemove(ctx, info.ID); err != nil {
		return fmt.Errorf("remove stale network %q: %w", name, err)
	}
	if _, err := d.c.NetworkCreate(ctx, name, create); err != nil {
		return fmt.Errorf("recreate network %q: %w", name, err)
	}
	return nil
}

// isAlreadyExistsErr reports whether err is Docker's "network already exists"
// response, so Ensure* helpers can treat re-runs as success.
func isAlreadyExistsErr(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "already exists")
}

// IsSystemNetworkName reports whether name is one of mudp's own platform
// networks (mesh/WAN) — used by the network-attachment validator to refuse
// user requests that try to attach to them directly.
func IsSystemNetworkName(name string) bool {
	return name == MeshNetworkName || name == WANNetworkName
}

// auxAddrsExcluding returns a copy of addrs with any entry whose value equals
// excludeIP removed. Used to prevent the IPAM gateway IP from appearing in the
// AuxAddress map — Docker pre-reserves the gateway and returns
// "Address already in use" if the same IP also appears as an AuxAddress.
func auxAddrsExcluding(addrs map[string]string, excludeIP string) map[string]string {
	out := make(map[string]string, len(addrs))
	for k, v := range addrs {
		if v != excludeIP {
			out[k] = v
		}
	}
	return out
}

// subnetFirstHost returns the first usable host IP in a CIDR, i.e. the network
// address incremented by one. Example: "172.31.252.0/22" → "172.31.252.1".
// Returns "" on any parse error or if the result falls outside the subnet.
func subnetFirstHost(cidr string) string {
	_, ipnet, err := net.ParseCIDR(cidr)
	if err != nil || ipnet == nil {
		return ""
	}
	b := ipnet.IP.To4()
	if b == nil {
		return ""
	}
	ip := make(net.IP, 4)
	copy(ip, b)
	for i := 3; i >= 0; i-- {
		ip[i]++
		if ip[i] != 0 {
			break
		}
	}
	if !ipnet.Contains(ip) {
		return ""
	}
	return ip.String()
}

// removeLegacySystemNetworks deletes platform-labelled networks whose names
// are no longer the current mesh/WAN names. These are leftovers from earlier
// mudp versions (pre-rename "mudp-egress", etc.). Because Docker IPAM forbids
// overlapping subnets, a stale network squatting on the WAN subnet blocks
// creation of mudp-wrt-wan with "Pool overlaps with other one on this address
// space" — so we proactively sweep them before creating the current pair.
//
// Only networks with no attached containers are removed; a still-connected one
// is left in place and logged, so the admin can drain and remove it manually
// rather than us yanking networking out from under a live container.
func (d *Client) removeLegacySystemNetworks(ctx context.Context) error {
	nets, err := d.c.NetworkList(ctx, types.NetworkListOptions{})
	if err != nil {
		return fmt.Errorf("list networks: %w", err)
	}
	for _, n := range nets {
		// Only touch networks mudp itself owns as platform infrastructure.
		if n.Labels[SystemLabel] != "true" || n.Labels[ManagedLabel] != "true" {
			continue
		}
		// Skip the current canonical platform networks.
		if n.Name == MeshNetworkName || n.Name == WANNetworkName {
			continue
		}
		// Never auto-remove a network that still has live containers attached —
		// that would sever a running container's networking. Log + skip.
		if len(n.Containers) > 0 {
			log.Printf("wrt: legacy system network %q still has %d container(s) attached; leaving it in place — remove it manually after draining (docker network rm %s)",
				n.Name, len(n.Containers), n.Name)
			continue
		}
		if err := d.c.NetworkRemove(ctx, n.ID); err != nil {
			// Best-effort per-network: log and continue rather than aborting the sweep.
			log.Printf("wrt: could not remove legacy system network %q: %v", n.Name, err)
			continue
		}
		log.Printf("wrt: removed legacy system network %q (no longer used by this mudp version)", n.Name)
	}
	return nil
}
