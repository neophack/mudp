package config

import (
	"crypto/rand"
	"encoding/hex"
	"log"
	"os"
	"strings"
)

type Config struct {
	Addr          string
	DBPath        string
	SessionSecret string
	AdminUser     string
	AdminPassword string
	// DockerHost overrides the Docker Engine endpoint. Empty = DOCKER_HOST env
	// or the default local socket, resolved by the Docker SDK.
	DockerHost string
	// WebDir, when non-empty, serves the UI from disk (dev mode) instead of the
	// embedded filesystem. Lets you iterate on web/ without rebuilding.
	WebDir          string
	DefaultLanguage string
	// TrustedProxies lists the peers (IPs or CIDRs, comma separated) whose
	// X-Forwarded-For header may be believed when attributing a request to a
	// client. Leave empty unless a reverse proxy is actually in front of the
	// server: trusting the header from any peer lets a client forge a new
	// identity per request and slip past the login rate limit.
	TrustedProxies string
	// GeoIPLookup controls whether the server resolves visitor IPs to a
	// country/city/coordinate via the free ip-api.com endpoint. Off (air-gapped
	// or no-egress deployments) leaves the location fields blank; the access log
	// and its map still work, just without geographic detail. Default is on.
	GeoIPLookup bool
}

// Load reads configuration from the environment. Missing secrets are generated
// automatically so the server can start without manual setup.
func Load() Config {
	cfg := Config{
		Addr:            env("MUDP_ADDR", "0.0.0.0:9000"),
		DBPath:          env("MUDP_DB", "mudp.db"),
		DockerHost:      env("MUDP_DOCKER_HOST", ""),
		WebDir:          env("MUDP_WEB_DIR", ""),
		DefaultLanguage: env("MUDP_DEFAULT_LANGUAGE", "en_US"),
		TrustedProxies:  env("MUDP_TRUSTED_PROXIES", ""),
		GeoIPLookup:     envBool("MUDP_GEOIP_LOOKUP", true),
	}

	cfg.SessionSecret = env("MUDP_SESSION_SECRET", "")
	if cfg.SessionSecret == "" {
		cfg.SessionSecret = randomSecret()
		log.Println("WARNING: MUDP_SESSION_SECRET is not set; using a random secret valid only for this process")
	}

	cfg.AdminUser = env("MUDP_ADMIN_USER", "admin")
	cfg.AdminPassword = env("MUDP_ADMIN_PASSWORD", "")
	if cfg.AdminPassword == "" {
		log.Println("INFO: MUDP_ADMIN_PASSWORD is not set; the first start will require initial setup via the web UI")
	}

	return cfg
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// envBool reads a boolean env var. Empty/unset returns the fallback; "0",
// "false", "no", "off" (case-insensitive) are false, everything else true.
func envBool(key string, fallback bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	switch strings.ToLower(v) {
	case "0", "false", "no", "off":
		return false
	}
	return true
}

func randomSecret() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		log.Fatalf("FATAL: unable to generate session secret: %v", err)
	}
	return hex.EncodeToString(b)
}
