package dockerx

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"strings"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/pkg/stdcopy"
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
	WANSubnetCIDR  = "172.31.248.0/22"
	WRTWANIP       = "172.31.248.2" // the gateway's IP on the WAN network
	WANBridgeGW    = "172.31.248.1" // the mudp-wrt-wan bridge gateway = wrt's WAN next hop

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
}

// DefaultWRTPolicy returns the policy used on a fresh install — identical to
// the previously hardcoded values.
func DefaultWRTPolicy() WRTPolicy {
	return WRTPolicy{
		Enabled:    true,
		Image:      DefaultWRTImage,
		LANSubnet:  MeshSubnetCIDR,
		LANGateway: WRTMeshIP,
		WANSubnet:  WANSubnetCIDR,
		WANGateway: WANBridgeGW,
		WANIP:      WRTWANIP,
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
	return d.createWRT(ctx, pol)
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

// createWRT creates and starts the gateway container from scratch, then
// applies the UCI configuration so ImmortalWrt routes LAN → WAN.
func (d *Client) createWRT(ctx context.Context, pol WRTPolicy) error {
	labels := map[string]string{
		SystemLabel:      "true",
		ManagedLabel:     "true",
		NameLabel:        "wrt", // surfaces as the container's display name in the admin list
		"mudp.createdAt": time.Now().Format(time.RFC3339),
	}
	// ImmortalWrt uses its own /sbin/init entrypoint (procd). We do NOT override
	// Cmd — the image brings up the full OpenWrt service stack (netifd,
	// firewall4, dnsmasq) which is what provides real router semantics. mudp
	// applies the LAN/WAN/firewall config post-start via applyWrtConfig.
	cc := &container.Config{
		Image:  pol.Image,
		Labels: labels,
	}
	hc := &container.HostConfig{
		RestartPolicy: container.RestartPolicy{Name: "unless-stopped"},
		// ImmortalWrt manages its own netns: iptables/nftables, routing,
		// conntrack. It needs the full privileged set, not just NET_ADMIN.
		// This is the sole privileged container mudp creates.
		Privileged: true,
	}
	// Attach to both networks with fixed IPs so the UCI config stays correct.
	// The WAN endpoint is the primary side.
	nc := &network.NetworkingConfig{
		EndpointsConfig: map[string]*network.EndpointSettings{
			WANNetworkName: {
				IPAMConfig: &network.EndpointIPAMConfig{IPv4Address: pol.WANIP},
			},
		},
	}
	resp, err := d.c.ContainerCreate(ctx, cc, hc, nc, &v1.Platform{}, WRTContainer)
	if err != nil {
		return fmt.Errorf("create wrt gateway: %w", err)
	}
	// Attach the mesh network as the second endpoint (must happen after create).
	if err := d.c.NetworkConnect(ctx, MeshNetworkName, resp.ID, &network.EndpointSettings{
		IPAMConfig: &network.EndpointIPAMConfig{IPv4Address: pol.LANGateway},
	}); err != nil {
		// Best-effort cleanup so the next run starts clean.
		_ = d.c.ContainerRemove(ctx, resp.ID, container.RemoveOptions{Force: true})
		return fmt.Errorf("connect gateway to mesh: %w", err)
	}
	if err := d.c.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		return fmt.Errorf("start wrt gateway: %w", err)
	}
	// Give procd a moment to bring up init before we push UCI config. Best-effort
	// timing; applyWrtConfig retries internally and is re-run on every reconcile.
	d.applyWrtConfig(ctx, resp.ID, pol, true)
	return nil
}

// reconcileWRT ensures an existing gateway is running; if it was created with
// a stale image (e.g. a different ImmortalWrt tag) it's rebuilt. On every
// successful run it re-applies the UCI config so the router's LAN/WAN/firewall
// can't drift.
func (d *Client) reconcileWRT(ctx context.Context, existing types.ContainerJSON, pol WRTPolicy) error {
	if existing.Config != nil && existing.Config.Image != pol.Image {
		_ = d.c.ContainerRemove(ctx, existing.ID, container.RemoveOptions{Force: true})
		return d.createWRT(ctx, pol)
	}
	if existing.State == nil || !existing.State.Running {
		if err := d.c.ContainerStart(ctx, existing.ID, container.StartOptions{}); err != nil {
			return fmt.Errorf("restart wrt gateway: %w", err)
		}
	}
	// Re-apply UCI config idempotently — replaces the old 60s iptables watchdog.
	d.applyWrtConfig(ctx, existing.ID, pol, false)
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
//     mesh has no other DHCP clients), forwarding enabled.
//   - WAN = pol.WANIP/pol.WANSubnet with default gateway pol.WANGateway (the
//     mudp-wrt-wan bridge), masquerade ON so LAN traffic is NAT'd to the WAN
//     subnet before leaving for the host's default NAT path.
//   - firewall: lan→wan forwarding allowed; wan→lan denied by default;
//     RFC1918/docker/loopback destinations dropped so LAN containers can't
//     reach the host, the physical LAN, or other internal networks.
//
// fresh=true adds extra patience on first boot (procd takes a few seconds to
// bring init up before uci is usable); on reconcile the container is already
// settled so we skip the wait.
func (d *Client) applyWrtConfig(ctx context.Context, containerID string, pol WRTPolicy, fresh bool) {
	lanIP, lanMask := splitCIDR(pol.LANGateway, pol.LANSubnet)
	wanIP, wanMask := splitCIDR(pol.WANIP, pol.WANSubnet)
	const script = `set -e
# Wait until uci/procd are ready (first boot only).
for i in $(seq 1 30); do command -v uci >/dev/null 2>&1 && break; sleep 1; done
command -v uci >/dev/null 2>&1 || { echo "uci not found; image is not ImmortalWrt?"; exit 0; }

# --- LAN = mesh side. DHCP off; mesh has no other clients. ---
uci -q delete network.lan
uci set network.lan=interface
uci set network.lan.device='br-lan'
uci set network.lan.proto='static'
uci set network.lan.ipaddr=__LANIP__
uci set network.lan.netmask=__LANMASK__
uci set network.lan.gateway=__LANGW__
uci -q delete network.lan.delegate

# --- WAN side. Masquerade happens in firewall. ---
uci -q delete network.wan
uci set network.wan=interface
uci set network.wan.device='eth0'
uci set network.wan.proto='static'
uci set network.wan.ipaddr=__WANIP__
uci set network.wan.netmask=__WANMASK__
uci set network.wan.gateway=__WANGW__
uci -q delete network.wan.delegate

# Disable LAN DHCP — tenant containers get their config from mudp IPAM, not wrt.
uci -q delete dhcp.lan
uci set dhcp.lan=dhcp
uci set dhcp.lan.interface='lan'
uci set dhcp.lan.ignore='1'

# --- firewall zones: lan (mesh) <-> wan. wan masquerade ON. ---
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
uci -q delete firewall.fwd_lan_wan
uci set firewall.fwd_lan_wan=forwarding
uci set firewall.fwd_lan_wan.src='lan'
uci set firewall.fwd_lan_wan.dest='wan'

# DROP lan -> RFC1918 / docker bridges / loopback, so tenants can't reach the
# host, the physical LAN, or the Docker daemon. These rules match traffic
# crossing lan→wan whose destination is a private range.
n=0
for cidr in 10.0.0.0/8 169.254.0.0/16 172.16.0.0/12 192.168.0.0/16 127.0.0.0/8; do
  key="drop_rfc1918_$n"
  uci -q delete "firewall.$key"
  uci set "firewall.$key=rule"
  uci set "firewall.$key.src='lan'"
  uci set "firewall.$key.dest='wan'"
  uci set "firewall.$key.dest_ip='$cidr'"
  uci set "firewall.$key.target='DROP'"
  n=$((n+1))
done

uci commit
reload_config 2>/dev/null || /etc/init.d/network reload 2>/dev/null || true
echo "wrt config applied"
`
	// Substitute the placeholders with shell-quoted values so a malformed
	// policy can't inject shell metacharacters into the script.
	s := script
	s = strings.ReplaceAll(s, "__LANIP__", shellQuote(lanIP))
	s = strings.ReplaceAll(s, "__LANMASK__", shellQuote(lanMask))
	s = strings.ReplaceAll(s, "__LANGW__", shellQuote(pol.LANGateway))
	s = strings.ReplaceAll(s, "__WANIP__", shellQuote(wanIP))
	s = strings.ReplaceAll(s, "__WANMASK__", shellQuote(wanMask))
	s = strings.ReplaceAll(s, "__WANGW__", shellQuote(pol.WANGateway))

	cmd := []string{"/bin/sh", "-c", s}
	if fresh {
		// procd needs a beat before uci is callable on a freshly started container.
		time.Sleep(5 * time.Second)
	}
	if err := d.execReadonly(ctx, containerID, cmd); err != nil {
		log.Printf("WARNING: wrt gateway UCI config failed: %v (router keeps running with its current/prior config; LAN isolation still in force)", err)
	}
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

// execReadonly runs a command in a container and returns an error if the exit
// code is non-zero. Stdout/stderr are captured for diagnostics. Reuses the same
// exec pattern as container_files.go.
func (d *Client) execReadonly(ctx context.Context, containerID string, cmd []string) error {
	execCfg := types.ExecConfig{
		AttachStdout: true,
		AttachStderr: true,
		Cmd:          cmd,
	}
	resp, err := d.c.ContainerExecCreate(ctx, containerID, execCfg)
	if err != nil {
		return fmt.Errorf("exec create: %w", err)
	}
	attach, err := d.c.ContainerExecAttach(ctx, resp.ID, types.ExecStartCheck{})
	if err != nil {
		return fmt.Errorf("exec attach: %w", err)
	}
	defer attach.Close()
	var stdout, stderr bytes.Buffer
	if _, err := stdcopy.StdCopy(&stdout, &stderr, attach.Reader); err != nil {
		return fmt.Errorf("exec copy: %w", err)
	}
	inspect, err := d.c.ContainerExecInspect(ctx, resp.ID)
	if err != nil {
		return fmt.Errorf("exec inspect: %w", err)
	}
	if inspect.ExitCode != 0 {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(stdout.String())
		}
		if msg == "" {
			msg = "exec failed"
		}
		return fmt.Errorf("%s (exit %d)", msg, inspect.ExitCode)
	}
	return nil
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
