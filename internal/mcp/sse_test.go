package mcp

import (
	"context"
	"testing"
)

// TestSSEHubActiveForContainer verifies the in-use indicator's data source: a
// container with an open session counts as active, and closing the session
// returns it to zero. Two sessions on one container both count.
func TestSSEHubActiveForContainer(t *testing.T) {
	hub := NewSSEHub()
	srv := newTestServer()
	ctx := context.Background()

	if got := hub.ActiveForContainer("c1"); got != 0 {
		t.Fatalf("empty hub reports %d active for c1", got)
	}
	if got := hub.ActiveForContainer(""); got != 0 {
		t.Errorf("empty containerID should never match: %d", got)
	}

	s1 := hub.OpenSession(srv, ctx, "c1")
	if got := hub.ActiveForContainer("c1"); got != 1 {
		t.Errorf("one open session: got %d, want 1", got)
	}
	// A second session on the same container (a second agent) adds up.
	s2 := hub.OpenSession(srv, ctx, "c1")
	if got := hub.ActiveForContainer("c1"); got != 2 {
		t.Errorf("two open sessions: got %d, want 2", got)
	}
	// A different container is independent.
	hub.OpenSession(srv, ctx, "c2")
	if got := hub.ActiveForContainer("c2"); got != 1 {
		t.Errorf("c2 should have 1: got %d", got)
	}

	hub.Close(s1)
	hub.Close(s2)
	if got := hub.ActiveForContainer("c1"); got != 0 {
		t.Errorf("after closing both: got %d, want 0", got)
	}
}
