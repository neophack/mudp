package dockerx

import (
	"context"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
	volumetypes "github.com/docker/docker/api/types/volume"
)

// SystemInfo is the host/environment snapshot shown on the dashboard.
type SystemInfo struct {
	Name       string         `json:"name"`
	OSType     string         `json:"osType"`
	OSVersion  string         `json:"osVersion"`
	Kernel     string         `json:"kernel"`
	Arch       string         `json:"arch"`
	CPUs       int            `json:"cpus"`
	MemoryGB   float64        `json:"memoryGb"`
	DockerVer  string         `json:"dockerVersion"`
	APIVersion string         `json:"apiVersion"`
	StorageDrv string         `json:"storageDriver"`
	ServerTime int64          `json:"serverTime"`
	Containers ContainerStats `json:"containers"`
	Images     ResourceStats  `json:"images"`
	Volumes    ResourceStats  `json:"volumes"`
	Networks   int            `json:"networks"`
	Healthy    bool           `json:"healthy"`
	HealthyMsg string         `json:"healthyMsg,omitempty"`
	AgentCPU   int            `json:"agentCpu"`
	AgentMemMB float64        `json:"agentMemMb"`
	AgentGoRt  string         `json:"agentGoRuntime"`
	// LANIPs lists the host's own non-loopback, non-link-local interface
	// addresses (e.g. 192.168.x.x). Surfaced on the dashboard so an operator
	// can see how to reach the host on the LAN without running ifconfig.
	LANIPs []string `json:"lanIps,omitempty"`
	// PublicIP is the host's best-known public/egress IP, discovered by an
	// outbound probe (see publicIP). It is the server's WAN address — distinct
	// from the visitor's browser IP, which the client detects via WebRTC.
	// Empty when the host has no outbound connectivity (intranet/offline) or
	// the probe hasn't completed yet; the dashboard then renders "—".
	PublicIP string `json:"publicIp,omitempty"`
}

// ContainerStats breaks container counts down by lifecycle state.
type ContainerStats struct {
	Total     int `json:"total"`
	Running   int `json:"running"`
	Stopped   int `json:"stopped"`
	Paused    int `json:"paused"`
	Healthy   int `json:"healthy"`
	Unhealthy int `json:"unhealthy"`
}

// ResourceStats counts a Docker resource kind plus its on-disk footprint.
type ResourceStats struct {
	Count  int     `json:"count"`
	SizeB  int64   `json:"sizeBytes"`
	SizeMB float64 `json:"sizeMb"`
}

// SystemInfo gathers the platform-wide environment snapshot used by the
// admin dashboard. Every sub-query is best-effort: a missing piece never
// fails the whole call so a partially-reachable daemon still renders a
// usable dashboard. Counts span every mudp-managed resource on the host.
func (d *Client) SystemInfo(ctx context.Context) SystemInfo {
	return d.gatherSystemInfo(ctx, "")
}

// SystemInfoForUser gathers the same environment snapshot but scopes the
// resource counts (containers, volumes, networks, images) to a single user.
// Environment/host fields (OS, kernel, CPUs, memory, Docker version, agent)
// are always host-wide since they are identical for everyone. Used by the
// non-admin dashboard so a user sees only their own footprint. An empty
// username falls back to the platform-wide rollup.
func (d *Client) SystemInfoForUser(ctx context.Context, username string) SystemInfo {
	return d.gatherSystemInfo(ctx, username)
}

// gatherSystemInfo is the shared implementation for SystemInfo and
// SystemInfoForUser. username == "" means platform-wide (admin view);
// any other value restricts resource counts to that owner.
func (d *Client) gatherSystemInfo(ctx context.Context, username string) SystemInfo {
	scoped := username != ""
	out := SystemInfo{
		Name:       hostname(),
		AgentGoRt:  runtime.Version(),
		ServerTime: time.Now().Unix(),
		AgentCPU:   runtime.NumCPU(),
		LANIPs:     localIPs(),
		PublicIP:   publicIP(),
	}
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	out.AgentMemMB = round2(float64(m.Alloc) / 1024 / 1024)

	info, err := d.c.Info(ctx)
	if err != nil {
		out.HealthyMsg = fmt.Sprintf("Docker unreachable: %v", err)
		return out
	}
	out.Healthy = true
	out.OSType = info.OSType
	out.OSVersion = info.OperatingSystem
	out.Kernel = info.KernelVersion
	out.Arch = info.Architecture
	out.CPUs = info.NCPU
	out.MemoryGB = round2(float64(info.MemTotal) / 1024 / 1024 / 1024)
	out.StorageDrv = info.Driver
	// Environment/host fields above are identical for everyone; resource
	// counts below are computed from the mudp-managed list and, when scoped,
	// further narrowed to the caller's own resources.

	if ver, err := d.c.ServerVersion(ctx); err == nil {
		out.DockerVer = ver.Version
		out.APIVersion = ver.APIVersion
	}

	// Determine the set of image IDs the scoped user's containers actually
	// reference. For the platform-wide view this stays empty, which signals
	// "use the mudp-published image catalog" in the images section below.
	var userImageIDs map[string]bool
	if scoped {
		userImageIDs = d.userImageSet(ctx, username)
	}

	// Images.
	//   Platform-wide: only mudp-published images (tagged with the mudp prefix).
	//   Scoped: only the distinct images this user's containers reference,
	//           since images carry no per-user owner label.
	//   Either way, internal derived images (final fused runtime images) are
	//   skipped so the dashboard only counts
	//   real user-facing base images.
	if imgs, err := d.c.ImageList(ctx, types.ImageListOptions{}); err == nil {
		var size int64
		count := 0
		for _, im := range imgs {
			if isDerivedImage(im) {
				continue
			}
			var match bool
			if scoped {
				match = userImageIDs[im.ID]
			} else {
				for _, tag := range im.RepoTags {
					if strings.HasPrefix(tag, Prefix) && !strings.Contains(tag, "<none>") {
						match = true
						break
					}
				}
			}
			if match {
				count++
				size += im.Size
			}
		}
		out.Images = ResourceStats{Count: count, SizeB: size, SizeMB: round2(float64(size) / 1024 / 1024)}
	}

	// Volumes: mudp-managed; scoped to the caller's own when requested.
	if dv, err := d.c.VolumeList(ctx, volumetypes.ListOptions{Filters: managedVolumeFilter(username)}); err == nil {
		var size int64
		for _, v := range dv.Volumes {
			if v.UsageData != nil {
				size += v.UsageData.Size
			}
		}
		out.Volumes = ResourceStats{Count: len(dv.Volumes), SizeB: size, SizeMB: round2(float64(size) / 1024 / 1024)}
	}

	// Networks: count what the user would see in the Networks view — mudp-managed
	// networks (the caller's own, when scoped) plus Docker's built-in defaults
	// (bridge, host, none). This keeps the dashboard tile consistent with the
	// Networks page for both admins and regular users.
	if nets, err := d.c.NetworkList(ctx, types.NetworkListOptions{}); err == nil {
		count := 0
		for _, n := range nets {
			if IsSystemNetworkName(n.Name) {
				count++
				continue
			}
			if n.Labels[ManagedLabel] != "true" {
				continue
			}
			if scoped && n.Labels[UserLabel] != username {
				continue
			}
			count++
		}
		out.Networks = count
	}

	// Containers: mudp-managed, broken down by lifecycle state; scoped to the
	// caller's own when requested.
	containerFilter := managedLabelFilter(username)
	if list, err := d.c.ContainerList(ctx, container.ListOptions{All: true, Filters: containerFilter}); err == nil {
		cs := ContainerStats{Total: len(list)}
		for _, c := range list {
			switch c.State {
			case "running":
				cs.Running++
			case "paused":
				cs.Paused++
			case "exited", "dead", "created":
				cs.Stopped++
			}
		}
		out.Containers = cs
	}

	// Health rollup needs per-container listing (same scope as above).
	if list, err := d.c.ContainerList(ctx, container.ListOptions{All: true, Filters: healthFilter("healthy", username)}); err == nil {
		out.Containers.Healthy = len(list)
	}
	if list, err := d.c.ContainerList(ctx, container.ListOptions{All: true, Filters: healthFilter("unhealthy", username)}); err == nil {
		out.Containers.Unhealthy = len(list)
	}
	return out
}

// userImageSet returns the distinct image IDs backing the containers a user
// owns. Used to scope the dashboard's image count to what that user actually
// runs, since Docker images carry no per-user owner label. Best-effort: on
// any error it returns an empty set so the caller degrades to "no images".
func (d *Client) userImageSet(ctx context.Context, username string) map[string]bool {
	set := map[string]bool{}
	args := filters.NewArgs(filters.Arg("label", ManagedLabel+"=true"), filters.Arg("label", UserLabel+"="+username))
	list, err := d.c.ContainerList(ctx, container.ListOptions{All: true, Filters: args})
	if err != nil {
		return set
	}
	for _, c := range list {
		if c.ImageID != "" {
			set[c.ImageID] = true
		}
	}
	return set
}

// derivedImageTagPrefixes are the repo-tag prefixes of internal fused images
// the dashboard should NOT count. They are build artifacts layered on top of
// real user-facing base images (mudp-fused-..., mudp-fused-validate-...).
var derivedImageTagPrefixes = []string{
	Prefix + "fused-",
	Prefix + "fused-validate-",
}

// isDerivedImage reports whether an image is an internal fused build artifact
// that the dashboard should exclude from its image count. It checks the
// mudp.fused / mudp.fused.layer labels first (the modern path) and falls back
// to the derived tag prefixes for legacy images built before labels existed.
func isDerivedImage(im image.Summary) bool {
	if im.Labels["mudp.fused"] == "true" || im.Labels["mudp.fused.layer"] == "true" {
		return true
	}
	for _, tag := range im.RepoTags {
		for _, p := range derivedImageTagPrefixes {
			if strings.HasPrefix(tag, p) {
				return true
			}
		}
	}
	return false
}

// DockerPing reports whether the engine responds. Used by health endpoints.
func (d *Client) DockerPing(ctx context.Context) error {
	_, err := d.c.Ping(ctx)
	return err
}

// healthFilter builds a label+health filter for the container health rollups.
// A non-empty username additionally scopes the match to that owner.
func healthFilter(state, username string) filters.Args {
	args := filters.NewArgs()
	args.Add("label", ManagedLabel+"=true")
	args.Add("health", state)
	if username != "" {
		args.Add("label", UserLabel+"="+username)
	}
	return args
}

// managedLabelFilter matches only mudp-managed resources (label
// mudp.managed=true). A non-empty username additionally scopes the match to
// that owner, so dashboard counts reflect what a user actually owns. An empty
// username keeps the platform-wide behavior.
func managedLabelFilter(username string) filters.Args {
	args := filters.NewArgs()
	args.Add("label", ManagedLabel+"=true")
	if username != "" {
		args.Add("label", UserLabel+"="+username)
	}
	return args
}

// managedVolumeFilter is the volume-list equivalent of managedLabelFilter.
func managedVolumeFilter(username string) filters.Args {
	return managedLabelFilter(username)
}

// round2 rounds to two decimals using a small epsilon to absorb binary
// float representation drift (e.g. 1.005 stored as 1.00499999...).
func round2(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	if v >= float64(math.MaxInt64)/100 {
		return float64(math.MaxInt64) / 100
	}
	if v <= float64(math.MinInt64)/100 {
		return float64(math.MinInt64) / 100
	}
	return float64(int(v*100+0.5+1e-9)) / 100
}

func hostname() string {
	if h, err := os.Hostname(); err == nil && h != "" {
		return h
	}
	return "docker-host"
}

// localIPs returns the host's own non-loopback, non-link-local interface
// addresses (IPv4 and IPv6). It mirrors what `ip addr` would show minus the
// noise, so the dashboard can tell an operator how to reach the host on the LAN.
// Best-effort: any interface-enumeration error yields an empty list.
func localIPs() []string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []string
	for _, ifc := range ifaces {
		// Skip down interfaces and loopback (we already exclude 127.x/::1
		// below, but a disabled adapter need not be scanned at all).
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			var ip net.IP
			switch v := addr.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			if ip == nil {
				continue
			}
			// Drop loopback, link-local (fe80::/169.254.x), and unspecified
			// addresses so only routable host IPs remain.
			if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() {
				continue
			}
			out = append(out, ip.String())
		}
	}
	return out
}

// Server public/egress IP discovery.
//
// The dashboard polls /api/dashboard every few seconds, and the "Public IP"
// row used to flicker between "—" and the value because the cache was empty
// for the first render(s) and re-probed on every poll. A server's egress IP
// effectively never changes during a process lifetime, so we resolve it once
// (blocking the very first caller a few seconds) and then serve the sticky
// value forever. A failed probe is also sticky: an intranet/offline host
// won't gain outbound connectivity next poll, so we stop hammering the
// external services and the dashboard shows a stable "—". Restart the agent
// to retry.

var (
	publicIPMu       sync.RWMutex
	publicIPValue    string // sticky cached value; "" means unknown
	publicIPResolved bool   // true once a probe (success or failure) has completed
	publicIPProbing  atomic.Bool
)

// publicIPFirstWait is how long the first-ever caller blocks for the probe.
// It covers a normal outbound round-trip so the first dashboard render already
// shows the value; a slower/unreachable host gives up and returns "" while the
// background probe keeps running and fills the sticky cache for later renders.
const publicIPFirstWait = 5 * time.Second

// publicIP returns the server's public/egress IP. It blocks briefly on the
// first call so the first dashboard render has a value; subsequent calls read
// the sticky cache without any network I/O. See the package comment above for
// the rationale.
func publicIP() string {
	publicIPMu.RLock()
	val, resolved := publicIPValue, publicIPResolved
	publicIPMu.RUnlock()
	if resolved {
		// Sticky: serve whatever the one probe landed (value or "").
		return val
	}

	// First call: kick off the probe (deduped across concurrent callers) and
	// wait a short while for it. Whoever wins the CAS owns the one probe.
	owned := false
	if publicIPProbing.CompareAndSwap(false, true) {
		owned = true
		go func() {
			defer publicIPProbing.Store(false)
			ip := probeEgressIP()
			publicIPMu.Lock()
			publicIPValue = ip
			publicIPResolved = true
			publicIPMu.Unlock()
		}()
	}

	// Poll the cache for up to publicIPFirstWait. If the probe finishes in
	// time the caller gets the value; otherwise it returns "" and later
	// renders pick up the sticky result once the goroutine lands.
	deadline := time.Now().Add(publicIPFirstWait)
	for time.Now().Before(deadline) {
		publicIPMu.RLock()
		val, resolved = publicIPValue, publicIPResolved
		publicIPMu.RUnlock()
		if resolved {
			return val
		}
		time.Sleep(50 * time.Millisecond)
	}
	_ = owned
	return val
}

// egressProbeURLs are tried in order; the first that returns a bare IP wins.
// A small, well-known mix keeps a single blocked host from stalling detection.
var egressProbeURLs = []string{
	"https://api.ipify.org",
	"https://ifconfig.me/ip",
	"https://ident.me",
}

// probeEgressIP discovers the server's public-facing IP by asking an external
// "what is my IP" service. Each request has a short timeout so an unreachable
// host resolves quickly to "". Returns "" if no service answers with a valid
// IP (intranet/offline) — the caller treats that as "unknown".
func probeEgressIP() string {
	client := &http.Client{Timeout: 3 * time.Second}
	for _, url := range egressProbeURLs {
		req, err := http.NewRequest(http.MethodGet, url, nil)
		if err != nil {
			continue
		}
		resp, err := client.Do(req)
		if err != nil {
			continue
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 64))
		resp.Body.Close()
		if err != nil {
			continue
		}
		ip := net.ParseIP(strings.TrimSpace(string(body)))
		if ip != nil {
			return ip.String()
		}
	}
	return ""
}
