package bootstrap

import (
	"archive/tar"
	"bytes"
	"io"
	"strings"
	"testing"
)

func TestTarballContainsEntrypoint(t *testing.T) {
	buf, err := Tarball(Config{
		EnableSSH:      true,
		EnableVSCode:   true,
		AccessPassword: "secret123",
		SSHScript:      "#!/bin/sh\necho ssh",
		VSCodeScript:   "#!/bin/sh\necho vscode",
		OrigCmd:        []string{"bash"},
	})
	if err != nil {
		t.Fatalf("Tarball() error = %v", err)
	}
	tr := tar.NewReader(bytes.NewReader(buf.Bytes()))
	files := map[string]string{}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("Next() error = %v", err)
		}
		body, err := io.ReadAll(tr)
		if err != nil {
			t.Fatalf("ReadAll() error = %v", err)
		}
		files[hdr.Name] = string(body)
	}
	entrypoint := files["mudp-bootstrap/entrypoint.sh"]
	for _, want := range []string{
		"export MUDP_ACCESS_PASSWORD='secret123'",
		"/bin/sh /mudp-bootstrap/ssh.sh",
		"/bin/sh /mudp-bootstrap/vscode.sh",
		"exec tail -f /dev/null",
	} {
		if !strings.Contains(entrypoint, want) {
			t.Fatalf("entrypoint missing %q\n%s", want, entrypoint)
		}
	}
	if files["mudp-bootstrap/ssh.sh"] != "#!/bin/sh\necho ssh" {
		t.Fatalf("unexpected ssh script: %q", files["mudp-bootstrap/ssh.sh"])
	}
	if files["mudp-bootstrap/vscode.sh"] != "#!/bin/sh\necho vscode" {
		t.Fatalf("unexpected vscode script: %q", files["mudp-bootstrap/vscode.sh"])
	}
}

func TestTarballRequiresPasswordWhenConnectionEnabled(t *testing.T) {
	_, err := Tarball(Config{EnableSSH: true})
	if err == nil {
		t.Fatal("expected error for missing password")
	}
}
