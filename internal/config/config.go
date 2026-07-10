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
	WebDir string
}

// Production reports whether dev affordances (on-disk web assets, verbose logs)
// should be disabled. It is true unless MUDP_WEB_DIR is set.
func (c Config) Production() bool { return c.WebDir == "" }

// Load reads configuration from the environment. Missing secrets are generated
// automatically so the server can start without manual setup.
func Load() Config {
	cfg := Config{
		Addr:       env("MUDP_ADDR", "0.0.0.0:9000"),
		DBPath:     env("MUDP_DB", "mudp.db"),
		DockerHost: env("MUDP_DOCKER_HOST", ""),
		WebDir:     env("MUDP_WEB_DIR", ""),
	}

	cfg.SessionSecret = env("MUDP_SESSION_SECRET", "")
	if cfg.SessionSecret == "" {
		cfg.SessionSecret = randomSecret()
		log.Println("WARNING: MUDP_SESSION_SECRET is not set; using a random secret valid only for this process")
	}

	cfg.AdminUser = env("MUDP_ADMIN_USER", "admin")
	cfg.AdminPassword = env("MUDP_ADMIN_PASSWORD", "")
	if cfg.AdminPassword == "" {
		cfg.AdminPassword = randomPassword()
		log.Printf("WARNING: MUDP_ADMIN_PASSWORD is not set; generated admin password: %s", cfg.AdminPassword)
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

func randomPassword() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		log.Fatalf("FATAL: unable to generate admin password: %v", err)
	}
	return hex.EncodeToString(b)
}
