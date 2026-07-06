package dockerx

import (
	"testing"

	"github.com/docker/docker/api/types/mount"
)

func TestParseMount(t *testing.T) {
	cases := []struct {
		spec     string
		typ      mount.Type
		source   string
		target   string
		readonly bool
	}{
		{"/host/data:/data", mount.TypeBind, "/host/data", "/data", false},
		{"./code:/workspace:ro", mount.TypeBind, "./code", "/workspace", true},
		{"myvol:/var/lib/postgresql/data", mount.TypeVolume, "myvol", "/var/lib/postgresql/data", false},
		{"myvol:/data:ro", mount.TypeVolume, "myvol", "/data", true},
	}
	for _, c := range cases {
		m, err := parseMount(c.spec)
		if err != nil {
			t.Errorf("parseMount(%q): %v", c.spec, err)
			continue
		}
		if m.Type != c.typ || m.Source != c.source || m.Target != c.target || m.ReadOnly != c.readonly {
			t.Errorf("parseMount(%q) = %+v, want type=%v source=%v target=%v ro=%v",
				c.spec, m, c.typ, c.source, c.target, c.readonly)
		}
	}
}

func TestParseMountErrors(t *testing.T) {
	for _, bad := range []string{"", "noseparator", ":onlycolon", "::", "src:", ":target", "a:b:c:d"} {
		if _, err := parseMount(bad); err == nil {
			t.Errorf("parseMount(%q) expected error", bad)
		}
	}
}

func TestNetworkingConfig(t *testing.T) {
	cfg := networkingConfig([]string{"frontend", "backend", "", "  "})
	if len(cfg.EndpointsConfig) != 2 {
		t.Errorf("got %d endpoints, want 2", len(cfg.EndpointsConfig))
	}
	if _, ok := cfg.EndpointsConfig["frontend"]; !ok {
		t.Error("missing frontend endpoint")
	}
	if _, ok := cfg.EndpointsConfig["backend"]; !ok {
		t.Error("missing backend endpoint")
	}
}

func TestNetworkingConfigEmpty(t *testing.T) {
	cfg := networkingConfig(nil)
	if len(cfg.EndpointsConfig) != 0 {
		t.Errorf("nil networks should yield empty config, got %d", len(cfg.EndpointsConfig))
	}
}

// TestParseDevice covers the --device spec parser used to pass host devices
// (NVIDIA device nodes, USB, serial) through to containers.
func TestParseDevice(t *testing.T) {
	cases := []struct {
		spec      string
		host      string
		container string
		perms     string
	}{
		{"/dev/nvidia0", "/dev/nvidia0", "/dev/nvidia0", "rwm"},
		{"/dev/foo:/dev/bar", "/dev/foo", "/dev/bar", "rwm"},
		{"/dev/foo:/dev/bar:r", "/dev/foo", "/dev/bar", "r"},
		{"/dev/nvidia0:/dev/nvidia0:rwm", "/dev/nvidia0", "/dev/nvidia0", "rwm"},
	}
	for _, c := range cases {
		dm, err := parseDevice(c.spec)
		if err != nil {
			t.Errorf("parseDevice(%q): %v", c.spec, err)
			continue
		}
		if dm.PathOnHost != c.host || dm.PathInContainer != c.container || dm.CgroupPermissions != c.perms {
			t.Errorf("parseDevice(%q) = %+v, want host=%v container=%v perms=%v",
				c.spec, dm, c.host, c.container, c.perms)
		}
	}
}

// TestParseDeviceErrors ensures malformed device specs are rejected, not silently
// passed to the Docker API where they would surface as confusing daemon errors.
func TestParseDeviceErrors(t *testing.T) {
	for _, bad := range []string{"", ":a", "a:b:c:d"} {
		if _, err := parseDevice(bad); err == nil {
			t.Errorf("parseDevice(%q) expected error", bad)
		}
	}
}
