package store

import (
	"encoding/json"
	"strings"
)

// External ("remote") MCP access publishes the MCP endpoints a second time on a
// dedicated loopback port, so a Cloudflare tunnel can map a public hostname onto
// 127.0.0.1:<port> without also exposing the console, the API, or the netdisk.
// The whole configuration is one JSON blob in the generic settings table: it is
// read on every remote request and rewritten whole by the admin form, so a
// dedicated table would buy nothing.
const mcpRemoteSettingKey = "mcp_remote"

const (
	// DefaultMCPRemotePort is the loopback port the external MCP listener binds
	// to when an administrator has not chosen another one.
	DefaultMCPRemotePort = 19090
	// DefaultMCPSafeNetwork is the Docker network a container must be attached
	// to before its token may be used from outside. Naming a network is what
	// makes external access safe to offer at all: only containers an admin has
	// deliberately placed on it are reachable from the internet.
	DefaultMCPSafeNetwork = "openwrt-lan"
)

// MCPRemoteConfig is the administrator-owned configuration for external MCP
// access.
type MCPRemoteConfig struct {
	// Enabled starts the second listener. It is not on its own sufficient:
	// SafeNetwork must also be set, and only containers on that network are
	// reachable through it.
	Enabled bool `json:"enabled"`
	// Port is the loopback port the tunnel connects to.
	Port int `json:"port"`
	// Domain is the public hostname the tunnel serves, without a scheme
	// (e.g. "mcp.example.com"). It is what users copy.
	Domain string `json:"domain"`
	// SafeNetwork is the Docker network a container must be on for its token to
	// work over the external listener.
	SafeNetwork string `json:"safeNetwork"`
}

// BaseURL is the public origin users prefix their token path with. Empty when
// no domain has been configured yet. Always https: a Cloudflare hostname
// terminates TLS, and handing out an http:// link would send tokens in clear.
func (c MCPRemoteConfig) BaseURL() string {
	if c.Domain == "" {
		return ""
	}
	return "https://" + c.Domain
}

// Public reports whether there is a usable external link to show a user: the
// listener is on, a safe network constrains it, and a domain points at it.
func (c MCPRemoteConfig) Public() bool {
	return c.Enabled && c.Domain != "" && c.SafeNetwork != ""
}

// MCPRemoteConfig returns the stored configuration with defaults filled in. A
// server that has never been configured reads back as "disabled, port 19090,
// safe network openwrt-lan" so the admin form opens on sensible values.
func (db *DB) MCPRemoteConfig() (MCPRemoteConfig, error) {
	cfg := MCPRemoteConfig{Port: DefaultMCPRemotePort, SafeNetwork: DefaultMCPSafeNetwork}
	raw, err := db.getSetting(mcpRemoteSettingKey)
	if err != nil {
		return cfg, err
	}
	if strings.TrimSpace(raw) == "" {
		return cfg, nil
	}
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return MCPRemoteConfig{Port: DefaultMCPRemotePort, SafeNetwork: DefaultMCPSafeNetwork}, err
	}
	return NormalizeMCPRemoteConfig(cfg), nil
}

// SaveMCPRemoteConfig replaces the stored configuration.
func (db *DB) SaveMCPRemoteConfig(cfg MCPRemoteConfig) error {
	raw, err := json.Marshal(NormalizeMCPRemoteConfig(cfg))
	if err != nil {
		return err
	}
	return db.setSetting(mcpRemoteSettingKey, string(raw))
}

// NormalizeMCPRemoteConfig trims the free-text fields and restores the defaults
// for anything left blank. The domain is stored bare — an admin who pastes
// "https://mcp.example.com/" gets "mcp.example.com", so BaseURL never produces
// a doubled scheme or a doubled slash in the link users copy.
func NormalizeMCPRemoteConfig(cfg MCPRemoteConfig) MCPRemoteConfig {
	cfg.Domain = NormalizeMCPRemoteDomain(cfg.Domain)
	cfg.SafeNetwork = strings.TrimSpace(cfg.SafeNetwork)
	if cfg.SafeNetwork == "" {
		cfg.SafeNetwork = DefaultMCPSafeNetwork
	}
	if cfg.Port == 0 {
		cfg.Port = DefaultMCPRemotePort
	}
	return cfg
}

// NormalizeMCPRemoteDomain strips a scheme, any path, and surrounding
// whitespace from a pasted hostname.
func NormalizeMCPRemoteDomain(domain string) string {
	domain = strings.TrimSpace(domain)
	domain = strings.TrimPrefix(domain, "https://")
	domain = strings.TrimPrefix(domain, "http://")
	if i := strings.IndexAny(domain, "/?#"); i >= 0 {
		domain = domain[:i]
	}
	return strings.TrimSpace(strings.Trim(domain, "/"))
}
