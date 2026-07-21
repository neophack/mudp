package config

import (
	"crypto/rand"
	"encoding/hex"
	"log"
	"os"
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
	// TrustedProxies is a comma-separated list of CIDRs whose connections may
	// set trusted forwarding headers (X-Forwarded-For / X-Real-IP /
	// CF-Connecting-IP). Empty uses the built-in private-LAN default. This is
	// essential when running behind nginx/Caddy/Cloudflare: without it the
	// rate limiter and the geo/brute-force checks see only the proxy's IP and
	// an attacker can forge XFF to bypass them.
	TrustedProxies string
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
		TrustedProxies:  os.Getenv("MUDP_TRUSTED_PROXIES"),
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

func randomSecret() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		log.Fatalf("FATAL: unable to generate session secret: %v", err)
	}
	return hex.EncodeToString(b)
}
