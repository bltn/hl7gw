//go:build e2e

package e2e

import (
	"strings"
	"testing"

	"github.com/bltn/hl7gw/e2e/internal/harness"
)

func TestUsage(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		args []string
	}{
		{"no arguments", nil},
		{"unknown subcommand", []string{"bogus"}},
		{"legacy flag form", []string{"-config", "x"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := harness.RunUntilExit(t, "", harness.StartupEnv(), tc.args...)
			if err != nil {
				t.Fatalf("run: %v", err)
			}
			if res.Code != 2 {
				t.Errorf("exit code = %d, want 2", res.Code)
			}
			for _, want := range []string{"usage:", "run -config", "configure"} {
				if !strings.Contains(res.Stderr, want) {
					t.Errorf("stderr missing %q\n--- stderr ---\n%s", want, res.Stderr)
				}
			}
			if res.Stdout != "" {
				t.Errorf("unexpected stdout\n--- stdout ---\n%s", res.Stdout)
			}
		})
	}
}
