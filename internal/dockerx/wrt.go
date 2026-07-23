package dockerx

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/docker/go-connections/nat"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
)

// Fixed addressing for the isolation networks. Because the mesh and WAN
// subnets are created with explicit IPAM configs, these addresses are stable
// across hosts and reboots, which lets the gateway's configuration be static.
//
// These are the DEFAULTS — the actual values at runtime come from a WRTPolicy
// (see DefaultWRTPolicy below / the store layer), so an admin can re-IP the
// whole topology from the Networks page without a code change. The constants
// stay as the fallback for a fresh install and for the network-creation IPAM
// config.
//
// MeshNetworkName / WANNetworkName live in network.go (shared with the network
// module); only the addressing constants are declared here.
const (
	MeshSubnetCIDR = "172.31.252.0/22"
	MeshGatewayIP  = "172.31.252.1" // mesh bridge gateway (unused by containers; wrt is the real gateway)
	WRTMeshIP      = "172.31.252.2" // the gateway's IP on the mesh network (LAN side)
	WANSubnetCIDR  = "172.31.240.0/22"
	WRTWANIP       = "172.31.240.2" // the gateway's IP on the WAN network
	WANBridgeGW    = "172.31.240.1" // the mudp-wrt-wan bridge gateway = wrt's WAN next hop

	// WRTContainer is the name of the managed ImmortalWrt gateway container.
	// It's the sole privileged container mudp creates, and it shows up in the
	// admin container list (Owner = system, read-only managed — start/stop/
	// restart/logs allowed, remove refused; rebuild it via the WRT settings).
	WRTContainer = "mudp-wrt"

	// LegacyEgressGatewayContainer is the pre-rename container name. We probe
	// for it on boot and, if found, log a one-line cleanup hint (the admin must
	// remove it manually — we don't auto-delete a running router).
	LegacyEgressGatewayContainer = "mudp-egress-gateway"

	// DefaultWRTImage is the ImmortalWrt router image the gateway runs by
	// default. The actual image is policy-driven (WRTPolicy.Image).
	DefaultWRTImage = "hkbase/immortalwrt:latest"

	// DefaultWRTLuCIPort is the default host port the gateway's web admin (LuCI,
	// container port 80) is published on. Policy-driven via WRTPolicy.LuCIHostPort.
	DefaultWRTLuCIPort = 18080
)

// WRTPolicy is the dockerx-local view of the gateway configuration. It mirrors
// store.WRTPolicy; the server layer converts between the two so dockerx does
// not depend on store (which would create an import cycle).
type WRTPolicy struct {
	Enabled    bool
	Image      string
	LANSubnet  string
	LANGateway string // wrt's IP on the LAN (its br-lan)
	WANSubnet  string
	WANGateway string // mudp-wrt-wan bridge gateway (wrt's WAN next hop)
	WANIP      string // wrt's IP on the WAN
	// LuCIHostPort publishes the gateway's web admin (container port 80) on this
	// host port. 0 = don't publish. See store.WRTPolicy.LuCIHostPort.
	LuCIHostPort int
}

// DefaultWRTPolicy returns the policy used on a fresh install — identical to
// the previously hardcoded values.
func DefaultWRTPolicy() WRTPolicy {
	return WRTPolicy{
		Enabled:      true,
		Image:        DefaultWRTImage,
		LANSubnet:    MeshSubnetCIDR,
		LANGateway:   WRTMeshIP,
		WANSubnet:    WANSubnetCIDR,
		WANGateway:   WANBridgeGW,
		WANIP:        WRTWANIP,
		LuCIHostPort: DefaultWRTLuCIPort,
	}
}

// EnsureWRT makes sure the gateway container exists and is running according
// to pol. Idempotent: if it's already up with the right spec it's a no-op; if
// it exists but is stopped it's started; otherwise it's (re)created.
//
// The gateway is the ONLY privileged container mudp creates: ImmortalWrt needs
// full netns/routing/firewall control, so it runs with --privileged. It does
// NOT mount the docker socket and is labelled mudp.system so non-admin users
// never see it (admins see it read-only in the container list).
func (d *Client) EnsureWRT(ctx context.Context, pol WRTPolicy) error {
	d.warnLegacyGateway(ctx)
	if pol.Image == "" {
		pol.Image = DefaultWRTImage
	}
	if err := d.ensureWRTImage(ctx, pol.Image); err != nil {
		return err
	}

	// If a gateway already exists, reconcile state instead of clobbering it.
	existing, err := d.c.ContainerInspect(ctx, WRTContainer)
	if err == nil {
		return d.reconcileWRT(ctx, existing, pol)
	}
	if !isNotFoundErr(err) {
		return fmt.Errorf("inspect wrt gateway: %w", err)
	}
	return d.createWRT(ctx, pol, nil)
}

// RecreateWRT force-rebuilds the gateway container from scratch: it removes any
// existing mudp-wrt container, tears down and recreates the platform networks
// (releasing all IP allocations), then creates + starts a fresh container and
// pushes UCI config. This is the "one-click deploy" path triggered from the
// Networks → WRT card — unlike EnsureWRT (idempotent reconcile), it ALWAYS
// tears down and rebuilds, so it briefly interrupts the gateway (mesh containers
// lose Internet for a few seconds during the swap).
//
// progress is called with each human-readable step/line (image pull layer status
// included); nil is allowed for a silent call. Returns an error if any step
// fails — a pull failure leaves no container, but the next boot's ensureWRT will
// retry once the image is available.
func (d *Client) RecreateWRT(ctx context.Context, pol WRTPolicy, progress func(line string)) error {
	if pol.Image == "" {
		pol.Image = DefaultWRTImage
	}
	// Step 1: remove any existing gateway container (force = stop + remove).
	if existing, err := d.c.ContainerInspect(ctx, WRTContainer); err == nil {
		if progress != nil {
			progress(fmt.Sprintf("Removing existing container %s (force)...", WRTContainer))
		}
		if err := d.c.ContainerRemove(ctx, existing.ID, container.RemoveOptions{Force: true, RemoveVolumes: true}); err != nil && !isNotFoundErr(err) {
			return fmt.Errorf("remove existing wrt container: %w", err)
		}
	} else if !isNotFoundErr(err) {
		// A non-"not found" inspect error is unexpected but not fatal — try to
		// proceed and let createWRT surface any real conflict.
		if progress != nil {
			progress(fmt.Sprintf("Warning: could not inspect existing %s: %v", WRTContainer, err))
		}
	}
	// Step 2: tear down the platform networks so IP allocations are fully
	// released and the networks are recreated with fresh IPAM state. This
	// guarantees the gateway gets its fixed IPs (.2) even if the old networks
	// had stale allocations. Any containers still on mudp-mesh are force-
	// disconnected first and reconnected after the networks come back.
	if progress != nil {
		progress("Tearing down platform networks (mudp-mesh, mudp-wrt-wan) to release IPs…")
	}
	meshContainers := d.forceTeardownSystemNetworks(ctx, progress)
	// Give the kernel a moment to finish removing the old bridge interfaces
	// (br-mudp-mesh, br-mudp-wrt-wan veth pairs). Without this pause Docker
	// can return "Address already in use" when starting the new container
	// because the old bridge netdev hasn't been unregistered from the kernel
	// yet.
	time.Sleep(3 * time.Second)

	// Step 3: ensure the image is present, pulling (with progress) if not.
	if err := d.ensureWRTImageWithProgress(ctx, pol.Image, progress); err != nil {
		return err
	}
	// Step 4: recreate the platform isolation networks.
	if progress != nil {
		progress("Recreating platform networks (mesh + wan)…")
	}
	if err := d.EnsureSystemNetworksWithPolicy(ctx, pol); err != nil {
		return fmt.Errorf("ensure system networks: %w", err)
	}
	// Step 5: reconnect tenant containers that were on mudp-mesh before the
	// teardown. Best-effort — a container that has been removed in the meantime
	// is silently skipped.
	if len(meshContainers) > 0 {
		if progress != nil {
			progress(fmt.Sprintf("Reconnecting %d container(s) to mudp-mesh…", len(meshContainers)))
		}
		for _, cid := range meshContainers {
			if err := d.c.NetworkConnect(ctx, MeshNetworkName, cid, nil); err != nil {
				if progress != nil {
					progress(fmt.Sprintf("  Warning: could not reconnect %s to mudp-mesh: %v", cid[:12], err))
				}
			}
		}
	}
	// Step 6: create + start + apply UCI config.
	if progress != nil {
		progress(fmt.Sprintf("Creating + starting container %s...", WRTContainer))
	}
	if err := d.createWRT(ctx, pol, progress); err != nil {
		return err
	}
	// Post-config sanity check: applyWrtConfig runs `/etc/init.d/network restart`
	// inside the privileged WRT container. On some hosts this briefly terminates
	// the container (triggering --restart unless-stopped), which causes Docker to
	// tear down and rebuild the WRT endpoint on mudp-mesh. In rare cases Docker
	// removes the mudp-mesh network during this window. Detect and heal.
	if _, netErr := d.c.NetworkInspect(ctx, MeshNetworkName, types.NetworkInspectOptions{}); netErr != nil {
		if progress != nil {
			progress("Warning: mudp-mesh vanished after WRT config — recreating platform networks…")
		}
		log.Printf("wrt: mudp-mesh disappeared after applyWrtConfig; re-running EnsureSystemNetworksWithPolicy")
		if err := d.EnsureSystemNetworksWithPolicy(ctx, pol); err != nil {
			return fmt.Errorf("re-ensure networks after config: %w", err)
		}
		// Reconnect WRT to the new mesh network (it may have been detached).
		cj, inspErr := d.c.ContainerInspect(ctx, WRTContainer)
		if inspErr == nil && cj.State != nil && cj.State.Running {
			attached := false
			for netName := range cj.NetworkSettings.Networks {
				if netName == MeshNetworkName {
					attached = true
					break
				}
			}
			if !attached {
				_ = d.c.NetworkConnect(ctx, MeshNetworkName, cj.ID, &network.EndpointSettings{
					IPAMConfig: &network.EndpointIPAMConfig{IPv4Address: pol.LANGateway},
				})
			}
		}
	}
	if progress != nil {
		progress("WRT gateway recreated and configured.")
	}
	return nil
}

// forceTeardownSystemNetworks disconnects every container from mudp-mesh and
// mudp-wrt-wan (force), then removes both networks. Returns the list of
// container IDs that were on mudp-mesh so the caller can reconnect them after
// the networks are recreated.
func (d *Client) forceTeardownSystemNetworks(ctx context.Context, progress func(string)) []string {
	logf := func(msg string) {
		log.Printf("wrt: %s", msg)
		if progress != nil {
			progress(msg)
		}
	}
	var meshContainerIDs []string
	for _, netName := range []string{MeshNetworkName, WANNetworkName} {
		info, err := d.c.NetworkInspect(ctx, netName, types.NetworkInspectOptions{})
		if err != nil {
			if !isNotFoundErr(err) {
				logf(fmt.Sprintf("  Warning: inspect %s: %v", netName, err))
			}
			continue // network doesn't exist yet — nothing to tear down
		}
		// Collect containers attached to mudp-mesh so we can reconnect them later.
		if netName == MeshNetworkName {
			for cid := range info.Containers {
				meshContainerIDs = append(meshContainerIDs, cid)
			}
		}
		// Force-disconnect all containers.
		for cid := range info.Containers {
			short := cid
			if len(cid) > 12 {
				short = cid[:12]
			}
			if err := d.c.NetworkDisconnect(ctx, netName, cid, true); err != nil {
				logf(fmt.Sprintf("  Warning: disconnect %s from %s: %v", short, netName, err))
			}
		}
		// Remove the now-empty network.
		if err := d.c.NetworkRemove(ctx, info.ID); err != nil {
			logf(fmt.Sprintf("  Warning: remove network %s: %v", netName, err))
		} else {
			logf(fmt.Sprintf("  Removed network %s (IPs released).", netName))
		}
	}
	return meshContainerIDs
}

// warnLegacyGateway logs a one-line hint when a pre-rename mudp-egress-gateway
// container is still around, so the admin knows to clean it up. Best-effort:
// never returns an error and never auto-removes a running router.
func (d *Client) warnLegacyGateway(ctx context.Context) {
	if _, err := d.c.ContainerInspect(ctx, LegacyEgressGatewayContainer); err == nil {
		log.Printf("NOTE: legacy container %q still exists; the WRT gateway is now %q. Remove the old one manually once the new gateway is healthy: docker rm -f %s",
			LegacyEgressGatewayContainer, WRTContainer, LegacyEgressGatewayContainer)
	}
}

// luciBindingMatches reports whether the existing container's LuCI port binding
// (container 80/tcp → host pol.LuCIHostPort) matches the policy. Port bindings
// are set at create time and can't be hot-edited, so a mismatch forces a rebuild
// in reconcileWRT. Returns true when no binding is wanted (pol.LuCIHostPort==0)
// and none exists — so toggling LuCI off also triggers a rebuild.
func luciBindingMatches(existing types.ContainerJSON, pol WRTPolicy) bool {
	const luciContainerPort = "80/tcp"
	var bound string
	if existing.HostConfig != nil {
		if bps, ok := existing.HostConfig.PortBindings[nat.Port(luciContainerPort)]; ok && len(bps) > 0 {
			bound = bps[0].HostPort
		}
	}
	want := strconv.Itoa(pol.LuCIHostPort)
	if pol.LuCIHostPort == 0 {
		// No binding wanted: match only if there's currently none.
		return bound == ""
	}
	return bound == want
}

// createWRT creates and starts the gateway container from scratch, then
// applies the UCI configuration so ImmortalWrt routes LAN → WAN.
func (d *Client) createWRT(ctx context.Context, pol WRTPolicy, progress func(string)) error {
	labels := map[string]string{
		SystemLabel:      "true",
		ManagedLabel:     "true",
		NameLabel:        "wrt", // surfaces as the container's display name in the admin list
		"mudp.createdAt": time.Now().Format(time.RFC3339),
	}
	// ImmortalWrt boots via /sbin/init (procd), which brings up the full OpenWrt
	// service stack (netifd, firewall4, dnsmasq) — that's what gives the container
	// real router semantics. Some ImmortalWrt images (e.g. hkbase/immortalwrt)
	// ship with an empty config (no ENTRYPOINT, no CMD), so we MUST set Cmd
	// explicitly: without it the Docker daemon rejects creation with
	// "no command specified". mudp applies the LAN/WAN/firewall config post-start
	// via applyWrtConfig.
	cc := &container.Config{
		Image:  pol.Image,
		Cmd:    []string{"/sbin/init"},
		Labels: labels,
	}
	// Publish the gateway's LuCI web admin (container port 80) on a host port so
	// the admin can reach it at http://<host>:<port>. LuCI is the only service we
	// expose — SSH (22), the UBUS RPC, etc. stay reachable only from inside the
	// mesh/WAN networks. 0 disables publishing.
	hc := &container.HostConfig{
		RestartPolicy: container.RestartPolicy{Name: "unless-stopped"},
		// ImmortalWrt manages its own netns: iptables/nftables, routing,
		// conntrack. It needs the full privileged set, not just NET_ADMIN.
		// This is the sole privileged container mudp creates.
		Privileged: true,
	}
	if pol.LuCIHostPort > 0 {
		const luciContainerPort = "80/tcp"
		cc.ExposedPorts = nat.PortSet{nat.Port(luciContainerPort): struct{}{}}
		hc.PortBindings = nat.PortMap{
			nat.Port(luciContainerPort): []nat.PortBinding{{HostPort: strconv.Itoa(pol.LuCIHostPort)}},
		}
	}
	// Attach to both networks with fixed IPs so the UCI config stays correct.
	//
	// IMPORTANT: mesh (LAN) must be the PRIMARY (first) endpoint so Docker
	// assigns it eth0. ImmortalWrt's netifd auto-bridges eth0 into br-lan, so
	// eth0 must be the LAN (mesh) side — otherwise the WAN veth gets wrongly
	// pulled into the LAN bridge and the whole topology breaks. WAN attaches
	// second (eth1) via NetworkConnect after create.
	//
	// WRT does NOT request pol.LANGateway (.2) explicitly: the bridge already
	// owns .2 (it is the IPAM gateway). Docker auto-allocates a pool IP for
	// WRT's eth0. netifd then reassigns br-lan = .2 via UCI, and sends a
	// gratuitous ARP so all mesh containers learn .2 → WRT's MAC.
	nc := &network.NetworkingConfig{
		EndpointsConfig: map[string]*network.EndpointSettings{
			MeshNetworkName: {}, // auto-allocate; netifd owns .2 via UCI
		},
	}
	resp, err := d.c.ContainerCreate(ctx, cc, hc, nc, &v1.Platform{}, WRTContainer)
	if err != nil {
		// If the name is already taken (race: --restart policy or another mudp
		// instance re-created the container between our Step-1 removal and now),
		// force-remove it and retry once.
		if isConflictErr(err) {
			log.Printf("wrt: container name %q conflict on create; force-removing and retrying", WRTContainer)
			if stale, iErr := d.c.ContainerInspect(ctx, WRTContainer); iErr == nil {
				_ = d.c.ContainerRemove(ctx, stale.ID, container.RemoveOptions{Force: true, RemoveVolumes: true})
			}
			resp, err = d.c.ContainerCreate(ctx, cc, hc, nc, &v1.Platform{}, WRTContainer)
		}
		if err != nil {
			return fmt.Errorf("create wrt gateway: %w", err)
		}
	}
	// Attach the WAN network as the second endpoint (eth1). Must happen after
	// create, and must NOT be primary or it'd become eth0 and get bridged into
	// br-lan (see note above).
	if err := d.c.NetworkConnect(ctx, WANNetworkName, resp.ID, &network.EndpointSettings{
		IPAMConfig: &network.EndpointIPAMConfig{IPv4Address: pol.WANIP},
	}); err != nil {
		// Best-effort cleanup so the next run starts clean.
		_ = d.c.ContainerRemove(ctx, resp.ID, container.RemoveOptions{Force: true})
		return fmt.Errorf("connect gateway to wan: %w", err)
	}
	if err := d.c.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		return fmt.Errorf("start wrt gateway: %w", err)
	}
	// Give procd a moment to bring up init before we push UCI config. Best-effort
	// timing; applyWrtConfig retries internally and is re-run on every reconcile.
	d.applyWrtConfig(ctx, resp.ID, pol, true, progress)
	return nil
}

// reconcileWRT ensures an existing gateway is running; if it was created with
// a stale image (e.g. a different ImmortalWrt tag) it's rebuilt. On every
// successful run it re-applies the UCI config so the router's LAN/WAN/firewall
// can't drift.
func (d *Client) reconcileWRT(ctx context.Context, existing types.ContainerJSON, pol WRTPolicy) error {
	// Port bindings (LuCI) and the image are baked in at create time and can't be
	// hot-edited, so if either drifted from policy we rebuild the container.
	if existing.Config != nil && existing.Config.Image != pol.Image || !luciBindingMatches(existing, pol) {
		_ = d.c.ContainerRemove(ctx, existing.ID, container.RemoveOptions{Force: true})
		return d.createWRT(ctx, pol, nil)
	}
	// fresh=true when WE had to start the container here: a freshly-started
	// ImmortalWrt needs several seconds for procd to bring up netifd/firewall4
	// before uci reload/fw4 can actually take effect. Without this delay the UCI
	// config appears to "apply" (uci commit succeeds) but reload_config/fw4 reload
	// silently no-op against a half-initialized procd, leaving br-lan on factory
	// defaults — which makes LuCI unreachable and NAT non-functional until the
	// next reconcile. (Existing container that was already running = fresh=false.)
	fresh := false
	if existing.State == nil || !existing.State.Running {
		if err := d.c.ContainerStart(ctx, existing.ID, container.StartOptions{}); err != nil {
			return fmt.Errorf("restart wrt gateway: %w", err)
		}
		fresh = true
	}
	// Re-apply UCI config idempotently — replaces the old 60s iptables watchdog.
	d.applyWrtConfig(ctx, existing.ID, pol, fresh, nil)
	return nil
}

// applyWrtConfig pushes the LAN/WAN/firewall configuration into the ImmortalWrt
// gateway via `docker exec`. It is idempotent: setting a UCI option to the same
// value is a no-op, and `uci commit` + `reload_config` only restarts services
// whose config actually changed. Best-effort: failures are logged but never
// block EnsureWRT — a half-configured router still isolates the LAN, and the
// admin can inspect the container directly.
//
// The configuration:
//   - LAN (mesh side, br-lan) = pol.LANGateway/pol.LANSubnet, DHCP disabled (the
//     mesh has no other DHCP clients), forwarding enabled. eth0 is the mesh veth
//     (the primary Docker endpoint) and netifd auto-bridges it into br-lan.
//   - WAN = eth1 (the secondary Docker endpoint, the mudp-wrt-wan veth) at
//     pol.WANIP/pol.WANSubnet with default gateway pol.WANGateway (the
//     mudp-wrt-wan bridge), masquerade ON so LAN traffic is NAT'd to the WAN
//     subnet before leaving for the host's default NAT path.
//   - firewall: lan→wan forwarding allowed; wan→lan denied by default;
//     RFC1918/docker/loopback destinations dropped so LAN containers can't
//     reach the host, the physical LAN, or other internal networks.
//   - LuCI: when pol.LuCIHostPort > 0, an extra firewall rule allows inbound
//     tcp/80 from the wan zone — Docker's published port arrives on eth1 (WAN),
//     which is otherwise REJECT'd, so without this rule LuCI is unreachable
//     from the host even though uhttpd serves it correctly on :80.
//
// fresh=true adds extra patience on first boot (procd takes a few seconds to
// bring init up before uci is usable); on reconcile the container is already
// settled so we skip the wait.
// applyWrtConfig pushes the LAN/WAN/firewall configuration into the ImmortalWrt
// gateway by running each UCI command directly via docker exec — no single sh -c
// wrapper. This surfaces per-command failures, avoids silent sh-script aborts,
// and lets the deploy modal stream each step via progress. Service restarts are
// also direct execs. Only the nft handle-parsing loop (which needs a shell
// pipeline) still runs via a minimal /bin/sh -c.
//
// progress may be nil; steps are always written to log.Printf regardless.
func (d *Client) applyWrtConfig(ctx context.Context, containerID string, pol WRTPolicy, fresh bool, progress func(string)) {
	logf := func(msg string) {
		log.Printf("wrt: %s", msg)
		if progress != nil {
			progress(msg)
		}
	}
	// run executes cmd and returns combined stdout+stderr. Errors are logged
	// unless ignoreErr is true (used for -q delete and best-effort commands).
	run := func(ignoreErr bool, cmd []string) string {
		out, err := d.execCapture(ctx, containerID, cmd)
		if err != nil && !ignoreErr {
			logf(fmt.Sprintf("  %v → %v", cmd, err))
		}
		return strings.TrimSpace(out)
	}
	uciSet := func(option string) { run(false, []string{"uci", "set", option}) }
	uciDel := func(option string) { run(true, []string{"uci", "-q", "delete", option}) }
	uciList := func(option string) { run(false, []string{"uci", "add_list", option}) }

	if fresh {
		logf("Waiting 8 s for procd/netifd to initialise…")
		time.Sleep(8 * time.Second)
	}

	// ── 0. Current state (before) ────────────────────────────────────────────
	if out := run(true, []string{"ifconfig"}); out != "" {
		logf("ifconfig (before):\n" + out)
	}

	// ── 1. Wait until uci is available ───────────────────────────────────────
	for i := 0; i < 30; i++ {
		if _, err := d.execCapture(ctx, containerID, []string{"uci", "show", "network.lan"}); err == nil {
			break
		}
		logf(fmt.Sprintf("Waiting for uci… (%d/30)", i+1))
		time.Sleep(1 * time.Second)
	}

	lanIP, lanMask := splitCIDR(pol.LANGateway, pol.LANSubnet)
	wanIP, wanMask := splitCIDR(pol.WANIP, pol.WANSubnet)

	// ── 2. Network UCI (each command is a separate exec, no shell wrapper) ────
	logf("Applying network config…")
	uciDel("network.lan")
	uciSet("network.lan=interface")
	uciSet("network.lan.device=br-lan")
	uciSet("network.lan.proto=static")
	uciSet("network.lan.ipaddr=" + lanIP)
	uciSet("network.lan.netmask=" + lanMask)
	uciDel("network.lan.delegate")
	uciDel("network.lan.ip6assign")

	uciDel("network.wan")
	uciSet("network.wan=interface")
	uciSet("network.wan.device=eth1")
	uciSet("network.wan.proto=static")
	uciSet("network.wan.ipaddr=" + wanIP)
	uciSet("network.wan.netmask=" + wanMask)
	uciSet("network.wan.gateway=" + pol.WANGateway)
	uciDel("network.wan.delegate")
	uciDel("network.wan.ip6assign")
	uciDel("network.wan6")

	uciDel("dhcp.lan")
	uciSet("dhcp.lan=dhcp")
	uciSet("dhcp.lan.interface=lan")
	// Enable DHCP on the LAN so containers can optionally use DHCP to get an
	// IP from WRT with the correct default gateway (pol.LANGateway). The pool
	// starts at offset 200 within the subnet (e.g. 172.31.252.200 for the
	// default /22) to avoid colliding with Docker’s IPAM auto-assignments which
	// begin at offset .8 and grow upward. Containers that run a DHCP client
	// (e.g. udhcpc -i eth0 -q) will receive the gateway automatically.
	uciSet("dhcp.lan.start=200")
	uciSet("dhcp.lan.limit=50")
	uciSet("dhcp.lan.leasetime=12h")
	uciDel("dhcp.lan.ignore")

	uciDel("dhcp.@dnsmasq[0].server")
	uciList("dhcp.@dnsmasq[0].server=8.8.8.8")
	uciList("dhcp.@dnsmasq[0].server=1.1.1.1")
	uciSet("dhcp.@dnsmasq[0].rebind_protection=0")

	// ── 3. Firewall UCI ───────────────────────────────────────────────────────
	logf("Applying firewall config…")
	uciSet("firewall.@defaults[0].flow_offloading=0")
	uciSet("firewall.@defaults[0].flow_offloading_hw=0")
	uciDel("firewall.@defaults[0].fullcone")
	uciDel("firewall.@defaults[0].fullcone6")

	// Clear anonymous zones/forwardings (shell loop: count not known ahead of time).
	run(true, []string{"/bin/sh", "-c", "while uci -q delete firewall.@zone[0]; do :; done; while uci -q delete firewall.@forwarding[0]; do :; done"})

	uciDel("firewall.lan")
	uciSet("firewall.lan=zone")
	uciSet("firewall.lan.name=lan")
	uciSet("firewall.lan.network=lan")
	uciSet("firewall.lan.input=ACCEPT")
	uciSet("firewall.lan.forward=ACCEPT")
	uciSet("firewall.lan.output=ACCEPT")

	uciDel("firewall.wan")
	uciSet("firewall.wan=zone")
	uciSet("firewall.wan.name=wan")
	uciSet("firewall.wan.network=wan")
	uciSet("firewall.wan.input=REJECT")
	uciSet("firewall.wan.forward=REJECT")
	uciSet("firewall.wan.output=ACCEPT")
	uciSet("firewall.wan.masq=1")

	uciDel("firewall.fwd_lan_wan")
	uciSet("firewall.fwd_lan_wan=forwarding")
	uciSet("firewall.fwd_lan_wan.src=lan")
	uciSet("firewall.fwd_lan_wan.dest=wan")

	// RFC1918 drop rules (unrolled — 5 fixed CIDRs, no shell loop needed).
	for i, cidr := range []string{"10.0.0.0/8", "169.254.0.0/16", "172.16.0.0/12", "192.168.0.0/16", "127.0.0.0/8"} {
		key := fmt.Sprintf("firewall.drop_rfc1918_%d", i)
		uciDel(key)
		uciSet(key + "=rule")
		uciSet(key + ".src=lan")
		uciSet(key + ".dest=wan")
		uciSet(key + ".dest_ip=" + cidr)
		uciSet(key + ".target=DROP")
	}

	if pol.LuCIHostPort > 0 {
		uciDel("firewall.allow_luci")
		uciSet("firewall.allow_luci=rule")
		uciSet("firewall.allow_luci.name=Allow-LuCI")
		uciSet("firewall.allow_luci.src=wan")
		uciSet("firewall.allow_luci.proto=tcp")
		uciSet("firewall.allow_luci.dest_port=80")
		uciSet("firewall.allow_luci.target=ACCEPT")
	}

	// ── 4. Commit + restart network service ───────────────────────────────────
	logf("Committing UCI changes…")
	run(false, []string{"uci", "commit"})

	logf("Restarting network (/etc/init.d/network restart)…")
	if out := run(true, []string{"/etc/init.d/network", "restart"}); out != "" {
		logf("  network: " + out)
	}
	logf("Waiting 8 s for netifd to settle…")
	time.Sleep(8 * time.Second)

	// ── 5. Reload firewall ────────────────────────────────────────────────────
	logf("Reloading firewall…")
	if out := run(true, []string{"/bin/sh", "-c", "fw4 reload 2>&1 || fw3 reload 2>&1 || /etc/init.d/firewall restart 2>&1 || true"}); out != "" {
		logf("  firewall: " + out)
	}

	// ── 6. nft zone-dispatch workaround ──────────────────────────────────────
	// fw4 compiles zone-dispatch rules with iifname "br-lan", but netifd bridges
	// eth0 into br-lan so the iif at the forward/input hooks is eth0, not br-lan.
	// Insert source-subnet rules directly into the base chains as a workaround.
	// A minimal /bin/sh is used only for the nft handle-parsing pipeline.
	nftScript := "nft_del_mudp() {\n" +
		`  for h in $(nft -a list chain "$1" "$2" 2>/dev/null | grep 'comment "mudp-' | grep -oE 'handle [0-9]+' | awk '{print $2}'); do` + "\n" +
		`    nft delete rule "$1" "$2" handle "$h" 2>/dev/null` + "\n" +
		"  done\n}\n" +
		"nft_del_mudp inet fw4 input\n" +
		"nft_del_mudp inet fw4 forward\n" +
		"nft insert rule inet fw4 input ip saddr " + shellQuote(pol.LANSubnet) + " accept comment \"mudp-allow-lan-input\"\n" +
		"nft insert rule inet fw4 forward iifname \"br-lan\" oifname \"eth1\" accept comment \"mudp-allow-lan-to-wan\"\n" +
		"nft insert rule inet fw4 forward iifname \"eth1\" oifname \"br-lan\" ct state established,related accept comment \"mudp-allow-wan-to-lan-est\"\n"
	// Belt-and-suspenders LuCI rule: add a direct nft ACCEPT for tcp/80 on eth1
	// (WAN side) so LuCI is reachable even if fw4 fails to compile the UCI
	// allow_luci rule (e.g. fw4 errors hidden by the || true fallback above).
	if pol.LuCIHostPort > 0 {
		nftScript += "nft insert rule inet fw4 input iifname \"eth1\" tcp dport 80 accept comment \"mudp-allow-luci-wan\"\n"
	}
	if out := run(true, []string{"/bin/sh", "-c", nftScript}); out != "" {
		logf("  nft: " + out)
	}

	// ── 7. Remove dnsmasq DNS-hijack rule + restart dnsmasq ───────────────────
	run(true, []string{"/bin/sh", "-c",
		`HIJACK_HANDLE=$(nft -a list chain inet dnsmasq prerouting 2>/dev/null | grep -i "DNSMASQ HIJACK" | grep -oE "handle [0-9]+" | awk '{print $2}'); [ -n "$HIJACK_HANDLE" ] && nft delete rule inet dnsmasq prerouting handle "$HIJACK_HANDLE" 2>/dev/null || true`,
	})
	run(true, []string{"/etc/init.d/dnsmasq", "restart"})
	// Restart uhttpd (LuCI web server) so it rebinds port 80 after the network
	// interface IPs were changed by /etc/init.d/network restart above.
	if pol.LuCIHostPort > 0 {
		if out := run(true, []string{"/etc/init.d/uhttpd", "restart"}); out != "" {
			logf("  uhttpd: " + out)
		}
	}

	// ── 8. Verify (after) ────────────────────────────────────────────────────
	if out := run(true, []string{"ifconfig"}); out != "" {
		logf("ifconfig (after):\n" + out)
	}
	logf("WRT config applied.")
}

// wrtConfigScript builds the ImmortalWrt configuration shell script for pol,
// substituting every placeholder with a shell-quoted value. Extracted from
// applyWrtConfig so the template-substitution logic is unit-testable without a
// Docker client. The script is idempotent (safe to re-run on every reconcile)
// and best-effort (no `set -e`).
func wrtConfigScript(pol WRTPolicy) string {
	lanIP, lanMask := splitCIDR(pol.LANGateway, pol.LANSubnet)
	wanIP, wanMask := splitCIDR(pol.WANIP, pol.WANSubnet)

	// NOTE: no `set -e`. `uci -q delete` returns rc=1 for an absent key, and
	// `set -e` would abort the whole script on the first such delete — leaving
	// `uci commit` never reached and the router stuck on factory defaults. The
	// script is idempotent and best-effort; each statement stands on its own and
	// we only care that the final `uci commit` + reload run.
	const script = `# Wait until uci/procd are ready (first boot only).
for i in $(seq 1 30); do command -v uci >/dev/null 2>&1 && break; sleep 1; done
command -v uci >/dev/null 2>&1 || { echo "uci not found; image is not ImmortalWrt?"; exit 0; }

# --- LAN = mesh side. eth0 is the mesh veth (primary Docker endpoint); netifd
# auto-bridges it into br-lan, so we keep device='br-lan'. DHCP off — mesh has
# no other DHCP clients (mudp IPAM assigns their IPs). ---
uci -q delete network.lan
uci set network.lan=interface
uci set network.lan.device='br-lan'
uci set network.lan.proto='static'
uci set network.lan.ipaddr=__LANIP__
uci set network.lan.netmask=__LANMASK__
uci set network.lan.gateway=__LANGW__
uci -q delete network.lan.delegate
uci -q delete network.lan.ip6assign

# --- WAN side. eth1 is the WAN veth (secondary Docker endpoint = mudp-wrt-wan).
# Masquerade happens in the firewall zone below. ---
uci -q delete network.wan
uci set network.wan=interface
uci set network.wan.device='eth1'
uci set network.wan.proto='static'
uci set network.wan.ipaddr=__WANIP__
uci set network.wan.netmask=__WANMASK__
uci set network.wan.gateway=__WANGW__
uci -q delete network.wan.delegate
uci -q delete network.wan.ip6assign
# Drop the default wan6 interface too — it pointed at eth1/dhcp6 and would fight
# our static wan config (the udhcpc on eth1 never gets a lease on this bridge).
uci -q delete network.wan6

# Disable LAN DHCP — tenant containers get their config from mudp IPAM, not wrt.
uci -q delete dhcp.lan
uci set dhcp.lan=dhcp
uci set dhcp.lan.interface='lan'
uci set dhcp.lan.ignore='1'

# Configure dnsmasq upstream resolvers. ImmortalWrt's dnsmasq installs a NAT
# redirect rule ("DNSMASQ HIJACK" in table inet dnsmasq) that captures every
# UDP/53 packet and hands it to the local dnsmasq. On a fresh image dnsmasq has
# no upstream configured (and the container's /etc/resolv.conf points at Docker's
# 127.0.0.11 embedded resolver, which would loop back), so every DNS query ends
# up REFUSED and tenant containers get "Temporary failure resolving". Giving it
# explicit public resolvers fixes this. uci -q delete + add_list is idempotent so
# repeated applies don't accumulate duplicates. rebind_protection off because some
# upstreams return private-space answers that dnsmasq would otherwise drop.
uci -q delete dhcp.@dnsmasq[0].server
uci add_list dhcp.@dnsmasq[0].server='8.8.8.8'
uci add_list dhcp.@dnsmasq[0].server='1.1.1.1'
uci set dhcp.@dnsmasq[0].rebind_protection='0'

# --- firewall globals: disable offloads the container kernel doesn't support. ---
# The image ships with flow_offloading/hardware offload/fullcone enabled by
# default. Inside a container none of those are available, so fw4 reload aborts
# with "Could not process rule: No such file or directory" on the flowtable
# definition and the whole table inet fw4 never gets generated. Turn them off so
# fw4 can compile cleanly (then our zone dispatch + nft rules land).
uci set firewall.@defaults[0].flow_offloading='0'
uci set firewall.@defaults[0].flow_offloading_hw='0'
uci -q delete firewall.@defaults[0].fullcone
uci -q delete firewall.@defaults[0].fullcone6

# --- firewall zones: lan (mesh) <-> wan. wan masquerade ON. ---
# Wipe the image's default anonymous zones (@zone[0], @zone[1]) first: if they
# coexist with our named lan/wan zones below, fw4 aborts reload with
# "redefinition of symbol 'lan_devices'" and the whole table inet fw4 is never
# generated — which in turn makes our nft insert rules below fail with "No such
# file or directory". Loop until no anonymous zone remains (idempotent).
while uci -q delete firewall.@zone[0]; do :; done
# Also clear any stale named zones from a prior run so we start clean.
uci -q delete firewall.lan
uci set firewall.lan=zone
uci set firewall.lan.name='lan'
uci set firewall.lan.network='lan'
uci set firewall.lan.input='ACCEPT'
uci set firewall.lan.forward='ACCEPT'
uci set firewall.lan.output='ACCEPT'

uci -q delete firewall.wan
uci set firewall.wan=zone
uci set firewall.wan.name='wan'
uci set firewall.wan.network='wan'
uci set firewall.wan.input='REJECT'
uci set firewall.wan.forward='REJECT'
uci set firewall.wan.output='ACCEPT'
uci set firewall.wan.masq='1'

# Allow lan -> wan forwarding (the only path LAN traffic may take out).
# Clear any image-default anonymous forwarding first (same redefinition hygiene
# as the zones above) to avoid duplicate lan->wan forward rules.
while uci -q delete firewall.@forwarding[0]; do :; done
uci -q delete firewall.fwd_lan_wan
uci set firewall.fwd_lan_wan=forwarding
uci set firewall.fwd_lan_wan.src='lan'
uci set firewall.fwd_lan_wan.dest='wan'

# DROP lan -> RFC1918 / docker bridges / loopback, so tenants can't reach the
# host, the physical LAN, or the Docker daemon. These rules match traffic
# crossing lan->wan whose destination is a private range.
#
# NOTE: uci values must be BARE here (no quotes). uci set foo.bar='val' would
# store the literal 'val' (including the single quotes), which fw4 then rejects
# as invalid options and skips the whole rule. The double quotes are only for
# shell variable expansion of $key; the value itself is unquoted.
n=0
for cidr in 10.0.0.0/8 169.254.0.0/16 172.16.0.0/12 192.168.0.0/16 127.0.0.0/8; do
  key="drop_rfc1918_$n"
  uci -q delete "firewall.$key"
  uci set "firewall.$key=rule"
  uci set "firewall.$key.src=lan"
  uci set "firewall.$key.dest=wan"
  uci set "firewall.$key.dest_ip=$cidr"
  uci set "firewall.$key.target=DROP"
  n=$((n+1))
done

__LUCI_RULE__

uci commit
# Reload network + firewall. firewall4 (fw4/nftables) is what this image uses;
# fall back to the legacy fw3 path and the init script. reload_config replays
# any committed UCI changes through netifd/firewall procd services.
reload_config 2>/dev/null
fw4 reload 2>/dev/null || fw3 reload 2>/dev/null || /etc/init.d/firewall restart 2>/dev/null || true

# Restart dnsmasq so the upstream resolver config above takes effect right now
# (uci commit only persists; the running dnsmasq keeps its old upstream until
# restarted, which would leave DNS broken for a window after first apply).
/etc/init.d/dnsmasq restart 2>/dev/null

# [fw4 zone-dispatch workaround] On ImmortalWrt-in-Docker, fw4 compiles the zone
# dispatch rules using iifname "br-lan" — but netifd bridges eth0 into br-lan, so
# at the netfilter input/forward hooks the packet's iif is the physical eth0, NOT
# br-lan. The zone rules therefore never match, and the main input/forward chains
# fall through to handle_reject, which answers every SYN with a TCP RST. Symptoms:
#   - tenant containers get "Connection refused" on every outbound (forward chain)
#   - LuCI is unreachable from the LAN side (input chain)
# Match by source subnet instead of interface name (reliable regardless of the
# netifd bridge), and append directly to the base chains so the dispatch runs
# before handle_reject. Idempotent: nft_del_mudp wipes prior mudp-* rules first.
nft_del_mudp() {
  # $1 = table, $2 = chain. Delete every rule whose comment starts "mudp-".
  for h in $(nft -a list chain "$1" "$2" 2>/dev/null | grep 'comment "mudp-' | grep -oE 'handle [0-9]+' | awk '{print $2}'); do
    nft delete rule "$1" "$2" handle "$h" 2>/dev/null
  done
}
nft_del_mudp inet fw4 input
nft_del_mudp inet fw4 forward
nft insert rule inet fw4 input ip saddr __LANSUBNET__ accept comment "mudp-allow-lan-input"
nft insert rule inet fw4 forward iifname "br-lan" oifname "eth1" accept comment "mudp-allow-lan-to-wan"
nft insert rule inet fw4 forward iifname "eth1" oifname "br-lan" ct state established,related accept comment "mudp-allow-wan-to-lan-est"
echo "wrt config applied"
`
	// Build the LuCI firewall rule only when publishing is on. Docker publishes
	// port 80 onto the host via eth1 (the WAN interface), so without this rule
	// the WAN zone's input=REJECT drops the published-port traffic and LuCI is
	// unreachable from the host.
	luciRule := ""
	if pol.LuCIHostPort > 0 {
		luciRule = `# Allow LuCI (tcp/80) inbound on wan — Docker's published port arrives
# via eth1 (wan), which is otherwise REJECT'd by the wan zone.
uci -q delete firewall.allow_luci
uci set firewall.allow_luci=rule
uci set firewall.allow_luci.name='Allow-LuCI'
uci set firewall.allow_luci.src='wan'
uci set firewall.allow_luci.proto='tcp'
uci set firewall.allow_luci.dest_port='80'
uci set firewall.allow_luci.target='ACCEPT'`
	}

	// Substitute the placeholders with shell-quoted values so a malformed
	// policy can't inject shell metacharacters into the script.
	s := script
	s = strings.ReplaceAll(s, "__LANIP__", shellQuote(lanIP))
	s = strings.ReplaceAll(s, "__LANMASK__", shellQuote(lanMask))
	s = strings.ReplaceAll(s, "__LANGW__", shellQuote(pol.LANGateway))
	s = strings.ReplaceAll(s, "__WANIP__", shellQuote(wanIP))
	s = strings.ReplaceAll(s, "__WANMASK__", shellQuote(wanMask))
	s = strings.ReplaceAll(s, "__WANGW__", shellQuote(pol.WANGateway))
	s = strings.ReplaceAll(s, "__LANSUBNET__", shellQuote(pol.LANSubnet))
	s = strings.ReplaceAll(s, "__LUCI_RULE__", luciRule)
	return s
}

// splitCIDR returns the host IP and the mask (in dotted-quad) for a host living
// in subnet. hostIP may already be a bare IP (preferred) or a CIDR; subnet must
// be a CIDR. Used to feed ImmortalWrt's network.lan.ipaddr/netmask separately.
func splitCIDR(hostIP, subnet string) (ip, mask string) {
	ip = hostIP
	if i := strings.IndexByte(ip, '/'); i >= 0 {
		ip = ip[:i]
	}
	if _, ipnet, err := net.ParseCIDR(subnet); err == nil && ipnet != nil {
		mask = ipv4Mask(ipnet.Mask)
	}
	if mask == "" {
		mask = "255.255.255.0" // sane fallback; reload_config will surface a real error
	}
	return ip, mask
}

// ipv4Mask renders an IPv4 mask as dotted-quad ("255.255.252.0"). Returns "" if
// the mask isn't a valid 4-byte mask.
func ipv4Mask(m net.IPMask) string {
	if m == nil || len(m) != 4 {
		return ""
	}
	return net.IP(m).String()
}

// reservedAuxAddresses returns an IPAM AuxAddress map that pins specific IPs as
// reserved, so Docker's default IPAM driver will NOT hand them out to
// auto-allocating containers. This is how we keep the gateway container's fixed
// addresses free for it to claim at attach time.
//
// We reserve a small block of low addresses (.1–.7 by default) rather than just
// the gateway's own IP, to leave headroom for future platform endpoints
// (bridge gateway, the gateway container, potential extra gateways) without
// another IPAM migration. Docker requires each reserved address to actually
// live inside the subnet, so addresses that fall outside are skipped. On any
// parse error the map is empty and the caller falls back to default allocation.
//
// keyPrefix lets the LAN/WAN networks use distinct map keys so they don't
// collide if ever merged.
func reservedAuxAddresses(subnet, keyPrefix string) map[string]string {
	_, ipnet, err := net.ParseCIDR(subnet)
	if err != nil || ipnet == nil {
		return nil
	}
	base := ipnet.IP.To4()
	if base == nil {
		return nil
	}
	out := make(map[string]string)
	const reserve = 8 // reserve .1–.7 (7 addresses), covering bridge+gateway+spares
	cur := make(net.IP, 4)
	copy(cur, base)
	for i := 1; i < reserve; i++ {
		// Increment the last octet by i (subnets here are > /24 so the low
		// block is contiguous within the network address's last octet).
		copy(cur, base)
		cur[3] = base[3] + byte(i)
		if !ipnet.Contains(cur) {
			continue
		}
		out[fmt.Sprintf("%s%d", keyPrefix, i)] = cur.String()
	}
	return out
}

// lanReservedAddrs is the AuxAddress set for the mesh (LAN) network — reserves
// the gateway's fixed LAN address (pol.LANGateway) and a few neighbours.
func lanReservedAddrs(subnet string) map[string]string {
	return reservedAuxAddresses(subnet, "lan")
}

// wanReservedAddrs is the AuxAddress set for the WAN network — reserves the
// gateway's fixed WAN address (pol.WANIP) and a few neighbours.
func wanReservedAddrs(subnet string) map[string]string {
	return reservedAuxAddresses(subnet, "wan")
}

// isAlreadyExistsErr → see network.go

// isConflictErr reports whether err is Docker's container name conflict error
// ("container name … already in use"). Used to detect the race where
// --restart unless-stopped re-created the container between our remove and create.
func isConflictErr(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "already in use") || strings.Contains(msg, "Conflict")
}

// execReadonly runs a command in a container and returns an error if the exit
// code is non-zero. Stdout/stderr are captured for diagnostics. Reuses the same
// exec pattern as container_files.go.
func (d *Client) execReadonly(ctx context.Context, containerID string, cmd []string) error {
	_, err := d.execCapture(ctx, containerID, cmd)
	return err
}

// execCapture runs cmd in the container and returns the combined stdout+stderr
// together with any error (non-zero exit code or Docker API error). It is the
// low-level primitive used by applyWrtConfig to run each UCI command directly.
func (d *Client) execCapture(ctx context.Context, containerID string, cmd []string) (string, error) {
	return d.execCaptureOpts(ctx, containerID, cmd, false)
}

// execPrivileged runs cmd in the container with Privileged=true so the exec
// gains all capabilities regardless of the container's own cap set. Used to
// run network commands (ip route, ifconfig) inside unprivileged containers.
func (d *Client) execPrivileged(ctx context.Context, containerID string, cmd []string) (string, error) {
	return d.execCaptureOpts(ctx, containerID, cmd, true)
}

func (d *Client) execCaptureOpts(ctx context.Context, containerID string, cmd []string, privileged bool) (string, error) {
	execCfg := types.ExecConfig{
		AttachStdout: true,
		AttachStderr: true,
		Privileged:   privileged,
		Cmd:          cmd,
	}
	resp, err := d.c.ContainerExecCreate(ctx, containerID, execCfg)
	if err != nil {
		return "", fmt.Errorf("exec create: %w", err)
	}
	attach, err := d.c.ContainerExecAttach(ctx, resp.ID, types.ExecStartCheck{})
	if err != nil {
		return "", fmt.Errorf("exec attach: %w", err)
	}
	defer attach.Close()
	var stdout, stderr bytes.Buffer
	if _, err := stdcopy.StdCopy(&stdout, &stderr, attach.Reader); err != nil {
		return "", fmt.Errorf("exec copy: %w", err)
	}
	combined := stdout.String() + stderr.String()
	inspect, err := d.c.ContainerExecInspect(ctx, resp.ID)
	if err != nil {
		return combined, fmt.Errorf("exec inspect: %w", err)
	}
	if inspect.ExitCode != 0 {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(stdout.String())
		}
		if msg == "" {
			msg = "exec failed"
		}
		return combined, fmt.Errorf("%s (exit %d)", msg, inspect.ExitCode)
	}
	return combined, nil
}

// WRTStatus reports whether the gateway container is running, for the admin
// dashboard / Networks page so admins can tell at a glance whether the router
// is up or degraded.
type WRTStatus struct {
	Image     string `json:"image"`
	Running   bool   `json:"running"`
	Container string `json:"container,omitempty"`
}

// WRTStatus inspects the gateway container. It never returns an error for "not
// running" — that's a normal state represented by Running=false.
func (d *Client) WRTStatus(ctx context.Context) WRTStatus {
	st := WRTStatus{Image: DefaultWRTImage}
	cj, err := d.c.ContainerInspect(ctx, WRTContainer)
	if err != nil {
		return st
	}
	st.Container = cj.Name
	if cj.Config != nil && cj.Config.Image != "" {
		st.Image = cj.Config.Image
	}
	if cj.State != nil {
		st.Running = cj.State.Running
	}
	return st
}

// WRTLog streams the gateway container's logs (for the admin UI to show router
// boot / procd diagnostics). Caller closes the returned reader.
func (d *Client) WRTLog(ctx context.Context, since time.Time) (io.ReadCloser, error) {
	return d.c.ContainerLogs(ctx, WRTContainer, container.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Follow:     true,
		Tail:       "200",
		Since:      since.Format(time.RFC3339),
	})
}

// isNotFoundErr reports whether err is Docker's "No such container" response.
func isNotFoundErr(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "no such container") || strings.Contains(msg, "not found")
}
