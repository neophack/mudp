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

func Load() Config {
	secret := env("MUDP_SESSION_SECRET", "")
	if secret == "" {
		secret = randomSecret()
	}
	adminPassword := env("MUDP_ADMIN_PASSWORD", "")
	if adminPassword == "" {
		adminPassword = "admin123"
		if os.Getenv("MUDP_WEB_DIR") == "" {
			log.Println("WARNING: using default admin password; set MUDP_ADMIN_PASSWORD before exposing this server")
		}
	}
	cfg := Config{
		Addr:              env("MUDP_ADDR", "0.0.0.0:9000"),
		DBPath:            env("MUDP_DB", "mudp.db"),
		SessionSecret:     secret,
		AdminUser:         env("MUDP_ADMIN_USER", "admin"),
		AdminPassword:     adminPassword,
		DockerHost:        env("MUDP_DOCKER_HOST", ""),
		WebDir:            env("MUDP_WEB_DIR", ""),
	}
	if cfg.Production() && os.Getenv("MUDP_SESSION_SECRET") == "" {
		log.Println("WARNING: MUDP_SESSION_SECRET is not set; sessions will be invalidated on every restart")
	}
	return cfg
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
		log.Fatalf("FATAL: unable to generate session secret: %v", err)
	}
	return hex.EncodeToString(b)
}
