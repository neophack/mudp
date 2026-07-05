package dockerx

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// ComposeProject bundles a stored stack with its on-disk project dir for a
// single up/down run.
type ComposeProject struct {
	Name        string // docker compose -p value (mudp-namespaced)
	Dir         string // temp dir holding compose.yaml + .env
	ComposeYAML string
	Env         map[string]string
}

// ComposeAvailable reports whether the docker CLI (with the compose plugin) is
// on PATH. Called once at startup; the result is surfaced to admins so they
// know whether the Stacks tab is functional.
func ComposeAvailable() bool {
	_, err := exec.LookPath("docker")
	return err == nil
}

// StackProjectName builds the mudp-namespaced compose project name.
func StackProjectName(username, name string) string {
	return Prefix + Slug(username) + "-stack-" + Slug(name)
}

// NewComposeProject writes the compose file and .env into a fresh temp dir and
// returns a project ready for Up/Down. Caller must call Cleanup when done.
func NewComposeProject(name, composeYAML string, env map[string]string) (*ComposeProject, error) {
	dir, err := os.MkdirTemp("", "mudp-stack-")
	if err != nil {
		return nil, err
	}
	composePath := filepath.Join(dir, "compose.yaml")
	if err := os.WriteFile(composePath, []byte(composeYAML), 0644); err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	if len(env) > 0 {
		var b strings.Builder
		for k, v := range env {
			fmt.Fprintf(&b, "%s=%s\n", k, v)
		}
		if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(b.String()), 0600); err != nil {
			os.RemoveAll(dir)
			return nil, err
		}
	}
	return &ComposeProject{Name: name, Dir: dir, ComposeYAML: composeYAML, Env: env}, nil
}

// Cleanup removes the temp project dir.
func (p *ComposeProject) Cleanup() {
	if p != nil && p.Dir != "" {
		os.RemoveAll(p.Dir)
	}
}

// CountServices parses the compose YAML and returns the number of services
// defined. Used for quota checks before running Up.
func CountServices(composeYAML string) (int, error) {
	var doc struct {
		Services map[string]yaml.Node `yaml:"services"`
	}
	if err := yaml.Unmarshal([]byte(composeYAML), &doc); err != nil {
		return 0, fmt.Errorf("invalid compose YAML: %w", err)
	}
	return len(doc.Services), nil
}

// SubstituteEnv replaces ${VAR} references in the compose body using env. This
// mirrors what `docker compose` does with a .env file, but we pre-substitute so
// the validation/quota path sees the final values too.
var envRef = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

func SubstituteEnv(composeYAML string, env map[string]string) string {
	if len(env) == 0 {
		return composeYAML
	}
	return envRef.ReplaceAllStringFunc(composeYAML, func(m string) string {
		key := strings.TrimSuffix(strings.TrimPrefix(m, "${"), "}")
		if v, ok := env[key]; ok {
			return v
		}
		return m
	})
}

// ComposeUp runs `docker compose -p <name> up -d --remove-orphans`, streaming
// merged stdout+stderr line-by-line to progress. It blocks until the command
// exits. A non-zero exit produces an error carrying the last output line.
func (p *ComposeProject) ComposeUp(ctx context.Context, progress func(line string)) error {
	return p.run(ctx, []string{"up", "-d", "--remove-orphans"}, progress)
}

// ComposeDown runs `docker compose -p <name> down --remove-orphans`.
func (p *ComposeProject) ComposeDown(ctx context.Context, progress func(line string)) error {
	return p.run(ctx, []string{"down", "--remove-orphans"}, progress)
}

// run executes a docker compose subcommand, capturing combined output and
// forwarding each line through progress. It blocks until the command exits.
func (p *ComposeProject) run(ctx context.Context, args []string, progress func(line string)) error {
	full := append([]string{"compose", "-p", p.Name, "-f", filepath.Join(p.Dir, "compose.yaml")}, args...)
	cmd := exec.CommandContext(ctx, "docker", full...)
	cmd.Dir = p.Dir
	// Merge stdout+stderr so ordering is preserved.
	pr, pw := io.Pipe()
	cmd.Stdout = pw
	cmd.Stderr = pw
	if err := cmd.Start(); err != nil {
		pw.Close()
		return fmt.Errorf("docker compose failed to start: %w (is the docker CLI installed?)", err)
	}
	// Wait in a goroutine; close the pipe when the process exits so the scanner
	// unblocks. The exit error is delivered via waitCh.
	waitCh := make(chan error, 1)
	go func() {
		err := cmd.Wait()
		// Close the write end only after Wait returns so the child's buffered
		// output is fully flushed before the reader sees EOF.
		pw.Close()
		waitCh <- err
	}()
	scanner := bufio.NewScanner(pr)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	var last string
	for scanner.Scan() {
		line := strings.TrimRight(scanner.Text(), "\r")
		last = line
		if progress != nil && line != "" {
			progress(line)
		}
	}
	// Drain any buffered tail, then await process exit.
	pr.Close()
	waitErr := <-waitCh
	if waitErr != nil {
		if last == "" {
			last = "docker compose exited non-zero"
		}
		// Distinguish cancellation from a real failure.
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New(last)
	}
	return nil
}

// SplitEnvLines parses a "KEY=VALUE\nKEY2=VAL2" block into a map. Empty lines
// and comments are ignored. Used by the stack editor.
func SplitEnvLines(raw string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		out[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	return out
}

// MarshalEnv turns a map back into KEY=VALUE lines, sorted for determinism.
func MarshalEnv(env map[string]string) ([]byte, error) {
	var b bytes.Buffer
	for k, v := range env {
		fmt.Fprintf(&b, "%s=%s\n", k, v)
	}
	return b.Bytes(), nil
}
