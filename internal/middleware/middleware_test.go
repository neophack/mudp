package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"mudp/internal/httpx"
)

func TestRecoverPanic(t *testing.T) {
	handler := RecoverPanic(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("boom")
	}))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("code = %d, want 500", rec.Code)
	}
}

func TestRequestLoggerSetsRequestID(t *testing.T) {
	handler := RequestLogger(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := httpx.RequestID(r)
		if id == "" {
			t.Error("request ID missing")
		}
		w.WriteHeader(http.StatusOK)
	}))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Header().Get("X-Request-ID") == "" {
		t.Error("response missing X-Request-ID")
	}
}

func TestRateLimiter(t *testing.T) {
	limiter := NewRateLimiter(1, 1, 0)
	handler := limiter.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// First request allowed.
	rec1 := httptest.NewRecorder()
	handler.ServeHTTP(rec1, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec1.Code != http.StatusOK {
		t.Fatalf("first request code = %d", rec1.Code)
	}

	// Immediate second request blocked.
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec2.Code != http.StatusTooManyRequests {
		t.Fatalf("second request code = %d, want 429", rec2.Code)
	}
}

// TestRateLimiterKeysByClientIP verifies that when a trusted proxy is
// configured, two distinct X-Forwarded-For clients behind the same proxy get
// separate buckets (the pre-fix bug had them sharing one).
func TestRateLimiterKeysByClientIP(t *testing.T) {
	trusted := httpx.DefaultTrustedProxies()
	limiter := NewRateLimiterWithProxies(1, 1, 0, trusted)
	defer limiter.Close()
	handler := limiter.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// Client A via the trusted proxy.
	rA := httptest.NewRequest(http.MethodGet, "/", nil)
	rA.RemoteAddr = "127.0.0.1:1234"
	rA.Header.Set("X-Forwarded-For", "203.0.113.10")
	recA := httptest.NewRecorder()
	handler.ServeHTTP(recA, rA)
	if recA.Code != http.StatusOK {
		t.Fatalf("client A first: %d", recA.Code)
	}

	// Client B via the same proxy must NOT be blocked by A's bucket.
	rB := httptest.NewRequest(http.MethodGet, "/", nil)
	rB.RemoteAddr = "127.0.0.1:1234"
	rB.Header.Set("X-Forwarded-For", "203.0.113.11")
	recB := httptest.NewRecorder()
	handler.ServeHTTP(recB, rB)
	if recB.Code != http.StatusOK {
		t.Fatalf("client B should have its own bucket: got %d, want 200", recB.Code)
	}

	// Client A's second immediate request must be blocked.
	recA2 := httptest.NewRecorder()
	handler.ServeHTTP(recA2, rA)
	if recA2.Code != http.StatusTooManyRequests {
		t.Fatalf("client A second: got %d, want 429", recA2.Code)
	}
}

func TestCSRFProtectSafeMethods(t *testing.T) {
	handler := CSRFProtect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET code = %d", rec.Code)
	}
}

func TestCSRFProtectBlocksMissingToken(t *testing.T) {
	handler := CSRFProtect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("should not reach handler")
	}))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", nil))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("code = %d, want 403", rec.Code)
	}
}

func TestCSRFTokenRoundTrip(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	token, err := CSRFToken(rec, req)
	if err != nil {
		t.Fatalf("CSRFToken: %v", err)
	}
	if token == "" {
		t.Fatal("empty token")
	}
	cookies := rec.Result().Cookies()
	if len(cookies) == 0 || cookies[0].Name != csrfCookieName {
		t.Fatalf("CSRF cookie not set: %+v", cookies)
	}

	req = httptest.NewRequest(http.MethodPost, "/", nil)
	req.AddCookie(cookies[0])
	req.Header.Set(csrfHeaderName, token)

	handler := CSRFProtect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req)
	if rec2.Code != http.StatusOK {
		t.Fatalf("code = %d", rec2.Code)
	}
}
