package dockerx

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/docker/docker/api/types/registry"
)

// AuthForRef returns the registry auth matching the given image reference's
// registry host. Returns an empty string (no auth) when none match. The string
// is the base64-encoded JSON the Docker API expects in X-Registry-Auth.
func AuthForRef(ref string, creds []RegistryCred) string {
	host := registryHost(ref)
	for _, c := range creds {
		if c.Host == host || (c.Host == "docker.io" && (host == "docker.io" || host == "registry-1.docker.io" || host == "")) {
			return encodeAuth(c.Username, c.Token)
		}
	}
	// Fall back to the first cred marked as the default (Host=="").
	if len(creds) > 0 {
		for _, c := range creds {
			if c.Host == "" {
				return encodeAuth(c.Username, c.Token)
			}
		}
	}
	return ""
}

// RegistryCred is the dockerx-local view of a registry credential.
type RegistryCred struct {
	Host     string
	Username string
	Token    string
}

// encodeAuth builds the base64-encoded JSON auth blob the Docker API expects.
func encodeAuth(user, token string) string {
	auth := registry.AuthConfig{
		Username:      user,
		Password:      token,
		ServerAddress: "",
	}
	body, _ := json.Marshal(auth)
	return base64.URLEncoding.EncodeToString(body)
}

// registryHost extracts the registry host from an image reference. For Docker
// Hub images (no host) it returns "docker.io".
func registryHost(ref string) string {
	// Strip any tag/digest suffix.
	if i := strings.LastIndex(ref, "@"); i >= 0 {
		ref = ref[:i]
	}
	if i := strings.LastIndex(ref, ":"); i >= 0 {
		// Only strip if the colon is after the last slash (tag), not a port.
		if !strings.Contains(ref[i+1:], "/") {
			ref = ref[:i]
		}
	}
	if i := strings.Index(ref, "/"); i >= 0 {
		first := ref[:i]
		// A first segment with a dot or colon or "localhost" is a registry host.
		if strings.Contains(first, ".") || strings.Contains(first, ":") || first == "localhost" {
			return first
		}
	}
	return "docker.io"
}

// Login tests registry credentials by performing a registry login.
func (d *Client) Login(ctx context.Context, url, username, token string) error {
	_, err := d.c.RegistryLogin(ctx, registry.AuthConfig{
		Username:      username,
		Password:      token,
		ServerAddress: url,
	})
	if err != nil {
		return fmt.Errorf("registry login failed: %w", err)
	}
	return nil
}
