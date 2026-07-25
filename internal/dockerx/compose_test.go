package dockerx

import (
	"testing"
)

func TestCountServices(t *testing.T) {
	cases := []struct {
		name string
		yaml string
		want int
	}{
		{"single", "services:\n  web:\n    image: nginx\n", 1},
		{"multi", "services:\n  web:\n    image: nginx\n  db:\n    image: postgres\n", 2},
		{"none", "version: '3'\n", 0},
		{"empty", "", 0},
		{"with anchors", "services:\n  a:\n    image: x\n  b:\n    image: y\n  c:\n    image: z\n", 3},
	}
	for _, c := range cases {
		got, err := CountServices(c.yaml)
		if err != nil {
			t.Errorf("%s: unexpected error %v", c.name, err)
			continue
		}
		if got != c.want {
			t.Errorf("%s: got %d, want %d", c.name, got, c.want)
		}
	}
}

func TestCountServicesInvalid(t *testing.T) {
	_, err := CountServices("services: [this is not valid yaml :::")
	if err == nil {
		t.Fatal("expected error for invalid YAML")
	}
}

func TestSubstituteEnv(t *testing.T) {
	env := map[string]string{"TAG": "1.2.3", "PORT": "8080"}
	in := "image: myapp:${TAG}\nports:\n  - ${PORT}:80"
	got := SubstituteEnv(in, env)
	want := "image: myapp:1.2.3\nports:\n  - 8080:80"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestSubstituteEnvMissingKeepsRef(t *testing.T) {
	got := SubstituteEnv("image: app:${MISSING}", map[string]string{"X": "y"})
	if got != "image: app:${MISSING}" {
		t.Errorf("missing ref should be preserved, got %q", got)
	}
}

func TestSubstituteEnvNoEnvNoop(t *testing.T) {
	in := "image: ${TAG}"
	if got := SubstituteEnv(in, nil); got != in {
		t.Errorf("nil env should be a no-op, got %q", got)
	}
}

func TestSplitEnvLines(t *testing.T) {
	raw := "TAG=1.0\n# comment\n\nPORT=8080\nEMPTY=\nBROKEN"
	got := SplitEnvLines(raw)
	want := map[string]string{"TAG": "1.0", "PORT": "8080", "EMPTY": ""}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("env[%q] = %q, want %q", k, got[k], v)
		}
	}
	if _, ok := got["BROKEN"]; ok {
		t.Error("malformed line without = should be skipped")
	}
	if _, ok := got["comment"]; ok {
		t.Error("comment line should be skipped")
	}
}

func TestStackProjectName(t *testing.T) {
	got := StackProjectName("alice", "Web App")
	want := "mudp-alice-stack-web-app"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestValidateComposeRejectsHostEscapes locks down the stack path against the
// privilege escalations it used to allow: `docker compose up` never goes
// through CreateContainer, so every restriction enforced there has to be
// re-enforced on the YAML.
func TestValidateComposeRejectsHostEscapes(t *testing.T) {
	limits := ComposeLimits{PortPrefix: 101}
	cases := []struct {
		name string
		yaml string
	}{
		{"privileged", "services:\n  x:\n    image: alpine\n    privileged: true\n"},
		{"root bind mount", "services:\n  x:\n    image: alpine\n    volumes:\n      - \"/:/host\"\n"},
		{"docker socket", "services:\n  x:\n    image: alpine\n    volumes:\n      - \"/var/run/docker.sock:/var/run/docker.sock\"\n"},
		{"long form bind", "services:\n  x:\n    image: alpine\n    volumes:\n      - type: bind\n        source: /etc\n        target: /etc\n"},
		{"host network", "services:\n  x:\n    image: alpine\n    network_mode: host\n"},
		{"join container netns", "services:\n  x:\n    image: alpine\n    network_mode: \"container:other\"\n"},
		{"host pid", "services:\n  x:\n    image: alpine\n    pid: host\n"},
		{"cap add", "services:\n  x:\n    image: alpine\n    cap_add:\n      - SYS_ADMIN\n"},
		{"devices", "services:\n  x:\n    image: alpine\n    devices:\n      - \"/dev/sda:/dev/sda\"\n"},
		{"security opt", "services:\n  x:\n    image: alpine\n    security_opt:\n      - \"apparmor:unconfined\"\n"},
		{"sysctls", "services:\n  x:\n    image: alpine\n    sysctls:\n      net.ipv4.ip_forward: 1\n"},
		{"userns host", "services:\n  x:\n    image: alpine\n    userns_mode: host\n"},
		{"build context", "services:\n  x:\n    build:\n      context: /etc\n"},
		{"volumes_from", "services:\n  x:\n    image: alpine\n    volumes_from:\n      - other\n"},
		{"driver_opts bind", "services:\n  x:\n    image: alpine\n    volumes:\n      - \"escape:/mnt\"\nvolumes:\n  escape:\n    driver_opts:\n      type: none\n      o: bind\n      device: /\n"},
		{"external volume", "services:\n  x:\n    image: alpine\nvolumes:\n  other:\n    external: true\n"},
		{"external network", "services:\n  x:\n    image: alpine\nnetworks:\n  other:\n    external: true\n"},
		{"port outside range", "services:\n  x:\n    image: alpine\n    ports:\n      - \"22:22\"\n"},
		{"port long form outside range", "services:\n  x:\n    image: alpine\n    ports:\n      - target: 80\n        published: 443\n"},
		{"random host port", "services:\n  x:\n    image: alpine\n    ports:\n      - \"80\"\n"},
	}
	for _, c := range cases {
		if err := ValidateCompose(c.yaml, limits); err == nil {
			t.Errorf("%s: expected rejection, got nil", c.name)
		}
	}
}

func TestValidateComposeAllowsOrdinaryStacks(t *testing.T) {
	limits := ComposeLimits{PortPrefix: 101}
	cases := []struct {
		name string
		yaml string
	}{
		{"plain", "services:\n  web:\n    image: nginx\n"},
		{"named volume", "services:\n  web:\n    image: nginx\n    volumes:\n      - \"data:/var/lib/data\"\nvolumes:\n  data:\n"},
		{"anonymous volume", "services:\n  web:\n    image: nginx\n    volumes:\n      - \"/var/cache\"\n"},
		{"port in range", "services:\n  web:\n    image: nginx\n    ports:\n      - \"10105:80\"\n"},
		{"port range in block", "services:\n  web:\n    image: nginx\n    ports:\n      - \"10110-10120:80\"\n"},
		{"bridge network", "services:\n  web:\n    image: nginx\n    network_mode: bridge\n"},
		{"explicit unprivileged", "services:\n  web:\n    image: nginx\n    privileged: false\n"},
		{"cap_drop is safe", "services:\n  web:\n    image: nginx\n    cap_drop:\n      - ALL\n"},
		{"project network", "services:\n  web:\n    image: nginx\n    networks:\n      - front\nnetworks:\n  front:\n"},
		{"tmpfs long form", "services:\n  web:\n    image: nginx\n    volumes:\n      - type: tmpfs\n        target: /tmp\n"},
	}
	for _, c := range cases {
		if err := ValidateCompose(c.yaml, limits); err != nil {
			t.Errorf("%s: expected acceptance, got %v", c.name, err)
		}
	}
}

// Admins keep the unrestricted path: they can already reach the same power
// through the image and host lifecycle endpoints.
func TestValidateComposeAdminBypass(t *testing.T) {
	yaml := "services:\n  x:\n    image: alpine\n    privileged: true\n"
	if err := ValidateCompose(yaml, ComposeLimits{Admin: true}); err != nil {
		t.Errorf("admin should bypass, got %v", err)
	}
}

// The env block must not be able to smuggle a forbidden value past validation:
// SubstituteEnv resolves what we know, and NewComposeProject escapes whatever
// is left so compose performs no expansion of its own.
func TestComposeInterpolationCannotBypassValidation(t *testing.T) {
	tmpl := "services:\n  x:\n    image: alpine\n    privileged: ${PRIV}\n"
	env := map[string]string{"PRIV": "true"}
	if err := ValidateCompose(SubstituteEnv(tmpl, env), ComposeLimits{PortPrefix: 101}); err == nil {
		t.Error("substituted body should be rejected")
	}
	// Unresolved references are neutralized rather than left for compose.
	if got := escapeComposeInterpolation("privileged: $PRIV\n"); got != "privileged: $$PRIV\n" {
		t.Errorf("escapeComposeInterpolation: got %q", got)
	}
	if got := escapeComposeInterpolation("echo $$HOME"); got != "echo $$HOME" {
		t.Errorf("already-escaped dollar should pass through: got %q", got)
	}
}
