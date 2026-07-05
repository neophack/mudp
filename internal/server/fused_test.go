package server

import "testing"

// TestFusedLayerCacheKeyIndependence verifies that SSH and VSCode incremental
// layer keys depend only on their own script and the base image, so a layer can
// be reused across final-image combinations.
func TestFusedLayerCacheKeyIndependence(t *testing.T) {
	const base = "sha256:abc"
	const ssh = "install openssh"
	const vscode = "install code-server"

	k1 := sshLayerCacheKey(base, ssh)
	k2 := sshLayerCacheKey(base, ssh)
	if k1 != k2 {
		t.Fatalf("ssh layer key not deterministic: %s vs %s", k1, k2)
	}

	// SSH layer key must not change when VSCode script changes.
	k3 := sshLayerCacheKey(base, ssh)
	if k1 != k3 {
		t.Fatalf("ssh layer key changed with identical inputs")
	}

	// SSH layer key must change when SSH script changes.
	k4 := sshLayerCacheKey(base, "install openssh-server")
	if k1 == k4 {
		t.Fatalf("ssh layer key did not change when ssh script changed")
	}

	// VSCode layer key must change when VSCode script changes, not SSH.
	v1 := vscodeLayerCacheKey(base, vscode)
	v2 := vscodeLayerCacheKey(base, "install code-server-4")
	if v1 == v2 {
		t.Fatalf("vscode layer key did not change when vscode script changed")
	}

	// SSH and VSCode layer keys must be distinct.
	if k1 == v1 {
		t.Fatalf("ssh and vscode layer keys collided")
	}
}

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

	// SSH-only and VSCode-only must not collide (regression).
	sshOnly := fusedCacheKey("sha256:abc", h, true, false)
	vscodeOnly := fusedCacheKey("sha256:abc", h, false, true)
	if sshOnly == vscodeOnly {
		t.Fatalf("ssh-only and vscode-only produced the same cache key")
	}

	// Both-image key must be distinct from the single-service keys.
	both := fusedCacheKey("sha256:abc", h, true, true)
	if both == sshOnly || both == vscodeOnly {
		t.Fatalf("both-image collided with a single-service key")
	}
}
