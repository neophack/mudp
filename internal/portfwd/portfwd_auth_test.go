package portfwd

import (
	"bufio"
	"io"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestRuleValidateRejectsRequireLoginOnUDP confirms a UDP forward cannot carry
// RequireLogin. The gate needs an HTTP request to read a cookie, which a UDP
// relay can never deliver; a rule that asked for it would otherwise bind and
// fail every connection, so it is rejected before it starts.
func TestRuleValidateRejectsRequireLoginOnUDP(t *testing.T) {
	r := Rule{HostPort: 10001, Proto: "udp", TargetIP: "10.210.1.3", TargetPort: 53, RequireLogin: true}
	if err := r.Validate(); err == nil {
		t.Fatal("Validate accepted a UDP rule with RequireLogin; it must be refused")
	}
	// The TCP form is fine — gating is supported there.
	r.Proto = "tcp"
	r.TargetPort = 8080
	if err := r.Validate(); err != nil {
		t.Fatalf("Validate rejected a valid gated TCP rule: %v", err)
	}
}

// TestGatedForwardReplaysPeekedBytes sets a gate that "authorises" a request
// and returns the bytes it read. The relay must dial the upstream and replay
// those bytes so the backend sees the whole request — checked by an echo server
// that prefixes whatever line it receives.
func TestGatedForwardReplaysPeekedBytes(t *testing.T) {
	targetIP, targetPort := echoTCP(t, "echo:")
	hostPort := freePort(t)
	m := quietManager(t)

	// The gate inspects nothing here; it stands in for "session valid" by
	// returning the bytes it read and authorising the relay.
	m.SetAuthGate(func(rule Rule, client net.Conn) ([]byte, bool) {
		buf := make([]byte, 256)
		_ = client.SetReadDeadline(time.Now().Add(2 * time.Second))
		n, _ := client.Read(buf)
		return buf[:n], true
	})

	rule := Rule{HostIP: "127.0.0.1", HostPort: hostPort, Proto: "tcp", TargetIP: targetIP, TargetPort: targetPort, Name: "gated", RequireLogin: true}
	if err := m.Apply([]Rule{rule}); err != nil {
		t.Fatalf("apply: %v", err)
	}

	// Send a request the gate will read; the echo server must receive it intact
	// (proving the peeked bytes were replayed) and reply through the relay.
	if got := roundTripTCP(t, hostPort, "hello"); got != "echo:hello" {
		t.Fatalf("relayed reply = %q, want %q", got, "echo:hello")
	}
}

// TestGatedForwardBlocksWhenGateDeclines verifies that a gate which declines a
// connection writes its own response to the client and never reaches the
// upstream — the backend's connection counter must stay at zero.
func TestGatedForwardBlocksWhenGateDeclines(t *testing.T) {
	// A backend that counts connections: if the relay dialled it, the test
	// fails. It also answers, so a leak would be observable.
	connCount := 0
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			connCount++
			_ = c.Close()
		}
	}()
	addr := ln.Addr().(*net.TCPAddr)

	hostPort := freePort(t)
	m := quietManager(t)
	// The gate declines and writes a redirect stand-in to the client.
	m.SetAuthGate(func(rule Rule, client net.Conn) ([]byte, bool) {
		_, _ = client.Write([]byte("HTTP/1.1 302 Found\r\nLocation: /login\r\n\r\n"))
		return nil, false
	})

	rule := Rule{HostIP: "127.0.0.1", HostPort: hostPort, Proto: "tcp", TargetIP: addr.IP.String(), TargetPort: addr.Port, Name: "gated", RequireLogin: true}
	if err := m.Apply([]Rule{rule}); err != nil {
		t.Fatalf("apply: %v", err)
	}

	c, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(hostPort)), 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(2 * time.Second))
	// Send an HTTP-ish request to trigger the gate.
	_, _ = c.Write([]byte("GET / HTTP/1.1\r\nHost: x\r\n\r\n"))
	// Read the status line: the gate writes a full response then closes, so the
	// first line is the authoritative evidence it ran. Reading only what we need
	// avoids racing the close for bytes that may already have been drained.
	br := bufio.NewReader(c)
	statusLine, err := br.ReadString('\n')
	if err != nil && statusLine == "" {
		t.Fatalf("read status line: %v", err)
	}
	if !strings.Contains(statusLine, "302 Found") {
		// Drain anything else for a useful failure message.
		rest, _ := io.ReadAll(br)
		t.Fatalf("client reply = %q, want the gate's 302 response", statusLine+string(rest))
	}
	// Give any in-flight dial a moment to surface, then assert the backend was
	// never contacted.
	time.Sleep(100 * time.Millisecond)
	if connCount != 0 {
		t.Fatalf("backend was contacted %d time(s); a declined gate must not dial upstream", connCount)
	}
}

// TestNoGateLeavesRequireLoginInert checks that when no auth gate is installed,
// a RequireLogin rule behaves as an ordinary open relay. This is the "trouble-
// shooting" path: an admin who has not enabled gating (or removed it) must not
// find their forwards silently refusing connections.
func TestNoGateLeavesRequireLoginInert(t *testing.T) {
	targetIP, targetPort := echoTCP(t, "open:")
	hostPort := freePort(t)
	m := quietManager(t)
	// No SetAuthGate call — m.authGate is nil.

	rule := Rule{HostIP: "127.0.0.1", HostPort: hostPort, Proto: "tcp", TargetIP: targetIP, TargetPort: targetPort, Name: "gated", RequireLogin: true}
	if err := m.Apply([]Rule{rule}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if got := roundTripTCP(t, hostPort, "ping"); got != "open:ping" {
		t.Fatalf("relayed reply = %q, want %q (no gate should mean open relay)", got, "open:ping")
	}
}
