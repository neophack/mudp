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

// TestIsSecureRequestWithGate pins the trusted-proxy contract: when a gate is
// installed (as the server does at startup), X-Forwarded-Proto is only
// believed for requests the gate accepts, so a direct client cannot forge its
// way to a Secure cookie flag.
func TestIsSecureRequestWithGate(t *testing.T) {
	SetSecureCheck(func(r *http.Request) bool {
		return r.Header.Get("X-Trusted-Peer") == "yes"
	})
	t.Cleanup(func() { SetSecureCheck(nil) })

	req := httptest.NewRequest(http.MethodGet, "http://example/", nil)
	req.Header.Set("X-Forwarded-Proto", "https")
	if IsSecureRequest(req) {
		t.Fatal("forwarded https accepted from a peer the gate rejects")
	}
	req.Header.Set("X-Trusted-Peer", "yes")
	if !IsSecureRequest(req) {
		t.Fatal("forwarded https rejected from a peer the gate accepts")
	}
	// The gate never overrides a real TLS connection...
	req = httptest.NewRequest(http.MethodGet, "http://example/", nil)
	req.TLS = &tls.ConnectionState{}
	if !IsSecureRequest(req) {
		t.Fatal("TLS connection rejected despite the gate")
	}
	// ...and installing a gate must not make plain-http-without-header secure.
	req = httptest.NewRequest(http.MethodGet, "http://example/", nil)
	req.Header.Set("X-Trusted-Peer", "yes")
	if IsSecureRequest(req) {
		t.Fatal("plain http with no forwarded proto reported secure")
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
