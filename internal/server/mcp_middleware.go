package server

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"mudp/internal/dockerx"
	"mudp/internal/httpx"
	"mudp/internal/store"
)

// mcpMinPort / mcpMaxPort define the random-port range for the SSE listener
// when the admin hasn't pinned a port. The range is chosen to sit well above
// the ephemeral Linux range on most kernels and below 60000, so it won't clash
// with Docker-published ports or common services.
const (
	mcpMinPort = 50000
	mcpMaxPort = 59999
)

// mcpSourceGate is the "only allow cloudflared" layer for the SSE listener. It
// rejects any connection whose source IP isn't in the admin-configured
// AllowCIDRs. Defaults to loopback only (a co-located cloudflared). A direct
// hit from the public Internet — even with a valid token — gets a flat 404, so
// the port can't be fingerprinted as an MCP endpoint. Evaluated after geoGate
// and before rate limiting.
func (a *App) mcpSourceGate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := a.mcpPolicy.Load()
		if p == nil || !p.Enabled {
			http.Error(w, "MCP listener disabled", http.StatusServiceUnavailable)
			return
		}
		ipStr := a.clientIP(r)
		ip := net.ParseIP(ipStr)
		allow, _ := a.mcpAllowNets.Load().([]*net.IPNet)
		if ip == nil || !matchAnyIPNet(ip, allow) {
			// Flat 404 — same posture as geoGate, to avoid fingerprinting.
			http.NotFound(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// matchAnyIPNet reports whether ip falls in any of the parsed nets. The slice
// is rebuilt on every policy change, so callers don't need to lock.
func matchAnyIPNet(ip net.IP, nets []*net.IPNet) bool {
	for _, n := range nets {
		if n != nil && n.Contains(ip) {
			return true
		}
	}
	return false
}

// loadAndApplyMCPPolicy reads the persisted MCP policy, resolves the effective
// port (generating + persisting a random one on first enable), applies it to
// the in-memory state, and starts/stops the SSE listener to match. Called once
// at boot; subsequent changes flow through applyMCPPolicy from the settings
// handler.
func (a *App) loadAndApplyMCPPolicy(ctx context.Context) error {
	pol, err := a.db.MCPPolicy()
	if err != nil {
		return err
	}
	// Env override (MUDP_MCP_PORT) wins if set and parseable.
	if port, perr := parsePort(a.cfg.MCPPort); perr == nil && port >= mcpMinPort && port <= mcpMaxPort {
		pol.Port = port
	}
	// First boot with the feature enabled but no usable port: pick a random
	// one in range and persist so it survives restarts.
	if pol.Enabled && (pol.Port < mcpMinPort || pol.Port > mcpMaxPort) {
		pol.Port = randomMcpPort()
		if err := a.db.SaveMCPPolicy(pol); err != nil {
			log.Printf("WARNING: could not persist random mcp port: %v", err)
		}
	}
	a.applyMCPPolicy(pol)
	return nil
}

// applyMCPPolicy swaps the active policy, refreshes the source-allowlist cache,
// and restarts the SSE listener when the enabled flag or port changed. Safe to
// call repeatedly; a no-op when nothing relevant changed.
func (a *App) applyMCPPolicy(p store.MCPPolicy) {
	a.mcpPolicy.Store(&p)
	// Parse + cache the allowlist. Default to loopback only when empty (the
	// common case: cloudflared running on the same host as mudp).
	cidrs := p.AllowCIDRs
	if len(cidrs) == 0 {
		cidrs = []string{"127.0.0.0/8", "::1/128"}
	}
	nets := httpx.ParseCIDRs(strings.Join(cidrs, ","))
	if nets == nil {
		nets = []*net.IPNet{}
	}
	a.mcpAllowNets.Store(nets)
	a.restartMcpListener(p)
}

// restartMcpListener reconciles the live listener with the desired policy. The
// mcpMu lock serializes concurrent restarts (e.g. a rapid admin edit storm).
// If enabled is false, any running listener is shut down. If the port matches
// the running listener, nothing happens.
func (a *App) restartMcpListener(p store.MCPPolicy) {
	a.mcpMu.Lock()
	defer a.mcpMu.Unlock()

	cur := a.mcpListener.Load()

	// Disabled → tear down any running listener.
	if !p.Enabled || p.Port < mcpMinPort || p.Port > mcpMaxPort {
		if cur != nil {
			a.stopMcpListenerLocked()
		}
		return
	}

	// Already listening on the desired port → nothing to do.
	if cur != nil {
		if curTCP, ok := (*cur).Addr().(*net.TCPAddr); ok && curTCP.Port == p.Port {
			return
		}
		// Port changed: stop the old one before starting the new.
		a.stopMcpListenerLocked()
	}

	// Open a fresh listener on the desired port (all interfaces — the source
	// gate + geoGate + token auth are the real guards; the OS firewall is the
	// admin's responsibility, typically "only cloudflared can reach this port").
	addr := ":" + strconv.Itoa(p.Port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Printf("WARNING: mcp listener failed to bind %s: %v (MCP stays disabled until the port is free)", addr, err)
		return
	}
	srv := &http.Server{
		Handler:           a.McpRoutes(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		// WriteTimeout disabled: SSE streams are long-lived.
		WriteTimeout:   0,
		IdleTimeout:    120 * time.Second,
		MaxHeaderBytes: 1 << 20,
	}
	a.mcpServer.Store(srv)
	a.mcpListener.Store(&ln)
	go func(port int) {
		log.Printf("mcp/sse listener on http://0.0.0.0:%d (dedicated MCP port)", port)
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, net.ErrClosed) {
			log.Printf("mcp listener: %v", err)
		}
	}(p.Port)
}

// stopMcpListenerLocked shuts down the current SSE listener and clears the
// atomic slots. Caller must hold a.mcpMu.
func (a *App) stopMcpListenerLocked() {
	if srv := a.mcpServer.Swap(nil); srv != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}
	if lnPtr := a.mcpListener.Swap(nil); lnPtr != nil {
		_ = (*lnPtr).Close()
	}
}

// stopMcpListener is the unlocked shutdown entry point, used by App.Close on
// process exit.
func (a *App) stopMcpListener() {
	a.mcpMu.Lock()
	defer a.mcpMu.Unlock()
	a.stopMcpListenerLocked()
}

// ensureWRTIsolation brings up the mesh + WAN networks and the WRT gateway
// container according to the stored WRTPolicy. Non-fatal on every error path:
// a missing gateway image, for example, degrades to "no outbound Internet" but
// still keeps containers off the LAN/host via the internal mesh network. When
// the policy has Enabled=false, the whole model is skipped.
func (a *App) ensureWRTIsolation(ctx context.Context) {
	pol, err := a.db.WRTPolicy()
	if err != nil {
		log.Printf("WARNING: wrt policy unreadable: %v (using defaults)", err)
		pol = store.DefaultWRTPolicy()
	}
	a.applyWRTPolicy(ctx, pol)
}

// applyWRTPolicy stores pol on the App (so handlers read the live value) and
// reconciles Docker state to match: creates the mesh/WAN networks with the
// policy's subnets and (when enabled) brings up the gateway container. Called
// at boot and on every Networks → WRT save.
func (a *App) applyWRTPolicy(ctx context.Context, pol store.WRTPolicy) {
	a.wrtPolicy.Store(&pol)
	if !pol.Enabled {
		log.Printf("wrt isolation disabled by policy — mesh/WAN networks and gateway not managed")
		return
	}
	dxPol := dockerx.WRTPolicy{
		Enabled:    pol.Enabled,
		Image:      pol.Image,
		LANSubnet:  pol.LANSubnet,
		LANGateway: pol.LANGateway,
		WANSubnet:  pol.WANSubnet,
		WANGateway: pol.WANGateway,
		WANIP:      pol.WANIP,
	}
	if err := a.docker.EnsureSystemNetworksWithPolicy(ctx, dxPol); err != nil {
		log.Printf("WARNING: wrt isolation networks unavailable: %v (container outbound isolation disabled — user containers will reach the LAN/host)", err)
		return
	}
	if err := a.docker.EnsureWRT(ctx, dxPol); err != nil {
		log.Printf("WARNING: wrt gateway not ready: %v (containers will be isolated from LAN/host but will have no outbound Internet until the gateway image is loaded)", err)
		return
	}
	log.Printf("wrt isolation active: user containers on mudp-mesh, outbound NAT via mudp-wrt (%s)", pol.Image)
}

// randomMcpPort returns a uniformly random port in [mcpMinPort, mcpMaxPort].
func randomMcpPort() int {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		// Cryptographic randomness should never fail here; fall back to a fixed
		// sensible port so boot still succeeds.
		return 54321
	}
	span := uint32(mcpMaxPort - mcpMinPort + 1)
	n := binary.BigEndian.Uint32(b[:]) % span
	return mcpMinPort + int(n)
}

// parsePort parses a numeric port string. Returns an error on malformed input.
func parsePort(s string) (int, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, errors.New("empty")
	}
	return strconv.Atoi(s)
}
