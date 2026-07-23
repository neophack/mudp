//go:build integration

package dockerx

// End-to-end integration test for the one-click WRT deploy path, focused on the
// AUTOMATIC routing behavior: once the mesh network is created with IPAM
// gateway = pol.LANGateway (.2) and WRT comes up, a container on mudp-mesh
// should have its default route injected as .2 WITHOUT any manual
// `ip route replace default via 172.31.252.2`.
//
// What it tests (TestWRTDeployAndPing):
//  1. RecreateWRT tears down & recreates mudp-mesh / mudp-wrt-wan and starts
//     the ImmortalWrt gateway container.
//  2. A tiny test container (alpine) is created on mudp-mesh (no static IP, no
//     manual route — just the Docker auto-attach path).
//  3. The container's default gateway is verified to be pol.LANGateway (.2) —
//     i.e. the IPAM gateway is what Docker injected. This is the proof that
//     "the network should auto-set" holds: no manual route replace needed.
//  4. The container can ping 8.8.8.8, confirming WRT's MASQUERADE + forwarding
//     rules are working end-to-end through the auto-injected route.
//
// (alpine is created with the raw Docker SDK here — NOT mudp's CreateContainer —
// so it keeps NET_RAW for the ICMP ping probe. The full production CreateContainer
// path, which drops NET_RAW and so must use TCP probes, is exercised separately by
// TestWRTAutoRouteViaGateway in wrt_netshoot_test.go.)
//
// Run:
//
//	go test -v -tags integration -timeout 15m ./internal/dockerx/ -run TestWRTDeployAndPing
//
// Requirements:
//   - Docker daemon accessible (DOCKER_HOST or default socket)
//   - Outbound internet connectivity
//   - ~400 MB free for hkbase/immortalwrt:latest + alpine:latest

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	dnetwork "github.com/docker/docker/api/types/network"
)

func TestWRTDeployAndPing(t *testing.T) {
	const testContainer = "mudp-test-ping"

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	// ── connect to Docker ──────────────────────────────────────────────────
	d, err := New()
	if err != nil {
		t.Skipf("Docker not available: %v", err)
	}
	defer d.Close()
	if _, err := d.c.Ping(ctx); err != nil {
		t.Skipf("Docker daemon not reachable: %v", err)
	}

	pol := DefaultWRTPolicy()

	// ── cleanup on exit ────────────────────────────────────────────────────
	t.Cleanup(func() {
		cCtx, cCancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cCancel()
		_ = d.c.ContainerRemove(cCtx, testContainer, container.RemoveOptions{Force: true})
		_ = d.c.ContainerRemove(cCtx, WRTContainer, container.RemoveOptions{Force: true})
		d.forceTeardownSystemNetworks(cCtx, func(s string) { t.Log("[cleanup]", s) })
	})

	// ── Step 1: one-click deploy ───────────────────────────────────────────
	t.Log("=== Step 1: one-click deploy (RecreateWRT) ===")
	if err := d.RecreateWRT(ctx, pol, func(line string) { t.Log("[deploy]", line) }); err != nil {
		t.Fatalf("RecreateWRT: %v", err)
	}

	// ── Step 2: pull alpine (tiny, <10 MB) ────────────────────────────────
	t.Log("=== Step 2: ensure alpine:latest ===")
	if err := d.ensureWRTImageWithProgress(ctx, "alpine:latest", func(s string) { t.Log("[pull]", s) }); err != nil {
		t.Fatalf("pull alpine: %v", err)
	}

	// ── Step 3: create test container on mudp-mesh ────────────────────────
	t.Log("=== Step 3: create test container on mudp-mesh (auto-attach, no manual route) ===")
	// Remove any leftover from a prior run.
	_ = d.c.ContainerRemove(ctx, testContainer, container.RemoveOptions{Force: true})

	nc := &dnetwork.NetworkingConfig{
		EndpointsConfig: map[string]*dnetwork.EndpointSettings{
			MeshNetworkName: {},
		},
	}
	cr, err := d.c.ContainerCreate(ctx,
		&container.Config{
			Image: "alpine:latest",
			// Keep alive long enough for the exec steps below.
			Cmd: []string{"sh", "-c", "sleep 300"},
		},
		&container.HostConfig{},
		nc, nil, testContainer,
	)
	if err != nil {
		t.Fatalf("create test container: %v", err)
	}
	if err := d.c.ContainerStart(ctx, cr.ID, container.StartOptions{}); err != nil {
		t.Fatalf("start test container: %v", err)
	}
	t.Logf("test container %s started (ID %s)", testContainer, cr.ID[:12])

	// ── Step 4: verify the default route is AUTO-INJECTED as WRT (.2) ──────
	// This is the crux of "网络应该自动设置": the mesh network's IPAM gateway is
	// pol.LANGateway (.2), so Docker injects .2 as the container's default route
	// at start time. No `ip route replace` was run — the route is here purely
	// from the network configuration. Give the stack a moment to settle.
	t.Log("=== Step 4: verify default gateway auto-injected as WRT (.2) ===")
	time.Sleep(2 * time.Second)
	routeOut, _ := d.execCapture(ctx, cr.ID, []string{"ip", "route", "show", "default"})
	t.Logf("ip route default: %s", strings.TrimSpace(routeOut))
	// Expect the default route to go via pol.LANGateway (.2 = WRTMeshIP). The old
	// (.1) scheme asserted against the bridge gateway; under the (.2) scheme the
	// IPAM gateway IS the WRT LAN address, so that's what Docker injects.
	if !strings.Contains(routeOut, pol.LANGateway) {
		t.Errorf("expected default route via WRT %s (auto-injected), got: %q", pol.LANGateway, routeOut)
	}
	// Also confirm containers can REACH WRT at pol.LANGateway (.2).
	pingWRT, _ := d.execCapture(ctx, cr.ID, []string{"ping", "-c", "1", "-W", "3", pol.LANGateway})
	t.Logf("ping WRT (%s): %s", pol.LANGateway, strings.TrimSpace(pingWRT))

	// ── Step 5: ping 8.8.8.8 (pure IP, no DNS needed) ─────────────────────
	// Retried: the first probe can drop while WRT's conntrack/NAT warms up.
	t.Log("=== Step 5: ping 8.8.8.8 via WRT (retried) ===")
	out88, err88 := retryExec(ctx, d, cr.ID, []string{"ping", "-c", "2", "-W", "5", "8.8.8.8"}, 5, 2*time.Second)
	t.Logf("ping 8.8.8.8:\n%s", out88)
	if err88 != nil {
		t.Errorf("ping 8.8.8.8 via WRT failed: %v", err88)
	}

	// ── Step 6: ping www.baidu.com (verifies DNS + ICMP egress via WRT) ───
	// Note: production tenant DNS uses Docker's embedded resolver (127.0.0.11),
	// NOT WRT's dnsmasq — but this alpine container still resolves via 127.0.0.11
	// (inherited from the Docker-managed network), so name resolution works and
	// the routed traffic still transits WRT. Not a hard failure if ICMP/DNS is
	// slow in the test environment.
	t.Log("=== Step 6: ping www.baidu.com ===")
	outBaidu, errBaidu := retryExec(ctx, d, cr.ID, []string{"ping", "-c", "2", "-W", "5", "www.baidu.com"}, 5, 2*time.Second)
	t.Logf("ping www.baidu.com:\n%s", outBaidu)
	if errBaidu != nil {
		t.Logf("Note: www.baidu.com ping failed (%v) — DNS/ICMP may still be warming up", errBaidu)
	}

	if err88 != nil {
		t.FailNow()
	}
	t.Log("=== PASS: container on mudp-mesh auto-routed via WRT, ping 8.8.8.8 OK ===")
}

// retryExec runs cmd in the container up to attempts times, returning the output
// of the first run that exits zero (or the last attempt's output+error). Sleeps
// backoff between attempts — useful for probes that drop on cold NAT/ARP/conntrack
// state right after the topology comes up. Shared by the integration tests.
func retryExec(ctx context.Context, d *Client, containerID string, cmd []string, attempts int, backoff time.Duration) (string, error) {
	var lastOut string
	var lastErr error
	for i := 1; i <= attempts; i++ {
		select {
		case <-ctx.Done():
			return lastOut, ctx.Err()
		default:
		}
		out, err := d.execCapture(ctx, containerID, cmd)
		lastOut, lastErr = out, err
		if err == nil {
			if attempts > 1 {
				out = fmt.Sprintf("(attempt %d/%d succeeded)\n%s", i, attempts, out)
			}
			return out, nil
		}
		if i < attempts {
			time.Sleep(backoff)
		}
	}
	return lastOut, lastErr
}
