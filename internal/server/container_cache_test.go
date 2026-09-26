package server

import (
	"context"
	"strings"
	"testing"

	"mudp/internal/dockerx"
	"mudp/internal/store"
)

// newCacheTestApp seeds the runtime container cache with two containers owned
// by alice; the docker client from newForwardApp points at a dead port, so any
// code path that reaches Docker fails fast — which is exactly what lets these
// tests prove the cache is answering instead.
func newCacheTestApp(t *testing.T) *App {
	t.Helper()
	a := newForwardApp(t)
	a.cacheMu.Lock()
	a.cachedContainers = []dockerx.Container{
		{ID: "abc123def4567890", Name: "app", FullName: "/mudp-alice-app", Owner: "alice", State: "running",
			Labels: map[string]string{"mudp.user": "alice", "mudp.name": "app", "mudp.managed": "true"}},
		{ID: "fff000aaa1112222", Name: "db", FullName: "/mudp-alice-db", Owner: "alice", State: "exited",
			Labels: map[string]string{"mudp.user": "alice", "mudp.name": "db", "mudp.managed": "true"}},
	}
	a.cacheMu.Unlock()
	return a
}

func cachedState(a *App, id string) (string, float64, bool) {
	a.cacheMu.RLock()
	defer a.cacheMu.RUnlock()
	for _, c := range a.cachedContainers {
		if c.ID == id {
			return c.State, c.MemoryMB, true
		}
	}
	return "", 0, false
}

func TestUpdateCachedContainerState(t *testing.T) {
	a := newCacheTestApp(t)
	a.cacheMu.Lock()
	a.cachedContainers[0].MemoryMB = 42
	a.cacheMu.Unlock()

	// A start must not fabricate a memory figure: it keeps whatever value the
	// cache already holds.
	a.updateCachedContainerState("abc123def4567890", "start")
	if state, mem, _ := cachedState(a, "abc123def4567890"); state != "running" || mem != 42 {
		t.Fatalf("start with a cached figure: state/mem = %q/%v, want running/42", state, mem)
	}

	cases := []struct {
		action, wantState string
		wantMem           float64
	}{
		// A stopped container reports no memory until the background sweep
		// refreshes the figure — and once zeroed, later running-states stay at
		// zero rather than resurrecting the stale value.
		{"stop", "exited", 0},
		{"start", "running", 0},
		{"restart", "running", 0},
		{"pause", "paused", 0},
		{"unpause", "running", 0},
	}
	for _, c := range cases {
		a.updateCachedContainerState("abc123def4567890", c.action)
		state, mem, ok := cachedState(a, "abc123def4567890")
		if !ok || state != c.wantState || mem != c.wantMem {
			t.Errorf("action %s: state/mem = %q/%v (ok=%v), want %q/%v", c.action, state, mem, ok, c.wantState, c.wantMem)
		}
	}

	// Non-mutating pseudo-actions must not touch the cache.
	a.updateCachedContainerState("abc123def4567890", "logs")
	if state, _, _ := cachedState(a, "abc123def4567890"); state != "running" {
		t.Errorf(`action "logs" changed state to %q`, state)
	}
	// Prefix IDs match, mirroring how evictCachedContainers treats short IDs.
	a.updateCachedContainerState("fff0", "stop")
	if state, _, _ := cachedState(a, "fff000aaa1112222"); state != "exited" {
		t.Errorf("short-id stop: state = %q, want exited", state)
	}
	// Unknown IDs are a no-op, not a panic.
	a.updateCachedContainerState("deadbeef", "start")
}

func TestBatchAuditTargetsFromCache(t *testing.T) {
	a := newCacheTestApp(t)
	if err := a.db.CreateUser("alice", "password123", store.RoleUser, 0, 0, 0); err != nil {
		t.Fatalf("create alice: %v", err)
	}
	// The owner is a real account, so the display-name resolution path (not
	// just the raw-username fallback) is exercised.
	targets := a.batchAuditTargets(context.Background(), []string{"abc123def4567890", "abc", "zzzznotfound"})
	if got := targets["abc123def4567890"]; !strings.Contains(got, "app") || !strings.Contains(got, "alice") {
		t.Errorf("full id: target = %q, want it to name the container and owner", got)
	}
	if got := targets["abc"]; got != targets["abc123def4567890"] {
		t.Errorf("short id: target = %q, want the same resolution as the full id (%q)", got, targets["abc123def4567890"])
	}
	// Cache miss falls back to live resolution, which against the dead docker
	// endpoint degrades to the raw id instead of failing.
	if got := targets["zzzznotfound"]; got != "zzzznotfound" {
		t.Errorf("miss: target = %q, want the raw id fallback", got)
	}
}

func TestContainerOwnedByUsesRuntimeCache(t *testing.T) {
	a := newCacheTestApp(t)
	admin := &store.User{Username: "admin", Role: store.RoleAdmin}
	alice := &store.User{Username: "alice", Role: store.RoleUser}
	bob := &store.User{Username: "bob", Role: store.RoleUser}

	if !a.containerOwnedBy(nil, admin, "whatever") {
		t.Error("admin should own everything")
	}
	// Cache hit answers true even though Docker is unreachable — if this
	// reached the live list it would return false.
	if !a.containerOwnedBy(nil, alice, "abc123def4567890") {
		t.Error("alice's own container (cache hit) should be owned")
	}
	if !a.containerOwnedBy(nil, alice, "abc1") {
		t.Error("prefix of alice's own container should be owned")
	}
	// Cache misses fall back to the live list, which against the dead
	// endpoint must fail closed.
	if a.containerOwnedBy(nil, alice, "deadbeef0000") {
		t.Error("unknown container should not be owned when the fallback list fails")
	}
	if a.containerOwnedBy(nil, bob, "abc123def4567890") {
		t.Error("bob should not own alice's container")
	}
}

func TestCarryDiskSizes(t *testing.T) {
	previous := []dockerx.Container{
		{ID: "kept", DiskMB: 42.5},
		{ID: "gone", DiskMB: 99},
	}
	next := []dockerx.Container{
		{ID: "kept", DiskMB: 0},
		{ID: "new", DiskMB: 0},
	}
	out := carryDiskSizes(previous, next)
	if out[0].DiskMB != 42.5 {
		t.Errorf("kept container DiskMB = %v, want the previous 42.5 carried across the unsized sweep", out[0].DiskMB)
	}
	if out[1].DiskMB != 0 {
		t.Errorf("new container DiskMB = %v, want 0 until the next sized sweep", out[1].DiskMB)
	}
	// Empty inputs pass through unchanged (no panic).
	if got := carryDiskSizes(nil, next); len(got) != 2 {
		t.Errorf("empty previous: len = %d, want passthrough", len(got))
	}
	if got := carryDiskSizes(previous, nil); got != nil {
		t.Errorf("empty next: got %v, want nil passthrough", got)
	}
}
