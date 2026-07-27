package server

// External MCP access. The console listener is meant to stay on the LAN, but an
// agent running elsewhere (a laptop off-site, a hosted Claude Code session) has
// no route to it. Rather than expose the whole console, mudp can publish *only*
// the MCP endpoints on a second listener bound to loopback, and let a Cloudflare
// tunnel map a public hostname onto 127.0.0.1:<port>. Nothing else — no console,
// no /api, no netdisk — is reachable through that hostname.
//
// The gate on all of this is the safe network: a token only works over the
// external listener when its container is attached to the Docker network the
// administrator nominated (default "openwrt-lan"). Turning external access on
// therefore never widens what is reachable by itself; an admin still has to put
// a container on the safe network for the internet to see it.

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"mudp/internal/dockerx"
	"mudp/internal/middleware"
	"mudp/internal/store"
)

// remoteMCPBindHost is deliberately loopback-only and not configurable: the
// tunnel daemon runs on the same host, and binding anywhere else would put the
// unauthenticated-at-the-socket-level MCP surface on a real interface where the
// safe-network rule is the only thing between it and the network.
const remoteMCPBindHost = "127.0.0.1"

// remoteMCPKey marks a request as having arrived on the external listener, so
// resolveMcpContainer can apply the safe-network rule to it. It is set by
// middleware on the remote router only — a request on the main listener can
// never carry it, and nothing derived from client input can set it.
const remoteMCPKey contextKey = "mcp-remote"

// markRemoteMCP stamps requests served by the external listener.
func markRemoteMCP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), remoteMCPKey, true)))
	})
}

// isRemoteMCP reports whether the request came in over the external listener.
func isRemoteMCP(r *http.Request) bool {
	v, _ := r.Context().Value(remoteMCPKey).(bool)
	return v
}

// remoteMCPRoutes is the entire surface of the external listener: the three MCP
// transports and a health check. Everything else 404s, so a misconfigured tunnel
// cannot accidentally publish the console.
func (a *App) remoteMCPRoutes() http.Handler {
	r := chi.NewRouter()
	r.Use(a.recoverPanic)
	r.Use(middleware.RequestLogger)
	r.Use(middleware.SecurityHeaders)

	// Every request reaches this listener from the tunnel daemon on loopback, so
	// without honouring its forwarding header the entire internet would share a
	// single rate-limit bucket and one busy agent would 429 the rest. Loopback is
	// the only peer that can connect at all, and a process already running on the
	// host has better options than forging X-Forwarded-For, so trusting it here
	// gives up nothing.
	loopback, err := middleware.ParseTrustedProxies("127.0.0.1,::1")
	if err != nil {
		loopback, _ = middleware.ParseTrustedProxies("")
	}
	limiter := middleware.DefaultAPIRateLimiter().TrustProxies(loopback)

	r.Get("/healthz", a.healthz)
	r.Group(func(r chi.Router) {
		r.Use(limiter.Middleware)
		r.Use(markRemoteMCP)
		r.Post("/mcp/{token}", a.mcpStreamableHTTP)
		r.Get("/mcp/{token}/sse", a.mcpSSE)
		r.Post("/mcp/{token}/messages", a.mcpMessages)
	})
	r.NotFound(func(w http.ResponseWriter, r *http.Request) {
		writeErr(w, http.StatusNotFound, "not found")
	})
	r.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
	})
	return r
}

// remoteMCPState holds the running external listener. Guarded by its own mutex
// so an admin saving settings never contends with the rest of the app.
type remoteMCPState struct {
	mu   sync.Mutex
	srv  *http.Server
	addr string
}

// ApplyRemoteMCP brings the external listener in line with the stored
// configuration — starting it, stopping it, or moving it to a new port. It is
// idempotent, so callers can invoke it at boot and after every settings save
// without checking what changed first.
//
// A port that is already in use is returned as an error rather than logged, so
// the admin who just typed it sees why it did not take.
func (a *App) ApplyRemoteMCP() error {
	cfg, err := a.db.MCPRemoteConfig()
	if err != nil {
		return err
	}
	// No safe network means nothing constrains which containers the internet can
	// reach, so the listener stays down however "enabled" is set.
	want := cfg.Enabled && cfg.SafeNetwork != "" && cfg.Port > 0
	addr := net.JoinHostPort(remoteMCPBindHost, strconv.Itoa(cfg.Port))

	a.remoteMCP.mu.Lock()
	defer a.remoteMCP.mu.Unlock()

	if a.remoteMCP.srv != nil && (!want || a.remoteMCP.addr != addr) {
		a.stopRemoteMCPLocked()
	}
	if !want || a.remoteMCP.srv != nil {
		return nil
	}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("cannot listen on %s: %w", addr, err)
	}
	srv := &http.Server{
		Handler:           a.remoteMCPRoutes(),
		ReadHeaderTimeout: 10 * time.Second,
		// No WriteTimeout, matching the main listener: an SSE stream stays open
		// for the whole agent session.
		WriteTimeout:   0,
		IdleTimeout:    120 * time.Second,
		MaxHeaderBytes: 1 << 20,
	}
	a.remoteMCP.srv = srv
	a.remoteMCP.addr = addr
	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Printf("mcp remote listener on %s stopped: %v", addr, err)
		}
	}()
	log.Printf("mcp remote access listening on http://%s (safe network %q)", addr, cfg.SafeNetwork)
	return nil
}

// StopRemoteMCP shuts the external listener down. Called from App.Close.
func (a *App) StopRemoteMCP() {
	a.remoteMCP.mu.Lock()
	defer a.remoteMCP.mu.Unlock()
	a.stopRemoteMCPLocked()
}

// stopRemoteMCPLocked stops the listener; the caller holds the mutex.
func (a *App) stopRemoteMCPLocked() {
	srv := a.remoteMCP.srv
	a.remoteMCP.srv = nil
	a.remoteMCP.addr = ""
	if srv == nil {
		return
	}
	// Graceful first, but an open SSE stream never returns on its own, so fall
	// back to Close rather than hang a settings save for the full timeout.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		_ = srv.Close()
	}
}

// remoteMCPRunning reports whether the external listener is currently up, and
// on which address.
func (a *App) remoteMCPRunning() (bool, string) {
	a.remoteMCP.mu.Lock()
	defer a.remoteMCP.mu.Unlock()
	return a.remoteMCP.srv != nil, a.remoteMCP.addr
}

// remoteMCPAllowed applies the safe-network rule to one token's container. It
// re-reads the configuration on every call so revoking external access (or
// renaming the safe network) takes effect immediately, without a restart.
//
// The returned message is deliberately specific: the person hitting this is the
// container's own operator, who otherwise has no way to tell "external access is
// off" apart from "my container is on the wrong network".
func (a *App) remoteMCPAllowed(ctx context.Context, containerID string) (bool, string) {
	cfg, err := a.db.MCPRemoteConfig()
	if err != nil {
		return false, "external access unavailable"
	}
	if !cfg.Enabled || cfg.SafeNetwork == "" {
		return false, "external MCP access is disabled"
	}
	names, err := a.docker.ContainerNetworkNames(ctx, containerID)
	if err != nil {
		return false, "container unavailable"
	}
	for _, name := range names {
		if dockerx.NetworkNameMatches(name, cfg.SafeNetwork) {
			return true, ""
		}
	}
	return false, fmt.Sprintf("external access requires the container to be on the safe network %q", cfg.SafeNetwork)
}

// --- management API ---

// mcpRemoteInfo tells the console what external link to show, if any. Readable
// by any activated user: it carries no secret, just the public hostname their
// own token gets appended to.
//
// GET /api/mcp/remote
func (a *App) mcpRemoteInfo(w http.ResponseWriter, r *http.Request) {
	cfg, err := a.db.MCPRemoteConfig()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled":     cfg.Public(),
		"domain":      cfg.Domain,
		"baseUrl":     cfg.BaseURL(),
		"safeNetwork": cfg.SafeNetwork,
	})
}

// mcpRemoteSettings reads and writes the external access configuration.
// Admin-only: it decides what the internet can reach.
//
// GET|POST /api/admin/mcp/remote
func (a *App) mcpRemoteSettings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		cfg, err := a.db.MCPRemoteConfig()
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		running, addr := a.remoteMCPRunning()
		if addr == "" {
			addr = net.JoinHostPort(remoteMCPBindHost, strconv.Itoa(cfg.Port))
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"enabled":     cfg.Enabled,
			"port":        cfg.Port,
			"domain":      cfg.Domain,
			"safeNetwork": cfg.SafeNetwork,
			"baseUrl":     cfg.BaseURL(),
			"listenAddr":  addr,
			"running":     running,
		})
	case http.MethodPost:
		var req store.MCPRemoteConfig
		if err := decodeJSON(r, &req); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		cfg := store.NormalizeMCPRemoteConfig(req)
		if err := a.validateRemoteMCP(cfg); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		// Save, then apply. If the listener will not come up (port already taken,
		// typically) roll the stored config back so what the admin sees on reload
		// matches what is actually running.
		prev, _ := a.db.MCPRemoteConfig()
		if err := a.db.SaveMCPRemoteConfig(cfg); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		if err := a.ApplyRemoteMCP(); err != nil {
			_ = a.db.SaveMCPRemoteConfig(prev)
			_ = a.ApplyRemoteMCP()
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		target := "disabled"
		if cfg.Enabled {
			target = fmt.Sprintf("%s → %s (safe network %s)", cfg.Domain, net.JoinHostPort(remoteMCPBindHost, strconv.Itoa(cfg.Port)), cfg.SafeNetwork)
		}
		a.record(r, "mcp.remote.settings", target)
		running, addr := a.remoteMCPRunning()
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":         true,
			"running":    running,
			"listenAddr": addr,
			"baseUrl":    cfg.BaseURL(),
		})
	default:
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// validateRemoteMCP rejects a configuration that cannot work, before it is
// stored. The domain is required when enabling because the link users copy is
// the whole point of the feature: a listener nobody has a hostname for is just
// an open port.
func (a *App) validateRemoteMCP(cfg store.MCPRemoteConfig) error {
	if cfg.Port < 1024 || cfg.Port > 65535 {
		return fmt.Errorf("port must be between 1024 and 65535")
	}
	if p := listenPort(a.cfg.Addr); p != 0 && p == cfg.Port {
		return fmt.Errorf("port %d is already the console's own port", cfg.Port)
	}
	if !cfg.Enabled {
		return nil
	}
	if cfg.Domain == "" {
		return fmt.Errorf("a public domain is required to enable external access")
	}
	if !validRemoteDomain(cfg.Domain) {
		return fmt.Errorf("%q is not a valid hostname", cfg.Domain)
	}
	if cfg.SafeNetwork == "" {
		return fmt.Errorf("a safe network is required to enable external access")
	}
	return nil
}

// validRemoteDomain does a shape check on a hostname: labels of letters,
// digits, and hyphens separated by dots, with at least one dot. It is not a
// public-suffix check — it exists to catch a pasted URL fragment or a stray
// space before it ends up in a link users are told to trust.
func validRemoteDomain(domain string) bool {
	if len(domain) > 253 || !strings.Contains(domain, ".") {
		return false
	}
	for _, label := range strings.Split(domain, ".") {
		if label == "" || len(label) > 63 || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return false
		}
		for _, ch := range label {
			switch {
			case ch >= 'a' && ch <= 'z', ch >= 'A' && ch <= 'Z', ch >= '0' && ch <= '9', ch == '-':
			default:
				return false
			}
		}
	}
	return true
}

// listenPort extracts the port from a listen address like "0.0.0.0:9000".
// Returns 0 when the address has no parseable port.
func listenPort(addr string) int {
	_, port, err := net.SplitHostPort(strings.TrimSpace(addr))
	if err != nil {
		return 0
	}
	n, err := strconv.Atoi(port)
	if err != nil {
		return 0
	}
	return n
}
