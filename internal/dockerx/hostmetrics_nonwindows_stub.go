//go:build !windows

package dockerx

// readHostMetricsWindows is a build-compatible stub for non-Windows targets.
func readHostMetricsWindows() HostMetrics {
	return HostMetrics{}
}
