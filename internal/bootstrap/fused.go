package bootstrap

import (
	"archive/tar"
	"bytes"
	"fmt"
	"strings"
)

// FusedContext builds a Docker build-context tar for the final fused derived
// image. It uses multi-stage builds to merge independent SSH/VSCode layer
// images into the base image, so each layer is only built once and can be
// reused across final combinations (ssh-only, vscode-only, both).
//
// The resulting context contains:
//   - Dockerfile          (FROM base + COPY --from=<layer> /mudp-layer-root/ / + ENTRYPOINT)
//   - mudp-bootstrap/fused-runtime.sh (boot-time entrypoint)
func FusedContext(cfg Config, sshLayerRef, vscodeLayerRef string) (*bytes.Buffer, error) {
	if (cfg.EnableSSH || cfg.EnableVSCode) && strings.TrimSpace(cfg.AccessPassword) == "" {
		return nil, fmt.Errorf("access password is required when SSH or VS Code is enabled")
	}
	dockerfile, err := fusedDockerfile(cfg, sshLayerRef, vscodeLayerRef)
	if err != nil {
		return nil, err
	}
	files := []struct {
		Name string
		Body string
	}{
		{Name: "Dockerfile", Body: dockerfile},
		{Name: "mudp-bootstrap/fused-runtime.sh", Body: fusedRuntimeScript(cfg)},
	}
	return tarballFromFiles(files)
}

// LayerContext builds a Docker build-context tar for a single-service layer
// image. A layer image is not runnable on its own; it only exposes the file
// system changes produced by installing SSH or VSCode under /mudp-layer-root.
// The service argument must be "ssh" or "vscode".
func LayerContext(cfg Config, service string) (*bytes.Buffer, error) {
	service = strings.ToLower(strings.TrimSpace(service))
	if service != "ssh" && service != "vscode" {
		return nil, fmt.Errorf("layer service must be ssh or vscode, got %q", service)
	}
	layerCfg := cfg
	layerCfg.EnableSSH = service == "ssh"
	layerCfg.EnableVSCode = service == "vscode"
	if strings.TrimSpace(layerCfg.AccessPassword) == "" {
		return nil, fmt.Errorf("access password is required when building a layer")
	}

	dockerfile, err := layerDockerfile(layerCfg)
	if err != nil {
		return nil, err
	}
	files := []struct {
		Name string
		Body string
	}{
		{Name: "Dockerfile", Body: dockerfile},
		{Name: "mudp-bootstrap/fused-build.sh", Body: fusedBuildScript(layerCfg)},
		{Name: "mudp-offline-packages/.keep", Body: ""},
	}
	if layerCfg.EnableSSH {
		files = append(files, struct {
			Name string
			Body string
		}{Name: "mudp-bootstrap/ssh.sh", Body: cfg.SSHScript})
	}
	if layerCfg.EnableVSCode {
		files = append(files, struct {
			Name string
			Body string
		}{Name: "mudp-bootstrap/vscode.sh", Body: cfg.VSCodeScript})
	}
	for _, pkg := range cfg.OfflinePackages {
		name := strings.Trim(strings.ReplaceAll(pkg.Name, "\\", "/"), "/")
		if name == "" || strings.HasPrefix(name, "..") || strings.Contains(name, "/../") {
			return nil, fmt.Errorf("invalid offline package name %q", pkg.Name)
		}
		files = append(files, struct {
			Name string
			Body string
		}{Name: "mudp-offline-packages/" + name, Body: string(pkg.Body)})
	}
	return tarballFromFiles(files)
}

// fusedDockerfile renders the multi-stage Dockerfile for the final fused image.
// It starts from the base image, copies file-system deltas from any enabled
// layer images, installs the runtime entrypoint, and sets it as ENTRYPOINT.
//
// Layer copy order matters: later COPYs overwrite earlier ones for files that
// exist in both. The SSH layer modifies base system files (/etc/ssh/sshd_config),
// so it is copied last when both services are enabled so those modifications
// survive.
func fusedDockerfile(cfg Config, sshLayerRef, vscodeLayerRef string) (string, error) {
	if cfg.EnableSSH && sshLayerRef == "" {
		return "", fmt.Errorf("ssh layer reference is required when SSH is enabled")
	}
	if cfg.EnableVSCode && vscodeLayerRef == "" {
		return "", fmt.Errorf("vscode layer reference is required when VS Code is enabled")
	}
	var b strings.Builder
	fmt.Fprintf(&b, "FROM %s\n", cfg.BaseRef)
	if cfg.EnableVSCode {
		fmt.Fprintf(&b, "COPY --from=%s /mudp-layer-root/ /\n", vscodeLayerRef)
	}
	if cfg.EnableSSH {
		fmt.Fprintf(&b, "COPY --from=%s /mudp-layer-root/ /\n", sshLayerRef)
	}
	b.WriteString("COPY mudp-bootstrap/fused-runtime.sh /mudp-bootstrap/fused-runtime.sh\n")
	b.WriteString(`ENTRYPOINT ["/bin/sh", "/mudp-bootstrap/fused-runtime.sh"]` + "\n")
	return b.String(), nil
}

// layerDockerfile renders the Dockerfile for a single-service layer image.
// The layer has no ENTRYPOINT because it is only used as a source for
// COPY --from in the final fused image. Its final stage is scratch and contains
// only /mudp-layer-root, the exported delta from the install stage.
func layerDockerfile(cfg Config) (string, error) {
	if !cfg.EnableSSH && !cfg.EnableVSCode {
		return "", fmt.Errorf("a layer must enable exactly one service")
	}
	if cfg.EnableSSH && cfg.EnableVSCode {
		return "", fmt.Errorf("a layer must enable exactly one service, not both")
	}
	var b strings.Builder
	fmt.Fprintf(&b, "FROM %s AS build\n", cfg.BaseRef)
	// Force the build stage to run as root: the bootstrap installs packages via
	// apt/apk/dnf, writes /etc/ssh/sshd_config, runs ssh-keygen, and snapshots
	// the filesystem. If the base image declares a non-root USER these operations
	// would fail. The final stage is scratch (only /mudp-layer-root), so this USER
	// directive never leaks into a runnable image. The original image's USER is
	// honored at runtime: CreateContainer runs the fused image as root and the runtime
	// entrypoint drops privileges to MUDP_CONNECTION_USER (the image's declared
	// account), preserving the image's intended execution context.
	b.WriteString("USER root\n")
	b.WriteString("COPY mudp-bootstrap/ /mudp-bootstrap/\n")
	b.WriteString("COPY mudp-offline-packages/ /mudp-offline-packages/\n")
	b.WriteString("RUN MUDP_ACCESS_PASSWORD='mudp-build-placeholder' /bin/sh /mudp-bootstrap/fused-build.sh\n")
	b.WriteString("FROM scratch\n")
	b.WriteString("COPY --from=build /mudp-layer-root/ /mudp-layer-root/\n")
	return b.String(), nil
}

// fusedBuildScript is the install-time script run during `docker build` of a
// layer image. It performs only the slow, network-bound work: installing
// openssh-server or code-server and base sshd_config. It deliberately skips
// setting a password or starting daemons — those are per-container and happen
// at runtime. The ssh.sh/vscode.sh bodies are sourced for the heavy lifting;
// this wrapper just ensures the environment is set up.
func fusedBuildScript(cfg Config) string {
	lines := []string{
		"#!/bin/sh",
		"set -eu",
		"",
		"export PATH=\"$PATH:/usr/sbin:/usr/local/bin:/usr/bin:/bin\"",
		"export MUDP_OFFLINE_PACKAGE_DIR=/mudp-offline-packages",
		"mkdir -p /var/run/sshd /tmp/mudp",
		"",
		"have_cmd() { command -v \"$1\" >/dev/null 2>&1; }",
		"",
		"have_gnu_tar_incremental() { tar --help 2>/dev/null | grep -q -- '--listed-incremental'; }",
		"",
		"snapshot_tree() {",
		"  out=\"$1\"",
		"  find / \\( -path /proc -o -path /sys -o -path /dev -o -path /run -o -path /tmp -o -path /mudp-bootstrap -o -path /mudp-layer-root \\) -prune -o -exec sh -c '",
		"    for p do",
		"      [ -e \"$p\" ] || [ -L \"$p\" ] || continue",
		"      stat -c \"%F\\t%a\\t%u\\t%g\\t%s\\t%Y\\t%n\" \"$p\" 2>/dev/null || true",
		"    done",
		"  ' sh {} + 2>/dev/null | sort > \"$out\"",
		"}",
		"",
		"snapshot_base() {",
		"  if have_gnu_tar_incremental; then",
		"    tar --listed-incremental=/tmp/mudp/base.snar -cf /dev/null --exclude=/proc --exclude=/sys --exclude=/dev --exclude=/run --exclude=/tmp --exclude=/mudp-bootstrap --exclude=/mudp-layer-root / 2>/dev/null || true",
		"  else",
		"    snapshot_tree /tmp/mudp/before.snapshot",
		"  fi",
		"}",
		"",
		"cleanup_build_caches() {",
		"  if have_cmd apt-get; then",
		"    apt-get clean >/dev/null 2>&1 || true",
		"    rm -rf /var/lib/apt/lists/* /var/cache/apt/archives/* /var/cache/debconf/*-old",
		"  fi",
		"  if have_cmd dnf; then dnf clean all >/dev/null 2>&1 || true; fi",
		"  if have_cmd yum; then yum clean all >/dev/null 2>&1 || true; fi",
		"  if have_cmd zypper; then zypper clean -a >/dev/null 2>&1 || true; fi",
		"  if have_cmd pip; then pip cache purge >/dev/null 2>&1 || true; fi",
		"  if have_cmd pip3; then pip3 cache purge >/dev/null 2>&1 || true; fi",
		"  if have_cmd npm; then npm cache clean --force >/dev/null 2>&1 || true; fi",
		"  if have_cmd yarn; then yarn cache clean >/dev/null 2>&1 || true; fi",
		"  if have_cmd pnpm; then pnpm store prune >/dev/null 2>&1 || true; fi",
		"  rm -rf /root/.cache /var/tmp/*",
		"  for p in /tmp/*; do",
		"    [ \"$p\" = /tmp/mudp ] && continue",
		"    rm -rf \"$p\"",
		"  done",
		"}",
		"",
		"export_layer_delta() {",
		"  before=\"$1\"",
		"  after=\"$2\"",
		"  mkdir -p /mudp-layer-root",
		"  if have_gnu_tar_incremental && [ -f /tmp/mudp/base.snar ]; then",
		"    tar --listed-incremental=/tmp/mudp/base.snar -cf /tmp/mudp/layer.tar --exclude=/proc --exclude=/sys --exclude=/dev --exclude=/run --exclude=/tmp --exclude=/mudp-bootstrap --exclude=/mudp-layer-root / 2>/dev/null || true",
		"    if [ -s /tmp/mudp/layer.tar ]; then",
		"      tar -C /mudp-layer-root -xf /tmp/mudp/layer.tar 2>/dev/null || true",
		"    fi",
		"    return 0",
		"  fi",
		"  list=/tmp/mudp/layer-files.txt",
		"  comm -13 \"$before\" \"$after\" | cut -f7- | sed 's#^/##' | grep -v '^$' > \"$list\" || true",
		"  if [ -s \"$list\" ]; then",
		"    tar -C / -cf - -T \"$list\" 2>/dev/null | tar -C /mudp-layer-root -xf -",
		"  fi",
		"}",
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
		"snapshot_base",
	}
	if cfg.EnableSSH {
		// Run the admin-supplied SSH install body. The default script guards its
		// daemon-start lines with [ -z "$MUDP_BUILD_PHASE" ] so the build only
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
		// [ -z "$MUDP_BUILD_PHASE" ] so the build installs the binary only.
		lines = append(lines,
			"if [ -f /mudp-bootstrap/vscode.sh ]; then",
			"  /bin/sh /mudp-bootstrap/vscode.sh",
			"fi",
			"touch /tmp/mudp/vscode.installed",
			"",
		)
	}
	lines = append(lines,
		"cleanup_build_caches",
		"snapshot_tree /tmp/mudp/after.snapshot",
		"export_layer_delta /tmp/mudp/before.snapshot /tmp/mudp/after.snapshot",
		"echo \"=== MUDP fused build completed $(date -u +%Y-%m-%dT%H:%M:%SZ) ===\"",
	)
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
		UserResolveSnippet(),
		"",
	}
	if cfg.EnableSSH {
		lines = append(lines,
			"if [ -n \"${MUDP_ACCESS_PASSWORD:-}\" ]; then",
			"  # Set the password on the connection account (root for root images,",
			"  # otherwise the image's declared USER); keep root usable too as admin fallback.",
			"  printf '%s\\n' \"${MUDP_CONNECTION_USER}:${MUDP_ACCESS_PASSWORD}\" | chpasswd 2>/dev/null || true",
			"  if [ \"$MUDP_CONNECTION_USER\" != \"root\" ]; then",
			"    printf 'root:%s\\n' \"${MUDP_ACCESS_PASSWORD}\" | chpasswd 2>/dev/null || true",
			"  fi",
			"fi",
			"command -v ssh-keygen >/dev/null 2>&1 && ssh-keygen -A >/dev/null 2>&1 || true",
			"if [ -f /etc/ssh/sshd_config ]; then",
			"  sed -i 's/^#\\?PermitRootLogin.*/PermitRootLogin yes/' /etc/ssh/sshd_config || true",
			"  sed -i 's/^#\\?PasswordAuthentication.*/PasswordAuthentication yes/' /etc/ssh/sshd_config || true",
			"  grep -q '^PermitRootLogin yes' /etc/ssh/sshd_config || echo 'PermitRootLogin yes' >> /etc/ssh/sshd_config || true",
			"  grep -q '^PasswordAuthentication yes' /etc/ssh/sshd_config || echo 'PasswordAuthentication yes' >> /etc/ssh/sshd_config || true",
			"fi",
			"if command -v sshd >/dev/null 2>&1; then",
			"  pgrep sshd >/dev/null 2>&1 || (/usr/sbin/sshd 2>/dev/null || sshd 2>/dev/null || true)",
			"  # Give sshd a moment to daemonize before checking",
			"  sleep 1 2>/dev/null || true",
			"fi",
			"if ! pgrep sshd >/dev/null 2>&1 && command -v service >/dev/null 2>&1; then",
			"  service ssh start 2>/dev/null || true",
			"  sleep 1 2>/dev/null || true",
			"fi",
			"# Touch the ready marker when sshd is confirmed running; fall back to",
			"# touching it anyway when pgrep is unavailable but sshd binary exists.",
			"if pgrep sshd >/dev/null 2>&1; then",
			"  touch /tmp/mudp/ssh.ready",
			"elif ! command -v pgrep >/dev/null 2>&1 && command -v sshd >/dev/null 2>&1; then",
			"  touch /tmp/mudp/ssh.ready",
			"else",
			"  echo 'ERROR: sshd is not running' >&2",
			"fi",
			"",
		)
	}
	if cfg.EnableVSCode {
		// Re-apply the per-container password into code-server's config (the
		// binary is already installed in the image), then (re)start it as the
		// connection user so its data dir is writable.
		lines = append(lines,
			"if [ -n \"${MUDP_ACCESS_PASSWORD:-}\" ] && command -v code-server >/dev/null 2>&1; then",
			"  CS_CFG=\"$MUDP_HOME/.config/code-server\"",
			"  CS_DATA=\"$MUDP_HOME/.local/share/code-server\"",
			"  mkdir -p \"$CS_CFG\" \"$CS_DATA\" /workspace",
			"  chown -R \"$MUDP_CONNECTION_USER\" \"$CS_CFG\" \"$CS_DATA\" /workspace 2>/dev/null || true",
			"  cat > \"$CS_CFG/config.yaml\" <<EOF",
			"bind-addr: 0.0.0.0:13337",
			"auth: password",
			"password: ${MUDP_ACCESS_PASSWORD}",
			"cert: false",
			"user-data-dir: ${CS_DATA}",
			"EOF",
			"  chown \"$MUDP_CONNECTION_USER\" \"$CS_CFG/config.yaml\" 2>/dev/null || true",
			"  if ! pgrep -x code-server >/dev/null 2>&1; then",
			"    if [ \"$MUDP_CONNECTION_USER\" = \"root\" ]; then",
			"      nohup code-server /workspace >/tmp/mudp/vscode.log 2>&1 &",
			"    elif command -v gosu >/dev/null 2>&1; then",
			"      nohup gosu \"$MUDP_CONNECTION_USER\" code-server /workspace >/tmp/mudp/vscode.log 2>&1 &",
			"    elif command -v runuser >/dev/null 2>&1; then",
			"      nohup runuser -u \"$MUDP_CONNECTION_USER\" -- code-server /workspace >/tmp/mudp/vscode.log 2>&1 &",
			"    elif command -v su >/dev/null 2>&1; then",
			"      nohup su -s /bin/sh \"$MUDP_CONNECTION_USER\" -c 'code-server /workspace' >/tmp/mudp/vscode.log 2>&1 &",
			"    else",
			"      nohup code-server /workspace >/tmp/mudp/vscode.log 2>&1 &",
			"    fi",
			"  fi",
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

// tarballFromFiles packs a list of named files into a build-context tar.
func tarballFromFiles(files []struct{ Name, Body string }) (*bytes.Buffer, error) {
	buf := &bytes.Buffer{}
	tw := tar.NewWriter(buf)
	for _, f := range files {
		hdr := &tar.Header{Name: f.Name, Mode: 0755, Size: int64(len(f.Body))}
		if err := tw.WriteHeader(hdr); err != nil {
			return nil, err
		}
		if _, err := tw.Write([]byte(f.Body)); err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	return buf, nil
}
