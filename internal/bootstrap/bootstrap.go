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
	// ConnectionUser is the login account SSH + code-server target (resolved from
	// the base image's USER). Empty means root. Exported into the entrypoint as
	// MUDP_CONNECTION_USER so the sourced ssh.sh/vscode.sh default scripts pick
	// it up.
	ConnectionUser string
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
			fmt.Sprintf("export MUDP_CONNECTION_USER=%s", shellSingleQuoted(connectionUserOrDefault(cfg.ConnectionUser))),
			"if [ -f /mudp-bootstrap/ssh.sh ]; then",
			"  /bin/sh /mudp-bootstrap/ssh.sh || true",
			"  touch /tmp/mudp/ssh.ready",
			"fi",
			"",
		)
	}
	if cfg.EnableVSCode {
		lines = append(lines,
			fmt.Sprintf("export MUDP_ACCESS_PASSWORD=%s", shellSingleQuoted(cfg.AccessPassword)),
			fmt.Sprintf("export MUDP_CONNECTION_USER=%s", shellSingleQuoted(connectionUserOrDefault(cfg.ConnectionUser))),
			"if [ -f /mudp-bootstrap/vscode.sh ]; then",
			"  /bin/sh /mudp-bootstrap/vscode.sh || true",
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

// connectionUserOrDefault returns the connection user, falling back to "root".
func connectionUserOrDefault(u string) string {
	if strings.TrimSpace(u) == "" {
		return "root"
	}
	return u
}

// UserResolveSnippet is a POSIX shell preamble that derives two env vars used by
// every SSH/VSCode bootstrap script:
//
//	MUDP_CONNECTION_USER  the login account SSH + code-server target (default root)
//	MUDP_HOME             that account's home directory
//
// The container always runs the bootstrap as root (CreateContainer forces
// containerCfg.User=root when SSH/VSCode is enabled), so this snippet can create
// the account when it does not yet exist — which happens when the image declared
// a bare numeric UID (resolved to the fixed "mudp" account) that has no passwd
// entry. Both vars fall back to root-safe values so the script works unchanged
// for root images.
func UserResolveSnippet() string {
	return `# --- mudp: resolve the connection user + home (runs as root) ---
MUDP_CONNECTION_USER="${MUDP_CONNECTION_USER:-root}"
if [ "$MUDP_CONNECTION_USER" = "root" ] || [ -z "$MUDP_CONNECTION_USER" ]; then
  MUDP_CONNECTION_USER="root"
  MUDP_HOME="/root"
else
  # Ensure the account exists (images with a bare UID USER resolve to "mudp",
  # which has no passwd entry until we create it here).
  if ! id "$MUDP_CONNECTION_USER" >/dev/null 2>&1; then
    if command -v useradd >/dev/null 2>&1; then
      useradd -m -s "$(command -v bash >/dev/null 2>&1 && echo /bin/bash || echo /bin/sh)" "$MUDP_CONNECTION_USER" 2>/dev/null || true
    elif command -v adduser >/dev/null 2>&1; then
      if command -v adduser >/dev/null 2>&1 && adduser 2>&1 | grep -q -- '--disabled-password'; then
        adduser --disabled-password --gecos "" "$MUDP_CONNECTION_USER" 2>/dev/null || true
      else
        adduser "$MUDP_CONNECTION_USER" 2>/dev/null || true
      fi
    fi
  fi
  MUDP_HOME="$(getent passwd "$MUDP_CONNECTION_USER" 2>/dev/null | cut -d: -f6)"
  if [ -z "$MUDP_HOME" ] || [ "$MUDP_HOME" = "/" ]; then
    MUDP_HOME="/home/$MUDP_CONNECTION_USER"
    mkdir -p "$MUDP_HOME"
    chown "$MUDP_CONNECTION_USER" "$MUDP_HOME" 2>/dev/null || true
  fi
fi
export MUDP_CONNECTION_USER MUDP_HOME
# --- mudp: end user resolve ---
`
}

func shellSingleQuoted(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}
