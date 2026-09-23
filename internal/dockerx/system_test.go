package dockerx

import (
	"context"
	"testing"
	"time"

	"github.com/docker/docker/api/types/image"
)

// infoCtx bounds daemon calls so a wedged daemon (pipe ping answers but the
// engine API never does) fails fast instead of hanging the test binary.
func infoCtx(t *testing.T) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 10*time.Second)
}

// skipIfNoDocker skips the test when the Docker daemon is not reachable. Tests
// that touch a real engine are tagged this way so CI without Docker still
// passes; pure logic tests run unconditionally.
func skipIfNoDocker(t *testing.T) *Client {
	t.Helper()
	c, err := New()
	if err != nil {
		t.Skipf("docker client unavailable: %v", err)
	}
	ctx, cancel := infoCtx(t)
	defer cancel()
	if err := c.DockerPing(ctx); err != nil {
		t.Skipf("docker daemon not reachable: %v", err)
	}
	return c
}

func TestSystemInfoShape(t *testing.T) {
	c := skipIfNoDocker(t)
	ctx, cancel := infoCtx(t)
	defer cancel()
	info := c.SystemInfo(ctx)
	if !info.Healthy {
		t.Skipf("daemon reported unhealthy: %s", info.HealthyMsg)
	}
	if info.DockerVer == "" {
		t.Error("DockerVer empty on healthy daemon")
	}
	if info.Containers.Total < info.Containers.Running {
		t.Errorf("total %d < running %d", info.Containers.Total, info.Containers.Running)
	}
	if info.Images.Count < 0 || info.Volumes.Count < 0 || info.Networks < 0 {
		t.Error("negative resource counts")
	}
}

// TestSystemInfoForUserShape exercises the per-user scoped snapshot. It only
// asserts invariants (non-negative counts, total >= running, host fields
// populated) since the exact counts depend on who owns what on the live host.
func TestSystemInfoForUserShape(t *testing.T) {
	c := skipIfNoDocker(t)
	ctx, cancel := infoCtx(t)
	defer cancel()
	info := c.SystemInfoForUser(ctx, "nobody-owns-this-name")
	if !info.Healthy {
		t.Skipf("daemon reported unhealthy: %s", info.HealthyMsg)
	}
	// Host fields are always populated regardless of scope.
	if info.DockerVer == "" {
		t.Error("DockerVer empty on healthy daemon")
	}
	if info.Containers.Total < info.Containers.Running {
		t.Errorf("total %d < running %d", info.Containers.Total, info.Containers.Running)
	}
	if info.Images.Count < 0 || info.Volumes.Count < 0 || info.Networks < 0 {
		t.Error("negative resource counts")
	}
	// A user that owns nothing should still see the built-in system networks
	// (bridge, host, none) but none of their own managed resources.
	if info.Networks < 3 {
		t.Errorf("expected at least 3 system networks, got %d", info.Networks)
	}
}

// TestSystemInfoForUserEmptyUsernameFallback ensures an empty username keeps
// the platform-wide semantics (no panic, no owner filtering), matching
// SystemInfo.
func TestSystemInfoForUserEmptyUsernameFallback(t *testing.T) {
	c := skipIfNoDocker(t)
	ctx, cancel := infoCtx(t)
	defer cancel()
	info := c.SystemInfoForUser(ctx, "")
	if !info.Healthy {
		t.Skipf("daemon reported unhealthy: %s", info.HealthyMsg)
	}
	// Empty username == platform-wide, so SystemInfo and the fallback must agree.
	want := c.SystemInfo(ctx)
	if info.Containers.Total != want.Containers.Total ||
		info.Images.Count != want.Images.Count ||
		info.Volumes.Count != want.Volumes.Count ||
		info.Networks != want.Networks {
		t.Error("SystemInfoForUser(\"\") disagrees with SystemInfo()")
	}
}

func TestRound2(t *testing.T) {
	cases := []struct{ in, want float64 }{
		{0, 0},
		{1.005, 1.01},
		{2.555, 2.56},
		{1234.567, 1234.57},
	}
	for _, c := range cases {
		if got := round2(c.in); got != c.want {
			t.Errorf("round2(%v) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestHostname(t *testing.T) {
	h := hostname()
	if h == "" {
		t.Error("hostname() returned empty string")
	}
}

// TestIsDerivedImage covers the dashboard image filter: internal fused runtime
// images must be excluded by both their labels and their legacy tag prefixes,
// while real user-facing base images are kept.
func TestIsDerivedImage(t *testing.T) {
	cases := []struct {
		name string
		im   image.Summary
		want bool
	}{
		{
			name: "fused label",
			im:   image.Summary{Labels: map[string]string{"mudp.fused": "true"}},
			want: true,
		},
		{
			name: "fused layer label",
			im:   image.Summary{Labels: map[string]string{"mudp.fused.layer": "true"}},
			want: true,
		},
		{
			name: "fused tag",
			im:   image.Summary{RepoTags: []string{"mudp-fused-ubuntu-3f9a:latest"}},
			want: true,
		},
		{
			name: "fused validate tag",
			im:   image.Summary{RepoTags: []string{"mudp-fused-validate-abc1:latest"}},
			want: true,
		},
		{
			name: "real base image kept",
			im:   image.Summary{RepoTags: []string{"mudp-ubuntu:latest"}, Labels: map[string]string{"mudp.managed": "true"}},
			want: false,
		},
		{
			name: "unmanaged external image kept",
			im:   image.Summary{RepoTags: []string{"nginx:latest"}},
			want: false,
		},
		{
			name: "no tags no labels kept",
			im:   image.Summary{},
			want: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isDerivedImage(c.im); got != c.want {
				t.Errorf("isDerivedImage(%+v) = %v, want %v", c.im, got, c.want)
			}
		})
	}
}
