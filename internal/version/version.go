// Package version exposes the build identifier shown in the console's help
// page. Version is overridden at build time via -ldflags "-X mudp/internal/version.Version=<short-sha>";
// unset builds (go run, go build without ldflags) fall back to "dev".
package version

var Version = "dev"
