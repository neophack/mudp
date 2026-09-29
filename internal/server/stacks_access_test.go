package server

import (
	"encoding/json"
	"net/http"
	"testing"
)

// Compose stacks are closed to regular users by default and open only through
// the admin toggle. These tests drive the real router so the gate is verified
// across every stack endpoint, the /api/me flag the sidebar hides on, and the
// admin-only settings route that flips the switch.
func TestStacksDisabledByDefault(t *testing.T) {
	_, admin, user := newSecurityTestServer(t)

	// Default is off: a regular user is refused everywhere, an admin is not.
	resp, _ := user.get("/api/stacks")
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("default GET /api/stacks for user = %d, want 403", resp.StatusCode)
	}
	resp, _ = user.get("/api/stacks/get?id=1")
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("default GET /api/stacks/get for user = %d, want 403", resp.StatusCode)
	}
	resp, _ = user.postJSON("/api/stacks/delete", map[string]any{"id": 1})
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("default POST /api/stacks/delete for user = %d, want 403", resp.StatusCode)
	}
	resp, _ = user.postJSON("/api/stacks/up/stream", map[string]any{"id": 1})
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("default POST /api/stacks/up/stream for user = %d, want 403", resp.StatusCode)
	}
	resp, _ = user.postJSON("/api/stacks/down/stream", map[string]any{"id": 1})
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("default POST /api/stacks/down/stream for user = %d, want 403", resp.StatusCode)
	}

	resp, body := admin.get("/api/stacks")
	if resp.StatusCode != http.StatusOK {
		t.Errorf("default GET /api/stacks for admin = %d (%s), want 200", resp.StatusCode, body)
	}
}

func TestStacksToggleAndMeFlag(t *testing.T) {
	_, admin, user := newSecurityTestServer(t)

	// The toggle route is admin-only.
	resp, _ := user.get("/api/admin/settings/stacks")
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("GET settings/stacks for user = %d, want 403", resp.StatusCode)
	}

	// /api/me reports the closed feature to the user and the open one to the
	// admin — this is what the sidebar and router guard read.
	var me struct {
		StacksEnabled bool `json:"stacksEnabled"`
	}
	resp, body := user.get("/api/me")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/me for user = %d", resp.StatusCode)
	}
	_ = json.Unmarshal(body, &me)
	if me.StacksEnabled {
		t.Error("user stacksEnabled = true by default, want false")
	}
	resp, body = admin.get("/api/me")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/me for admin = %d", resp.StatusCode)
	}
	_ = json.Unmarshal(body, &me)
	if !me.StacksEnabled {
		t.Error("admin stacksEnabled = false, want true")
	}

	// Admin opens the feature: every stack endpoint and the me-flag open for
	// the regular user too.
	resp, body = admin.postJSON("/api/admin/settings/stacks", map[string]any{"enabled": true})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST settings/stacks enable = %d (%s)", resp.StatusCode, body)
	}
	resp, body = user.get("/api/admin/settings/stacks")
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("GET settings/stacks for user after enable = %d, want 403 (route stays admin-only)", resp.StatusCode)
	}
	resp, body = user.get("/api/stacks")
	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET /api/stacks for user after enable = %d (%s), want 200", resp.StatusCode, body)
	}
	resp, body = user.get("/api/me")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/me for user after enable = %d", resp.StatusCode)
	}
	_ = json.Unmarshal(body, &me)
	if !me.StacksEnabled {
		t.Error("user stacksEnabled = false after enable, want true")
	}

	// Admin closes it again: the user is locked out once more.
	resp, _ = admin.postJSON("/api/admin/settings/stacks", map[string]any{"enabled": false})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST settings/stacks disable = %d", resp.StatusCode)
	}
	resp, _ = user.get("/api/stacks")
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("GET /api/stacks for user after re-disable = %d, want 403", resp.StatusCode)
	}
}
