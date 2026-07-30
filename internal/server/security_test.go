package server

import "testing"

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
