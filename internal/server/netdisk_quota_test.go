package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"mudp/internal/store"
)

// newQuotaTestApp is newNetdiskTestApp plus the pieces the size-cache needs
// outside Routes(): the maps and the background rescan semaphore.
func newQuotaTestApp(t *testing.T) (*App, *store.User, string) {
	t.Helper()
	a, u := newNetdiskTestApp(t)
	a.chunkUploads = NewChunkUploadRegistry()
	a.dirSizeSemaphore = make(chan struct{}, 1)
	a.dirSizeCache = map[string]dirSizeEntry{}
	a.dirSizeRunning = map[string]bool{}
	root, err := a.userNetdiskRoot(u)
	if err != nil {
		t.Fatalf("netdisk root: %v", err)
	}
	return a, u, root
}

func writeNetdiskFile(t *testing.T, root, name string, size int) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, name), bytes.Repeat([]byte{0}, size), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func waitForCachedSize(t *testing.T, a *App, root string, want int64) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		a.dirSizeMu.Lock()
		entry, ok := a.dirSizeCache[root]
		a.dirSizeMu.Unlock()
		if ok && !entry.updated.IsZero() && entry.bytes == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("background rescan did not converge on %d bytes for %s", want, root)
}

// netdiskDeleteRequest drives the delete handler the way the router does:
// handler function + user injected into the request context.
func netdiskDeleteRequest(t *testing.T, a *App, u *store.User, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/netdisk/delete", bytes.NewBufferString(body))
	req = req.WithContext(context.WithValue(req.Context(), userKey, u))
	rec := httptest.NewRecorder()
	a.netdiskDelete(rec, req)
	return rec
}

func decodeJSONBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode response %q: %v", rec.Body.String(), err)
	}
	return out
}

// A cold root must be measured exactly (one synchronous walk), not reported
// as zero from an empty cache — the first upload after startup still has to
// enforce the quota.
func TestNetdiskUsedEstimateColdRoot(t *testing.T) {
	a, _, root := newQuotaTestApp(t)
	writeNetdiskFile(t, root, "a.txt", 100)
	writeNetdiskFile(t, root, "b.txt", 250)

	if got := a.netdiskUsedEstimate(root); got != 350 {
		t.Fatalf("cold estimate = %d, want exact walk 350", got)
	}
	waitForCachedSize(t, a, root, 350)
}

// Once a scan exists, estimates come from the cache (writes mark it stale
// instead of walking), and invalidateDirSize schedules exactly one background
// rescan that converges on the real size.
func TestNetdiskUsedEstimatePrefersCacheUntilInvalidated(t *testing.T) {
	a, _, root := newQuotaTestApp(t)
	a.dirSizeMu.Lock()
	a.dirSizeCache[root] = dirSizeEntry{bytes: 1000, updated: time.Now()}
	a.dirSizeMu.Unlock()
	writeNetdiskFile(t, root, "new.txt", 500)

	if got := a.netdiskUsedEstimate(root); got != 1000 {
		t.Fatalf("estimate = %d, want cached 1000 without a walk", got)
	}
	// The entry is stale but still readable: quota checks keep a bounded
	// baseline instead of dropping to zero.
	a.invalidateDirSize(root)
	a.dirSizeMu.Lock()
	entry := a.dirSizeCache[root]
	a.dirSizeMu.Unlock()
	if !entry.updated.IsZero() || entry.bytes != 1000 {
		t.Fatalf("after invalidation entry = %+v, want zeroed timestamp with bytes 1000", entry)
	}
	// The next quota check reads the stale value and kicks off the rescan,
	// which then lands the real size.
	if got := a.netdiskUsedEstimate(root); got != 1000 {
		t.Fatalf("stale estimate = %d, want the readable 1000", got)
	}
	waitForCachedSize(t, a, root, 500)
}

// Batch delete reports one result per path, removes every listed file, and
// marks the size cache stale; root-normalising paths stay a hard 400.
func TestNetdiskDeletePerItemResults(t *testing.T) {
	a, u, root := newQuotaTestApp(t)
	writeNetdiskFile(t, root, "gone-a.txt", 10)
	writeNetdiskFile(t, root, "gone-b.txt", 20)
	a.dirSizeMu.Lock()
	a.dirSizeCache[root] = dirSizeEntry{bytes: 30, updated: time.Now()}
	a.dirSizeMu.Unlock()

	body := `{"paths":["gone-a.txt","gone-b.txt"]}`
	rec := netdiskDeleteRequest(t, a, u, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete = %d %s", rec.Code, rec.Body.String())
	}
	resp := decodeJSONBody(t, rec)
	if resp["ok"] != true {
		t.Errorf("ok = %v, want true (%s)", resp["ok"], rec.Body.String())
	}
	results, _ := resp["results"].([]any)
	if len(results) != 2 {
		t.Fatalf("results = %v, want one entry per path", resp["results"])
	}
	for _, name := range []string{"gone-a.txt", "gone-b.txt"} {
		if _, err := os.Stat(filepath.Join(root, name)); !os.IsNotExist(err) {
			t.Errorf("%s still exists after delete", name)
		}
	}
	// Deletes invalidate the size cache: the entry must be marked stale for
	// the next quota check.
	a.dirSizeMu.Lock()
	entry := a.dirSizeCache[root]
	a.dirSizeMu.Unlock()
	if !entry.updated.IsZero() {
		t.Errorf("delete did not invalidate the size cache: %+v", entry)
	}
	// The next check reads the stale 30 and kicks the rescan, which lands 0.
	if got := a.netdiskUsedEstimate(root); got != 30 {
		t.Errorf("stale estimate = %d, want 30", got)
	}
	waitForCachedSize(t, a, root, 0)

	// A path that normalises to the root is rejected before anything is removed.
	rootRec := netdiskDeleteRequest(t, a, u, `{"paths":[".."]}`)
	if rootRec.Code != http.StatusBadRequest {
		t.Errorf("root deletion = %d, want 400", rootRec.Code)
	}
}
