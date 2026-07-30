package server

// Login-gating for forwarded ports. A forward whose rule carries RequireLogin
// (set per-image in the preset, or per-manual-forward) must only let a browser
// through that has a valid console session — an image that ships no auth of its
// own would otherwise expose a bare web desktop or dev server to anyone on the
// LAN the moment its host port opens.
//
// The port forwarder (internal/portfwd) is a protocol-agnostic byte relay, so
// it cannot check a cookie itself. Instead the Manager accepts an "auth gate"
// (see Manager.SetAuthGate) that this file provides: for each accepted TCP
// connection on a gated rule the relay hands the client conn to the gate before
// dialling the container. The gate peeks the opening request, and either
// returns the bytes it read (so the relay replays them upstream and the
// connection proceeds) or writes its own response (a redirect to the login
// page, or a refusal for non-HTTP traffic) and tells the relay to close.
//
// The session cookie (mudp_session) is host-scoped, not port-scoped, so a
// browser logged into the console on :9000 carries the same cookie to a
// forwarded port on :10001 of the same host — which is exactly what makes a
// transparent "already logged in" experience possible without any proxy in
// front. Behind a tunnel that maps different hosts this no longer holds, so an
// admin can pin the console URL in ForwardAuthConfig.

import (
	"bufio"
	"bytes"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"mudp/internal/portfwd"
)

const (
	// forwardAuthReadTimeout bounds how long the gate waits for the opening
	// request line. A client that opens a connection and sends nothing (a port
	// scanner, a half-open probe) must not hold a goroutine indefinitely.
	forwardAuthReadTimeout = 3 * time.Second
	// forwardAuthPeekLimit is the most bytes the gate reads looking for the end
	// of the HTTP headers. Larger than any plausible request line + header
	// block, while bounding memory if a client sends an unending header.
	forwardAuthPeekLimit = 16 * 1024
)

// newForwardAuthGate returns the function installed on the port-forward
// Manager. It closes over the app so it can verify session cookies and read the
// current ForwardAuthConfig on every connection (a config change applies to the
// next request, with no restart).
func (a *App) newForwardAuthGate() func(portfwd.Rule, net.Conn) ([]byte, bool) {
	return func(rule portfwd.Rule, client net.Conn) ([]byte, bool) {
		return a.checkForwardAuth(rule, client)
	}
}

// checkForwardAuth implements one gate decision. It reads the request, classifies
// it, and either authorises the relay or writes a response to the client.
//
// It never dials the container and never returns data on the un-authorised
// path, so a gated port reveals nothing about its upstream to an unauthenticated
// caller beyond "you must log in".
func (a *App) checkForwardAuth(rule portfwd.Rule, client net.Conn) ([]byte, bool) {
	// The master switch lives in ForwardAuthConfig. When disabled the gate is a
	// pass-through, so an admin can open everything up for troubleshooting
	// without editing each image preset.
	cfg, _ := a.db.ForwardAuthConfig()
	if !cfg.Enabled {
		return nil, true
	}

	// Peek the opening request with a bounded deadline, stopping at the blank
	// line that ends an HTTP header block. The exact bytes are preserved so the
	// relay can replay them to the container unchanged on the authorised path.
	_ = client.SetReadDeadline(time.Now().Add(forwardAuthReadTimeout))
	headerBlock, ok := readHTTPHeader(client)
	_ = client.SetReadDeadline(time.Time{}) // reset; the relay owns the conn now
	if !ok {
		// Not an HTTP request (timeout, malformed, or non-HTTP bytes). A gated
		// port serves browsers, so anything else is refused outright — relaying
		// it would defeat the login requirement for raw-TCP clients.
		writeHTTP(client, http.StatusForbidden, "text/plain; charset=utf-8",
			"This port requires login and only serves HTTP.\n")
		return nil, false
	}

	// Parse the captured header block into a request to inspect the cookie. The
	// block already contains everything http.ReadRequest would have consumed.
	req, err := http.ReadRequest(bufio.NewReader(bytes.NewReader(headerBlock)))
	if err != nil {
		writeHTTP(client, http.StatusBadRequest, "text/plain; charset=utf-8",
			"Malformed HTTP request.\n")
		return nil, false
	}

	// A valid session cookie means the browser is already logged into the
	// console — let it straight through, replaying the peeked request upstream.
	if uid, ok := a.auth.UserID(req); ok && uid != 0 {
		return headerBlock, true
	}

	// Not authenticated: redirect the browser to the console login page, carrying
	// the original URL so login can send it back here once it succeeds.
	target := forwardLoginTarget(cfg.ConsoleURL, a.cfg.Addr, rule, req)
	body := "<!doctype html><meta http-equiv=\"refresh\" content=\"0;url=" + htmlEscapeAttr(target) + "\">" +
		"<title>Login required</title><body><a href=\"" + htmlEscapeAttr(target) + "\">" +
		"Login required</a></body>"
	writeHTTP(client, http.StatusFound, "text/html; charset=utf-8", body)
	return nil, false
}

// readHTTPHeader reads from r until the end of the HTTP request headers (the
// blank line "\r\n\r\n"), returning the exact bytes read including that
// terminator. It returns ok=false if the input does not look like an HTTP
// request within the byte/time limits — used to tell "a browser GET" from "a
// raw TCP client that is not speaking HTTP".
//
// The bytes are returned verbatim so the relay can replay them; we do not let
// http.ReadRequest own the connection, because that would consume and discard
// them.
func readHTTPHeader(r net.Conn) ([]byte, bool) {
	br := bufio.NewReader(r)
	var buf bytes.Buffer
	// An HTTP request line starts with a method. Peek a few bytes to confirm it
	// is plausibly HTTP before committing to reading a header block, so a binary
	// handshake (SSH, TLS) is rejected quickly.
	prefix, _ := br.Peek(8)
	if !looksLikeHTTP(prefix) {
		return nil, false
	}
	for {
		line, err := br.ReadBytes('\n')
		if err != nil {
			return nil, false
		}
		buf.Write(line)
		if buf.Len() > forwardAuthPeekLimit {
			return nil, false
		}
		// The header block ends at a blank line. HTTP allows "\r\n" and (rarely)
		// a bare "\n"; accept both so a slightly non-conformant client still works.
		trimmed := bytes.TrimRight(line, "\r\n")
		if len(trimmed) == 0 {
			break
		}
	}
	// The bufio reader may have already pulled the start of the request body into
	// its buffer while scanning for the header terminator. Those bytes are no
	// longer reachable from the underlying connection, so the relay's later
	// io.Copy would skip them and corrupt the upstream request. Fold everything
	// still buffered in here: the returned slice is then the complete window the
	// relay must replay, and reading the rest straight off the conn loses nothing.
	if extra := br.Buffered(); extra > 0 {
		if pre, err := br.Peek(extra); err == nil {
			buf.Write(pre)
		}
	}
	return buf.Bytes(), true
}

// looksLikeHTTP reports whether the opening bytes could be an HTTP request line.
// It checks only the leading method token, which is enough to distinguish a
// browser request from an SSH/TLS/SMTP handshake without a full parser.
func looksLikeHTTP(b []byte) bool {
	s := string(b)
	for _, m := range knownHTTPMethods {
		if strings.HasPrefix(s, m) {
			return true
		}
	}
	return false
}

// knownHTTPMethods are the request methods a browser or HTTP client opens with.
// Kept explicit (rather than derived) so a future method added to the standard
// library does not silently widen what the gate considers HTTP.
var knownHTTPMethods = []string{
	http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch,
	http.MethodDelete, http.MethodHead, http.MethodOptions,
	http.MethodConnect, http.MethodTrace,
}

// writeHTTP writes a minimal HTTP/1.1 response to a raw connection. The relay
// owns the conn, so we cannot use http.ResponseWriter — this is the only place
// a response is synthesised directly onto the socket.
func writeHTTP(w net.Conn, status int, contentType, body string) {
	reason := http.StatusText(status)
	if reason == "" {
		reason = "Error"
	}
	header := fmt.Sprintf("HTTP/1.1 %d %s\r\n", status, reason) +
		"Content-Type: " + contentType + "\r\n" +
		"Content-Length: " + fmt.Sprintf("%d", len(body)) + "\r\n" +
		"Connection: close\r\n" +
		"Cache-Control: no-store\r\n" +
		"\r\n" + body
	_ = w.SetWriteDeadline(time.Now().Add(forwardAuthReadTimeout))
	_, _ = w.Write([]byte(header))
}

// forwardLoginTarget builds the URL an unauthenticated request is redirected to.
// When the admin pinned a console URL it is used as-is; otherwise the URL is
// derived from the request's Host header with the port replaced by the
// console's listen port. The original request URL is appended as ?next= so the
// login page can return the browser to the forwarded port afterwards.
func forwardLoginTarget(consoleURL, consoleAddr string, rule portfwd.Rule, req *http.Request) string {
	original := originalRequestURL(req, rule)

	base := strings.TrimRight(strings.TrimSpace(consoleURL), "/")
	if base == "" {
		// Derive from the request Host: keep the host, swap the port for the
		// console's. The console listens on a known port from cfg.Addr; keeping
		// the same host is correct for direct LAN access (the cookie matches).
		host := req.Host
		consolePort := listenPort(consoleAddr)
		if host != "" && consolePort != 0 {
			if h, _, err := net.SplitHostPort(host); err == nil {
				host = net.JoinHostPort(h, fmt.Sprintf("%d", consolePort))
			} else {
				// No port in Host: append the console port.
				host = net.JoinHostPort(strings.TrimSpace(host), fmt.Sprintf("%d", consolePort))
			}
		}
		scheme := "http"
		if req.TLS != nil {
			scheme = "https"
		}
		base = scheme + "://" + host
	}
	return base + "/?next=" + url.QueryEscape(original)
}

// originalRequestURL reconstructs the URL the browser asked for on the forwarded
// port, so the login page can send it back there after a successful login.
func originalRequestURL(req *http.Request, rule portfwd.Rule) string {
	scheme := "http"
	if req.TLS != nil {
		scheme = "https"
	}
	path := req.URL.RequestURI()
	if path == "" {
		path = "/"
	}
	host := req.Host
	if host == "" {
		host = fmt.Sprintf("localhost:%d", rule.HostPort)
	}
	return scheme + "://" + host + path
}

// htmlEscapeAttr escapes a string for safe inclusion in an HTML attribute
// (href=...). It covers the characters that could break out of the attribute.
func htmlEscapeAttr(s string) string {
	r := strings.NewReplacer(
		"&", "&amp;",
		`"`, "&#34;",
		"'", "&#39;",
		"<", "&lt;",
		">", "&gt;",
	)
	return r.Replace(s)
}
