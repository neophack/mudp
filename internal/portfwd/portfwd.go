// Package portfwd forwards host ports to container addresses in mudp's own
// process, as an alternative to Docker's port publishing.
//
// Docker publishes a port by installing DNAT rules and (for the userland proxy)
// binding the host port itself. On a host whose firewall/NAT is owned by
// something else — an OpenWrt router appliance, where containers sit on a LAN
// network and Docker's iptables integration is off or overwritten — that
// machinery does not work: the bind either fails outright or the rules are
// flushed and the published port answers nothing. The container is still
// perfectly reachable at its own LAN address (`curl 10.210.1.3:8080`), so the
// missing piece is only the hop from the host port to that address.
//
// This package is that hop: a plain userspace relay. A rule says "host port
// 10001 → 10.210.1.3:8080", and the Manager keeps a listener alive for it. TCP
// and UDP are both carried, byte for byte, with no interpretation of the
// payload — SSH, HTTP, WebSockets, gRPC, DNS, WireGuard and anything else work
// because nothing here knows what protocol it is relaying.
package portfwd

import (
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// dialTimeout bounds how long a new TCP connection waits for the container.
	// A container that is restarting should fail the connection quickly rather
	// than hold the client (and a goroutine) for the OS default.
	dialTimeout = 10 * time.Second
	// udpIdleTimeout retires a UDP flow that has been silent for this long. UDP
	// has no close, so every client address that ever sends a packet would
	// otherwise leak a socket and a goroutine for the process's lifetime.
	udpIdleTimeout = 2 * time.Minute
	// udpBufferSize is the read buffer for one datagram. 64KiB is the largest a
	// UDP payload can be, so no datagram is ever truncated.
	udpBufferSize = 64 * 1024
)

// Rule is one host port to forward, and where to forward it. Rules are derived
// from container state on every reconcile, never stored: the container's
// address changes when it restarts, and the rule has to follow it.
type Rule struct {
	// HostIP is the address to bind. Empty means every interface, which is what
	// makes the forwarded port reachable from the LAN the way a published
	// Docker port would be.
	HostIP string `json:"hostIp,omitempty"`
	// HostPort is the port on the host, allocated from the owner's port range.
	HostPort int `json:"hostPort"`
	// Proto is "tcp" or "udp".
	Proto string `json:"proto"`
	// TargetIP is the container's address on the forwarding network, e.g.
	// "10.210.1.3".
	TargetIP string `json:"targetIp"`
	// TargetPort is the port inside the container.
	TargetPort int `json:"targetPort"`
	// Container and Owner are informational: they let the console show who a
	// listener belongs to, and make the logs readable.
	Container string `json:"container,omitempty"`
	Owner     string `json:"owner,omitempty"`
	// Name is the container's display name, for the same reason.
	Name string `json:"name,omitempty"`
	// Source says where the rule came from: "container" for one derived from a
	// container on a forwarding network, "manual" for one an administrator added
	// by hand. The console lists both together and only lets manual ones be
	// deleted, so it has to be able to tell them apart.
	Source string `json:"source,omitempty"`
	// ManualID is the stored id of a manual rule, so the console can delete it.
	ManualID int64 `json:"manualId,omitempty"`
	// Note carries an administrator's description of a manual rule.
	Note string `json:"note,omitempty"`
	// RequireLogin, when true, gates this forward behind the manager's auth gate
	// (see Manager.SetAuthGate): a client must present a valid console session
	// before the relay connects through to the container. Only TCP forwards are
	// honoured — the gate needs an HTTP request to read a cookie — and a UDP rule
	// carrying this flag is rejected at validation. Honoured only when an auth
	// gate is installed; otherwise the flag is inert and the relay is open.
	RequireLogin bool `json:"requireLogin,omitempty"`
}

// Key identifies the host-side socket a rule occupies. Two rules with the same
// key are the same listener, so the key is what the reconcile diff works on and
// what makes "the target moved" distinguishable from "a new rule appeared".
func (r Rule) Key() string {
	host := r.HostIP
	if host == "" {
		host = "0.0.0.0"
	}
	return fmt.Sprintf("%s/%s", r.Proto, net.JoinHostPort(host, strconv.Itoa(r.HostPort)))
}

// ListenAddr is the address the host-side listener binds.
func (r Rule) ListenAddr() string {
	return net.JoinHostPort(r.HostIP, strconv.Itoa(r.HostPort))
}

// Target is the container-side address connections are relayed to.
func (r Rule) Target() string {
	return net.JoinHostPort(r.TargetIP, strconv.Itoa(r.TargetPort))
}

// sameTarget reports whether two rules point at the same place. A rule whose
// target changed keeps its key but needs its listener restarted, so the diff
// compares this rather than the whole struct (the informational fields must not
// cause a restart on their own).
func (r Rule) sameTarget(o Rule) bool {
	return r.TargetIP == o.TargetIP && r.TargetPort == o.TargetPort
}

// Validate rejects a rule that cannot be served, before it reaches a listener.
func (r Rule) Validate() error {
	if r.Proto != "tcp" && r.Proto != "udp" {
		return fmt.Errorf("unsupported protocol %q", r.Proto)
	}
	if r.HostPort < 1 || r.HostPort > 65535 {
		return fmt.Errorf("invalid host port %d", r.HostPort)
	}
	if r.TargetPort < 1 || r.TargetPort > 65535 {
		return fmt.Errorf("invalid container port %d", r.TargetPort)
	}
	if strings.TrimSpace(r.TargetIP) == "" {
		return errors.New("container address is unknown")
	}
	// Login-gating works by peeking an HTTP request, so a UDP forward cannot
	// honour it: rejecting it here keeps a misconfigured rule from silently
	// passing traffic ungated (the only thing worse than a closed port is one
	// the admin believes is gated but is not).
	if r.RequireLogin && r.Proto == "udp" {
		return errors.New("require_login is only supported for tcp forwards")
	}
	return nil
}

// Status is one running listener, for the admin view.
type Status struct {
	Rule
	// Active is the number of connections (TCP) or client flows (UDP) currently
	// relayed by this listener.
	Active int64 `json:"active"`
	// Total counts every connection the listener has accepted since it started.
	Total int64 `json:"total"`
	// Since is when the listener came up, RFC3339.
	Since string `json:"since"`
}

// Manager owns the running listeners and reconciles them against a desired set
// of rules. It is safe for concurrent use; Apply serialises against itself so
// two reconciles cannot both decide to bind the same port.
type Manager struct {
	mu      sync.Mutex
	entries map[string]*entry
	closed  bool
	// logf is the log sink, swapped out in tests to keep output quiet.
	logf func(format string, args ...any)
	// authGate, when set, is consulted by every TCP relay whose rule carries
	// RequireLogin. It peeks the client's first bytes (an HTTP request), decides
	// whether to let the connection through, and — when it does — hands back the
	// bytes already read so they are not lost to the container. When it declines,
	// it has already written its own response to the client (a redirect or an
	// error) and the relay simply closes. Set with SetAuthGate; nil means no
	// gating, in which case RequireLogin flags are inert and forwards are open.
	authGate func(rule Rule, client net.Conn) (peeked []byte, allow bool)
}

// NewManager returns a Manager with no listeners running.
func NewManager() *Manager {
	return &Manager{entries: map[string]*entry{}, logf: log.Printf}
}

// SetLogger replaces the log sink.
func (m *Manager) SetLogger(logf func(format string, args ...any)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if logf != nil {
		m.logf = logf
	}
}

// SetAuthGate installs the function used to login-gate forwards whose rule
// carries RequireLogin. Passing nil disables gating (RequireLogin then has no
// effect). Existing listeners pick the gate up on their next accepted
// connection — they read it fresh each time rather than capturing it at start.
func (m *Manager) SetAuthGate(fn func(rule Rule, client net.Conn) (peeked []byte, allow bool)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.authGate = fn
}

// Apply makes the running listeners match rules: it starts what is missing,
// stops what is no longer wanted, and restarts a listener whose container moved
// to a new address. It is idempotent, so callers reconcile on a timer and after
// every container change without tracking what altered.
//
// A rule that cannot be served (its port is taken by something else, its
// container has no address yet) is reported in the returned error but does not
// stop the rest: one broken forward must not take the others down with it.
func (m *Manager) Apply(rules []Rule) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return errors.New("port forwarder is closed")
	}

	var errs []error
	want := make(map[string]Rule, len(rules))
	for _, r := range rules {
		r.Proto = strings.ToLower(strings.TrimSpace(r.Proto))
		if err := r.Validate(); err != nil {
			errs = append(errs, fmt.Errorf("forward %s:%d: %w", r.Name, r.HostPort, err))
			continue
		}
		// Two containers claiming one host port cannot both win. Keep the first
		// (port allocation should have prevented this) and say so, rather than
		// flapping the listener between them on every reconcile.
		if prev, ok := want[r.Key()]; ok {
			if !prev.sameTarget(r) {
				errs = append(errs, fmt.Errorf("host port %d/%s is claimed by both %s and %s", r.HostPort, r.Proto, prev.Name, r.Name))
			}
			continue
		}
		want[r.Key()] = r
	}

	for key, e := range m.entries {
		r, ok := want[key]
		if ok && e.rule.sameTarget(r) {
			// Same socket, same destination: keep the listener and its open
			// connections, but refresh the informational fields.
			e.rule.Container = r.Container
			e.rule.Owner = r.Owner
			e.rule.Name = r.Name
			continue
		}
		e.stop()
		delete(m.entries, key)
		if ok {
			m.logf("port forward %s: target moved to %s", key, r.Target())
		}
	}

	for key, r := range want {
		if _, ok := m.entries[key]; ok {
			continue
		}
		e, err := m.start(r)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		m.entries[key] = e
		m.logf("port forward %s → %s (%s)", r.ListenAddr(), r.Target(), r.Name)
	}
	return errors.Join(errs...)
}

// Status returns the running listeners, ordered by protocol and host port so
// the admin view is stable between polls.
func (m *Manager) Status() []Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Status, 0, len(m.entries))
	for _, e := range m.entries {
		out = append(out, Status{
			Rule:   e.rule,
			Active: e.active.Load(),
			Total:  e.total.Load(),
			Since:  e.since.Format(time.RFC3339),
		})
	}
	sortStatus(out)
	return out
}

// Rules returns the rules currently being served. Callers use it to keep the
// listeners they cannot re-derive right now: when the source of one kind of rule
// is unavailable (Docker is down, say), reconciling with the rules already
// running for that source leaves them alone instead of tearing them down.
func (m *Manager) Rules() []Rule {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Rule, 0, len(m.entries))
	for _, e := range m.entries {
		out = append(out, e.rule)
	}
	return out
}

// Close stops every listener. The Manager stays closed afterwards: Apply on a
// closed Manager is an error rather than a silent restart, so a reconcile that
// races shutdown cannot resurrect a listener.
func (m *Manager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	for key, e := range m.entries {
		e.stop()
		delete(m.entries, key)
	}
}

// sortStatus orders by protocol then host port (insertion sort: the list is a
// handful of entries, and this avoids pulling in a dependency for it).
func sortStatus(s []Status) {
	less := func(a, b Status) bool {
		if a.Proto != b.Proto {
			return a.Proto < b.Proto
		}
		return a.HostPort < b.HostPort
	}
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && less(s[j], s[j-1]); j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// entry is one running listener.
type entry struct {
	rule   Rule
	closer io.Closer
	since  time.Time
	active atomic.Int64
	total  atomic.Int64
	// done is closed when the listener is being torn down, so the accept loops
	// can tell a deliberate stop from a real error.
	done chan struct{}
	once sync.Once
	logf func(format string, args ...any)
	// manager points back at the owner, so the relay can read the current auth
	// gate (which may be swapped after this listener started) for each accepted
	// connection. Nil only in standalone tests that never set a gate.
	manager *Manager
}

// stop closes the listener and signals its loops to exit. Open connections are
// not force-closed: they are relayed to completion, which keeps a settings save
// or a reconcile from cutting an in-flight SSH session that is still valid.
func (e *entry) stop() {
	e.once.Do(func() {
		close(e.done)
		_ = e.closer.Close()
	})
}

// stopping reports whether the entry has been torn down.
func (e *entry) stopping() bool {
	select {
	case <-e.done:
		return true
	default:
		return false
	}
}

// start binds a rule's host socket and runs its relay loop.
func (m *Manager) start(r Rule) (*entry, error) {
	e := &entry{rule: r, since: time.Now(), done: make(chan struct{}), logf: m.logf, manager: m}
	if r.Proto == "udp" {
		pc, err := net.ListenPacket("udp", r.ListenAddr())
		if err != nil {
			return nil, fmt.Errorf("forward %s/udp for %s: %w", r.ListenAddr(), r.Name, err)
		}
		e.closer = pc
		go e.serveUDP(pc)
		return e, nil
	}
	ln, err := net.Listen("tcp", r.ListenAddr())
	if err != nil {
		return nil, fmt.Errorf("forward %s/tcp for %s: %w", r.ListenAddr(), r.Name, err)
	}
	e.closer = ln
	go e.serveTCP(ln)
	return e, nil
}

// serveTCP accepts connections and relays each one to the container.
func (e *entry) serveTCP(ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			if e.stopping() {
				return
			}
			// A transient accept error (fd exhaustion, a client that vanished
			// mid-handshake) must not kill the listener. Back off briefly so a
			// persistent one cannot spin the CPU.
			e.logf("port forward %s: accept: %v", e.rule.Key(), err)
			time.Sleep(100 * time.Millisecond)
			continue
		}
		e.total.Add(1)
		e.active.Add(1)
		go func() {
			defer e.active.Add(-1)
			e.relayTCP(conn)
		}()
	}
}

// relayTCP copies bytes both ways between one client and the container. Each
// direction is closed independently so a half-close (the client's "no more
// input" on an SSH or HTTP/1.0 exchange) reaches the container instead of the
// connection hanging until one side times out.
//
// When the rule is login-gated and an auth gate is installed, the gate runs
// first: it peeks the opening request, and only if it accepts does the relay
// connect through. The bytes the gate read are replayed to the container so the
// upstream sees the complete request; if the gate declines it has already
// written its own response and the relay closes without ever dialling upstream.
func (e *entry) relayTCP(src net.Conn) {
	defer src.Close()

	var peeked []byte
	if e.rule.RequireLogin && e.manager != nil {
		gate := e.manager.authGate
		if gate != nil {
			ok := false
			peeked, ok = gate(e.rule, src)
			if !ok {
				// The gate wrote its own response (a redirect or an error). Drain
				// any bytes the client still has in flight before closing: closing
				// a socket with unread receive data makes the kernel send RST
				// instead of FIN, which discards the response we just wrote. A
				// short bounded drain turns that close into an orderly one.
				drainConn(src)
				return
			}
		}
	}

	dst, err := net.DialTimeout("tcp", e.rule.Target(), dialTimeout)
	if err != nil {
		e.logf("port forward %s: dial %s: %v", e.rule.Key(), e.rule.Target(), err)
		return
	}
	defer dst.Close()

	// Replay the bytes the auth gate already consumed, so the upstream service
	// receives the request intact. A short write here would corrupt the stream,
	// so it is flushed fully before the bidirectional copy begins.
	if len(peeked) > 0 {
		if _, err := dst.Write(peeked); err != nil {
			e.logf("port forward %s: replay peeked bytes: %v", e.rule.Key(), err)
			return
		}
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(dst, src)
		closeWrite(dst)
	}()
	go func() {
		defer wg.Done()
		_, _ = io.Copy(src, dst)
		closeWrite(src)
	}()
	wg.Wait()
}

// closeWrite half-closes a TCP connection when the transport supports it.
func closeWrite(c net.Conn) {
	type writeCloser interface{ CloseWrite() error }
	if cw, ok := c.(writeCloser); ok {
		_ = cw.CloseWrite()
	}
}

// drainConn reads and discards any data the peer still has in flight, with a
// short deadline, so the caller's subsequent Close becomes an orderly FIN
// rather than an RST. An RST is sent when a socket with unread receive data is
// closed, which would throw away a response the caller already wrote (the auth
// gate's redirect, for instance). Draining a bounded amount covers the typical
// "client sent a request then waits" case without letting a misbehaving peer
// hold the goroutine.
func drainConn(c net.Conn) {
	_ = c.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	buf := make([]byte, 1024)
	for {
		if _, err := c.Read(buf); err != nil {
			return
		}
	}
}

// udpFlow is one client address's conversation with the container. UDP has no
// connection, so the flow is the substitute: it holds the socket replies come
// back on and the deadline that eventually retires it.
type udpFlow struct {
	conn *net.UDPConn
	// lastSeen is guarded by the manager entry's flow mutex.
	lastSeen time.Time
}

// serveUDP relays datagrams both ways. Each client address gets its own socket
// to the container, so replies can be routed back to the client that asked —
// the same NAT behaviour Docker's proxy provides, and what makes DNS, QUIC,
// game protocols and WireGuard work through the forward.
func (e *entry) serveUDP(pc net.PacketConn) {
	var mu sync.Mutex
	flows := map[string]*udpFlow{}

	// closeFlows retires everything; used on shutdown and by the reaper.
	closeFlow := func(key string, f *udpFlow) {
		mu.Lock()
		if cur, ok := flows[key]; ok && cur == f {
			delete(flows, key)
		}
		mu.Unlock()
		_ = f.conn.Close()
	}

	stop := make(chan struct{})
	defer close(stop)
	go func() {
		ticker := time.NewTicker(udpIdleTimeout / 2)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-e.done:
				return
			case <-ticker.C:
				now := time.Now()
				mu.Lock()
				var stale []*net.UDPConn
				for key, f := range flows {
					if now.Sub(f.lastSeen) > udpIdleTimeout {
						stale = append(stale, f.conn)
						delete(flows, key)
					}
				}
				mu.Unlock()
				// Closing the socket ends the flow's reply goroutine, which is
				// what decrements the active count — doing it here as well
				// would double-count the retirement.
				for _, c := range stale {
					_ = c.Close()
				}
			}
		}
	}()

	buf := make([]byte, udpBufferSize)
	for {
		n, client, err := pc.ReadFrom(buf)
		if err != nil {
			if e.stopping() {
				break
			}
			e.logf("port forward %s: read: %v", e.rule.Key(), err)
			time.Sleep(100 * time.Millisecond)
			continue
		}
		key := client.String()
		mu.Lock()
		f, ok := flows[key]
		if !ok {
			conn, derr := dialUDP(e.rule.Target())
			if derr != nil {
				mu.Unlock()
				e.logf("port forward %s: dial %s: %v", e.rule.Key(), e.rule.Target(), derr)
				continue
			}
			f = &udpFlow{conn: conn}
			flows[key] = f
			e.total.Add(1)
			e.active.Add(1)
			go func() {
				defer e.active.Add(-1)
				e.relayUDPReplies(pc, client, f)
				closeFlow(key, f)
			}()
		}
		f.lastSeen = time.Now()
		mu.Unlock()
		if _, err := f.conn.Write(buf[:n]); err != nil {
			e.logf("port forward %s: write %s: %v", e.rule.Key(), e.rule.Target(), err)
		}
	}

	mu.Lock()
	for key, f := range flows {
		delete(flows, key)
		_ = f.conn.Close()
	}
	mu.Unlock()
}

// relayUDPReplies pumps the container's answers back to one client until the
// flow's socket is closed by the reaper or by shutdown.
func (e *entry) relayUDPReplies(pc net.PacketConn, client net.Addr, f *udpFlow) {
	buf := make([]byte, udpBufferSize)
	for {
		n, err := f.conn.Read(buf)
		if err != nil {
			return
		}
		if _, err := pc.WriteTo(buf[:n], client); err != nil {
			if e.stopping() {
				return
			}
			e.logf("port forward %s: reply to %s: %v", e.rule.Key(), client, err)
		}
	}
}

// dialUDP opens a connected UDP socket to the container.
func dialUDP(target string) (*net.UDPConn, error) {
	addr, err := net.ResolveUDPAddr("udp", target)
	if err != nil {
		return nil, err
	}
	return net.DialUDP("udp", nil, addr)
}
