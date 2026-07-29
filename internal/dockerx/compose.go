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
	"sort"
	"strconv"
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
	// Neutralize any interpolation left for compose itself. SubstituteEnv has
	// already resolved every ${VAR} we know about; whatever "$" remains would be
	// expanded by compose from .env or the daemon environment *after*
	// ValidateCompose has run, which would make the validation bypassable
	// (`privileged: $P` with `P=true` in the env block). Escaping to "$$" makes
	// compose emit a literal "$" instead, so the file that runs is byte-for-byte
	// the file that was validated.
	body := escapeComposeInterpolation(composeYAML)
	if err := os.WriteFile(composePath, []byte(body), 0644); err != nil {
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

// ComposeLimits carries the per-user restrictions ValidateCompose enforces.
type ComposeLimits struct {
	// PortPrefix is the user's assigned host-port block: published host ports
	// must fall in [PortPrefix*100, PortPrefix*100+99], matching the rule
	// CreateContainer applies to the native create path. Zero means the user has
	// no block assigned and may not publish host ports at all.
	PortPrefix int
	// Admin skips the restrictions entirely. Administrators can already reach
	// the same power through the image/host lifecycle endpoints.
	Admin bool
}

// forbiddenServiceKeys lists compose service fields that hand a container
// host-level power. `docker compose up` bypasses CreateContainer entirely, so
// without this gate a stack is a trivial escape hatch around validateMountSource
// (bind mounts) and validateCreate (capabilities/devices/sysctls): a three-line
// compose file with `privileged: true` and `volumes: ["/:/host"]` gives any
// activated user root on the host. Values are the reason shown to the user.
var forbiddenServiceKeys = map[string]string{
	"privileged":          "privileged containers are not allowed",
	"cap_add":             "adding capabilities requires admin privileges",
	"devices":             "host device passthrough requires admin privileges",
	"device_cgroup_rules": "device cgroup rules require admin privileges",
	"security_opt":        "security_opt (seccomp/apparmor) requires admin privileges",
	"sysctls":             "sysctls require admin privileges",
	"userns_mode":         "userns_mode is not allowed",
	"cgroup":              "cgroup namespace control is not allowed",
	"cgroup_parent":       "cgroup_parent is not allowed",
	"pid":                 "sharing a PID namespace is not allowed",
	"ipc":                 "sharing an IPC namespace is not allowed",
	"uts":                 "sharing a UTS namespace is not allowed",
	"volumes_from":        "volumes_from is not allowed",
	"extends":             "extends can pull in an unvalidated file and is not allowed",
	"build":               "building images from a stack is not allowed; ask an admin to publish the image",
	"runtime":             "selecting a container runtime requires admin privileges",
	"group_add":           "group_add is not allowed",
}

// allowedNetworkModes are the network_mode values that keep a container inside
// its own network namespace. "host" would expose every host interface (and, on
// a default Docker install, the Docker socket's loopback listeners);
// "container:"/"service:" would let a user join a namespace they may not own.
var allowedNetworkModes = map[string]bool{
	"bridge": true, "none": true, "default": true,
}

// ValidateCompose rejects a stack definition that would grant its author more
// privilege than the native container-create path allows. It is deliberately a
// denylist over the *raw* keys rather than a struct unmarshal: an unknown or
// newly added compose field must not silently pass through, so anything not
// understood here is left alone only when it cannot reach the host.
//
// Callers must validate the body that will actually be written to disk, i.e.
// after SubstituteEnv — see runStackSSE. Validating only the stored template
// would let `privileged: ${P}` pass and resolve to true at deploy time.
func ValidateCompose(composeYAML string, limits ComposeLimits) error {
	if limits.Admin {
		return nil
	}
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(composeYAML), &doc); err != nil {
		return fmt.Errorf("invalid compose YAML: %w", err)
	}
	services, _ := doc["services"].(map[string]any)
	if len(services) == 0 {
		return errors.New("compose file defines no services")
	}
	names := make([]string, 0, len(services))
	for name := range services {
		names = append(names, name)
	}
	sort.Strings(names) // deterministic error for a file with several problems
	for _, name := range names {
		svc, ok := services[name].(map[string]any)
		if !ok {
			return fmt.Errorf("service %q is not a mapping", name)
		}
		if err := validateComposeService(name, svc, limits); err != nil {
			return err
		}
	}
	if err := validateTopLevelVolumes(doc); err != nil {
		return err
	}
	return validateTopLevelNetworks(doc)
}

func validateComposeService(name string, svc map[string]any, limits ComposeLimits) error {
	for key, reason := range forbiddenServiceKeys {
		if v, present := svc[key]; present {
			// `privileged: false` and empty lists are the compose defaults and
			// carry no privilege, so accept them rather than failing a file that
			// is merely explicit about being unprivileged.
			if isZeroComposeValue(v) {
				continue
			}
			return fmt.Errorf("service %q: %s (%s)", name, reason, key)
		}
	}
	if v, present := svc["network_mode"]; present {
		mode := strings.TrimSpace(fmt.Sprint(v))
		if !allowedNetworkModes[mode] {
			return fmt.Errorf("service %q: network_mode %q is not allowed (use bridge or none)", name, mode)
		}
	}
	if err := validateServiceVolumes(name, svc["volumes"]); err != nil {
		return err
	}
	return validateServicePorts(name, svc["ports"], limits)
}

// validateServiceVolumes allows named volumes and tmpfs but never a host bind.
// Compose namespaces named volumes under the (mudp-prefixed) project name, so
// they cannot collide with another user's volumes.
func validateServiceVolumes(name string, raw any) error {
	list, ok := raw.([]any)
	if !ok {
		return nil
	}
	for _, item := range list {
		switch v := item.(type) {
		case string:
			source, _, found := strings.Cut(v, ":")
			if !found {
				continue // anonymous volume ("/data"): no host source
			}
			if isHostPath(source) {
				return fmt.Errorf("service %q: bind mounting host path %q is not allowed; use a named volume", name, source)
			}
		case map[string]any:
			typ := strings.TrimSpace(fmt.Sprint(v["type"]))
			if typ != "" && typ != "volume" && typ != "tmpfs" {
				return fmt.Errorf("service %q: volume type %q is not allowed", name, typ)
			}
			if source, ok := v["source"].(string); ok && isHostPath(source) {
				return fmt.Errorf("service %q: bind mounting host path %q is not allowed; use a named volume", name, source)
			}
		}
	}
	return nil
}

// validateServicePorts confines published host ports to the user's assigned
// block, mirroring the addHostPort check in CreateContainer. Without it a stack
// could squat on port 22/443 or hijack another user's range.
func validateServicePorts(name string, raw any, limits ComposeLimits) error {
	list, ok := raw.([]any)
	if !ok {
		return nil
	}
	for _, item := range list {
		var published string
		switch v := item.(type) {
		case string:
			// "8080:80", "127.0.0.1:8080:80", "80". The host port is the
			// second-to-last colon-separated field; a bare value publishes to a
			// random host port, which would land outside the user's block.
			parts := strings.Split(strings.TrimSpace(v), ":")
			if len(parts) < 2 {
				return fmt.Errorf("service %q: port %q must name an explicit host port inside your range", name, v)
			}
			published = parts[len(parts)-2]
		case map[string]any:
			if v["published"] == nil {
				return fmt.Errorf("service %q: every published port must name an explicit host port inside your range", name)
			}
			published = strings.TrimSpace(fmt.Sprint(v["published"]))
		default:
			published = strings.TrimSpace(fmt.Sprint(item))
		}
		if err := checkHostPortRange(name, published, limits.PortPrefix); err != nil {
			return err
		}
	}
	return nil
}

// checkHostPortRange accepts a single port or a "start-end" range string.
func checkHostPortRange(service, published string, prefix int) error {
	published = strings.Trim(published, `"'`)
	if published == "" {
		return fmt.Errorf("service %q: empty host port", service)
	}
	lo, hi, isRange := strings.Cut(published, "-")
	if !isRange {
		hi = lo
	}
	low, err := strconv.Atoi(strings.TrimSpace(lo))
	if err != nil {
		return fmt.Errorf("service %q: host port %q is not a number", service, published)
	}
	high, err := strconv.Atoi(strings.TrimSpace(hi))
	if err != nil {
		return fmt.Errorf("service %q: host port %q is not a number", service, published)
	}
	if prefix <= 0 {
		return fmt.Errorf("service %q: no host port range is assigned to you; remove the published port", service)
	}
	start, end := prefix*100, prefix*100+99
	if low < start || high > end || low > high {
		return fmt.Errorf("service %q: host port %s is outside your assigned range %d-%d", service, published, start, end)
	}
	return nil
}

// validateTopLevelVolumes blocks the local-driver bind trick
// (driver_opts: {type: none, o: bind, device: /}), which mounts an arbitrary
// host path through what looks like an ordinary named volume, and external
// volumes, which would attach an existing volume owned by someone else.
func validateTopLevelVolumes(doc map[string]any) error {
	volumes, _ := doc["volumes"].(map[string]any)
	names := make([]string, 0, len(volumes))
	for name := range volumes {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		spec, ok := volumes[name].(map[string]any)
		if !ok {
			continue // `myvol:` with an empty body is the plain named volume
		}
		if !isZeroComposeValue(spec["external"]) {
			return fmt.Errorf("volume %q: external volumes are not allowed", name)
		}
		if !isZeroComposeValue(spec["driver_opts"]) {
			return fmt.Errorf("volume %q: driver_opts are not allowed (they can bind a host path)", name)
		}
		if driver, ok := spec["driver"].(string); ok && strings.TrimSpace(driver) != "local" && strings.TrimSpace(driver) != "" {
			return fmt.Errorf("volume %q: volume driver %q is not allowed", name, driver)
		}
	}
	return nil
}

// validateTopLevelNetworks blocks attaching the stack to a pre-existing network
// the user may not own, mirroring validateNetworkAttachment.
func validateTopLevelNetworks(doc map[string]any) error {
	networks, _ := doc["networks"].(map[string]any)
	names := make([]string, 0, len(networks))
	for name := range networks {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		spec, ok := networks[name].(map[string]any)
		if !ok {
			continue
		}
		if !isZeroComposeValue(spec["external"]) {
			return fmt.Errorf("network %q: external networks are not allowed", name)
		}
	}
	return nil
}

// isHostPath reports whether a compose volume source refers to the host
// filesystem rather than a named volume. Compose treats anything containing a
// path separator, or starting with . / ~ or $, as a bind source.
func isHostPath(source string) bool {
	source = strings.TrimSpace(source)
	if source == "" {
		return false
	}
	if strings.HasPrefix(source, "/") || strings.HasPrefix(source, ".") ||
		strings.HasPrefix(source, "~") || strings.HasPrefix(source, "$") {
		return true
	}
	if strings.ContainsAny(source, `/\`) {
		return true
	}
	// Windows drive-qualified path ("C:\data") — the drive letter survives the
	// caller's colon split as a single character.
	if len(source) == 1 {
		return true
	}
	return false
}

// isZeroComposeValue reports whether a value is absent, false, or an empty
// collection — i.e. the compose default, carrying no extra privilege.
func isZeroComposeValue(v any) bool {
	switch t := v.(type) {
	case nil:
		return true
	case bool:
		return !t
	case string:
		s := strings.TrimSpace(strings.ToLower(t))
		return s == "" || s == "false" || s == "no"
	case []any:
		return len(t) == 0
	case map[string]any:
		return len(t) == 0
	}
	return false
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

// escapeComposeInterpolation doubles every "$" so `docker compose` performs no
// variable expansion of its own. An already-escaped "$$" is passed through
// unchanged so files that correctly escape a literal dollar keep working.
func escapeComposeInterpolation(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] != '$' {
			b.WriteByte(s[i])
			continue
		}
		if i+1 < len(s) && s[i+1] == '$' {
			b.WriteString("$$")
			i++
			continue
		}
		b.WriteString("$$")
	}
	return b.String()
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
	cmd := exec.CommandContext(ctx, "docker", full...) // #nosec G204 -- argv is fully server-derived (p.Name is Slug()'d, p.Dir is a MkdirTemp, args are fixed up/-d/--remove-orphans/down); no shell
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
	if scanErr := scanner.Err(); scanErr != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("docker compose output: %w", scanErr)
	}
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
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b bytes.Buffer
	for _, k := range keys {
		fmt.Fprintf(&b, "%s=%s\n", k, env[k])
	}
	return b.Bytes(), nil
}
