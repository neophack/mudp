package config

import (
	"crypto/rand"
	"encoding/hex"
	"log"
	"os"
	"path/filepath"
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
	DefaultLanguage string
	// TrustedProxies lists the peers (IPs or CIDRs, comma separated) whose
	// X-Forwarded-For header may be believed when attributing a request to a
	// client. Leave empty unless a reverse proxy is actually in front of the
	// server: trusting the header from any peer lets a client forge a new
	// identity per request and slip past the login rate limit.
	TrustedProxies string
	// CaptchaTestAnswers exposes each captcha's answer in a response header.
	// Strictly for automated test harnesses that cannot solve the image; never
	// enable on a production deployment.
	CaptchaTestAnswers bool
}

// Load reads configuration from the environment. Missing secrets are generated
// automatically so the server can start without manual setup.
func Load() Config {
	cfg := Config{
		Addr:            env("MUDP_ADDR", "0.0.0.0:9000"),
		DBPath:          env("MUDP_DB", defaultDBPath()),
		DockerHost:      env("MUDP_DOCKER_HOST", ""),
		DefaultLanguage:   env("MUDP_DEFAULT_LANGUAGE", "en_US"),
		TrustedProxies:    env("MUDP_TRUSTED_PROXIES", ""),
		CaptchaTestAnswers: env("MUDP_CAPTCHA_TEST_ANSWERS", "") != "" && env("MUDP_CAPTCHA_TEST_ANSWERS", "") != "0",
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

// defaultDBPath anchors the database next to the executable rather than the
// (arbitrary) working directory: when run as a Windows service the CWD is
// C:\Windows\System32, and a console started from elsewhere would scatter
// copies of the database per directory.
func defaultDBPath() string {
	exe, err := os.Executable()
	if err != nil {
		return "mudp.db"
	}
	return filepath.Join(filepath.Dir(exe), "mudp.db")
}

func randomSecret() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		log.Fatalf("FATAL: unable to generate session secret: %v", err)
	}
	return hex.EncodeToString(b)
}
