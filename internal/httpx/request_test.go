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
