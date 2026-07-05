package server

import "testing"

func TestCleanDisplayContainerPath(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", "/"},
		{"/", "/"},
		{"work", "/work"},
		{"/work/", "/work"},
		{"/work/../tmp", "/tmp"},
		{" ./app ", "/app"},
	}
	for _, c := range cases {
		if got := cleanDisplayContainerPath(c.in); got != c.want {
			t.Errorf("cleanDisplayContainerPath(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
