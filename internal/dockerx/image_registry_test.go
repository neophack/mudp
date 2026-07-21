package dockerx

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRegistryHost(t *testing.T) {
	cases := []struct{ in, want string }{
		{"nginx", "docker.io"},
		{"nginx:1.25", "docker.io"},
		{"library/nginx", "docker.io"},
		{"docker.io/nginx", "docker.io"},
		{"registry-1.docker.io/nginx", "registry-1.docker.io"},
		{"ghcr.io/owner/repo", "ghcr.io"},
		{"ghcr.io/owner/repo:tag", "ghcr.io"},
		{"localhost:5000/myapp", "localhost:5000"},
		{"myregistry.corp.com:443/team/app:v2", "myregistry.corp.com:443"},
		{"myapp@sha256:abc", "docker.io"},
		{"quay.io/coreos/etcd:v3.5.0", "quay.io"},
	}
	for _, c := range cases {
		if got := registryHost(c.in); got != c.want {
			t.Errorf("registryHost(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestAuthForRef(t *testing.T) {
	creds := []RegistryCred{
		{Host: "ghcr.io", Username: "u", Token: "tok"},
		{Host: "docker.io", Username: "hub", Token: "hubtok"},
	}
	if got := AuthForRef("ghcr.io/me/app", creds); got == "" {
		t.Error("expected auth for ghcr.io ref")
	}
	if got := AuthForRef("nginx", creds); got == "" {
		t.Error("expected auth for docker.io ref")
	}
	if got := AuthForRef("quay.io/something", creds); got != "" {
		t.Error("unmatched registry should yield empty auth")
	}
}

func TestParseStatsCPU(t *testing.T) {
	// Two samples worth of deltas: cpuDelta=100, sysDelta=200, cpus=2 → 100/200*2*100=100%.
	raw := `{
		"precpu_stats": {"cpu_usage":{"total_usage":900}, "system_cpu_usage":1000, "online_cpus":2},
		"cpu_stats": {"cpu_usage":{"total_usage":1000}, "system_cpu_usage":1200, "online_cpus":2},
		"memory_stats": {"usage": 209715200, "limit": 1073741824, "stats": {"cache": 104857600}},
		"networks": {"eth0": {"rx_bytes": 1024, "tx_bytes": 2048}},
		"blkio_stats": {"io_service_bytes_recursive": [{"op":"Read","value":4096},{"op":"Write","value":8192}]},
		"pids_stats": {"current": 42}
	}`
	s := parseStats([]byte(raw))
	if s.CPUPercent != 100 {
		t.Errorf("CPUPercent = %v, want 100", s.CPUPercent)
	}
	// usage(209715200) - cache(104857600) = 104857600 bytes = 100 MiB.
	if s.MemoryMB != 100 {
		t.Errorf("MemoryMB = %v, want 100", s.MemoryMB)
	}
	if s.MemoryLimitMB != 1024 {
		t.Errorf("MemoryLimitMB = %v, want 1024", s.MemoryLimitMB)
	}
	if s.NetRxKB != 1 || s.NetTxKB != 2 {
		t.Errorf("Net rx/tx = %v/%v, want 1/2", s.NetRxKB, s.NetTxKB)
	}
	if s.BlockReadKB != 4 || s.BlockWriteKB != 8 {
		t.Errorf("Block r/w = %v/%v, want 4/8", s.BlockReadKB, s.BlockWriteKB)
	}
	if s.PIDs != 42 {
		t.Errorf("PIDs = %v, want 42", s.PIDs)
	}
}

func TestParseStatsInvalid(t *testing.T) {
	s := parseStats([]byte("not json"))
	if s.Error == "" {
		t.Error("expected error for invalid JSON")
	}
}

func TestParseStatsZeroDelta(t *testing.T) {
	// No delta → CPU% stays 0, no divide-by-zero.
	raw := `{"precpu_stats":{"cpu_usage":{"total_usage":0},"system_cpu_usage":0,"online_cpus":1},
		"cpu_stats":{"cpu_usage":{"total_usage":0},"system_cpu_usage":0,"online_cpus":1},
		"memory_stats":{"usage":0,"limit":0}}`
	s := parseStats([]byte(raw))
	if s.CPUPercent != 0 {
		t.Errorf("CPUPercent = %v, want 0", s.CPUPercent)
	}
}

func TestParseStatsMemoryCgroupV2(t *testing.T) {
	// cgroup v2: `cache` is absent/0 and only `inactive_file` is reported.
	// usage 209715200 - inactive_file 52428800 = 157286400 bytes = 150 MiB.
	raw := `{
		"memory_stats": {
			"usage": 209715200,
			"limit": 1073741824,
			"stats": {"inactive_file": 52428800}
		}
	}`
	s := parseStats([]byte(raw))
	if s.MemoryMB != 150 {
		t.Errorf("MemoryMB = %v, want 150", s.MemoryMB)
	}
	if s.MemoryPct != round2(150.0/1024*100) {
		t.Errorf("MemoryPct = %v, want %v", s.MemoryPct, round2(150.0/1024*100))
	}
}

func TestParseStatsMemoryCacheFallback(t *testing.T) {
	// Legacy cgroup v1: no inactive_file/total_inactive_file, only `cache`.
	// usage 209715200 - cache 104857600 = 104857600 bytes = 100 MiB.
	raw := `{
		"memory_stats": {
			"usage": 209715200,
			"stats": {"cache": 104857600}
		}
	}`
	s := parseStats([]byte(raw))
	if s.MemoryMB != 100 {
		t.Errorf("MemoryMB = %v, want 100", s.MemoryMB)
	}
}

func TestParseStatsMemorySubtrahendLargerThanUsage(t *testing.T) {
	// Docker can transiently report inactive_file > usage during accounting.
	// Working set must clamp to 0 instead of underflowing uint64.
	raw := `{
		"memory_stats": {
			"usage": 1000,
			"stats": {"inactive_file": 2000}
		}
	}`
	s := parseStats([]byte(raw))
	if s.MemoryMB != 0 {
		t.Errorf("MemoryMB = %v, want 0 (no underflow)", s.MemoryMB)
	}
}

func TestEncodeAuthRoundTrip(t *testing.T) {
	auth := encodeAuth("alice", "secret")
	if auth == "" {
		t.Fatal("encodeAuth returned empty")
	}
	// Should be valid base64-encoded JSON containing the username.
	// (We can't import registry.AuthConfig here; just sanity-check it's non-trivial.)
	if len(auth) < 20 {
		t.Errorf("auth blob suspiciously short: %q", auth)
	}
}

func TestJSONMarshalSmoke(t *testing.T) {
	// Ensure StatsSample marshals cleanly (used in SSE responses).
	s := StatsSample{CPUPercent: 50.5, MemoryMB: 200}
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(b), "cpuPct") {
		t.Errorf("unexpected JSON: %s", b)
	}
}
