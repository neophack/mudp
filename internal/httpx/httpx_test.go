package httpx

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandlerError(t *testing.T) {
	err := BadRequest("invalid input", errors.New("missing field"))
	if err.Status != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", err.Status, http.StatusBadRequest)
	}
	if !strings.Contains(err.Error(), "invalid input") {
		t.Fatalf("error message missing summary: %v", err.Error())
	}
}

func TestDecodeJSON(t *testing.T) {
	type payload struct {
		Name string `json:"name"`
	}
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"name":"alice"}`))
	var p payload
	if err := DecodeJSON(req, &p); err != nil {
		t.Fatalf("decode valid json: %v", err)
	}
	if p.Name != "alice" {
		t.Fatalf("name = %q, want alice", p.Name)
	}

	req2 := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"name":"alice","extra":1}`))
	var p2 payload
	if err := DecodeJSON(req2, &p2); err == nil {
		t.Fatal("expected error for unknown field")
	}
}

func TestWriteJSON(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteJSON(rec, http.StatusCreated, map[string]string{"ok": "yes"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("code = %d, want %d", rec.Code, http.StatusCreated)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	if body["ok"] != "yes" {
		t.Fatalf("body = %v", body)
	}
}

func TestWriteErr(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteErr(rec, Forbidden("nope"))
	if rec.Code != http.StatusForbidden {
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

func TestQueryInt(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/?page=3&size=bad", nil)
	if got := QueryInt(req, "page", 1); got != 3 {
		t.Fatalf("page = %d, want 3", got)
	}
	if got := QueryInt(req, "size", 10); got != 10 {
		t.Fatalf("size = %d, want 10", got)
	}
	if got := QueryInt(req, "missing", 5); got != 5 {
		t.Fatalf("missing = %d, want 5", got)
	}
}

func TestLoggerHandler(t *testing.T) {
	h := LoggerHandler(func(w http.ResponseWriter, r *http.Request) *HandlerError {
		return InternalServerError("boom", errors.New("db down"))
	})
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("code = %d", rec.Code)
	}
}

func TestDecodeAndValidateJSON(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"email":"a@b.com"}`))
	p := &validatedPayload{}
	if err := DecodeAndValidateJSON(req, p); err != nil {
		t.Fatalf("valid payload: %v", err)
	}
	if !p.validated {
		t.Fatal("validator not called")
	}
	if p.Email != "a@b.com" {
		t.Fatalf("email = %q", p.Email)
	}
}

type validatedPayload struct {
	Email     string `json:"email"`
	validated bool
}

func (p *validatedPayload) Validate(r *http.Request) error {
	p.validated = true
	if p.Email == "" {
		return errors.New("email required")
	}
	return nil
}

func bodyReader(s string) io.ReadCloser {
	return io.NopCloser(bytes.NewReader([]byte(s)))
}
