package server

import "testing"

// TestFusedCacheKeyDeterminism locks in the contract that the fused-image cache
// key is deterministic for identical inputs and distinct when any input
// changes (base image ID, script bodies, or enable flags). This is the invariant
// that makes fused-image reuse correct: same inputs → same key → cache hit.
func TestFusedCacheKeyDeterminism(t *testing.T) {
	const ssh = "install openssh"
	const vscode = "install code-server"
	h := hashScripts(ssh, vscode)

	a := fusedCacheKey("sha256:abc", h, true, false)
	b := fusedCacheKey("sha256:abc", h, true, false)
	if a != b {
		t.Fatalf("same inputs produced different cache keys: %s vs %s", a, b)
	}

	// Different base image → different key.
	c := fusedCacheKey("sha256:xyz", h, true, false)
	if a == c {
		t.Fatalf("different base image ID produced the same cache key")
	}

	// Different scripts → different key.
	h2 := hashScripts("install openssh-server", vscode)
	d := fusedCacheKey("sha256:abc", h2, true, false)
	if a == d {
		t.Fatalf("different script bodies produced the same cache key")
	}

	// Different enable flags → different key.
	e := fusedCacheKey("sha256:abc", h, true, true)
	if a == e {
		t.Fatalf("different enable flags produced the same cache key")
	}
}
