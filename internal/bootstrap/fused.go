package bootstrap

import (
	"archive/tar"
	"bytes"
	"fmt"
	"strings"
)

// FusedContext builds a Docker build-context tar for a fused derived image.
// The resulting image pre-installs SSH/VSCode at build time (the slow, network
// steps) and runs only the fast, per-boot configuration at container start.
//
// The context contains:
//   - Dockerfile          (FROM base, COPY + RUN the build script, ENTRYPOINT the runtime script)
//   - mudp-bootstrap/fused-build.sh   (install-time: apt/apk/dnf/yum/zypper + code-server)
//   - mudp-bootstrap/fused-runtime.sh (boot-time: password, host keys, start daemons, markers)
//   - mudp-bootstrap/ssh.sh / vscode.sh (the admin-customized install bodies, sourced by build.sh)
//
// The base image's Entrypoint/Cmd are captured and replayed by the runtime
// script (the same passthrough logic as the runtime-injection entrypoint), so
// fused images behave like the base image after bootstrap.
func FusedContext(cfg Config) (*bytes.Buffer, error) {
	if (cfg.EnableSSH || cfg.EnableVSCode) && strings.TrimSpace(cfg.AccessPassword) == "" {
		return nil, fmt.Errorf("access password is required when SSH or VS Code is enabled")
	}
	dockerfile, err := fusedDockerfile(cfg)
	if err != nil {
		return nil, err
	}
	files := []struct {
		Name string
		Body string
	}{
		{Name: "Dockerfile", Body: dockerfile},
		{Name: "mudp-bootstrap/fused-build.sh", Body: fusedBuildScript(cfg)},
		{Name: "mudp-bootstrap/fused-runtime.sh", Body: fusedRuntimeScript(cfg)},
	}
	if cfg.EnableSSH {
		files = append(files, struct {
			Name string
			Body string
		}{Name: "mudp-bootstrap/ssh.sh", Body: cfg.SSHScript})
	}
	if cfg.EnableVSCode {
		files = append(files, struct {
			Name string
			Body string
		}{Name: "mudp-bootstrap/vscode.sh", Body: cfg.VSCodeScript})
	}
	buf := &bytes.Buffer{}
	tw := tar.NewWriter(buf)
	for _, f := range files {
		body := f.Body
		hdr := &tar.Header{Name: f.Name, Mode: 0755, Size: int64(len(body))}
		if err := tw.WriteHeader(hdr); err != nil {
			return nil, err
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	return buf, nil
}

// fusedDockerfile renders the Dockerfile for the fused image. The build runs
// the install script once; the resulting image's ENTRYPOINT is the runtime
// script which performs per-boot configuration. The placeholder password is
// required because the install scripts may reference $MUDP_ACCESS_PASSWORD
// even during install (e.g. seeding code-server config); the real per-container
// password is supplied at container start via the same env var.
func fusedDockerfile(cfg Config) (string, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "FROM %s\n", cfg.BaseRef)
	b.WriteString("COPY mudp-bootstrap/ /mudp-bootstrap/\n")
	b.WriteString("RUN MUDP_ACCESS_PASSWORD='mudp-build-placeholder' /bin/sh /mudp-bootstrap/fused-build.sh\n")
	b.WriteString(`ENTRYPOINT ["/bin/sh", "/mudp-bootstrap/fused-runtime.sh"]` + "\n")
	return b.String(), nil
}

// fusedBuildScript is the install-time script run during `docker build` (once,
// baked into the image layer). It performs only the slow, network-bound work:
// installing openssh-server, code-server, and base sshd_config. It deliberately
// skips setting a password or starting daemons — those are per-container and
// happen at runtime. The ssh.sh/vscode.sh bodies are sourced for the heavy
// lifting; this wrapper just ensures the environment is set up.
func fusedBuildScript(cfg Config) string {
	lines := []string{
		"#!/bin/sh",
		"set -eu",
		"",
		"export PATH=\"$PATH:/usr/sbin:/usr/local/bin:/usr/bin:/bin\"",
		"mkdir -p /var/run/sshd /tmp/mudp /root/.config/code-server",
		"",
		"have_cmd() { command -v \"$1\" >/dev/null 2>&1; }",
		"",
		"install_packages() {",
		"  if have_cmd apt-get; then",
		"    export DEBIAN_FRONTEND=noninteractive",
		"    apt-get update",
		"    apt-get install -y \"$@\"",
		"    return 0",
		"  fi",
		"  if have_cmd apk; then",
		"    apk add --no-cache \"$@\"",
		"    return 0",
		"  fi",
		"  if have_cmd dnf; then",
		"    dnf install -y \"$@\"",
		"    return 0",
		"  fi",
		"  if have_cmd yum; then",
		"    yum install -y \"$@\"",
		"    return 0",
		"  fi",
		"  if have_cmd zypper; then",
		"    zypper --non-interactive install \"$@\"",
		"    return 0",
		"  fi",
		"  echo \"No supported package manager found to install: $*\" >&2",
		"  return 1",
		"}",
		"",
		"echo \"=== MUDP fused build started $(date -u +%Y-%m-%dT%H:%M:%SZ) ===\"",
		// Signal to ssh.sh/vscode.sh that we are in the BUILD phase: they must
		// only install packages and write config, NOT start daemons or background
		// services (those would hang the build or be killed when RUN ends).
		"export MUDP_BUILD_PHASE=1",
	}
	if cfg.EnableSSH {
		// Run the admin-supplied SSH install body. The default script guards its
		// daemon-start lines with [ -z \"$MUDP_BUILD_PHASE\" ] so the build only
		// installs openssh-server + sshd_config + host keys, never starts sshd.
		lines = append(lines,
			"if [ -f /mudp-bootstrap/ssh.sh ]; then",
			"  /bin/sh /mudp-bootstrap/ssh.sh",
			"fi",
			"if command -v ssh-keygen >/dev/null 2>&1; then",
			"  ssh-keygen -A >/dev/null 2>&1 || true",
			"fi",
			"touch /tmp/mudp/ssh.installed",
			"",
		)
	}
	if cfg.EnableVSCode {
		// code-server install is the slowest step; baking it is the main win.
		// The default vscode.sh guards its `nohup code-server &` start with
		// [ -z \"$MUDP_BUILD_PHASE\" ] so the build installs the binary only.
		lines = append(lines,
			"if [ -f /mudp-bootstrap/vscode.sh ]; then",
			"  /bin/sh /mudp-bootstrap/vscode.sh",
			"fi",
			"touch /tmp/mudp/vscode.installed",
			"",
		)
	}
	lines = append(lines, "echo \"=== MUDP fused build completed $(date -u +%Y-%m-%dT%H:%M:%SZ) ===\"")
	return strings.Join(lines, "\n")
}

// fusedRuntimeScript is the container ENTRYPOINT of a fused image. It runs on
// every start, does NO network work, and is fast: set the per-container root
// password, ensure host keys exist, start sshd + code-server, drop the
// readiness markers (so the existing waitForReady probe sees the services up),
// then exec the base image's original entrypoint/cmd.
func fusedRuntimeScript(cfg Config) string {
	lines := []string{
		"#!/bin/sh",
		"set -eu",
		"",
		"export PATH=\"$PATH:/usr/sbin:/usr/local/bin:/usr/bin:/bin\"",
		"mkdir -p /var/run/sshd /tmp/mudp",
		"LOG_FILE=/tmp/mudp/bootstrap.log",
		"touch \"$LOG_FILE\"",
		"exec >>\"$LOG_FILE\" 2>&1",
		"echo \"=== MUDP fused runtime started $(date -u +%Y-%m-%dT%H:%M:%SZ) ===\"",
		"",
	}
	if cfg.EnableSSH {
		lines = append(lines,
			"if [ -n \"${MUDP_ACCESS_PASSWORD:-}\" ]; then",
			"  printf '%%s\\n' \"root:${MUDP_ACCESS_PASSWORD}\" | chpasswd 2>/dev/null || true",
			"fi",
			"command -v ssh-keygen >/dev/null 2>&1 && ssh-keygen -A >/dev/null 2>&1 || true",
			"if command -v service >/dev/null 2>&1; then service ssh start 2>/dev/null || true; fi",
			"if command -v sshd >/dev/null 2>&1; then",
			"  pgrep -x sshd >/dev/null 2>&1 || (/usr/sbin/sshd 2>/dev/null || sshd 2>/dev/null || true)",
			"fi",
			"touch /tmp/mudp/ssh.ready",
			"",
		)
	}
	if cfg.EnableVSCode {
		// Re-apply the per-container password into code-server's config (the
		// binary is already installed in the image), then (re)start it.
		lines = append(lines,
			"if [ -n \"${MUDP_ACCESS_PASSWORD:-}\" ] && command -v code-server >/dev/null 2>&1; then",
			"  mkdir -p /root/.config/code-server",
			"  cat > /root/.config/code-server/config.yaml <<EOF",
			"bind-addr: 0.0.0.0:13337",
			"auth: password",
			"password: ${MUDP_ACCESS_PASSWORD}",
			"cert: false",
			"EOF",
			"  pgrep -x code-server >/dev/null 2>&1 || (mkdir -p /workspace && nohup code-server /workspace >/tmp/mudp/vscode.log 2>&1 &) || true",
			"fi",
			"touch /tmp/mudp/vscode.ready",
			"",
		)
	}
	// Entrypoint/cmd passthrough: identical to the runtime-injection entrypoint.
	lines = append(lines,
		"if [ \"$#\" -gt 0 ]; then",
		"  exec \"$@\"",
		"fi",
	)
	lines = append(lines, "echo \"=== MUDP fused runtime completed $(date -u +%Y-%m-%dT%H:%M:%SZ) ===\"")
	if len(cfg.OrigEntrypoint) > 0 {
		lines = append(lines, "set -- "+joinShell(cfg.OrigEntrypoint))
		if len(cfg.OrigCmd) > 0 {
			lines = append(lines, "set -- \"$@\" "+joinShell(cfg.OrigCmd))
		}
		lines = append(lines, "if [ \"$#\" -eq 1 ] && { [ \"$1\" = \"/bin/bash\" ] || [ \"$1\" = \"bash\" ] || [ \"$1\" = \"/bin/sh\" ] || [ \"$1\" = \"sh\" ]; }; then")
		lines = append(lines, "  exec tail -f /dev/null")
		lines = append(lines, "fi")
		lines = append(lines, "exec \"$@\"")
	} else if len(cfg.OrigCmd) > 0 {
		lines = append(lines, "set -- "+joinShell(cfg.OrigCmd))
		lines = append(lines, "if [ \"$#\" -eq 1 ] && { [ \"$1\" = \"/bin/bash\" ] || [ \"$1\" = \"bash\" ] || [ \"$1\" = \"/bin/sh\" ] || [ \"$1\" = \"sh\" ]; }; then")
		lines = append(lines, "  exec tail -f /dev/null")
		lines = append(lines, "fi")
		lines = append(lines, "exec \"$@\"")
	} else {
		lines = append(lines, "exec tail -f /dev/null")
	}
	return strings.Join(lines, "\n") + "\n"
}
