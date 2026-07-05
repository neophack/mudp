package bootstrap

import (
	"archive/tar"
	"bytes"
	"fmt"
	"strings"
)

type Config struct {
	EnableSSH      bool
	EnableVSCode   bool
	AccessPassword string
	SSHScript      string
	VSCodeScript   string
	OrigEntrypoint []string
	OrigCmd        []string
	// BaseRef is the base image reference for the fused-image Dockerfile (FROM).
	// Only used by FusedContext; the runtime-injection Tarball ignores it.
	BaseRef string
}

func Tarball(cfg Config) (*bytes.Buffer, error) {
	script, err := entrypointScript(cfg)
	if err != nil {
		return nil, err
	}
	buf := &bytes.Buffer{}
	tw := tar.NewWriter(buf)
	files := []struct {
		Name string
		Body string
		Mode int64
	}{
		{Name: "mudp-bootstrap/entrypoint.sh", Body: script, Mode: 0755},
	}
	if cfg.EnableSSH {
		files = append(files, struct {
			Name string
			Body string
			Mode int64
		}{Name: "mudp-bootstrap/ssh.sh", Body: cfg.SSHScript, Mode: 0755})
	}
	if cfg.EnableVSCode {
		files = append(files, struct {
			Name string
			Body string
			Mode int64
		}{Name: "mudp-bootstrap/vscode.sh", Body: cfg.VSCodeScript, Mode: 0755})
	}
	for _, file := range files {
		hdr := &tar.Header{
			Name: file.Name,
			Mode: file.Mode,
			Size: int64(len(file.Body)),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return nil, err
		}
		if _, err := tw.Write([]byte(file.Body)); err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	return buf, nil
}

func entrypointScript(cfg Config) (string, error) {
	if (cfg.EnableSSH || cfg.EnableVSCode) && strings.TrimSpace(cfg.AccessPassword) == "" {
		return "", fmt.Errorf("access password is required when SSH or VS Code is enabled")
	}
	lines := []string{
		"#!/bin/sh",
		"set -eu",
		"",
		"export PATH=\"$PATH:/usr/sbin:/usr/local/bin:/usr/bin:/bin\"",
		"mkdir -p /var/run/sshd /tmp/mudp",
		"LOG_FILE=/tmp/mudp/bootstrap.log",
		"touch \"$LOG_FILE\"",
		"exec >>\"$LOG_FILE\" 2>&1",
		"echo \"=== MUDP bootstrap started $(date -u +%Y-%m-%dT%H:%M:%SZ) ===\"",
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
		"  echo \"No supported package manager found to install: $*\" >&2",
		"  return 1",
		"}",
		"",
	}
	if cfg.EnableSSH {
		lines = append(lines,
			fmt.Sprintf("export MUDP_ACCESS_PASSWORD=%s", shellSingleQuoted(cfg.AccessPassword)),
			"if [ -f /mudp-bootstrap/ssh.sh ]; then",
			"  /bin/sh /mudp-bootstrap/ssh.sh",
			"  touch /tmp/mudp/ssh.ready",
			"fi",
			"",
		)
	}
	if cfg.EnableVSCode {
		lines = append(lines,
			fmt.Sprintf("export MUDP_ACCESS_PASSWORD=%s", shellSingleQuoted(cfg.AccessPassword)),
			"if [ -f /mudp-bootstrap/vscode.sh ]; then",
			"  /bin/sh /mudp-bootstrap/vscode.sh",
			"  touch /tmp/mudp/vscode.ready",
			"fi",
			"",
		)
	}
	lines = append(lines,
		"if [ \"$#\" -gt 0 ]; then",
		"  exec \"$@\"",
		"fi",
	)
	lines = append(lines, "echo \"=== MUDP bootstrap completed $(date -u +%Y-%m-%dT%H:%M:%SZ) ===\"")
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
	lines = append(lines, "")
	return strings.Join(lines, "\n"), nil
}

func joinShell(parts []string) string {
	escaped := make([]string, 0, len(parts))
	for _, part := range parts {
		escaped = append(escaped, shellSingleQuoted(part))
	}
	return strings.Join(escaped, " ")
}

func shellSingleQuoted(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}
