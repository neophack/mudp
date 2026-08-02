package server

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"mudp/internal/config"
	"mudp/internal/store"
)

// TestParseUserAgent covers the dependency-free UA parser used by both the
// login security monitor and the MCP attack log. It must return a browser
// family with its major.minor version, an OS label, and a coarse device class.
func TestParseUserAgent(t *testing.T) {
	cases := []struct {
		name, ua, browser, osName, device string
	}{
		{
			name:    "chrome on windows",
			ua:      "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36",
			browser: "Chrome 120.0",
			osName:  "Windows 10/11",
			device:  "desktop",
		},
		{
			name:    "edge chromium",
			ua:      "Mozilla/5.0 (Windows NT 10.0) AppleWebKit/537.36 Edg/119.0.0.0 Chrome/119.0 Safari/537.36",
			browser: "Microsoft Edge 119.0",
			osName:  "Windows 10/11",
			device:  "desktop",
		},
		{
			name:    "firefox on mac",
			ua:      "Mozilla/5.0 (Macintosh; Intel Mac OS X 13.5) Gecko/20100101 Firefox/118.0",
			browser: "Firefox 118.0",
			osName:  "macOS",
			device:  "desktop",
		},
		{
			name:    "safari on iphone",
			ua:      "Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) Version/17.0 Mobile Safari/604.1",
			browser: "Safari 17.0",
			osName:  "iOS",
			device:  "mobile",
		},
		{
			name:    "curl bot",
			ua:      "curl/8.0.1",
			browser: "",
			osName:  "",
			device:  "bot",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b, o, d := parseUserAgent(c.ua)
			if b != c.browser {
				t.Errorf("browser = %q, want %q", b, c.browser)
			}
			if o != c.osName {
				t.Errorf("os = %q, want %q", o, c.osName)
			}
			if d != c.device {
				t.Errorf("device = %q, want %q", d, c.device)
			}
		})
	}
}

// TestTZMismatchReason covers the VPN/timezone-mismatch detector. A browser
// timezone that disagrees with the IP timezone is flagged, and a flagged proxy
// is flagged even when both timezones are blank.
func TestTZMismatchReason(t *testing.T) {
	if got := tzMismatchReason("Asia/Shanghai", "Europe/London", false, false); got == "" {
		t.Error("expected tz-mismatch flag for disagreeing timezones")
	}
	// Same offset (Asia/Shanghai == Asia/Hong Kong, both UTC+8) → not a mismatch.
	if got := tzMismatchReason("Asia/Shanghai", "Asia/Hong_Kong", false, false); got != "" {
		t.Errorf("same-offset zones flagged as suspicious: %q", got)
	}
	// Identical zone → never flagged.
	if got := tzMismatchReason("Asia/Shanghai", "Asia/Shanghai", false, false); got != "" {
		t.Errorf("identical zone flagged: %q", got)
	}
	// VPN/proxy address flagged regardless of timezone.
	if got := tzMismatchReason("", "", true, false); got == "" {
		t.Error("expected vpn/proxy flag for a proxy address")
	}
}

// TestProxyTypeLabel covers the two-boolean → one-label collapse.
func TestProxyTypeLabel(t *testing.T) {
	if got := proxyTypeLabel(true, true); got != "vpn/hosting" {
		t.Errorf("proxy+hosting = %q, want vpn/hosting", got)
	}
	if got := proxyTypeLabel(true, false); got != "proxy" {
		t.Errorf("proxy = %q, want proxy", got)
	}
	if got := proxyTypeLabel(false, true); got != "hosting" {
		t.Errorf("hosting = %q, want hosting", got)
	}
	if got := proxyTypeLabel(false, false); got != "" {
		t.Errorf("clean = %q, want empty", got)
	}
}

// TestSameOriginWS covers the WebSocket Origin-vs-Host guard against
// cross-site WebSocket hijacking. A browser always sends Origin on an upgrade,
// so a present-but-different value is refused; an absent header means a
// non-browser client (no ambient cookie authority) and is allowed.
func TestSameOriginWS(t *testing.T) {
	cases := []struct {
		name, target, origin string
		want                 bool
	}{
		{name: "no origin header allowed", target: "http://example.com/ws", origin: "", want: true},
		{name: "origin host equals host", target: "http://example.com/ws", origin: "http://example.com", want: true},
		{name: "scheme ignored", target: "http://example.com/ws", origin: "https://example.com", want: true},
		{name: "host compared case-insensitively", target: "http://example.com/ws", origin: "http://EXAMPLE.COM", want: true},
		{name: "different host refused", target: "http://example.com/ws", origin: "http://evil.com", want: false},
		{name: "subdomain refused", target: "http://example.com/ws", origin: "http://sub.example.com", want: false},
		{name: "different port refused", target: "http://example.com/ws", origin: "http://example.com:8080", want: false},
		{name: "host with matching port allowed", target: "http://example.com:8080/ws", origin: "http://example.com:8080", want: true},
		{name: "garbage origin refused", target: "http://example.com/ws", origin: "://not-a-url", want: false},
		{name: "hostless origin refused", target: "http://example.com/ws", origin: "just-a-string", want: false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, c.target, nil)
			if c.origin != "" {
				r.Header.Set("Origin", c.origin)
			}
			if got := sameOriginWS(r); got != c.want {
				t.Errorf("sameOriginWS(origin=%q, host=%q) = %v, want %v", c.origin, r.Host, got, c.want)
			}
		})
	}
}

// TestFeishuRedirectURL covers the OAuth callback URL built from r.Host and
// X-Forwarded-Proto: https only when TLS is present or the exact header value
// is "https" (anything else, including "HTTPS", falls back to http).
func TestFeishuRedirectURL(t *testing.T) {
	cases := []struct {
		name, target, forwardedProto, want string
	}{
		{name: "plain http", target: "http://panel.example.com/login", forwardedProto: "", want: "http://panel.example.com/api/feishu/callback"},
		{name: "forwarded https", target: "http://panel.example.com/login", forwardedProto: "https", want: "https://panel.example.com/api/feishu/callback"},
		{name: "forwarded http stays http", target: "http://panel.example.com/login", forwardedProto: "http", want: "http://panel.example.com/api/feishu/callback"},
		{name: "uppercase forwarded proto not matched", target: "http://panel.example.com/login", forwardedProto: "HTTPS", want: "http://panel.example.com/api/feishu/callback"},
		{name: "tls request", target: "https://panel.example.com/login", forwardedProto: "", want: "https://panel.example.com/api/feishu/callback"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, c.target, nil)
			if c.forwardedProto != "" {
				r.Header.Set("X-Forwarded-Proto", c.forwardedProto)
			}
			if got := feishuRedirectURL(r); got != c.want {
				t.Errorf("feishuRedirectURL = %q, want %q", got, c.want)
			}
		})
	}
}

// TestFeishuStateSig pins the signing scheme for the Feishu OAuth state cookie:
// hex(HMAC-SHA256(secret, state)). It is deterministic, 64 hex chars, and
// changes with either input.
func TestFeishuStateSig(t *testing.T) {
	sig := feishuStateSig("test-secret", "0123456789abcdef")
	if len(sig) != 64 {
		t.Errorf("sig length = %d, want 64 hex chars", len(sig))
	}
	if _, err := hex.DecodeString(sig); err != nil {
		t.Errorf("sig is not hex: %v", err)
	}
	// Independently recompute the expected digest to pin the construction.
	m := hmac.New(sha256.New, []byte("test-secret"))
	m.Write([]byte("0123456789abcdef"))
	if want := hex.EncodeToString(m.Sum(nil)); sig != want {
		t.Errorf("sig = %q, want %q", sig, want)
	}
	if feishuStateSig("test-secret", "state-a") != feishuStateSig("test-secret", "state-a") {
		t.Error("sig is not deterministic")
	}
	if feishuStateSig("other-secret", "state-a") == feishuStateSig("test-secret", "state-a") {
		t.Error("sig unchanged by different secret")
	}
	if feishuStateSig("test-secret", "state-b") == feishuStateSig("test-secret", "state-a") {
		t.Error("sig unchanged by different state")
	}
}

// TestFeishuStateVerify covers the CSRF guard on the Feishu SSO callback: the
// signed state cookie must exist, be well-formed, match the state query param,
// and carry a valid HMAC made with the configured session secret.
func TestFeishuStateVerify(t *testing.T) {
	const secret = "test-secret"
	a := &App{cfg: config.Config{SessionSecret: secret}}
	const raw = "0123456789abcdef"

	signedCookie := func(cookieSecret, state string) *http.Cookie {
		return &http.Cookie{
			Name:  feishuStateCookie,
			Value: state + "." + feishuStateSig(cookieSecret, state),
		}
	}
	newReq := func(stateQuery string, cookie *http.Cookie) *http.Request {
		target := "http://panel.example.com/api/feishu/callback"
		if stateQuery != "" {
			target += "?state=" + stateQuery
		}
		r := httptest.NewRequest(http.MethodGet, target, nil)
		if cookie != nil {
			r.AddCookie(cookie)
		}
		return r
	}

	cases := []struct {
		name    string
		r       *http.Request
		wantErr string // "" means the request must verify
	}{
		{name: "valid signed state verifies", r: newReq(raw, signedCookie(secret, raw)), wantErr: ""},
		{name: "missing cookie rejected", r: newReq(raw, nil), wantErr: "no cookie"},
		{
			name:    "malformed cookie rejected",
			r:       newReq(raw, &http.Cookie{Name: feishuStateCookie, Value: raw}),
			wantErr: "malformed state cookie",
		},
		{
			name:    "query state mismatch rejected",
			r:       newReq("aaaaaaaaaaaaaaaa", signedCookie(secret, raw)),
			wantErr: "state mismatch",
		},
		{
			name:    "missing query state rejected",
			r:       newReq("", signedCookie(secret, raw)),
			wantErr: "state mismatch",
		},
		{
			name: "tampered state value rejected",
			r: newReq("bbbbbbbbbbbbbbbb", &http.Cookie{
				// Cookie value swapped, signature still computed over raw.
				Name:  feishuStateCookie,
				Value: "bbbbbbbbbbbbbbbb" + "." + feishuStateSig(secret, raw),
			}),
			wantErr: "state signature invalid",
		},
		{name: "different secret rejected", r: newReq(raw, signedCookie("other-secret", raw)), wantErr: "state signature invalid"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := a.feishuStateVerify(c.r)
			if c.wantErr == "" {
				if err != nil {
					t.Fatalf("feishuStateVerify = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("feishuStateVerify = nil, want error %q", c.wantErr)
			}
			if c.wantErr != "no cookie" && err.Error() != c.wantErr {
				t.Errorf("error = %q, want %q", err.Error(), c.wantErr)
			}
		})
	}
}

// TestSanitizePathPart covers the rune mapping applied to usernames before
// they become on-disk directory names: [a-zA-Z0-9._-] survive, everything else
// (including path separators and unicode) becomes '-', blank input becomes
// "user". Note: '.' is in the allowlist, so ".." survives verbatim — the
// traversal defence at the call sites is the mandatory "-<id>" suffix, not
// this function.
func TestSanitizePathPart(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{name: "plain name", in: "alice", want: "alice"},
		{name: "allowed charset unchanged", in: "Alice_01-x.y", want: "Alice_01-x.y"},
		{name: "dot-dot kept verbatim", in: "..", want: ".."},
		{name: "traversal slashes become dashes", in: "../..", want: "..-.."},
		{name: "forward slash", in: "a/b", want: "a-b"},
		{name: "backslash", in: `a\b`, want: "a-b"},
		{name: "leading trailing dots kept", in: ".hidden.", want: ".hidden."},
		{name: "surrounding spaces trimmed", in: "  padded  ", want: "padded"},
		{name: "inner whitespace becomes dash", in: "a b\tc", want: "a-b-c"},
		{name: "empty becomes user", in: "", want: "user"},
		{name: "blank becomes user", in: "   ", want: "user"},
		{name: "unicode becomes dashes", in: "张三", want: "--"},
		{name: "emoji becomes dash", in: "emoji🙂x", want: "emoji-x"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := sanitizePathPart(c.in); got != c.want {
				t.Errorf("sanitizePathPart(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// TestSanitizeFilename pins the Content-Disposition filename sanitizer. It
// only rewrites '/' and ':' to '_'; '"' and '\' (and everything else) pass
// through verbatim, even though the result is interpolated inside a quoted
// filename="..." header value — a header-injection smell kept here as a pin of
// current behavior.
func TestSanitizeFilename(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{name: "plain name unchanged", in: "nginx", want: "nginx"},
		{name: "tag colon replaced", in: "alpine:latest", want: "alpine_latest"},
		{name: "registry slash replaced", in: "library/nginx", want: "library_nginx"},
		{name: "full ref rewritten", in: "ghcr.io/org/img:1.0", want: "ghcr.io_org_img_1.0"},
		{name: "quote passes through", in: `evil"name`, want: `evil"name`},
		{name: "backslash passes through", in: `a\b`, want: `a\b`},
		{name: "empty unchanged", in: "", want: ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := sanitizeFilename(c.in); got != c.want {
				t.Errorf("sanitizeFilename(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// TestIPSourceKind covers the intranet/extranet classification shown on the
// Security page. Unparseable input returns "". Note: the classification is a
// bare net.IP.IsGlobalUnicast check, which only excludes loopback/link-local/
// multicast/unspecified — it does NOT exclude RFC1918 private ranges, so
// 10/8, 172.16/12 and 192.168/16 are classified "extranet" even though the
// function's doc comment calls them intranet. Pinned as current behavior.
func TestIPSourceKind(t *testing.T) {
	cases := []struct {
		name, ip, want string
	}{
		{name: "rfc1918 class a classed extranet", ip: "10.0.0.5", want: "extranet"},
		{name: "rfc1918 172.16/12 classed extranet", ip: "172.16.3.4", want: "extranet"},
		{name: "rfc1918 172.31 classed extranet", ip: "172.31.255.255", want: "extranet"},
		{name: "172.32 is public", ip: "172.32.0.1", want: "extranet"},
		{name: "rfc1918 class c classed extranet", ip: "192.168.1.1", want: "extranet"},
		{name: "loopback", ip: "127.0.0.1", want: "intranet"},
		{name: "ipv6 loopback", ip: "::1", want: "intranet"},
		{name: "link local", ip: "169.254.1.1", want: "intranet"},
		{name: "public ipv4", ip: "8.8.8.8", want: "extranet"},
		{name: "public ipv6", ip: "2001:4860:4860::8888", want: "extranet"},
		{name: "empty", ip: "", want: ""},
		{name: "garbage", ip: "not-an-ip", want: ""},
		{name: "out of range octet", ip: "10.0.0.256", want: ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ipSourceKind(c.ip); got != c.want {
				t.Errorf("ipSourceKind(%q) = %q, want %q", c.ip, got, c.want)
			}
		})
	}
}

// TestPublicIPFromCookie covers the validation of the browser-reported WAN IP
// cookie (mudp_pubip): only a real IP literal is trusted, so a tampered cookie
// cannot smuggle an arbitrary string into the access log or a GeoIP URL.
func TestPublicIPFromCookie(t *testing.T) {
	cases := []struct {
		name, cookieValue string
		want              string
	}{
		{name: "valid ipv4", cookieValue: "203.0.113.7", want: "203.0.113.7"},
		{name: "valid ipv6", cookieValue: "2001:db8::1", want: "2001:db8::1"},
		{name: "script injection rejected", cookieValue: "<script>alert(1)</script>", want: ""},
		{name: "hostname rejected", cookieValue: "example.com", want: ""},
		{name: "malformed ip rejected", cookieValue: "1.2.3.4.5", want: ""},
		{name: "empty value rejected", cookieValue: "", want: ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "http://example.com/api/login", nil)
			r.AddCookie(&http.Cookie{Name: "mudp_pubip", Value: c.cookieValue})
			if got := publicIPFromCookie(r); got != c.want {
				t.Errorf("publicIPFromCookie(cookie=%q) = %q, want %q", c.cookieValue, got, c.want)
			}
		})
	}
	t.Run("no cookie", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "http://example.com/api/login", nil)
		if got := publicIPFromCookie(r); got != "" {
			t.Errorf("publicIPFromCookie(no cookie) = %q, want empty", got)
		}
	})
}

// TestDeviceFromRequest covers the CF-Device-Type allowlist: only
// desktop/mobile/tablet are taken from the Cloudflare edge; any other value
// falls through to the User-Agent parser.
func TestDeviceFromRequest(t *testing.T) {
	cases := []struct {
		name, cfDevice, ua, want string
	}{
		{name: "cf desktop", cfDevice: "desktop", want: "desktop"},
		{name: "cf mobile", cfDevice: "mobile", want: "mobile"},
		{name: "cf tablet", cfDevice: "tablet", want: "tablet"},
		{name: "cf value normalised", cfDevice: " Desktop ", want: "desktop"},
		{name: "cf bot rejected, ua fallback", cfDevice: "bot", ua: "curl/8.0.1", want: "bot"},
		{name: "cf bot rejected, empty ua", cfDevice: "bot", ua: "", want: "desktop"},
		{name: "no cf header, iphone ua", cfDevice: "", ua: "Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) Version/17.0 Mobile Safari/604.1", want: "mobile"},
		{name: "no cf header, empty ua", cfDevice: "", ua: "", want: "desktop"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
			if c.cfDevice != "" {
				r.Header.Set("CF-Device-Type", c.cfDevice)
			}
			if c.ua != "" {
				r.Header.Set("User-Agent", c.ua)
			}
			if got := deviceFromRequest(r); got != c.want {
				t.Errorf("deviceFromRequest(cf=%q, ua=%q) = %q, want %q", c.cfDevice, c.ua, got, c.want)
			}
		})
	}
}

// TestGeoFromCFHeaders covers the Cloudflare edge geo headers: when any geo
// header is present the struct is built from them (country upper-cased,
// coordinates parsed best-effort); when none are present a zero geoInfo
// signals "not behind Cloudflare".
func TestGeoFromCFHeaders(t *testing.T) {
	t.Run("no headers yields zero value", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
		if got := geoFromCFHeaders(r); got != (geoInfo{}) {
			t.Errorf("geoFromCFHeaders(no headers) = %+v, want zero geoInfo", got)
		}
	})
	t.Run("full header set", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
		r.Header.Set("CF-IPCountry", " jp ")
		r.Header.Set("CF-IPRegion", "Tokyo")
		r.Header.Set("CF-IPCity", "Shinjuku")
		r.Header.Set("CF-IPLatitude", "35.68950")
		r.Header.Set("CF-IPLongitude", "139.69170")
		r.Header.Set("CF-IPTimezone", "Asia/Tokyo")
		want := geoInfo{
			Country: "JP", CountryCode: "JP", Region: "Tokyo", City: "Shinjuku",
			Latitude: 35.6895, Longitude: 139.6917, Timezone: "Asia/Tokyo",
		}
		if got := geoFromCFHeaders(r); got != want {
			t.Errorf("geoFromCFHeaders = %+v, want %+v", got, want)
		}
	})
	t.Run("country alone is enough", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
		r.Header.Set("CF-IPCountry", "us")
		want := geoInfo{Country: "US", CountryCode: "US"}
		if got := geoFromCFHeaders(r); got != want {
			t.Errorf("geoFromCFHeaders = %+v, want %+v", got, want)
		}
	})
	t.Run("unparseable latitude becomes zero", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
		r.Header.Set("CF-IPLatitude", "abc")
		// The header's presence still marks the request as Cloudflare-fronted.
		if got := geoFromCFHeaders(r); got != (geoInfo{}) {
			t.Errorf("geoFromCFHeaders(bad lat) = %+v, want zero-field geoInfo", got)
		}
	})
}

// TestFprintfCSV pins the RFC 4180 quoting used by the access-log CSV export.
// Fields containing a comma, quote, CR or LF are quoted with internal quotes
// doubled. A formula-injection probe ("=cmd|...") is emitted verbatim —
// leading '=' is NOT neutralised — pinned here as current behavior.
func TestFprintfCSV(t *testing.T) {
	cases := []struct {
		name   string
		fields []string
		want   string
	}{
		{name: "plain fields", fields: []string{"a", "b", "c"}, want: "a,b,c\n"},
		{name: "empty fields", fields: []string{"", "", "x"}, want: ",,x\n"},
		{name: "comma quoted", fields: []string{"a,b"}, want: "\"a,b\"\n"},
		{name: "quote doubled", fields: []string{`say "hi"`, "x"}, want: "\"say \"\"hi\"\"\",x\n"},
		{name: "newline quoted", fields: []string{"line1\nline2"}, want: "\"line1\nline2\"\n"},
		{name: "carriage return quoted", fields: []string{"a\rb"}, want: "\"a\rb\"\n"},
		{name: "formula probe verbatim", fields: []string{"=cmd|'/c calc'!A1", "safe"}, want: "=cmd|'/c calc'!A1,safe\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var buf bytes.Buffer
			fprintfCSV(&buf, c.fields...)
			if got := buf.String(); got != c.want {
				t.Errorf("fprintfCSV(%q) = %q, want %q", c.fields, got, c.want)
			}
		})
	}
}

// TestCheckSharePassword covers the bcrypt guard on password-protected
// netdisk shares: the password is read from the ?password= query param first
// and the X-Share-Password header second, then compared with bcrypt.
func TestCheckSharePassword(t *testing.T) {
	hash, err := bcrypt.GenerateFromPassword([]byte("s3cret"), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("GenerateFromPassword: %v", err)
	}
	protected := store.NetdiskShare{HasPassword: true, PasswordHash: string(hash)}

	cases := []struct {
		name           string
		share          store.NetdiskShare
		queryPassword  string
		headerPassword string
		wantErr        string // "" means the password check must pass
	}{
		{name: "share without password passes", share: store.NetdiskShare{}, wantErr: ""},
		{name: "correct via query param", share: protected, queryPassword: "s3cret", wantErr: ""},
		{name: "correct via header", share: protected, headerPassword: "s3cret", wantErr: ""},
		{name: "query takes precedence over header", share: protected, queryPassword: "wrong", headerPassword: "s3cret", wantErr: "incorrect password"},
		{name: "wrong password rejected", share: protected, queryPassword: "wrong", wantErr: "incorrect password"},
		{name: "missing password rejected", share: protected, wantErr: "password required"},
		{name: "empty hash never matches", share: store.NetdiskShare{HasPassword: true}, queryPassword: "s3cret", wantErr: "incorrect password"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			target := "http://example.com/api/shares/token/dl"
			if c.queryPassword != "" {
				target += "?password=" + c.queryPassword
			}
			r := httptest.NewRequest(http.MethodGet, target, nil)
			if c.headerPassword != "" {
				r.Header.Set("X-Share-Password", c.headerPassword)
			}
			err := checkSharePassword(r, c.share)
			if c.wantErr == "" {
				if err != nil {
					t.Fatalf("checkSharePassword = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("checkSharePassword = nil, want error %q", c.wantErr)
			}
			if err.Error() != c.wantErr {
				t.Errorf("error = %q, want %q", err.Error(), c.wantErr)
			}
		})
	}
}
