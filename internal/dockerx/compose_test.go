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
