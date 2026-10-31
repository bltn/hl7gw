//go:build e2e

package e2e

import (
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/bltn/hl7gw/e2e/internal/harness"
)

func TestRelativeConfigPathsResolveAgainstConfigDir(t *testing.T) {
	t.Parallel()
	ports, err := harness.FreePorts(2)
	if err != nil {
		t.Fatalf("find free ports: %v", err)
	}
	metricsPort, channelPort := ports[0], ports[1]
	sink := harness.NewSink(t, http.StatusOK, "")

	cfgDir, gatewayCwd, cliCwd := shortDir(t), t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(cfgDir, "channel.token"), []byte("e2e-channel-token\n"), 0o600); err != nil {
		t.Fatalf("write channel.token: %v", err)
	}
	cfgPath := filepath.Join(cfgDir, "config.json")
	cfg := fmt.Sprintf(`{"metrics": {"port": %d}, "channels": [{"name": %q, "source": {"port": %d, "idle_timeout": "24h", `+tlsOffOK+`}, "destination": {"url": %q, "auth": {"type": "bearer", "token_file": "channel.token"}}}]}`,
		metricsPort, channelName, channelPort, sink.URL())
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	gw, err := harness.StartGatewayFromConfig(t, gatewayCwd, cfgPath, metricsPort, map[string]int{channelName: channelPort})
	if err != nil {
		t.Fatalf("start gateway from %s: %v", gatewayCwd, err)
	}
	sendControlIDs(t, gw, channelName, "MSG00001")

	if _, err := os.Stat(filepath.Join(cfgDir, "data", channelName+".db")); err != nil {
		t.Errorf("channel database next to the config file: %v", err)
	}
	if _, err := os.Stat(filepath.Join(gatewayCwd, "data")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("data dir under the gateway's working directory: stat err = %v, want not exist", err)
	}
	if _, err := os.Stat(filepath.Join(cfgDir, "data", "hl7gw.sock")); err != nil {
		t.Errorf("socket next to the config file: %v", err)
	}

	res, err := harness.RunUntilExit(cliCwd, "", subcommandMessages, flagConfig, cfgPath, channelName)
	if err != nil {
		t.Fatalf("run messages from %s: %v", cliCwd, err)
	}
	if res.Code != 0 {
		t.Fatalf("exit code = %d\n--- stderr ---\n%s", res.Code, res.Stderr)
	}
	summaries := decodeSummaries(t, res.Stdout)
	if len(summaries) != 1 || summaries[0].ControlID != "MSG00001" {
		t.Errorf("messages from another directory = %+v, want the one message with control id MSG00001", summaries)
	}
}
