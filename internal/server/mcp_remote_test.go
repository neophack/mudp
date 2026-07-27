package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

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
