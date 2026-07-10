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
	mu      sync.Mutex
	limiters map[string]*rate.Limiter
	r       rate.Limit
	b       int
	ttl     time.Duration
}

// NewRateLimiter creates a limiter that allows r requests per second with a
// burst of b. Idle entries are cleaned up every ttl.
func NewRateLimiter(r rate.Limit, b int, ttl time.Duration) *RateLimiter {
	return &RateLimiter{
		limiters: make(map[string]*rate.Limiter),
		r:        r,
		b:        b,
		ttl:      ttl,
	}
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

// Middleware returns an http.Handler that rate limits by remote IP.
func (rl *RateLimiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			ip = r.RemoteAddr
		}
		l := rl.getLimiter(ip)
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

// DefaultAPIRateLimiter is a reasonable default: 20 req/s with burst 40.
func DefaultAPIRateLimiter() *RateLimiter {
	return NewRateLimiter(20, 40, 10*time.Minute)
}

// StrictRateLimiter is for login endpoints: 1 req/s with burst 5.
func StrictRateLimiter() *RateLimiter {
	return NewRateLimiter(1, 5, 10*time.Minute)
}
