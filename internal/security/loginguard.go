// Package security hosts in-process brute-force protection for the login
// endpoint. It is deliberately a single-node, in-memory facility: a windowed
// failure counter per IP and per account, with short automatic lockouts.
//
// Why in-memory and not SQLite: each failure is a write, and login attempts
// are the exact path an attacker floods. Writing to the DB on every guess
// trades a CPU problem for a disk-serialization one (and risks locking the DB
// under load). State loss on restart is acceptable — only transient lockout
// metadata is lost, never the durable security policy (which lives in the
// settings table). Multi-node deployments would need Redis here; that is out
// of scope for this release.
package security

import (
	"sync"
	"sync/atomic"
	"time"
)

// DefaultConfig is the "balanced" policy chosen with the operator:
//   - per IP:        5 failures in a rolling 15 min window  → 30 min lockout
//   - per account:  10 failures in a rolling 15 min window  → 15 min lockout
//
// Thresholds are tuned to absorb a user mistyping their password a few times
// while stopping an online brute-force that does ~1 attempt/sec.
func DefaultConfig() Config {
	return Config{
		Enabled:           true,
		IPFailThreshold:   5,
		IPFailWindow:      15 * time.Minute,
		IPLockDuration:    30 * time.Minute,
		AcctFailThreshold: 10,
		AcctFailWindow:    15 * time.Minute,
		AcctLockDuration:  15 * time.Minute,
	}
}

// Config is the policy a LoginGuard enforces. It is swapped atomically so an
// admin editing the security-policy settings does not require a restart.
type Config struct {
	Enabled bool // master switch; when false IsLocked/Record* are no-ops

	IPFailThreshold   int           // failures within IPFailWindow that trip an IP lock
	IPFailWindow      time.Duration // rolling window counting IP failures
	IPLockDuration    time.Duration // how long the IP stays locked once tripped
	AcctFailThreshold int           // failures within AcctFailWindow that trip an account lock
	AcctFailWindow    time.Duration
	AcctLockDuration  time.Duration
}

// counter tracks failures for one key (an IP or a username) within a rolling
// window, plus any active lockout. Guarded by its own mutex.
type counter struct {
	mu          sync.Mutex
	failures    []time.Time // timestamps inside the window; trimmed on read
	lockedUntil time.Time
}

// Status is a read-only snapshot of one locked entry, used to render the
// "currently locked" list in the admin UI.
type Status struct {
	Key         string    `json:"key"`
	Kind        string    `json:"kind"` // "ip" | "account"
	LockedUntil time.Time `json:"lockedUntil"`
	Reason      string    `json:"reason"`
}

// LoginGuard tracks login failures and lockouts in memory. Safe for concurrent
// use. Lock ordering across the guard is g.mu → c.mu; no path takes them in
// the reverse order, so there is no deadlock risk.
type LoginGuard struct {
	cfg atomic.Pointer[Config]

	mu      sync.Mutex
	ips     map[string]*counter
	accts   map[string]*counter
	stop    chan struct{}
	stopped bool
}

// NewLoginGuard returns a guard running with DefaultConfig and starts a
// background sweeper that periodically evicts expired entries so the maps
// cannot grow unbounded under an attacker flood.
func NewLoginGuard() *LoginGuard {
	g := &LoginGuard{
		ips:   make(map[string]*counter),
		accts: make(map[string]*counter),
		stop:  make(chan struct{}),
	}
	g.cfg.Store(ptr(DefaultConfig()))
	go g.sweep(time.Minute)
	return g
}

// UpdateConfig swaps the active policy atomically. Existing lockouts are not
// retroactively shortened or extended — the new thresholds apply to future
// failures. Pass Enabled=false to disable the guard at runtime.
func (g *LoginGuard) UpdateConfig(c Config) { g.cfg.Store(&c) }

// Config returns the currently active policy.
func (g *LoginGuard) Config() Config {
	if c := g.cfg.Load(); c != nil {
		return *c
	}
	return DefaultConfig()
}

// IsLocked reports whether the given IP or username is currently locked, and
// if so, when the lock expires and a short human-readable reason. The reason
// does not reveal which axis tripped (IP vs account) to avoid leaking signal
// to an attacker probing the system; the UI shows one generic message.
func (g *LoginGuard) IsLocked(ip, username string) (locked bool, until time.Time, reason string) {
	cfg := g.Config()
	if !cfg.Enabled {
		return false, time.Time{}, ""
	}
	now := time.Now()
	if ip != "" {
		if u, exp := lockedAt(g.lookup(g.ips, ip), now); u {
			return true, exp, "too many failed attempts, please try again later"
		}
	}
	if username != "" {
		if u, exp := lockedAt(g.lookup(g.accts, username), now); u {
			return true, exp, "too many failed attempts, please try again later"
		}
	}
	return false, time.Time{}, ""
}

// lockedAt reports the lock state of a counter at now. Takes c.mu.
func lockedAt(c *counter, now time.Time) (bool, time.Time) {
	if c == nil {
		return false, time.Time{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if now.Before(c.lockedUntil) {
		return true, c.lockedUntil
	}
	return false, time.Time{}
}

// RecordFailure registers one failed attempt for ip and (if non-empty)
// username. If the rolling-window count crosses the threshold, the
// appropriate lock is engaged. Each call records exactly one failure.
func (g *LoginGuard) RecordFailure(ip, username string) {
	cfg := g.Config()
	if !cfg.Enabled {
		return
	}
	now := time.Now()
	if ip != "" {
		g.record(g.ips, ip, now, cfg.IPFailWindow, cfg.IPFailThreshold, cfg.IPLockDuration)
	}
	if username != "" {
		g.record(g.accts, username, now, cfg.AcctFailWindow, cfg.AcctFailThreshold, cfg.AcctLockDuration)
	}
}

// record appends a failure timestamp, trims to the window, and engages the
// lock if the threshold is met. Map access under g.mu, counter mutation under
// c.mu (taken AFTER releasing g.mu to preserve ordering).
func (g *LoginGuard) record(bucket map[string]*counter, key string, now time.Time, window time.Duration, threshold int, lockFor time.Duration) {
	g.mu.Lock()
	c, ok := bucket[key]
	if !ok {
		c = &counter{}
		bucket[key] = c
	}
	g.mu.Unlock()

	c.mu.Lock()
	defer c.mu.Unlock()
	cutoff := now.Add(-window)
	kept := c.failures[:0]
	for _, t := range c.failures {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	kept = append(kept, now)
	c.failures = kept
	// Only (re)engage the lock if it isn't already active — otherwise an
	// attacker hammering past the threshold would keep extending their lock,
	// effectively self-DoSing a single IP into a permanent ban.
	if len(c.failures) >= threshold && now.After(c.lockedUntil) {
		c.lockedUntil = now.Add(lockFor)
	}
}

// RecordSuccess clears the failure history for the given IP and username — a
// successful login resets the rolling count so a user who fat-fingers once
// does not accumulate toward a lock across days.
func (g *LoginGuard) RecordSuccess(ip, username string) {
	cfg := g.Config()
	if !cfg.Enabled {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if ip != "" {
		delete(g.ips, ip)
	}
	if username != "" {
		delete(g.accts, username)
	}
}

// LockedStatuses returns a snapshot of currently-locked IPs and accounts for
// display in the admin UI. Cheap enough to call on each GET /api/settings/security.
func (g *LoginGuard) LockedStatuses() []Status {
	now := time.Now()
	var out []Status
	g.mu.Lock()
	defer g.mu.Unlock()
	for k, c := range g.ips {
		c.mu.Lock()
		if now.Before(c.lockedUntil) {
			out = append(out, Status{Key: k, Kind: "ip", LockedUntil: c.lockedUntil, Reason: "ip lock"})
		}
		c.mu.Unlock()
	}
	for k, c := range g.accts {
		c.mu.Lock()
		if now.Before(c.lockedUntil) {
			out = append(out, Status{Key: k, Kind: "account", LockedUntil: c.lockedUntil, Reason: "account lock"})
		}
		c.mu.Unlock()
	}
	return out
}

// sweep periodically evicts counters with no live lockout and no in-window
// failures, bounding memory. Stops when Close() is called.
func (g *LoginGuard) sweep(interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-g.stop:
			return
		case <-t.C:
			g.evictExpired()
		}
	}
}

func (g *LoginGuard) evictExpired() {
	cfg := g.Config()
	now := time.Now()
	// Use the longer window so we don't churn entries that still hold data.
	window := cfg.IPFailWindow
	if cfg.AcctFailWindow > window {
		window = cfg.AcctFailWindow
	}
	cutoff := now.Add(-window)
	g.mu.Lock()
	defer g.mu.Unlock()
	g.evictFrom(g.ips, now, cutoff)
	g.evictFrom(g.accts, now, cutoff)
}

// evictFrom deletes expired entries from a bucket. Caller holds g.mu.
func (g *LoginGuard) evictFrom(bucket map[string]*counter, now, cutoff time.Time) {
	for k, c := range bucket {
		c.mu.Lock()
		stillLocked := now.Before(c.lockedUntil)
		hasRecent := false
		for _, t := range c.failures {
			if t.After(cutoff) {
				hasRecent = true
				break
			}
		}
		c.mu.Unlock()
		if !stillLocked && !hasRecent {
			delete(bucket, k)
		}
	}
}

// lookup returns the counter for key or nil if absent.
func (g *LoginGuard) lookup(bucket map[string]*counter, key string) *counter {
	g.mu.Lock()
	defer g.mu.Unlock()
	return bucket[key]
}

// Close stops the background sweeper. Safe to call once; intended for tests
// and graceful shutdown.
func (g *LoginGuard) Close() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.stopped {
		return
	}
	g.stopped = true
	close(g.stop)
}

func ptr[T any](v T) *T { return &v }
