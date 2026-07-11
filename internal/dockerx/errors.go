package dockerx

import (
	"errors"
	"os"

	"github.com/docker/docker/client"
)

// IsUnavailableError reports whether err indicates the Docker engine is
// unreachable (daemon not running, socket missing, permission denied, etc.).
func IsUnavailableError(err error) bool {
	if err == nil {
		return false
	}
	if client.IsErrConnectionFailed(err) {
		return true
	}
	// Also surface missing socket files or permission-denied errors as
	// Docker-unavailable so callers can return a clear 503 instead of 500.
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, os.ErrPermission) {
		return true
	}
	return false
}
