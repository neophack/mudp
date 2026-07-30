package server

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"mudp/internal/middleware"
	"mudp/internal/store"
)

func TestValidateRemoteMCP(t *testing.T) {
	a := &App{}
	a.cfg.Addr = "0.0.0.0:9000"

	ok := store.MCPRemoteConfig{Enabled: true, Port: 19090, Domain: "mcp.example.com", SafeNetwork: "openwrt-lan"}
	if err := a.validateRemoteMCP(ok); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}

	cases := map[string]store.MCPRemoteConfig{
		"privileged port":          {Enabled: true, Port: 80, Domain: "mcp.example.com", SafeNetwork: "openwrt-lan"},
		"port above range":         {Enabled: true, Port: 70000, Domain: "mcp.example.com", SafeNetwork: "openwrt-lan"},
		"console's own port":       {Enabled: true, Port: 9000, Domain: "mcp.example.com", SafeNetwork: "openwrt-lan"},
		"enabled without a domain": {Enabled: true, Port: 19090, SafeNetwork: "openwrt-lan"},
		"enabled without network":  {Enabled: true, Port: 19090, Domain: "mcp.example.com"},
		"malformed domain":         {Enabled: true, Port: 19090, Domain: "not a host", SafeNetwork: "openwrt-lan"},
	}
	for name, cfg := range cases {
		if err := a.validateRemoteMCP(cfg); err == nil {
			t.Errorf("%s: accepted, want rejected", name)
		}
	}

	// A disabled config only has to have a usable port: an admin may save a
	// half-filled form while they wait for DNS.
	if err := a.validateRemoteMCP(store.MCPRemoteConfig{Port: 19090}); err != nil {
		t.Errorf("disabled config rejected: %v", err)
	}
}

func TestValidRemoteDomain(t *testing.T) {
	for _, d := range []string{"mcp.example.com", "a.b.c.example.co.uk", "x-1.example.com"} {
		if !validRemoteDomain(d) {
			t.Errorf("validRemoteDomain(%q) = false, want true", d)
		}
	}
	for _, d := range []string{"", "localhost", "no_underscores.example.com", "-lead.example.com", "trail-.example.com", "a..b.com", "has space.com", "http://mcp.example.com"} {
		if validRemoteDomain(d) {
			t.Errorf("validRemoteDomain(%q) = true, want false", d)
		}
	}
}

func TestListenPort(t *testing.T) {
	cases := map[string]int{
		"0.0.0.0:9000": 9000,
		"  :9000  ":    9000,
		"[::]:8080":    8080,
		"9000":         0,
		"":             0,
	}
	for addr, want := range cases {
		if got := listenPort(addr); got != want {
			t.Errorf("listenPort(%q) = %d, want %d", addr, got, want)
		}
	}
}

// A request must only count as remote when the external listener's middleware
// says so — the safe-network rule hangs off this flag, and a request on the main
// listener can never be allowed to claim it.
func TestIsRemoteMCP(t *testing.T) {
	plain := httptest.NewRequest(http.MethodGet, "/mcp/tok/sse", nil)
	if isRemoteMCP(plain) {
		t.Error("a request with no marker reported as remote")
	}
	// Headers are client-controlled, so nothing a caller can send may set it.
	plain.Header.Set("X-Mcp-Remote", "true")
	if isRemoteMCP(plain) {
		t.Error("a client header set the remote flag")
	}

	var seen bool
	markRemoteMCP(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seen = isRemoteMCP(r)
	})).ServeHTTP(httptest.NewRecorder(), plain)
	if !seen {
		t.Error("markRemoteMCP did not mark the request")
	}
}

// The external listener must expose the MCP transports and nothing else: a
// tunnel pointed at it should not be able to reach the console or the API.
func TestRemoteMCPRoutesSurface(t *testing.T) {
	h := (&App{}).remoteMCPRoutes()
	for _, path := range []string{"/", "/index.html", "/api/containers", "/api/mcp/tokens", "/metrics"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("GET %s on the external listener = %d, want 404", path, rec.Code)
		}
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("GET /healthz = %d, want 200", rec.Code)
	}
}

// safeNetworkReached is what the token-list handler uses to decide whether to
// offer an external link for a token. It must mirror the runtime gate in
// remoteMCPAllowed, so an offered link never points at a URL the gate rejects.
func TestSafeNetworkReached(t *testing.T) {
	pub := store.MCPRemoteConfig{Enabled: true, Domain: "mcp.example.com", SafeNetwork: "openwrt-lan"}

	cases := []struct {
		name  string
		names []string
		cfg   store.MCPRemoteConfig
		want  bool
	}{
		{"on safe network (full name)", []string{"openwrt-lan"}, pub, true},
		// A mudp-managed network is namespaced on the host ("mudp-<user>-net-<display>")
		// but admins type the display name they see; the suffix still matches.
		{"on safe network (managed name)", []string{"mudp-alice-net-openwrt-lan"}, pub, true},
		{"on safe network among others", []string{"bridge", "openwrt-lan"}, pub, true},
		{"not on safe network", []string{"bridge", "host"}, pub, false},
		{"empty attachment", nil, pub, false},
		// A not-yet-published remote has no safe network to gate against.
		{"disabled config", []string{"openwrt-lan"}, store.MCPRemoteConfig{Enabled: false, SafeNetwork: "openwrt-lan"}, false},
		{"no safe network", []string{"openwrt-lan"}, store.MCPRemoteConfig{Enabled: true, Domain: "mcp.example.com"}, false},
		{"no domain", []string{"openwrt-lan"}, store.MCPRemoteConfig{Enabled: true, SafeNetwork: "openwrt-lan"}, false},
	}
	for _, c := range cases {
		if got := safeNetworkReached(c.names, c.cfg); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

// recordMcpAttack must persist one attack row per refused remote request,
// resolving the client IP from the trusted-proxy headers the tunnel sets. This
// exercises the full gather→store path against a real database.
func TestRecordMcpAttack(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "mcp.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Migrate("admin", "test-admin-pw"); err != nil {
		t.Fatal(err)
	}
	tp, _ := middleware.ParseTrustedProxies("127.0.0.1,::1")
	a := &App{db: db, trusted: tp}

	// A request that arrived via the tunnel: loopback peer, real IP in the
	// Cloudflare header.
	r := httptest.NewRequest(http.MethodPost, "/mcp/badtoken", nil)
	r.RemoteAddr = "127.0.0.1:1234"
	r.Header.Set("CF-Connecting-IP", "203.0.113.9")
	// A browser-style UA so parseUserAgent returns a recognizable browser label.
	r.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) Chrome/120.0")
	a.recordMcpAttack(r, "invalid or expired token")

	rows, err := db.MCPAttackLogs(store.MCPAttackFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 attack row, got %d", len(rows))
	}
	got := rows[0]
	if got.IP != "203.0.113.9" {
		t.Errorf("IP not resolved from CF header: got %q", got.IP)
	}
	if got.Reason != "invalid or expired token" {
		t.Errorf("reason mismatch: %q", got.Reason)
	}
	if got.Path != "/mcp/badtoken" {
		t.Errorf("path mismatch: %q", got.Path)
	}
	if got.Browser != "Chrome 120.0" {
		t.Errorf("UA parse mismatch: %q", got.Browser)
	}
}
