package harness

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

var buildState struct {
	sync.Mutex
	path string
	err  error
	done bool
}

func builtBinary() (string, error) {
	buildState.Lock()
	defer buildState.Unlock()
	if !buildState.done {
		buildState.path, buildState.err = buildBinary()
		buildState.done = true
	}
	return buildState.path, buildState.err
}

func RemoveBuild() {
	buildState.Lock()
	defer buildState.Unlock()
	if buildState.done && buildState.err == nil {
		_ = os.RemoveAll(filepath.Dir(buildState.path))
	}
}

func buildBinary() (string, error) {
	dir, err := moduleDir()
	if err != nil {
		return "", err
	}

	tmp, err := os.MkdirTemp("", "hl7gw-e2e")
	if err != nil {
		return "", fmt.Errorf("mkdtemp: %w", err)
	}

	binPath := filepath.Join(tmp, "hl7gw")

	//nolint:gosec,noctx // G204: fixed go-toolchain command in a test harness; noctx: short-lived synchronous build, no context to thread
	build := exec.Command("go", "build", "-race", "-cover", "-coverpkg=./...", "-o", binPath, ".")
	build.Dir = dir
	build.Stdout, build.Stderr = os.Stderr, os.Stderr
	if err := build.Run(); err != nil {
		_ = os.RemoveAll(tmp)
		return "", fmt.Errorf("build hl7gw: %w", err)
	}

	return binPath, nil
}

func moduleDir() (string, error) {
	//nolint:noctx // short-lived synchronous lookup, no context to thread
	out, err := exec.Command("go", "env", "GOMOD").Output()
	if err != nil {
		return "", fmt.Errorf("go env GOMOD: %w", err)
	}

	gomod := strings.TrimSpace(string(out))
	if gomod == "" || gomod == os.DevNull {
		return "", fmt.Errorf("not inside a go module")
	}
	return filepath.Dir(gomod), nil
}
