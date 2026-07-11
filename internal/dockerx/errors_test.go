package dockerx

import (
	"errors"
	"fmt"
	"os"
	"testing"

	"github.com/docker/docker/client"
)

func TestIsUnavailableError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"random error", errors.New("something broke"), false},
		{"wrapped random", fmt.Errorf("wrapped: %w", errors.New("something broke")), false},
		{"connection failed", client.ErrorConnectionFailed(""), true},
		{"wrapped connection failed", fmt.Errorf("wrapped: %w", client.ErrorConnectionFailed("")), true},
		{"not exist", os.ErrNotExist, true},
		{"permission denied", os.ErrPermission, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := IsUnavailableError(c.err); got != c.want {
				t.Errorf("IsUnavailableError(%v) = %v, want %v", c.err, got, c.want)
			}
		})
	}
}
