//go:build e2e

package e2e

import (
	"os"
	"testing"

	"github.com/bltn/hl7gw/e2e/internal/harness"
)

func TestMain(m *testing.M) {
	code := m.Run()
	harness.RemoveBuild()
	os.Exit(code)
}
