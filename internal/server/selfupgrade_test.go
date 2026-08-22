package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"mudp/internal/store"
)

func TestStartUpgradeValidation(t *testing.T) {
	db := newServerTestDB(t)
	app := &App{db: db}

	// Missing tag.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/admin/upgrade", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	app.startUpgrade(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing tag: status = %d, want 400", rec.Code)
	}

	// go run / test-binary instances cannot self-upgrade: the resolved
	// executable lives in a go-build scratch directory. (Under `go test` the
	// test binary itself sits there, so the guard fires.)
	admin := &store.User{ID: 1, Username: "admin", Role: store.RoleAdmin}
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/admin/upgrade", strings.NewReader(`{"tag":"v9.9.9"}`))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(context.WithValue(req.Context(), userKey, admin))
	app.startUpgrade(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "go run") {
		t.Fatalf("go-build guard: status = %d body = %s", rec.Code, rec.Body.String())
	}
}
