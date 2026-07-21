package httpx

import (
	"net"
	"net/http"
	"strings"
)

// DefaultTrustedProxies is the set of CIDRs trusted to set forwarding headers
// when the caller does not configure MUDP_TRUSTED_PROXIES. It covers private
// LAN space, where a co-located nginx/Caddy on the same host or network would
// originate connections. A direct connection from any of these is treated as a
// trusted proxy whose forwarding headers can be honored.
func DefaultTrustedProxies() []*net.IPNet {
	cidrs := []string{
		"127.0.0.0/8",    // IPv4 loopback
		"10.0.0.0/8",     // RFC1918
		"172.16.0.0/12",  // RFC1918
		"192.168.0.0/16", // RFC1918
		"::1/128",        // IPv6 loopback
		"fc00::/7",       // IPv6 unique-local
		"fe80::/10",      // IPv6 link-local
	}
	out := make([]*net.IPNet, 0, len(cidrs))
	for _, c := range cidrs {
		if _, n, err := net.ParseCIDR(c); err == nil {
			out = append(out, n)
		}
	}
	return out
}

// ParseCIDRs parses a comma/space-separated list of CIDRs (or bare IPs, which
// are promoted to /32 or /128). Invalid entries are skipped silently — this is
// startup config, and a single typo should not prevent boot. Returns nil if
// the input is empty (callers fall back to the default set); an all-invalid
// input returns a non-nil empty slice.
func ParseCIDRs(raw string) []*net.IPNet {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var out []*net.IPNet
	for _, tok := range strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' || r == '\n' }) {
		tok = strings.TrimSpace(tok)
		if tok == "" {
			continue
		}
		if !strings.Contains(tok, "/") {
			ip := net.ParseIP(tok)
			if ip == nil {
				continue
			}
			if ip.To4() != nil {
				tok += "/32"
			} else {
				tok += "/128"
			}
		}
		if _, n, err := net.ParseCIDR(tok); err == nil {
			out = append(out, n)
		}
	}
	return out
}

// contains reports whether ip falls within any of the nets.
func contains(nets []*net.IPNet, ip net.IP) bool {
	for _, n := range nets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// ClientIP determines the originating client IP for r, honoring forwarding
// headers only when the immediate connection comes from a trusted proxy.
//
// Header precedence when the direct peer is trusted:
//  1. CF-Connecting-IP (set by Cloudflare; single value, cannot be spoofed
//     once the peer is trusted)
//  2. X-Real-IP (set by nginx etc.; single value)
//  3. X-Forwarded-For (comma list; the LEFT-most entry is the original client)
//
// If the peer is NOT a trusted proxy, the forwarding headers are ignored and
// the direct peer address is returned — this prevents a public client from
// forging X-Forwarded-For to bypass region locks or brute-force counters.
func ClientIP(r *http.Request, trustedProxies []*net.IPNet) string {
	peer, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		peer = r.RemoteAddr
	}
	peerIP := net.ParseIP(peer)
	if peerIP == nil {
		return peer // unparseable; return as-is rather than guess
	}
	if !contains(trustedProxies, peerIP) {
		return peer
	}
	// Peer is trusted: consult forwarding headers in order.
	if v := strings.TrimSpace(r.Header.Get("CF-Connecting-IP")); v != "" {
		if ip := firstValidIP(v); ip != "" {
			return ip
		}
	}
	if v := strings.TrimSpace(r.Header.Get("X-Real-IP")); v != "" {
		if ip := firstValidIP(v); ip != "" {
			return ip
		}
	}
	if v := r.Header.Get("X-Forwarded-For"); v != "" {
		// Left-most non-empty token is the original client (each proxy appends
		// to the right). We trim rather than trust the right-most, because
		// trusted-proxies config already guarantees the peer is our proxy.
		for _, tok := range strings.Split(v, ",") {
			if ip := firstValidIP(tok); ip != "" {
				return ip
			}
		}
	}
	return peer
}

// firstValidIP trims a single header token and returns it if it parses as an
// IP, else "". Used to skip stray commas/whitespace and invalid values.
func firstValidIP(token string) string {
	token = strings.TrimSpace(token)
	if token == "" {
		return ""
	}
	if net.ParseIP(token) == nil {
		return ""
	}
	return token
}
