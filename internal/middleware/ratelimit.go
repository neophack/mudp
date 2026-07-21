package middleware

import (
	"net"
	"net/http"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"mudp/internal/httpx"
)

// RateLimiter is a per-IP rate limiter. It is safe for concurrent use.
type RateLimiter struct {
	mu       sync.Mutex
	limiters map[string]*rate.Limiter
	r        rate.Limit
	b        int
	ttl      time.Duration

	// trustedProxies, when set, makes the limiter key on the real client IP
	// resolved from forwarding headers (see httpx.ClientIP) instead of the raw
	// RemoteAddr. This matters behind nginx/Caddy/Cloudflare: without it every
	// request appears to come from the proxy and they all share one bucket.
	trustedProxies []*net.IPNet

	stop chan struct{}
}

// NewRateLimiter creates a limiter that allows r requests per second with a
// burst of b. Idle entries are cleaned up every ttl — the cleanup goroutine is
// now actually started (previously the ttl was stored but unused, leaking an
// entry per unique IP forever).
func NewRateLimiter(r rate.Limit, b int, ttl time.Duration) *RateLimiter {
	return NewRateLimiterWithProxies(r, b, ttl, nil)
}

// NewRateLimiterWithProxies is like NewRateLimiter but keys on the real client
// IP when the immediate peer is in trustedProxies. Pass nil to key on the raw
// RemoteAddr (direct-exposure deployments).
func NewRateLimiterWithProxies(r rate.Limit, b int, ttl time.Duration, trustedProxies []*net.IPNet) *RateLimiter {
	rl := &RateLimiter{
		limiters:       make(map[string]*rate.Limiter),
		r:              r,
		b:              b,
		ttl:            ttl,
		trustedProxies: trustedProxies,
		stop:           make(chan struct{}),
	}
	if ttl > 0 {
		go rl.sweep(ttl)
	}
	return rl
}

func (rl *RateLimiter) getLimiter(ip string) *rate.Limiter {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	l, ok := rl.limiters[ip]
	if !ok {
		l = rate.NewLimiter(rl.r, rl.b)
		rl.limiters[ip] = l
	}
	return l
}

// keyFor returns the lookup key for a request: the resolved client IP when a
// proxy is trusted, otherwise the raw RemoteAddr host.
func (rl *RateLimiter) keyFor(r *http.Request) string {
	if len(rl.trustedProxies) > 0 {
		return httpx.ClientIP(r, rl.trustedProxies)
	}
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return ip
}

// Middleware returns an http.Handler that rate limits by client IP.
func (rl *RateLimiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		l := rl.getLimiter(rl.keyFor(r))
		if !l.Allow() {
			httpx.WriteErr(w, &httpx.HandlerError{
				Status:  http.StatusTooManyRequests,
				Message: "too many requests",
			})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// sweep periodically drops limiters that have been idle longer than ttl,
// bounding memory under attacker IP churn. Runs until Close() is called.
func (rl *RateLimiter) sweep(interval time.Duration) {
	// We don't track per-entry last-used timestamps (the rate.Limiter doesn't
	// expose them). Instead we drop the whole table on each sweep: a returning
	// client simply rebuilds its bucket at the configured burst. This is a
	// tradeoff — correct under typical burst limits, and cheap.
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-rl.stop:
			return
		case <-t.C:
			rl.mu.Lock()
			rl.limiters = make(map[string]*rate.Limiter)
			rl.mu.Unlock()
		}
	}
}

// Close stops the background sweeper. Safe to call multiple times.
func (rl *RateLimiter) Close() {
	select {
	case <-rl.stop:
		// already closed
	default:
		close(rl.stop)
	}
}

// DefaultAPIRateLimiter is a reasonable default: 20 req/s with burst 40.
func DefaultAPIRateLimiter() *RateLimiter {
	return NewRateLimiter(20, 40, 10*time.Minute)
}

// StrictRateLimiter is for login endpoints: 1 req/s with burst 5.
func StrictRateLimiter() *RateLimiter {
	return NewRateLimiter(1, 5, 10*time.Minute)
}
