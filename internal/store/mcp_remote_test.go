package store

import "testing"

func TestMCPRemoteConfigDefaults(t *testing.T) {
	db := newTestDB(t)
	cfg, err := db.MCPRemoteConfig()
	if err != nil {
		t.Fatalf("MCPRemoteConfig: %v", err)
	}
	// A server nobody has configured must still open the admin form on values
	// that would work, and must be off.
	if cfg.Enabled {
		t.Error("external MCP access is enabled by default; it must be opt-in")
	}
	if cfg.Port != DefaultMCPRemotePort {
		t.Errorf("Port = %d, want %d", cfg.Port, DefaultMCPRemotePort)
	}
	if cfg.SafeNetwork != DefaultMCPSafeNetwork {
		t.Errorf("SafeNetwork = %q, want %q", cfg.SafeNetwork, DefaultMCPSafeNetwork)
	}
	if cfg.Public() {
		t.Error("Public() = true with no domain configured")
	}
}

func TestMCPRemoteConfigRoundTrip(t *testing.T) {
	db := newTestDB(t)
	// A pasted URL, not a bare hostname: the stored value has to come back
	// clean or every link built from it doubles the scheme.
	if err := db.SaveMCPRemoteConfig(MCPRemoteConfig{
		Enabled: true, Port: 19091, Domain: "https://mcp.example.com/", SafeNetwork: " lab-lan ",
	}); err != nil {
		t.Fatalf("SaveMCPRemoteConfig: %v", err)
	}
	cfg, err := db.MCPRemoteConfig()
	if err != nil {
		t.Fatalf("MCPRemoteConfig: %v", err)
	}
	if cfg.Domain != "mcp.example.com" {
		t.Errorf("Domain = %q, want %q", cfg.Domain, "mcp.example.com")
	}
	if cfg.SafeNetwork != "lab-lan" {
		t.Errorf("SafeNetwork = %q, want %q", cfg.SafeNetwork, "lab-lan")
	}
	if cfg.Port != 19091 || !cfg.Enabled {
		t.Errorf("Port/Enabled = %d/%v, want 19091/true", cfg.Port, cfg.Enabled)
	}
	if got, want := cfg.BaseURL(), "https://mcp.example.com"; got != want {
		t.Errorf("BaseURL() = %q, want %q", got, want)
	}
	if !cfg.Public() {
		t.Error("Public() = false for an enabled config with a domain and a safe network")
	}
}

func TestNormalizeMCPRemoteConfigRestoresBlanks(t *testing.T) {
	cfg := NormalizeMCPRemoteConfig(MCPRemoteConfig{SafeNetwork: "   "})
	if cfg.SafeNetwork != DefaultMCPSafeNetwork {
		t.Errorf("SafeNetwork = %q, want %q", cfg.SafeNetwork, DefaultMCPSafeNetwork)
	}
	if cfg.Port != DefaultMCPRemotePort {
		t.Errorf("Port = %d, want %d", cfg.Port, DefaultMCPRemotePort)
	}
}

func TestNormalizeMCPRemoteDomain(t *testing.T) {
	cases := map[string]string{
		"mcp.example.com":                "mcp.example.com",
		" https://mcp.example.com/ ":     "mcp.example.com",
		"http://mcp.example.com/mcp":     "mcp.example.com",
		"https://mcp.example.com?x=1":    "mcp.example.com",
		"":                               "",
		"https://a.b.example.com#anchor": "a.b.example.com",
	}
	for in, want := range cases {
		if got := NormalizeMCPRemoteDomain(in); got != want {
			t.Errorf("NormalizeMCPRemoteDomain(%q) = %q, want %q", in, got, want)
		}
	}
}
