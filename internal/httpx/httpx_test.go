package httpx

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHandlerError(t *testing.T) {
	underlying := errors.New("disk full")
	err := InternalServerError("boom", underlying)
	if err.Status != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", err.Status, http.StatusInternalServerError)
	}
	if err.Message != "boom" {
		t.Errorf("message = %q, want %q", err.Message, "boom")
	}
	if !errors.Is(err, underlying) {
		t.Errorf("errors.Is(err, underlying) = false, want the wrapped error reachable")
	}
}

// An empty message falls back to the HTTP status text so the JSON envelope
// never ships an empty "error" field.
func TestHandlerErrorEmptyMessageFallsBackToStatusText(t *testing.T) {
	err := InternalServerError("", errors.New("disk full"))
	if err.Message != http.StatusText(http.StatusInternalServerError) {
		t.Errorf("message = %q, want %q", err.Message, http.StatusText(http.StatusInternalServerError))
	}
}

func TestWriteErr(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteErr(rec, InternalServerError("nope"))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("code = %d", rec.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if body["error"] != "nope" {
		t.Fatalf("error = %q", body["error"])
	}
}
