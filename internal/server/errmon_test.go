package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"mudp/internal/store"
)

func TestErrorFingerprintGroupsVariableIDs(t *testing.T) {
	a := errorFingerprint(errorKindHTTP, "GET", "/api/containers", "user 7 not found")
	b := errorFingerprint(errorKindHTTP, "GET", "/api/containers", "user 128 not found")
	if a != b {
		t.Fatal("messages differing only in digits should share a fingerprint")
	}
	c := errorFingerprint(errorKindHTTP, "POST", "/api/containers", "user 7 not found")
	if a == c {
		t.Fatal("different methods should not share a fingerprint")
	}
}

func TestRecordErrorAggregates(t *testing.T) {
	db := newServerTestDB(t)
	a := &App{db: db}

	first := a.recordError(errorKindPanic, "GET", "/api/x", "boom at step 3", "stack")
	if !first {
		t.Fatal("first occurrence should be new")
	}
	second := a.recordError(errorKindPanic, "GET", "/api/x", "boom at step 9", "stack")
	if second {
		t.Fatal("recurrence should not be new")
	}

	events, err := db.ListErrorEvents()
	if err != nil || len(events) != 1 {
		t.Fatalf("expected one aggregated event, got %d (%v)", len(events), err)
	}
	if events[0].Count != 2 {
		t.Fatalf("count = %d, want 2", events[0].Count)
	}

	// The list handler serves events + stats.
	rec := httptest.NewRecorder()
	a.errorsList(rec, httptest.NewRequest(http.MethodGet, "/api/admin/errors", nil))
	var res struct {
		Events []store.ErrorEvent `json:"events"`
		Stats  map[string]int64   `json:"stats"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if len(res.Events) != 1 || res.Stats["occurrences"] != 2 || res.Stats["panics"] != 1 {
		t.Fatalf("unexpected list response: %+v %+v", res.Events, res.Stats)
	}
}
