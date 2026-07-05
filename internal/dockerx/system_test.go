package dockerx

import (
	"context"
	"testing"
)

// skipIfNoDocker skips the test when the Docker daemon is not reachable. Tests
// that touch a real engine are tagged this way so CI without Docker still
// passes; pure logic tests run unconditionally.
func skipIfNoDocker(t *testing.T) *Client {
	t.Helper()
	c, err := New()
	if err != nil {
		t.Skipf("docker client unavailable: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := c.DockerPing(ctx); err != nil {
		t.Skipf("docker daemon not reachable: %v", err)
	}
	return c
}

func TestSystemInfoShape(t *testing.T) {
	c := skipIfNoDocker(t)
	info := c.SystemInfo(context.Background())
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
