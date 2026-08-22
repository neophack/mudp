//go:build !windows

package upgrader

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPrecheckDetectsReadOnlyDir guards the fast-fail on the classic Linux
// misconfiguration: binary directory owned by root while the service runs as
// an unprivileged user (or sandboxed by systemd ProtectSystem=strict).
func TestPrecheckDetectsReadOnlyDir(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory permissions")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o755) })
	err := Precheck(filepath.Join(dir, "mudp"))
	if err == nil {
		t.Fatal("Precheck should fail on a read-only directory")
	}
	// The message must name the directory and the user so the operator can
	// fix ownership in one look.
	for _, want := range []string{dir, "not writable"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("Precheck error should mention %q: %v", want, err)
		}
	}
}

// TestPrecheckPassesOnWritableDir is the positive counterpart.
func TestPrecheckPassesOnWritableDir(t *testing.T) {
	dir := t.TempDir()
	if err := Precheck(filepath.Join(dir, "mudp")); err != nil {
		t.Fatalf("Precheck on a writable dir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".mudp-upgrade-probe")); err == nil {
		t.Fatal("Precheck must clean up its probe file")
	}
}
