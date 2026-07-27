package portfwd

import (
	"bufio"
	"errors"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"
)

// quietManager is a Manager whose log output is captured, so a test that
// deliberately provokes a dial failure does not print into the test log.
func quietManager(t *testing.T) *Manager {
	t.Helper()
	m := NewManager()
	m.SetLogger(func(format string, args ...any) { t.Logf(format, args...) })
	t.Cleanup(m.Close)
	return m
}

// echoTCP starts a TCP server that echoes each line back with a prefix, so a
// test can tell which backend answered.
func echoTCP(t *testing.T, prefix string) (host string, port int) {
	t.Helper()
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
			go func(c net.Conn) {
				defer c.Close()
				sc := bufio.NewScanner(c)
				for sc.Scan() {
					if _, err := c.Write([]byte(prefix + sc.Text() + "\n")); err != nil {
						return
					}
				}
			}(c)
		}
	}()
	addr := ln.Addr().(*net.TCPAddr)
	return addr.IP.String(), addr.Port
}

// echoUDP starts a UDP server that answers each datagram with a prefixed copy.
func echoUDP(t *testing.T, prefix string) (host string, port int) {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen udp: %v", err)
	}
	t.Cleanup(func() { _ = pc.Close() })
	go func() {
		buf := make([]byte, 2048)
		for {
			n, addr, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			if _, err := pc.WriteTo([]byte(prefix+string(buf[:n])), addr); err != nil {
				return
			}
		}
	}()
	addr := pc.LocalAddr().(*net.UDPAddr)
	return addr.IP.String(), addr.Port
}

// freePort returns a port number nothing is listening on.
func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("probe port: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return port
}

// roundTripTCP sends one line through the forwarded port and returns the reply.
func roundTripTCP(t *testing.T, port int, line string) string {
	t.Helper()
	c, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 2*time.Second)
	if err != nil {
		t.Fatalf("dial forwarded port %d: %v", port, err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := c.Write([]byte(line + "\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	reply, err := bufio.NewReader(c).ReadString('\n')
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return strings.TrimSpace(reply)
}

func TestApplyForwardsTCP(t *testing.T) {
	targetIP, targetPort := echoTCP(t, "one:")
	hostPort := freePort(t)
	m := quietManager(t)

	if err := m.Apply([]Rule{{HostIP: "127.0.0.1", HostPort: hostPort, Proto: "tcp", TargetIP: targetIP, TargetPort: targetPort, Name: "dev01"}}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if got := roundTripTCP(t, hostPort, "hello"); got != "one:hello" {
		t.Fatalf("relayed reply = %q, want %q", got, "one:hello")
	}

	st := m.Status()
	if len(st) != 1 {
		t.Fatalf("status has %d entries, want 1", len(st))
	}
	if st[0].Total != 1 || st[0].Name != "dev01" {
		t.Fatalf("status = %+v, want one connection for dev01", st[0])
	}
}

func TestApplyForwardsUDP(t *testing.T) {
	targetIP, targetPort := echoUDP(t, "udp:")
	hostPort := freePort(t)
	m := quietManager(t)

	if err := m.Apply([]Rule{{HostIP: "127.0.0.1", HostPort: hostPort, Proto: "udp", TargetIP: targetIP, TargetPort: targetPort, Name: "dns"}}); err != nil {
		t.Fatalf("apply: %v", err)
	}

	c, err := net.Dial("udp", net.JoinHostPort("127.0.0.1", strconv.Itoa(hostPort)))
	if err != nil {
		t.Fatalf("dial udp: %v", err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := c.Write([]byte("query")); err != nil {
		t.Fatalf("write: %v", err)
	}
	buf := make([]byte, 512)
	n, err := c.Read(buf)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got := string(buf[:n]); got != "udp:query" {
		t.Fatalf("relayed datagram = %q, want %q", got, "udp:query")
	}
}

// A container that restarts comes back on a new address. The rule keeps its
// host port, so the listener has to be repointed rather than left serving the
// address that no longer exists.
func TestApplyRetargetsWhenContainerMoves(t *testing.T) {
	firstIP, firstPort := echoTCP(t, "first:")
	secondIP, secondPort := echoTCP(t, "second:")
	hostPort := freePort(t)
	m := quietManager(t)

	rule := Rule{HostIP: "127.0.0.1", HostPort: hostPort, Proto: "tcp", TargetIP: firstIP, TargetPort: firstPort, Name: "dev01"}
	if err := m.Apply([]Rule{rule}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if got := roundTripTCP(t, hostPort, "x"); got != "first:x" {
		t.Fatalf("before move = %q, want first:x", got)
	}

	rule.TargetIP, rule.TargetPort = secondIP, secondPort
	if err := m.Apply([]Rule{rule}); err != nil {
		t.Fatalf("re-apply: %v", err)
	}
	if got := roundTripTCP(t, hostPort, "x"); got != "second:x" {
		t.Fatalf("after move = %q, want second:x", got)
	}
}

// Reconciling with the same rule twice must not restart the listener, or every
// 15-second sweep would drop every open SSH session.
func TestApplyIsIdempotent(t *testing.T) {
	targetIP, targetPort := echoTCP(t, "one:")
	hostPort := freePort(t)
	m := quietManager(t)
	rule := Rule{HostIP: "127.0.0.1", HostPort: hostPort, Proto: "tcp", TargetIP: targetIP, TargetPort: targetPort, Name: "dev01"}

	if err := m.Apply([]Rule{rule}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	before := m.Status()[0].Since
	if err := m.Apply([]Rule{rule}); err != nil {
		t.Fatalf("second apply: %v", err)
	}
	after := m.Status()
	if len(after) != 1 || after[0].Since != before {
		t.Fatalf("listener restarted on an unchanged rule: %+v", after)
	}
}

// A rule that disappears (its container was deleted) must free the host port,
// or the owner's range leaks a port per removed container.
func TestApplyStopsRemovedRules(t *testing.T) {
	targetIP, targetPort := echoTCP(t, "one:")
	hostPort := freePort(t)
	m := quietManager(t)

	if err := m.Apply([]Rule{{HostIP: "127.0.0.1", HostPort: hostPort, Proto: "tcp", TargetIP: targetIP, TargetPort: targetPort}}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if err := m.Apply(nil); err != nil {
		t.Fatalf("apply empty: %v", err)
	}
	if st := m.Status(); len(st) != 0 {
		t.Fatalf("status = %+v, want empty", st)
	}
	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(hostPort)))
	if err != nil {
		t.Fatalf("host port %d still bound after the rule was removed: %v", hostPort, err)
	}
	_ = ln.Close()
}

// One unusable rule must not prevent the others from being served: a container
// whose address is not known yet is reported, and its neighbours still work.
func TestApplyReportsBadRuleAndKeepsGoing(t *testing.T) {
	targetIP, targetPort := echoTCP(t, "ok:")
	goodPort, badPort := freePort(t), freePort(t)
	m := quietManager(t)

	err := m.Apply([]Rule{
		{HostIP: "127.0.0.1", HostPort: badPort, Proto: "tcp", TargetPort: 80, Name: "no-address"},
		{HostIP: "127.0.0.1", HostPort: goodPort, Proto: "tcp", TargetIP: targetIP, TargetPort: targetPort, Name: "fine"},
	})
	if err == nil {
		t.Fatal("apply returned no error for a rule with no container address")
	}
	if !strings.Contains(err.Error(), "no-address") {
		t.Fatalf("error %q does not name the broken rule", err)
	}
	if got := roundTripTCP(t, goodPort, "x"); got != "ok:x" {
		t.Fatalf("healthy rule = %q, want ok:x", got)
	}
}

// Two containers cannot share a host port. The conflict is reported instead of
// the listener flapping between them on every reconcile.
func TestApplyRejectsDuplicateHostPort(t *testing.T) {
	firstIP, firstPort := echoTCP(t, "first:")
	secondIP, secondPort := echoTCP(t, "second:")
	hostPort := freePort(t)
	m := quietManager(t)

	err := m.Apply([]Rule{
		{HostIP: "127.0.0.1", HostPort: hostPort, Proto: "tcp", TargetIP: firstIP, TargetPort: firstPort, Name: "dev01"},
		{HostIP: "127.0.0.1", HostPort: hostPort, Proto: "tcp", TargetIP: secondIP, TargetPort: secondPort, Name: "dev02"},
	})
	if err == nil || !strings.Contains(err.Error(), "claimed by both") {
		t.Fatalf("apply error = %v, want a duplicate host port report", err)
	}
	if st := m.Status(); len(st) != 1 {
		t.Fatalf("status has %d listeners, want 1", len(st))
	}
}

// The same port number in TCP and UDP are different sockets and must both be
// servable — a container publishing 5000/tcp and 5000/udp is ordinary.
func TestApplyKeepsTCPAndUDPSeparate(t *testing.T) {
	tcpIP, tcpPort := echoTCP(t, "t:")
	udpIP, udpPort := echoUDP(t, "u:")
	hostPort := freePort(t)
	m := quietManager(t)

	if err := m.Apply([]Rule{
		{HostIP: "127.0.0.1", HostPort: hostPort, Proto: "tcp", TargetIP: tcpIP, TargetPort: tcpPort},
		{HostIP: "127.0.0.1", HostPort: hostPort, Proto: "udp", TargetIP: udpIP, TargetPort: udpPort},
	}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if st := m.Status(); len(st) != 2 {
		t.Fatalf("status has %d listeners, want 2", len(st))
	}
	if got := roundTripTCP(t, hostPort, "x"); got != "t:x" {
		t.Fatalf("tcp reply = %q", got)
	}
}

func TestCloseStopsEverythingAndRefusesApply(t *testing.T) {
	targetIP, targetPort := echoTCP(t, "one:")
	hostPort := freePort(t)
	m := NewManager()
	m.SetLogger(func(string, ...any) {})

	if err := m.Apply([]Rule{{HostIP: "127.0.0.1", HostPort: hostPort, Proto: "tcp", TargetIP: targetIP, TargetPort: targetPort}}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	m.Close()
	if _, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(hostPort)), time.Second); err == nil {
		t.Fatal("forwarded port still accepts connections after Close")
	}
	if err := m.Apply(nil); err == nil {
		t.Fatal("Apply on a closed manager returned no error")
	}
}

func TestRuleValidate(t *testing.T) {
	base := Rule{HostPort: 10001, Proto: "tcp", TargetIP: "10.210.1.3", TargetPort: 8080}
	if err := base.Validate(); err != nil {
		t.Fatalf("valid rule rejected: %v", err)
	}
	cases := map[string]Rule{
		"bad protocol":    {HostPort: 10001, Proto: "sctp", TargetIP: "10.210.1.3", TargetPort: 8080},
		"host port zero":  {HostPort: 0, Proto: "tcp", TargetIP: "10.210.1.3", TargetPort: 8080},
		"no address":      {HostPort: 10001, Proto: "tcp", TargetPort: 8080},
		"bad target port": {HostPort: 10001, Proto: "tcp", TargetIP: "10.210.1.3", TargetPort: 70000},
	}
	for name, r := range cases {
		if err := r.Validate(); err == nil {
			t.Errorf("%s: Validate() = nil, want an error", name)
		}
	}
}

func TestRuleKeyDistinguishesProtocolAndPort(t *testing.T) {
	tcp := Rule{HostPort: 10001, Proto: "tcp"}
	udp := Rule{HostPort: 10001, Proto: "udp"}
	if tcp.Key() == udp.Key() {
		t.Fatalf("tcp and udp share key %q", tcp.Key())
	}
	if got, want := tcp.Key(), "tcp/0.0.0.0:10001"; got != want {
		t.Fatalf("Key() = %q, want %q", got, want)
	}
}

// A port already taken by another process surfaces as an error naming the
// container, which is what the admin sees in the console.
func TestApplyReportsBindFailure(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	taken := ln.Addr().(*net.TCPAddr).Port
	targetIP, targetPort := echoTCP(t, "one:")
	m := quietManager(t)

	err = m.Apply([]Rule{{HostIP: "127.0.0.1", HostPort: taken, Proto: "tcp", TargetIP: targetIP, TargetPort: targetPort, Name: "dev01"}})
	if err == nil {
		t.Fatal("apply on an occupied port returned no error")
	}
	if !strings.Contains(err.Error(), "dev01") {
		t.Fatalf("error %q does not name the container", err)
	}
	var opErr *net.OpError
	if !errors.As(err, &opErr) {
		t.Logf("bind error was not a net.OpError: %v", err)
	}
}
