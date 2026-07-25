package middleware

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"mudp/internal/httpx"
)

// TrustedProxies is the set of peer addresses whose forwarding headers may be
// believed. It must stay empty unless a reverse proxy really does sit in front
// of the server: X-Forwarded-For is trivially spoofable, so honouring it from
// an arbitrary peer would let a single client mint a fresh rate-limit bucket
// per request and defeat login throttling entirely.
type TrustedProxies struct {
	nets []*net.IPNet
}

// ParseTrustedProxies builds a TrustedProxies from a comma-separated list of IP
// addresses and CIDR blocks (e.g. "127.0.0.1,10.0.0.0/8"). Unparsable entries
// are reported so a typo fails loudly at startup instead of silently disabling
// the forwarding it was meant to enable.
func ParseTrustedProxies(spec string) (*TrustedProxies, error) {
	tp := &TrustedProxies{}
	for _, item := range strings.Split(spec, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if _, block, err := net.ParseCIDR(item); err == nil {
			tp.nets = append(tp.nets, block)
			continue
		}
		ip := net.ParseIP(item)
		if ip == nil {
			return nil, &net.ParseError{Type: "trusted proxy address", Text: item}
		}
		bits := 32
		if ip.To4() == nil {
			bits = 128
		}
		tp.nets = append(tp.nets, &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)})
	}
	return tp, nil
}

func (tp *TrustedProxies) trusts(ip net.IP) bool {
	if tp == nil || ip == nil {
		return false
	}
	for _, n := range tp.nets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// ClientIP returns the address to attribute a request to. It is the socket peer
// unless that peer is a configured trusted proxy, in which case the last
// untrusted hop in X-Forwarded-For is used. Walking the chain from the right
// means a client-supplied prefix cannot displace the value the proxy appended.
func (tp *TrustedProxies) ClientIP(r *http.Request) string {
	peer := remoteIP(r)
	if !tp.trusts(net.ParseIP(peer)) {
		return peer
	}
	parts := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
	for i := len(parts) - 1; i >= 0; i-- {
		candidate := strings.TrimSpace(parts[i])
		if candidate == "" {
			continue
		}
		ip := net.ParseIP(candidate)
		if ip == nil {
			continue
		}
		if tp.trusts(ip) {
			continue // another proxy in the chain; keep walking left
		}
		return ip.String()
	}
	return peer
}

func remoteIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// RateLimiter is a per-IP rate limiter. It is safe for concurrent use.
type RateLimiter struct {
	mu       sync.Mutex
	limiters map[string]*limiterEntry
	r        rate.Limit
	b        int
	ttl      time.Duration
	lastGC   time.Time
	proxies  *TrustedProxies
}

type limiterEntry struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

// NewRateLimiter creates a limiter that allows r requests per second with a
// burst of b. Idle entries are dropped once they go unused for ttl; a zero ttl
// keeps them forever (used by tests).
func NewRateLimiter(r rate.Limit, b int, ttl time.Duration) *RateLimiter {
	return &RateLimiter{
		limiters: make(map[string]*limiterEntry),
		r:        r,
		b:        b,
		ttl:      ttl,
		lastGC:   time.Now(),
	}
}

// TrustProxies configures which peers may supply a forwarding header. Returns
// the limiter so it can be chained onto a constructor.
func (rl *RateLimiter) TrustProxies(tp *TrustedProxies) *RateLimiter {
	rl.proxies = tp
	return rl
}

func (rl *RateLimiter) getLimiter(ip string) *rate.Limiter {
	now := time.Now()
	rl.mu.Lock()
	defer rl.mu.Unlock()
	rl.gcLocked(now)
	e, ok := rl.limiters[ip]
	if !ok {
		e = &limiterEntry{limiter: rate.NewLimiter(rl.r, rl.b)}
		rl.limiters[ip] = e
	}
	e.lastSeen = now
	return e.limiter
}

// gcLocked drops entries that have been idle for longer than ttl. Without it
// the map grows without bound — one entry per distinct source address, which an
// attacker with an IPv6 range can inflate at will until the process runs out of
// memory. It runs at most once per ttl to keep the common path cheap.
func (rl *RateLimiter) gcLocked(now time.Time) {
	if rl.ttl <= 0 || now.Sub(rl.lastGC) < rl.ttl {
		return
	}
	rl.lastGC = now
	for ip, e := range rl.limiters {
		if now.Sub(e.lastSeen) > rl.ttl {
			delete(rl.limiters, ip)
		}
	}
}

// Middleware returns an http.Handler that rate limits by client IP.
func (rl *RateLimiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !rl.getLimiter(rl.proxies.ClientIP(r)).Allow() {
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
