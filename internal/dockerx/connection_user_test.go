package dockerx

import "testing"

// TestResolveConnectionUser covers the normalisation of an image's USER
// directive into the login account SSH and code-server target. sshd/chpasswd
// need a name, so a bare numeric UID maps to a fixed runtime account.
func TestResolveConnectionUser(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", "root"},
		{"root", "root"},
		{"0", "root"},
		{"  root  ", "root"},
		{"node", "node"},
		{"postgres", "postgres"},
		{"1000", "mudp"},   // bare UID -> fixed runtime account
		{"1000:1000", "mudp"}, // uid:gid -> drop group, then uid
		{"  1000  ", "mudp"},
		{"1000:", "mudp"},
	}
	for _, c := range cases {
		if got := ResolveConnectionUser(c.in); got != c.want {
			t.Errorf("ResolveConnectionUser(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
