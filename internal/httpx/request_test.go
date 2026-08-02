package httpx

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestIsSecureRequest(t *testing.T) {
	tests := []struct {
		name  string
		proto string // X-Forwarded-Proto value; "" means header not set
		tls   bool
		want  bool
	}{
		{name: "tls connection", tls: true, want: true},
		{name: "tls with http forwarded proto", tls: true, proto: "http", want: true},
		{name: "plain http, no header", want: false},
		{name: "forwarded proto https", proto: "https", want: true},
		{name: "forwarded proto HTTPS uppercase", proto: "HTTPS", want: true},
		{name: "forwarded proto Https mixed case", proto: "Https", want: true},
		{name: "forwarded proto http", proto: "http", want: false},
		{name: "forwarded proto HTTP uppercase", proto: "HTTP", want: false},
		{name: "forwarded proto garbage", proto: "javascript", want: false},
		{name: "forwarded proto empty string", proto: "", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "http://example/", nil)
			if tt.tls {
				req.TLS = &tls.ConnectionState{}
			}
			if tt.proto != "" {
				req.Header.Set("X-Forwarded-Proto", tt.proto)
			}
			if got := IsSecureRequest(req); got != tt.want {
				t.Fatalf("IsSecureRequest = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRequestID(t *testing.T) {
	t.Run("round trip", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req = WithRequestID(req, "req-123")
		if got := RequestID(req); got != "req-123" {
			t.Fatalf("RequestID = %q, want %q", got, "req-123")
		}
	})
	t.Run("empty string round trip", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req = WithRequestID(req, "")
		if got := RequestID(req); got != "" {
			t.Fatalf("RequestID = %q, want empty", got)
		}
	})
	t.Run("not set returns empty", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		if got := RequestID(req); got != "" {
			t.Fatalf("RequestID = %q, want empty", got)
		}
	})
	t.Run("does not mutate original request", func(t *testing.T) {
		orig := httptest.NewRequest(http.MethodGet, "/", nil)
		_ = WithRequestID(orig, "req-123")
		if got := RequestID(orig); got != "" {
			t.Fatalf("original request RequestID = %q, want empty", got)
		}
	})
}

func TestQueryString(t *testing.T) {
	tests := []struct {
		name     string
		target   string
		param    string
		fallback string
		want     string
	}{
		{name: "present", target: "/?q=hello", param: "q", fallback: "def", want: "hello"},
		{name: "missing uses fallback", target: "/", param: "q", fallback: "def", want: "def"},
		{name: "empty value uses fallback", target: "/?q=", param: "q", fallback: "def", want: "def"},
		{name: "whitespace trimmed", target: "/?q=%20hello%20", param: "q", fallback: "def", want: "hello"},
		{name: "whitespace only uses fallback", target: "/?q=%20%20", param: "q", fallback: "def", want: "def"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tt.target, nil)
			if got := QueryString(req, tt.param, tt.fallback); got != tt.want {
				t.Fatalf("QueryString = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestQueryBool(t *testing.T) {
	tests := []struct {
		name     string
		target   string
		fallback bool
		want     bool
	}{
		{name: "true", target: "/?b=true", want: true},
		{name: "1", target: "/?b=1", want: true},
		{name: "yes", target: "/?b=yes", want: true},
		{name: "TRUE uppercase", target: "/?b=TRUE", want: true},
		{name: "false", target: "/?b=false", want: false, fallback: true},
		{name: "0", target: "/?b=0", want: false, fallback: true},
		{name: "no", target: "/?b=no", want: false, fallback: true},
		{name: "missing uses fallback true", target: "/", fallback: true, want: true},
		{name: "missing uses fallback false", target: "/", fallback: false, want: false},
		{name: "garbage uses fallback", target: "/?b=maybe", fallback: true, want: true},
		{name: "whitespace trimmed", target: "/?b=%20true%20", fallback: false, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tt.target, nil)
			if got := QueryBool(req, "b", tt.fallback); got != tt.want {
				t.Fatalf("QueryBool = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRequireQuery(t *testing.T) {
	t.Run("present", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/?token=abc", nil)
		v, err := RequireQuery(req, "token")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if v != "abc" {
			t.Fatalf("value = %q, want abc", v)
		}
	})
	t.Run("whitespace trimmed", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/?token=%20abc%20", nil)
		v, err := RequireQuery(req, "token")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if v != "abc" {
			t.Fatalf("value = %q, want abc", v)
		}
	})
	t.Run("missing returns error", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		if _, err := RequireQuery(req, "token"); err == nil {
			t.Fatal("expected error for missing param")
		}
	})
	t.Run("empty value returns error", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/?token=", nil)
		if _, err := RequireQuery(req, "token"); err == nil {
			t.Fatal("expected error for empty value")
		}
	})
	t.Run("whitespace only returns error", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/?token=%20%20", nil)
		if _, err := RequireQuery(req, "token"); err == nil {
			t.Fatal("expected error for whitespace-only value")
		}
	})
}

func TestMethod(t *testing.T) {
	tests := []struct {
		name       string
		method     string
		allowed    string
		wantStatus int // 0 means nil (allowed)
	}{
		{name: "matching GET", method: http.MethodGet, allowed: http.MethodGet, wantStatus: 0},
		{name: "matching POST", method: http.MethodPost, allowed: http.MethodPost, wantStatus: 0},
		{name: "POST not allowed on GET route", method: http.MethodPost, allowed: http.MethodGet, wantStatus: http.StatusMethodNotAllowed},
		{name: "DELETE not allowed on GET route", method: http.MethodDelete, allowed: http.MethodGet, wantStatus: http.StatusMethodNotAllowed},
		{name: "case-sensitive mismatch", method: "get", allowed: http.MethodGet, wantStatus: http.StatusMethodNotAllowed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, "/", nil)
			err := Method(req, tt.allowed)
			if tt.wantStatus == 0 {
				if err != nil {
					t.Fatalf("expected nil, got %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected HandlerError with status %d, got nil", tt.wantStatus)
			}
			if err.Status != tt.wantStatus {
				t.Fatalf("status = %d, want %d", err.Status, tt.wantStatus)
			}
		})
	}
}
