package dockerx

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/go-connections/nat"
	"golang.org/x/crypto/ssh"
)

// ValidateFusedLayer builds a temporary runtime image from the base image plus
// the layer described by plan/service, starts a throwaway container, and checks
// that the service actually works (SSH login or VS Code page). It cleans up the
// temporary container and image before returning.
func (d *Client) ValidateFusedLayer(ctx context.Context, plan *FusedPlan, service, accessPassword string, emit func(stage, msg string)) error {
	if service != "ssh" && service != "vscode" {
		return fmt.Errorf("unknown layer service: %s", service)
	}
	if accessPassword == "" {
		return fmt.Errorf("access password is required for validation")
	}

	suffix, err := randomSuffix(8)
	if err != nil {
		return fmt.Errorf("generate validation suffix: %w", err)
	}

	// Build a validation plan that enables only the service under test and uses
	// a unique final image tag so it does not collide with real fused images.
	validationPlan := *plan
	validationPlan.EnableSSH = service == "ssh"
	validationPlan.EnableVSCode = service == "vscode"
	validationPlan.AccessPassword = accessPassword
	validationPlan.FusedRef = Prefix + "fused-validate-" + service + "-" + suffix + ":latest"

	emit("validate", "Building temporary runtime image for validation…")
	// Build the validation final image without forcing a layer rebuild: the layer
	// was just built by the caller and we want to validate that exact layer.
	if err := d.buildFused(ctx, &validationPlan, emit, false); err != nil {
		return fmt.Errorf("build validation image: %w", err)
	}
	defer func() {
		_ = d.RemoveManagedImage(ctx, validationPlan.FusedRef)
	}()

	containerName := Prefix + "validate-" + service + "-" + suffix
	exposed := nat.PortSet{}
	portMap := nat.PortMap{}
	var privatePort uint16
	if service == "ssh" {
		privatePort = 22
		exposed["22/tcp"] = struct{}{}
		portMap["22/tcp"] = []nat.PortBinding{{HostPort: "0"}}
	} else {
		privatePort = 13337
		exposed["13337/tcp"] = struct{}{}
		portMap["13337/tcp"] = []nat.PortBinding{{HostPort: "0"}}
	}

	containerCfg := &container.Config{
		Image:        validationPlan.FusedRef,
		Env:          []string{"MUDP_ACCESS_PASSWORD=" + accessPassword},
		ExposedPorts: exposed,
		Labels: map[string]string{
			ManagedLabel:      "true",
			"mudp.validation": "true",
			"mudp.service":    service,
		},
	}
	hostCfg := &container.HostConfig{
		PortBindings: portMap,
		AutoRemove:   false,
	}

	emit("validate", "Starting temporary validation container…")
	resp, err := d.c.ContainerCreate(ctx, containerCfg, hostCfg, nil, nil, containerName)
	if err != nil {
		return fmt.Errorf("create validation container: %w", err)
	}
	defer func() {
		_ = d.c.ContainerRemove(ctx, resp.ID, container.RemoveOptions{Force: true, RemoveVolumes: true})
	}()

	if err := d.c.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		return fmt.Errorf("start validation container: %w", err)
	}

	emit("validate", "Waiting for service to come up…")
	if err := d.waitForReady(ctx, resp.ID, privatePort); err != nil {
		d.emitValidationDiagnostics(ctx, resp.ID, service, emit)
		return fmt.Errorf("service not ready: %w", err)
	}

	hostPort := d.publishedHostPort(ctx, resp.ID, privatePort)
	if hostPort == "" {
		return fmt.Errorf("could not determine published host port")
	}
	addr := "127.0.0.1:" + hostPort

	if service == "ssh" {
		emit("validate", "Testing SSH login…")
		if err := validateSSHLogin(addr, "root", accessPassword); err != nil {
			return fmt.Errorf("ssh login failed: %w", err)
		}
	} else {
		emit("validate", "Testing VS Code Server page…")
		if err := validateVSCodePage(ctx, addr); err != nil {
			return fmt.Errorf("vscode page check failed: %w", err)
		}
	}

	emit("validate", "Validation passed")
	return nil
}

func (d *Client) emitValidationDiagnostics(ctx context.Context, id, service string, emit func(stage, msg string)) {
	if emit == nil {
		return
	}
	marker := "/tmp/mudp/" + service + ".ready"
	cmd := "echo 'validation diagnostics:'; " +
		"printf 'marker %s: ' " + shellQuote(marker) + "; test -f " + shellQuote(marker) + " && echo yes || echo no; " +
		"printf 'container userland: '; uname -a 2>/dev/null || true; " +
		"printf 'sshd path: '; command -v sshd || true; " +
		"printf 'code-server path: '; command -v code-server || true; " +
		"ps aux 2>/dev/null | grep -E '[s]shd|[c]ode-server' || true; " +
		"echo '--- /tmp/mudp/bootstrap.log ---'; tail -120 /tmp/mudp/bootstrap.log 2>/dev/null || true"
	out := d.execOutput(ctx, id, cmd)
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			emit("validate", line)
		}
	}
}

func (d *Client) execOutput(ctx context.Context, id, cmd string) string {
	probeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	execCfg := container.ExecOptions{
		AttachStdout: true,
		AttachStderr: true,
		Cmd:          []string{"/bin/sh", "-lc", cmd},
	}
	resp, err := d.c.ContainerExecCreate(probeCtx, id, execCfg)
	if err != nil {
		return err.Error()
	}
	attach, err := d.c.ContainerExecAttach(probeCtx, resp.ID, container.ExecAttachOptions{})
	if err != nil {
		return err.Error()
	}
	defer attach.Close()
	var buf bytes.Buffer
	_, _ = io.Copy(&buf, attach.Reader)
	return buf.String()
}

// validateSSHLogin attempts a real SSH handshake and password authentication.
func validateSSHLogin(addr, user, password string) error {
	config := &ssh.ClientConfig{
		User: user,
		Auth: []ssh.AuthMethod{
			ssh.Password(password),
		},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         10 * time.Second,
	}
	client, err := ssh.Dial("tcp", addr, config)
	if err != nil {
		return err
	}
	defer client.Close()
	return nil
}

// validateVSCodePage fetches the code-server landing page and checks that it
// returns a non-empty response that looks like the code-server login page.
// It retries a few times because code-server may accept the TCP port before
// it is ready to serve HTTP.
func validateVSCodePage(ctx context.Context, addr string) error {
	url := "http://" + addr + "/"
	var lastErr error
	for attempt := 0; attempt < 5; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(2 * time.Second):
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return err
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			lastErr = err
			continue
		}
		if len(body) == 0 {
			lastErr = fmt.Errorf("empty response body")
			continue
		}
		s := strings.ToLower(string(body))
		if strings.Contains(s, "code-server") || strings.Contains(s, "coder") || strings.Contains(s, "password") {
			return nil
		}
		lastErr = fmt.Errorf("response does not look like the code-server login page")
	}
	return lastErr
}

// randomSuffix returns a random hex string of the requested byte length.
func randomSuffix(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
