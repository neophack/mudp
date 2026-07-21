package security

import (
	"testing"
	"time"
)

func newTestGuard(t *testing.T) *LoginGuard {
	t.Helper()
	g := &LoginGuard{
		ips:   make(map[string]*counter),
		accts: make(map[string]*counter),
		stop:  make(chan struct{}),
	}
	// Enabled but with short windows so thresholds trip deterministically.
	g.cfg.Store(&Config{
		Enabled:           true,
		IPFailThreshold:   3,
		IPFailWindow:      10 * time.Second,
		IPLockDuration:    30 * time.Second,
		AcctFailThreshold: 5,
		AcctFailWindow:    10 * time.Second,
		AcctLockDuration:  20 * time.Second,
	})
	// No background sweeper for tests — we drive eviction explicitly.
	return g
}

func TestIPLockAfterThreshold(t *testing.T) {
	g := newTestGuard(t)
	defer g.Close()

	ip := "203.0.113.7"
	for i := 0; i < 3; i++ {
		if locked, _, _ := g.IsLocked(ip, ""); locked {
			t.Fatalf("locked too early at attempt %d", i)
		}
		g.RecordFailure(ip, "")
	}
	// 3 failures == threshold → now locked.
	locked, until, reason := g.IsLocked(ip, "")
	if !locked {
		t.Fatal("expected lock after 3 failures")
	}
	if until.IsZero() {
		t.Error("expected non-zero lock expiry")
	}
	if reason == "" {
		t.Error("expected non-empty reason")
	}
}

func TestIPNotLockedBelowThreshold(t *testing.T) {
	g := newTestGuard(t)
	defer g.Close()

	ip := "203.0.113.8"
	g.RecordFailure(ip, "")
	g.RecordFailure(ip, "")
	if locked, _, _ := g.IsLocked(ip, ""); locked {
		t.Fatal("should not lock below threshold (3)")
	}
}

func TestRecordSuccessClearsIP(t *testing.T) {
	g := newTestGuard(t)
	defer g.Close()
	ip := "203.0.113.9"
	g.RecordFailure(ip, "")
	g.RecordFailure(ip, "")
	g.RecordSuccess(ip, "")
	// Counter is removed → 2 fresh failures won't trip (need 3 in a row).
	g.RecordFailure(ip, "")
	g.RecordFailure(ip, "")
	if locked, _, _ := g.IsLocked(ip, ""); locked {
		t.Fatal("RecordSuccess should have reset the count")
	}
}

func TestAccountLockIndependentOfIP(t *testing.T) {
	g := newTestGuard(t)
	defer g.Close()
	// Hit account threshold (5) but stay under IP threshold (3): we send each
	// failure from a distinct IP so no single IP accumulates.
	user := "alice"
	for i := 0; i < 5; i++ {
		ip := "10.0.0." + itoa(i)
		g.RecordFailure(ip, user)
	}
	locked, _, _ := g.IsLocked("10.0.0.9", user)
	if !locked {
		t.Fatal("expected account locked after 5 failures")
	}
	// A different account from the same network is NOT locked.
	if locked2, _, _ := g.IsLocked("10.0.0.9", "bob"); locked2 {
		t.Fatal("different account should not be locked")
	}
}

func TestDisabledGuardNoOps(t *testing.T) {
	g := newTestGuard(t)
	defer g.Close()
	g.UpdateConfig(Config{Enabled: false})
	for i := 0; i < 100; i++ {
		g.RecordFailure("203.0.113.50", "root")
	}
	if locked, _, _ := g.IsLocked("203.0.113.50", "root"); locked {
		t.Fatal("disabled guard must not lock")
	}
}

func TestUpdateConfigTakesEffect(t *testing.T) {
	g := newTestGuard(t)
	defer g.Close()
	// 2 failures under old config (threshold 3).
	g.RecordFailure("203.0.113.60", "")
	g.RecordFailure("203.0.113.60", "")
	// Tighten to threshold 1 going forward.
	g.UpdateConfig(Config{
		Enabled:         true,
		IPFailThreshold: 1,
		IPFailWindow:    time.Minute,
		IPLockDuration:  time.Minute,
	})
	g.RecordFailure("203.0.113.60", "")
	if locked, _, _ := g.IsLocked("203.0.113.60", ""); !locked {
		t.Fatal("new threshold should trip lock on next failure")
	}
}

func TestLockDoesNotExtendUnderHammer(t *testing.T) {
	g := newTestGuard(t)
	defer g.Close()
	ip := "203.0.113.70"
	for i := 0; i < 3; i++ {
		g.RecordFailure(ip, "")
	}
	_, firstLock, _ := g.IsLocked(ip, "")
	// Hammer 50 more failures; the original lock window must NOT keep sliding.
	for i := 0; i < 50; i++ {
		g.RecordFailure(ip, "")
	}
	_, secondLock, _ := g.IsLocked(ip, "")
	if !secondLock.Equal(firstLock) {
		t.Errorf("lock extended under hammer: first=%v second=%v", firstLock, secondLock)
	}
}

func TestLockedStatusesListsBothKinds(t *testing.T) {
	g := newTestGuard(t)
	defer g.Close()
	// Lock an IP (3) and an account (5 from distinct IPs).
	for i := 0; i < 3; i++ {
		g.RecordFailure("203.0.113.80", "")
	}
	for i := 0; i < 5; i++ {
		g.RecordFailure("10.0.1."+itoa(i), "carol")
	}
	statuses := g.LockedStatuses()
	kinds := map[string]int{}
	for _, s := range statuses {
		kinds[s.Kind]++
	}
	if kinds["ip"] == 0 {
		t.Error("no ip lock in statuses")
	}
	if kinds["account"] == 0 {
		t.Error("no account lock in statuses")
	}
}

func TestEvictExpired(t *testing.T) {
	g := newTestGuard(t)
	defer g.Close()
	ip := "203.0.113.90"
	// One failure, not enough to lock, window 10s.
	g.RecordFailure(ip, "")
	// Simulate time passing past the window by mangling the timestamp.
	g.mu.Lock()
	if c := g.ips[ip]; c != nil {
		c.mu.Lock()
		for i := range c.failures {
			c.failures[i] = time.Now().Add(-time.Hour)
		}
		c.mu.Unlock()
	}
	g.mu.Unlock()
	g.evictExpired()
	g.mu.Lock()
	_, present := g.ips[ip]
	g.mu.Unlock()
	if present {
		t.Error("expired entry should have been evicted")
	}
}

func TestConcurrentRecordFailure(t *testing.T) {
	g := newTestGuard(t)
	defer g.Close()
	// Many goroutines hammering different keys; must not panic/race.
	done := make(chan struct{})
	for w := 0; w < 8; w++ {
		go func(id int) {
			defer func() { done <- struct{}{} }()
			for i := 0; i < 30; i++ {
				ip := "10.2.0." + itoa((id*30+i)%250)
				g.RecordFailure(ip, "user"+itoa(id))
				_, _, _ = g.IsLocked(ip, "user"+itoa(id))
			}
		}(w)
	}
	for i := 0; i < 8; i++ {
		<-done
	}
}

// itoa is a tiny strconv.Itoa without the import, to keep the test focused.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [12]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
