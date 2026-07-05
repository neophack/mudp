package config

import (
	"crypto/rand"
	"encoding/hex"
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
	WebDir string
}

func Load() Config {
	return Config{
		Addr:          env("MUDP_ADDR", "127.0.0.1:9000"),
		DBPath:        env("MUDP_DB", "mudp.db"),
		SessionSecret: env("MUDP_SESSION_SECRET", randomSecret()),
		AdminUser:     env("MUDP_ADMIN_USER", "admin"),
		AdminPassword: env("MUDP_ADMIN_PASSWORD", "admin123"),
		DockerHost:    env("MUDP_DOCKER_HOST", ""),
		WebDir:        env("MUDP_WEB_DIR", ""),
	}
}

// Production reports whether dev affordances (on-disk web assets, verbose logs)
// should be disabled. It is true unless MUDP_WEB_DIR is set.
func (c Config) Production() bool { return c.WebDir == "" }

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func randomSecret() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "mudp-dev-secret-change-me"
	}
	return hex.EncodeToString(b)
}
