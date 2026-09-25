package server

import (
	"path/filepath"
	"testing"
	"time"

	"mudp/internal/store"
)

// seedChunkSession registers an upload session and writes the on-disk resume
// state it needs to survive a snapshot sweep (sessions whose state file is
// gone are dropped as a side effect of snapshotting).
func seedChunkSession(t *testing.T, a *App, root, name string) {
	t.Helper()
	dst := filepath.Join(root, name)
	if err := writeChunkState(dst, &chunkUploadState{
		Size: 100, ChunkSize: 10, TotalChunks: 10,
		Received: map[int]bool{0: true, 1: true},
		UploadID: "test-" + name,
	}); err != nil {
		t.Fatalf("write chunk state %s: %v", name, err)
	}
	a.chunkUploads.start(dst, name, 100, &store.User{ID: 7, Username: "alice"})
}

func TestCachedChunkSnapshotTTL(t *testing.T) {
	a := newForwardApp(t)
	a.activeTasks = NewActiveTaskRegistry()
	a.chunkUploads = NewChunkUploadRegistry()
	root := t.TempDir()

	seedChunkSession(t, a, root, "one.bin")
	first := a.cachedChunkSnapshot()
	if len(first) != 1 {
		t.Fatalf("first snapshot = %d tasks, want 1", len(first))
	}

	// A session registered after the snapshot must stay invisible until the
	// cache expires: /api/tasks polls every few seconds and each poll must not
	// re-read every session's on-disk state.
	seedChunkSession(t, a, root, "two.bin")
	if got := a.cachedChunkSnapshot(); len(got) != 1 {
		t.Fatalf("cached snapshot = %d tasks, want the cached 1", len(got))
	}

	// Expiring the timestamp forces the next call to re-read the registry.
	a.taskSnapAt = time.Time{}
	got := a.cachedChunkSnapshot()
	if len(got) != 2 {
		t.Fatalf("expired snapshot = %d tasks, want 2", len(got))
	}
	// The re-read must have stamped a fresh timestamp (the cache is warm again).
	if a.taskSnapAt.IsZero() {
		t.Fatal("re-read did not refresh taskSnapAt")
	}
}
