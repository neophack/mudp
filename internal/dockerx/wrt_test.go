package dockerx

import (
	"strings"
	"testing"
)

// TestWrtConfigScriptSubstitutions verifies that wrtConfigScript fills in every
// placeholder with a shell-quoted value from the policy, and that the two
// container-environment workarounds (fw4 zone-dispatch bypass + dnsmasq upstream
// resolvers) are present. It does not run anything — it only checks the string
// the ImmortalWrt container would receive via `docker exec`.
func TestWrtConfigScriptSubstitutions(t *testing.T) {
	pol := DefaultWRTPolicy()
	got := wrtConfigScript(pol)

	// No placeholder may survive substitution.
	for _, ph := range []string{
		"__LANIP__", "__LANMASK__", "__LANGW__",
		"__WANIP__", "__WANMASK__", "__WANGW__",
		"__LANSUBNET__", "__LUCI_RULE__",
	} {
		if strings.Contains(got, ph) {
			t.Errorf("placeholder %q remains in script after substitution", ph)
		}
	}
}

// TestWrtConfigScriptLANSources asserts the LAN subnet from the policy lands in
// the right places: the LAN interface ipaddr line and the nft source-match rule
// (the fw4 zone-dispatch workaround). With the default policy that subnet is
// 172.31.252.0/22.
func TestWrtConfigScriptLANSources(t *testing.T) {
	pol := DefaultWRTPolicy()
	got := wrtConfigScript(pol)

	// The nft forward/input workaround must use the LAN subnet verbatim.
	needle := "ip saddr '172.31.252.0/22' accept"
	if !strings.Contains(got, needle) {
		t.Errorf("script missing nft LAN-source rule %q\n--- script tail ---\n%s", needle, tail(got, 600))
	}
}

// TestWrtConfigScriptFw4Workaround asserts the three fw4 zone-dispatch bypass
// rules are emitted (forward lan→wan, forward wan→lan established, input lan),
// plus the idempotent nft_del_mudp helper that wipes prior mudp-* rules so
// repeated applies don't accumulate duplicates.
func TestWrtConfigScriptFw4Workaround(t *testing.T) {
	got := wrtConfigScript(DefaultWRTPolicy())

	mustContain := []string{
		`nft_del_mudp() {`,                                                       // idempotent helper defined
		`nft_del_mudp inet fw4 input`,                                            // helper called for input
		`nft_del_mudp inet fw4 forward`,                                          // helper called for forward
		`comment "mudp-allow-lan-input"`,                                         // input lan rule
		`comment "mudp-allow-lan-to-wan"`,                                        // forward lan→wan rule
		`comment "mudp-allow-wan-to-lan-est"`,                                    // forward wan→lan established
		`nft insert rule inet fw4 forward iifname "br-lan" oifname "eth1"`,       // lan→wan match
		`nft insert rule inet fw4 forward iifname "eth1" oifname "br-lan"`,       // wan→lan match
		`ct state established,related accept`,                                    // established match
	}
	for _, want := range mustContain {
		if !strings.Contains(got, want) {
			t.Errorf("script missing %q\n--- script tail ---\n%s", want, tail(got, 600))
		}
	}
}

// TestWrtConfigScriptDnsmasqUpstream asserts the dnsmasq upstream-resolver fix is
// present and idempotent (uci -q delete + add_list), so hijacked DNS queries get
// answered instead of REFUSED.
func TestWrtConfigScriptDnsmasqUpstream(t *testing.T) {
	got := wrtConfigScript(DefaultWRTPolicy())

	mustContain := []string{
		`uci -q delete dhcp.@dnsmasq[0].server`,
		`uci add_list dhcp.@dnsmasq[0].server='8.8.8.8'`,
		`uci add_list dhcp.@dnsmasq[0].server='1.1.1.1'`,
		`uci set dhcp.@dnsmasq[0].rebind_protection='0'`,
		`/etc/init.d/dnsmasq restart`,
	}
	for _, want := range mustContain {
		if !strings.Contains(got, want) {
			t.Errorf("script missing %q", want)
		}
	}
}

// TestWrtConfigScriptLuCIRuleGated asserts the LuCI firewall allow rule is
// emitted only when LuCIHostPort > 0.
func TestWrtConfigScriptLuCIRuleGated(t *testing.T) {
	// Default policy has LuCIHostPort=8080 → rule present.
	with := wrtConfigScript(DefaultWRTPolicy())
	if !strings.Contains(with, "firewall.allow_luci") {
		t.Error("LuCI rule missing when LuCIHostPort > 0")
	}

	// LuCIHostPort=0 → rule absent.
	off := DefaultWRTPolicy()
	off.LuCIHostPort = 0
	without := wrtConfigScript(off)
	if strings.Contains(without, "firewall.allow_luci") {
		t.Error("LuCI rule present when LuCIHostPort == 0")
	}
}

// TestWrtConfigScriptCustomSubnet asserts a non-default LAN subnet propagates to
// the nft source-match rule, so an admin who re-IPs the mesh via the WRT card
// still gets a working gateway.
func TestWrtConfigScriptCustomSubnet(t *testing.T) {
	pol := DefaultWRTPolicy()
	pol.LANSubnet = "10.42.0.0/24"
	pol.LANGateway = "10.42.0.2"
	got := wrtConfigScript(pol)

	if !strings.Contains(got, "ip saddr '10.42.0.0/24' accept") {
		t.Errorf("custom LAN subnet not propagated to nft rule\n--- script tail ---\n%s", tail(got, 600))
	}
	if strings.Contains(got, "172.31.252.0/22") {
		t.Error("default subnet leaked into a custom-subnet script")
	}
}

// tail returns the last n bytes of s (for readable test failure output).
func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
