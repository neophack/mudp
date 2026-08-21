package httpx

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHandlerError(t *testing.T) {
	err := InternalServerError("boom", nil)
	if err.Status != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", err.Status, http.StatusInternalServerError)
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
