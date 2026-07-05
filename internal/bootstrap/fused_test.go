package bootstrap

import (
	"archive/tar"
	"bytes"
	"strings"
	"testing"
)

// readTarFiles extracts the named file contents from a build-context tar.
func readTarFiles(t *testing.T, buf *bytes.Buffer) map[string]string {
	t.Helper()
	files := make(map[string]string)
	tr := tar.NewReader(buf)
	for {
		hdr, err := tr.Next()
		if err != nil {
			break
		}
		body := new(bytes.Buffer)
		if _, err := body.ReadFrom(tr); err != nil {
			t.Fatalf("read %s from tar: %v", hdr.Name, err)
		}
		files[hdr.Name] = body.String()
	}
	return files
}

func TestLayerContextSSH(t *testing.T) {
	cfg := Config{
		BaseRef:        "ubuntu:22.04",
		AccessPassword: "pw",
		EnableSSH:      true,
		SSHScript:      "apt-get install -y openssh-server",
		OrigEntrypoint: []string{"/entrypoint"},
		OrigCmd:        []string{"cmd"},
	}
	buf, err := LayerContext(cfg, "ssh")
	if err != nil {
		t.Fatalf("LayerContext(ssh): %v", err)
	}
	files := readTarFiles(t, buf)

	df := files["Dockerfile"]
	if !strings.Contains(df, "FROM ubuntu:22.04 AS build") {
		t.Errorf("Dockerfile missing base image; got:\n%s", df)
	}
	if strings.Contains(df, "ENTRYPOINT") {
		t.Errorf("layer Dockerfile must not set ENTRYPOINT; got:\n%s", df)
	}
	if !strings.Contains(df, "fused-build.sh") {
		t.Errorf("layer Dockerfile missing fused-build.sh; got:\n%s", df)
	}
	if !strings.Contains(df, "FROM scratch") {
		t.Errorf("layer Dockerfile should finish from scratch; got:\n%s", df)
	}
	if !strings.Contains(df, "COPY --from=build /mudp-layer-root/ /mudp-layer-root/") {
		t.Errorf("layer Dockerfile should expose only the exported delta; got:\n%s", df)
	}

	if _, ok := files["mudp-bootstrap/ssh.sh"]; !ok {
		t.Errorf("ssh.sh missing from layer context")
	}
	if _, ok := files["mudp-bootstrap/vscode.sh"]; ok {
		t.Errorf("vscode.sh should not be present in ssh layer context")
	}
	if _, ok := files["mudp-bootstrap/fused-runtime.sh"]; ok {
		t.Errorf("fused-runtime.sh should not be present in layer context")
	}
}

func TestLayerContextVSCode(t *testing.T) {
	cfg := Config{
		BaseRef:        "ubuntu:22.04",
		AccessPassword: "pw",
		EnableVSCode:   true,
		VSCodeScript:   "curl -fsSL https://code-server.dev/install.sh | sh",
	}
	buf, err := LayerContext(cfg, "vscode")
	if err != nil {
		t.Fatalf("LayerContext(vscode): %v", err)
	}
	files := readTarFiles(t, buf)

	if _, ok := files["mudp-bootstrap/vscode.sh"]; !ok {
		t.Errorf("vscode.sh missing from layer context")
	}
	if _, ok := files["mudp-bootstrap/ssh.sh"]; ok {
		t.Errorf("ssh.sh should not be present in vscode layer context")
	}

	buildScript := files["mudp-bootstrap/fused-build.sh"]
	for _, want := range []string{
		"snapshot_tree /tmp/mudp/before.snapshot",
		"cleanup_build_caches",
		"snapshot_base",
		"--listed-incremental=/tmp/mudp/base.snar",
		"export_layer_delta /tmp/mudp/before.snapshot /tmp/mudp/after.snapshot",
		"find / \\( -path /proc",
		"apt-get clean",
		"npm cache clean --force",
	} {
		if !strings.Contains(buildScript, want) {
			t.Errorf("fused-build.sh missing %q; got:\n%s", want, buildScript)
		}
	}
}

func TestLayerContextInvalidService(t *testing.T) {
	cfg := Config{BaseRef: "ubuntu:22.04", AccessPassword: "pw", EnableSSH: true, SSHScript: "x"}
	if _, err := LayerContext(cfg, "ftp"); err == nil {
		t.Fatalf("expected error for invalid service")
	}
}

func TestFusedContextBoth(t *testing.T) {
	cfg := Config{
		BaseRef:        "ubuntu:22.04",
		AccessPassword: "pw",
		EnableSSH:      true,
		EnableVSCode:   true,
		SSHScript:      "apt-get install -y openssh-server",
		VSCodeScript:   "install code-server",
		OrigEntrypoint: []string{"/entrypoint"},
		OrigCmd:        []string{"cmd"},
	}
	buf, err := FusedContext(cfg, "mudp-layer-ssh-abc:latest", "mudp-layer-vscode-abc:latest")
	if err != nil {
		t.Fatalf("FusedContext: %v", err)
	}
	files := readTarFiles(t, buf)

	df := files["Dockerfile"]
	if !strings.Contains(df, "FROM ubuntu:22.04") {
		t.Errorf("Dockerfile missing base image; got:\n%s", df)
	}
	if !strings.Contains(df, "COPY --from=mudp-layer-vscode-abc:latest /mudp-layer-root/ /") {
		t.Errorf("Dockerfile missing VSCode layer copy; got:\n%s", df)
	}
	if !strings.Contains(df, "COPY --from=mudp-layer-ssh-abc:latest /mudp-layer-root/ /") {
		t.Errorf("Dockerfile missing SSH layer copy; got:\n%s", df)
	}
	// SSH must be copied after VSCode so SSH's base-system modifications survive.
	sshIdx := strings.Index(df, "COPY --from=mudp-layer-ssh-abc:latest")
	vscodeIdx := strings.Index(df, "COPY --from=mudp-layer-vscode-abc:latest")
	if sshIdx <= vscodeIdx {
		t.Errorf("SSH layer must be copied after VSCode layer; got:\n%s", df)
	}
	if !strings.Contains(df, `ENTRYPOINT ["/bin/sh", "/mudp-bootstrap/fused-runtime.sh"]`) {
		t.Errorf("Dockerfile missing runtime ENTRYPOINT; got:\n%s", df)
	}

	if _, ok := files["mudp-bootstrap/fused-runtime.sh"]; !ok {
		t.Errorf("fused-runtime.sh missing from final context")
	}
	if _, ok := files["mudp-bootstrap/fused-build.sh"]; ok {
		t.Errorf("fused-build.sh should not be present in final context")
	}
}

func TestFusedContextSSHOlny(t *testing.T) {
	cfg := Config{
		BaseRef:        "ubuntu:22.04",
		AccessPassword: "pw",
		EnableSSH:      true,
		SSHScript:      "apt-get install -y openssh-server",
	}
	buf, err := FusedContext(cfg, "mudp-layer-ssh-abc:latest", "")
	if err != nil {
		t.Fatalf("FusedContext: %v", err)
	}
	files := readTarFiles(t, buf)

	df := files["Dockerfile"]
	if !strings.Contains(df, "COPY --from=mudp-layer-ssh-abc:latest /mudp-layer-root/ /") {
		t.Errorf("Dockerfile missing SSH layer copy; got:\n%s", df)
	}
	if strings.Contains(df, "vscode") {
		t.Errorf("vscode layer copy should not appear in ssh-only Dockerfile; got:\n%s", df)
	}
}

func TestFusedContextMissingLayerRef(t *testing.T) {
	cfg := Config{
		BaseRef:        "ubuntu:22.04",
		AccessPassword: "pw",
		EnableSSH:      true,
		EnableVSCode:   true,
	}
	if _, err := FusedContext(cfg, "", "mudp-layer-vscode-abc:latest"); err == nil {
		t.Fatalf("expected error when SSH layer ref is missing")
	}
}
