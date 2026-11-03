package main

import (
	"errors"
	"fmt"
	"testing"

	"taurus-readiness/dockerx"
)

func TestCleanupExitCodeSeparatesRefusalFromInfraFailure(t *testing.T) {
	notOwned := fmt.Errorf("x: %w", dockerx.ErrNotOwned)
	infra := errors.New("Cannot connect to the Docker daemon")
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"nil", nil, 0},
		{"refused", notOwned, exitNotOwned},
		{"all refused", errors.Join(notOwned, notOwned), exitNotOwned},
		{"daemon down", infra, 1},
		{"mixed", errors.Join(notOwned, infra), 1},
	}
	for _, c := range cases {
		if got := cleanupExitCode(c.err); got != c.want {
			t.Errorf("%s: got %d want %d", c.name, got, c.want)
		}
	}
}
