package upgrader

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// The integration tests below exercise RestartAndWatch for real: they spawn
// this same test binary as the "replacement process", in a mode selected by
// the MUDP_UPGRADE_TEST_* environment variables.
//
//	serve  — bind MUDP_UPGRADE_TEST_ADDR and answer /healthz (a healthy new
//	         version); /exit terminates the child so the test can clean up.
//	crash  — rewrite the mode file to "serve" and exit immediately: the
//	         "broken new binary". After the watcher rolls back, the respawned
//	         OLD copy of the same binary reads "serve" and comes up healthy —
//	         modelling "new version cannot start, previous version can".

func TestUpgradeHelperProcess(t *testing.T) {
	dir := os.Getenv("MUDP_UPGRADE_TEST_DIR")
	if dir == "" {
		return // regular `go test` run: nothing to do
	}
	modeFile := filepath.Join(dir, "child-mode")
	mode, err := os.ReadFile(modeFile)
	if err != nil {
		os.Exit(2)
	}
	if string(mode) == "crash" {
		// The old binary (respawned after rollback) must be healthy.
		_ = os.WriteFile(modeFile, []byte("serve"), 0o644)
		os.Exit(1)
	}
	addr := os.Getenv("MUDP_UPGRADE_TEST_ADDR")
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"status":"healthy"}`))
	})
	mux.HandleFunc("/exit", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("bye"))
		os.Exit(0)
	})
	fmt.Fprintf(os.Stderr, "child serving on %s\n", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		fmt.Fprintf(os.Stderr, "child server: %v\n", err)
		os.Exit(3)
	}
}

// childArgs runs the replacement copy of this test binary in helper mode.
var childArgs = []string{"-test.run=TestUpgradeHelperProcess", "--"}

func childExe(t *testing.T, dir string) string {
	t.Helper()
	name := "mudp"
	if runtime.GOOS == "windows" {
		name = "mudp.exe"
	}
	exe := filepath.Join(dir, name)
	data, err := os.ReadFile(os.Args[0])
	if err != nil {
		t.Fatalf("read test binary: %v", err)
	}
	if err := os.WriteFile(exe, data, 0o755); err != nil {
		t.Fatalf("stage test binary: %v", err)
	}
	return exe
}

func freePort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().String()
}

func stopChild(t *testing.T, addr string) {
	t.Helper()
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get("http://" + addr + "/exit")
	if err == nil {
		resp.Body.Close()
	}
	time.Sleep(200 * time.Millisecond)
}

// watchTimeout bounds each WaitHealthy phase inside the tests. Real upgrades
// use upgradeWatchTimeout in the server; here it only needs to outlive the
// child test-binary startup (~4 s on a slow machine).
const watchTimeout = 10 * time.Second

func TestRestartAndWatchHealthy(t *testing.T) {
	if testing.Short() {
		t.Skip("process-spawning test skipped in -short mode")
	}
	dir := t.TempDir()
	exe := childExe(t, dir)
	addr := freePort(t)
	t.Setenv("MUDP_UPGRADE_TEST_DIR", dir)
	t.Setenv("MUDP_UPGRADE_TEST_ADDR", addr)
	if err := os.WriteFile(filepath.Join(dir, "child-mode"), []byte("serve"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stopChild(t, addr) })

	rolledBack, err := RestartAndWatch(exe, childArgs, addr, watchTimeout)
	if err != nil || rolledBack {
		t.Fatalf("RestartAndWatch = (%v, %v), want (false, nil)", rolledBack, err)
	}
	// No rollback means no side files were created.
	if _, err := os.Stat(SidecarPath(exe, ".failed")); err == nil {
		t.Fatal("healthy takeover must not park a failed binary")
	}
}

func TestRestartAndWatchRollbackOnCrash(t *testing.T) {
	if testing.Short() {
		t.Skip("process-spawning test skipped in -short mode")
	}
	dir := t.TempDir()
	exe := childExe(t, dir)
	addr := freePort(t)
	t.Setenv("MUDP_UPGRADE_TEST_DIR", dir)
	t.Setenv("MUDP_UPGRADE_TEST_ADDR", addr)
	// "New" binary crashes once; the backup (same bits in real life the old
	// release) then serves — the flip happens in the child helper.
	if err := os.WriteFile(filepath.Join(dir, "child-mode"), []byte("crash"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteMarker(exe, Marker{From: "v1", To: "v2-broken"}); err != nil {
		t.Fatal(err)
	}
	bak := SidecarPath(exe, ".bak")
	if err := copyFile(exe, bak); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stopChild(t, addr) })

	rolledBack, err := RestartAndWatch(exe, childArgs, addr, watchTimeout)
	if err != nil || !rolledBack {
		t.Fatalf("RestartAndWatch = (%v, %v), want (true, nil)", rolledBack, err)
	}
	// Rollback contract: old binary back in place, marker cleared, failed
	// binary parked, and the old version now serving.
	if m := ReadMarker(exe); m != nil {
		t.Fatalf("rollback should clear the marker, got %+v", m)
	}
	if _, err := os.Stat(SidecarPath(exe, ".failed")); err != nil {
		t.Fatalf("broken binary should be parked as .failed: %v", err)
	}
	resp, err := (&http.Client{Timeout: 3 * time.Second}).Get("http://" + addr + "/healthz")
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("old binary should be serving after rollback: %v %v", resp, err)
	}
	if resp != nil {
		resp.Body.Close()
	}
}

func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o755)
}

// TestDownloadOverwritesStaleMode downloads over a pre-existing dest with no
// exec bit (a leftover from an aborted attempt) and verifies the result is
// executable — the umask/stale-mode trap on Linux.
func TestDownloadOverwritesStaleMode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("new-binary"))
	}))
	defer srv.Close()
	dest := filepath.Join(t.TempDir(), "mudp.new")
	if err := os.WriteFile(dest, []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Download(context.Background(), srv.URL, dest, nil); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(dest)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode()&0o111 == 0 {
			t.Fatal("downloaded binary must be executable")
		}
	}
	data, _ := os.ReadFile(dest)
	if string(data) != "new-binary" {
		t.Fatalf("dest = %q, want fresh download", data)
	}
}
