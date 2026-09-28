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

// ScopeBasis carries the host-wide owner/size facts the server needs to derive
// a single user's dashboard resource counts in memory (see server.scopedSystem)
// instead of re-querying Docker on every dashboard poll. ImageSizes holds every
// non-derived image on the host; Volumes/Networks hold mudp-managed resources
// with their owner label, plus Docker's built-in system networks.
type ScopeBasis struct {
	ImageSizes map[string]int64
	Volumes    []VolumeScope
	Networks   []NetworkScope
}

// VolumeScope is one mudp-managed volume with its owner label and usage.
type VolumeScope struct {
	Owner string
	SizeB int64
}

// NetworkScope is one network visible to the dashboard: either a Docker
// built-in (System) or a mudp-managed one with its owner label.
type NetworkScope struct {
	Owner   string
	System  bool
	Managed bool
}

// SizeMB renders a byte figure as rounded megabytes for the dashboard tiles.
func SizeMB(b int64) float64 { return round2(float64(b) / 1024 / 1024) }

// SystemInfo gathers the platform-wide environment snapshot used by the
// admin dashboard. Every sub-query is best-effort: a missing piece never
// fails the whole call so a partially-reachable daemon still renders a
// usable dashboard. Counts span every mudp-managed resource on the host.
func (d *Client) SystemInfo(ctx context.Context) SystemInfo {
	sys, _ := d.gatherSystemInfo(ctx)
	return sys
}

// SystemInfoWithScopeBasis is SystemInfo plus the ScopeBasis the server uses
// to derive per-user scoped views in memory. The basis is a free by-product:
// it reuses the very image/volume/network listings the snapshot itself needs,
// so the 15s sweep pays no extra Docker calls for it.
func (d *Client) SystemInfoWithScopeBasis(ctx context.Context) (SystemInfo, ScopeBasis) {
	return d.gatherSystemInfo(ctx)
}

// gatherSystemInfo builds the platform-wide snapshot. Host fields (OS, kernel,
// CPUs, memory, Docker version, agent) are host-wide facts; resource counts
// span every mudp-managed resource on the host.
func (d *Client) gatherSystemInfo(ctx context.Context) (SystemInfo, ScopeBasis) {
	basis := ScopeBasis{
		ImageSizes: map[string]int64{},
		Volumes:    []VolumeScope{},
		Networks:   []NetworkScope{},
	}
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
		return out, basis
	}
	out.Healthy = true
	out.OSType = info.OSType
	out.OSVersion = info.OperatingSystem
	out.Kernel = info.KernelVersion
	out.Arch = info.Architecture
	out.CPUs = info.NCPU
	out.MemoryGB = round2(float64(info.MemTotal) / 1024 / 1024 / 1024)
	out.StorageDrv = info.Driver

	if ver, err := d.c.ServerVersion(ctx); err == nil {
		out.DockerVer = ver.Version
		out.APIVersion = ver.APIVersion
	}

	// Images. The snapshot counts only mudp-published images (tagged with the
	// mudp prefix); internal derived images (final fused runtime images) are
	// skipped so the dashboard only counts real user-facing base images. The
	// basis keeps every non-derived image so a user's own view can later pick
	// out the images their containers reference.
	if imgs, err := d.c.ImageList(ctx, types.ImageListOptions{}); err == nil {
		var size int64
		count := 0
		for _, im := range imgs {
			if isDerivedImage(im) {
				continue
			}
			basis.ImageSizes[im.ID] = im.Size
			for _, tag := range im.RepoTags {
				if strings.HasPrefix(tag, Prefix) && !strings.Contains(tag, "<none>") {
					count++
					size += im.Size
					break
				}
			}
		}
		out.Images = ResourceStats{Count: count, SizeB: size, SizeMB: SizeMB(size)}
	}

	// Volumes: mudp-managed, with per-owner facts recorded for the basis.
	if dv, err := d.c.VolumeList(ctx, volumetypes.ListOptions{Filters: managedVolumeFilter("")}); err == nil {
		var size int64
		for _, v := range dv.Volumes {
			var usage int64
			if v.UsageData != nil {
				usage = v.UsageData.Size
			}
			size += usage
			basis.Volumes = append(basis.Volumes, VolumeScope{Owner: v.Labels[UserLabel], SizeB: usage})
		}
		out.Volumes = ResourceStats{Count: len(dv.Volumes), SizeB: size, SizeMB: SizeMB(size)}
	}

	// Networks: count what the Networks view shows — mudp-managed networks plus
	// Docker's built-in defaults (bridge, host, none). The basis keeps both
	// kinds so a user's view can count system networks plus their own.
	if nets, err := d.c.NetworkList(ctx, types.NetworkListOptions{}); err == nil {
		count := 0
		for _, n := range nets {
			managed := n.Labels[ManagedLabel] == "true"
			system := IsSystemNetworkName(n.Name)
			if !managed && !system {
				continue
			}
			count++
			basis.Networks = append(basis.Networks, NetworkScope{
				Owner: n.Labels[UserLabel], System: system, Managed: managed,
			})
		}
		out.Networks = count
	}

	// Containers: mudp-managed, broken down by lifecycle state. The health
	// rollup reads the health status the summary already carries, so the old
	// two extra filtered ContainerList calls per snapshot are gone.
	if list, err := d.c.ContainerList(ctx, container.ListOptions{All: true, Filters: managedLabelFilter("")}); err == nil {
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
			switch HealthFromStatus(c.Status) {
			case "healthy":
				cs.Healthy++
			case "unhealthy":
				cs.Unhealthy++
			}
		}
		out.Containers = cs
	}
	return out, basis
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
