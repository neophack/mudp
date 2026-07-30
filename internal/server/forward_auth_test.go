package server

import (
	"bytes"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"mudp/internal/portfwd"
)

// TestLooksLikeHTTP covers the cheap classifier that decides whether the gate
// treats a connection as HTTP or refuses it. It is the first line of defence
// against raw-TCP traffic on a gated port, so its true/false boundaries matter.
func TestLooksLikeHTTP(t *testing.T) {
	good := []string{
		"GET / HTTP/1.1\r\n",
		"POST /api/x HTTP/1.1",
		"PUT /p",
		"DELETE /",
		"HEAD",
		"OPTIONS *",
	}
	for _, s := range good {
		if !looksLikeHTTP([]byte(s)) {
			t.Errorf("looksLikeHTTP(%q) = false, want true", s)
		}
	}
	bad := []string{
		"SSH-2.0-OpenSSH_9.0\r\n",
		"\x16\x03\x01\x02\x00\x01\x00\x01\xfc", // TLS ClientHello prefix
		"220 smtp\r\n",
		"",
		"not-a-method / HTTP/1.1",
	}
	for _, s := range bad {
		if looksLikeHTTP([]byte(s)) {
			t.Errorf("looksLikeHTTP(%q) = true, want false", s)
		}
	}
}

// TestReadHTTPHeader verifies the header-block reader returns the request line
// and headers verbatim up to the terminating blank line, and refuses input that
// is not a complete header block.
func TestReadHTTPHeader(t *testing.T) {
	// A complete request: line, two headers, blank line. The gate must return
	// exactly these bytes so the relay can replay them unchanged.
	req := "GET /path HTTP/1.1\r\nHost: example:9001\r\nCookie: mudp_session=abc\r\n\r\n"
	conn := newPipeConn(req)
	got, ok := readHTTPHeader(conn)
	if !ok {
		t.Fatal("readHTTPHeader returned ok=false for a valid request")
	}
	if string(got) != req {
		t.Errorf("readHTTPHeader = %q, want %q", string(got), req)
	}
}

// TestReadHTTPHeaderIncludesPrefetchedBody guards against a data-loss bug: the
// header scanner reads through a bufio buffer, which may already hold the start
// of the request body by the time the header terminator is found. Those bytes
// are gone from the underlying connection, so they must be returned alongside
// the headers — otherwise the relay's replay would drop them and corrupt the
// upstream request.
func TestReadHTTPHeaderIncludesPrefetchedBody(t *testing.T) {
	headers := "POST /api HTTP/1.1\r\nHost: x\r\nContent-Length: 5\r\n\r\n"
	body := "hello"
	conn := newPipeConn(headers + body)
	got, ok := readHTTPHeader(conn)
	if !ok {
		t.Fatal("readHTTPHeader returned ok=false")
	}
	// The returned window must contain the body bytes the scanner prefetched.
	if !strings.Contains(string(got), body) {
		t.Errorf("readHTTPHeader = %q; missing prefetched body %q (would be lost on replay)", string(got), body)
	}
}

// TestReadHTTPHeaderRejectsIncomplete covers the case where the client closes
// before sending the blank line that ends the headers — the gate must refuse
// rather than hang.
func TestReadHTTPHeaderRejectsIncomplete(t *testing.T) {
	conn := newPipeConn("GET / HTTP/1.1\r\nHost: x\r\n") // no terminating \r\n\r\n
	if _, ok := readHTTPHeader(conn); ok {
		t.Fatal("readHTTPHeader returned ok=true for an incomplete header block")
	}
}

// TestForwardLoginTargetDerivesFromHost checks that with no configured console
// URL, the redirect target is derived by swapping the request's port for the
// console's, and the original URL is carried in ?next=.
func TestForwardLoginTargetDerivesFromHost(t *testing.T) {
	req := &http.Request{Host: "192.168.1.1:10001"}
	req.URL = mustParseURL("/app")
	target := forwardLoginTarget("", "0.0.0.0:9000", portfwd.Rule{HostPort: 10001}, req)
	if !strings.HasPrefix(target, "http://192.168.1.1:9000/?next=") {
		t.Fatalf("target = %q, want console on :9000 with ?next=", target)
	}
	// The original URL (port 10001) must be echoed back so login can return there.
	if !strings.Contains(target, "10001") {
		t.Fatalf("target %q does not echo the original port back in next=", target)
	}
}

// TestForwardLoginTargetUsesPinnedURL checks a configured console URL is used
// verbatim (sans trailing slash) and still carries the original URL.
func TestForwardLoginTargetUsesPinnedURL(t *testing.T) {
	req := &http.Request{Host: "tunnel.example.com:10001"}
	req.URL = mustParseURL("/")
	target := forwardLoginTarget("https://console.example.com/", ":9000", portfwd.Rule{HostPort: 10001}, req)
	if !strings.HasPrefix(target, "https://console.example.com/?next=") {
		t.Fatalf("target = %q, want the pinned console URL", target)
	}
}

// mustParseURL parses a path-only reference, failing the test on an impossible
// error. Mirrors how http.ReadRequest populates Request.URL for a path target.
func mustParseURL(ref string) *url.URL {
	u, err := url.Parse(ref)
	if err != nil {
		panic(err)
	}
	return u
}

// pipeConn is a net.Conn backed by a bytes.Reader, for feeding synthesised
// request bytes into readHTTPHeader without a real socket.
type pipeConn struct {
	r *bytes.Reader
}

func newPipeConn(s string) net.Conn {
	return &pipeConn{r: bytes.NewReader([]byte(s))}
}

func (c *pipeConn) Read(b []byte) (int, error)         { return c.r.Read(b) }
func (c *pipeConn) Write(b []byte) (int, error)        { return len(b), nil }
func (c *pipeConn) Close() error                       { return nil }
func (c *pipeConn) LocalAddr() net.Addr                { return pipeAddr{} }
func (c *pipeConn) RemoteAddr() net.Addr               { return pipeAddr{} }
func (c *pipeConn) SetDeadline(t time.Time) error      { return nil }
func (c *pipeConn) SetReadDeadline(t time.Time) error  { return nil }
func (c *pipeConn) SetWriteDeadline(t time.Time) error { return nil }

type pipeAddr struct{}

func (pipeAddr) Network() string { return "pipe" }
func (pipeAddr) String() string  { return "pipe" }
