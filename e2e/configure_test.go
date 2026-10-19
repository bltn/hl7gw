//go:build e2e

package e2e

import (
	_ "embed"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bltn/hl7gw/e2e/internal/harness"
)

//go:embed testdata/configure/two_channels/session.txt
var twoChannelsSession string

//go:embed testdata/configure/two_channels/hl7gw.json
var twoChannelsConfig string

//go:embed testdata/configure/reprompt/session.txt
var repromptSession string

//go:embed testdata/configure/reprompt/hl7gw.json
var repromptConfig string

//go:embed testdata/configure/eof/session.txt
var eofSession string

const (
	subcommandConfigure = "configure"
	flagOutput          = "-output"
	configFileName      = "hl7gw.json"
)

func runConfigure(t *testing.T, session string) (harness.ExitResult, string, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), configFileName)
	res, err := harness.RunUntilExit(t, session, harness.StartupEnv(), subcommandConfigure, flagOutput, path)
	return res, path, err
}

func TestConfigureWritesConfig(t *testing.T) {
	t.Parallel()
	res, path, err := runConfigure(t, twoChannelsSession)
	if err != nil {
		t.Fatalf("run configure: %v", err)
	}
	if res.Code != 0 {
		t.Fatalf("exit code = %d\n--- stderr ---\n%s", res.Code, res.Stderr)
	}
	got, err := os.ReadFile(path) //nolint:gosec // G304 -- test temp path
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != twoChannelsConfig {
		t.Errorf("written config\n--- got ---\n%s\n--- want ---\n%s", got, twoChannelsConfig)
	}
	want := "Next steps:\n  export GW_ADMIN=...\n  export ADT_API_TOKEN=...\n  export LAB_API_TOKEN=...\n  ./hl7gw run -config " + path + "\n"
	if !strings.HasSuffix(res.Stdout, want) {
		t.Errorf("stdout missing next steps\n--- stdout ---\n%s", res.Stdout)
	}
}

func TestConfigureRepromptsOnInvalidInput(t *testing.T) {
	t.Parallel()
	res, path, err := runConfigure(t, repromptSession)
	if err != nil {
		t.Fatalf("run configure: %v", err)
	}
	if res.Code != 0 {
		t.Fatalf("exit code = %d\n--- stderr ---\n%s", res.Code, res.Stderr)
	}
	for _, want := range []string{"must be a whole number", "port 8081 is already in use", `must be an absolute http(s) URL, got "/hl7"`} {
		if !strings.Contains(res.Stdout, want) {
			t.Errorf("stdout missing %q\n--- stdout ---\n%s", want, res.Stdout)
		}
	}
	got, err := os.ReadFile(path) //nolint:gosec // G304 -- test temp path
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != repromptConfig {
		t.Errorf("written config\n--- got ---\n%s\n--- want ---\n%s", got, repromptConfig)
	}
}

func TestConfigureStopsWhenInputEnds(t *testing.T) {
	t.Parallel()
	res, path, err := runConfigure(t, eofSession)
	if err != nil {
		t.Fatalf("run configure: %v", err)
	}
	if res.Code != 1 {
		t.Errorf("exit code = %d, want 1", res.Code)
	}
	for _, want := range []string{errorPrefix, "input ended before the wizard finished"} {
		if !strings.Contains(res.Stderr, want) {
			t.Errorf("stderr missing %q\n--- stderr ---\n%s", want, res.Stderr)
		}
	}
	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("stat = %v, want not exist", err)
	}
}

func TestConfigureOverwrite(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		session  string
		wantCode int
		kept     bool
	}{
		{"declined", "n\n", 1, true},
		{"blank declines", "\n", 1, true},
		{"accepted", "y\n" + repromptSession, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), configFileName)
			if err := os.WriteFile(path, []byte("keep me"), 0o600); err != nil {
				t.Fatal(err)
			}
			res, err := harness.RunUntilExit(t, tc.session, harness.StartupEnv(), subcommandConfigure, flagOutput, path)
			if err != nil {
				t.Fatalf("run configure: %v", err)
			}
			if res.Code != tc.wantCode {
				t.Errorf("exit code = %d, want %d\n--- stderr ---\n%s", res.Code, tc.wantCode, res.Stderr)
			}
			if !strings.Contains(res.Stdout, path+" already exists. Overwrite? [y/N]") {
				t.Errorf("stdout missing the overwrite prompt\n--- stdout ---\n%s", res.Stdout)
			}
			got, err := os.ReadFile(path) //nolint:gosec // G304 -- test temp path
			if err != nil {
				t.Fatal(err)
			}
			if kept := string(got) == "keep me"; kept != tc.kept {
				t.Errorf("kept = %v, want %v\n--- file ---\n%s", kept, tc.kept, got)
			}
			if tc.kept && !strings.Contains(res.Stderr, "was not overwritten") {
				t.Errorf("stderr missing the refusal\n--- stderr ---\n%s", res.Stderr)
			}
		})
	}
}

func TestConfiguredGatewayBoots(t *testing.T) {
	t.Parallel()
	ports, err := harness.FreePorts(3)
	if err != nil {
		t.Fatalf("find free ports: %v", err)
	}
	apiPort, adtPort, securePort := ports[0], ports[1], ports[2]
	ct, err := harness.NewMTLS(t)
	if err != nil {
		t.Fatalf("generate mtls material: %v", err)
	}
	sink := harness.NewSink(t, http.StatusOK, "")

	session := fmt.Sprintf("%d\n%s\n\n\n\n%s\n%d\n\n\n\n%s\n%s\ny\n%s\n%d\n\n\nmtls\n%s\n%s\n%s\n\n%s\n%s\nn\n1\n",
		apiPort, harness.AdminTokenEnv,
		channelName, adtPort, sink.URL(), harness.ChannelTokenEnv,
		secureChannel, securePort, ct.ServerCertPath, ct.ServerPKPath, ct.ClientCAPath, sink.URL(), harness.ChannelTokenEnv)
	res, path, err := runConfigure(t, session)
	if err != nil {
		t.Fatalf("run configure: %v", err)
	}
	if res.Code != 0 {
		t.Fatalf("exit code = %d\n--- stdout ---\n%s\n--- stderr ---\n%s", res.Code, res.Stdout, res.Stderr)
	}

	gw, err := harness.StartGatewayFromConfig(t, path, apiPort, map[string]int{channelName: adtPort, secureChannel: securePort})
	if err != nil {
		t.Fatalf("start gateway from the written config: %v", err)
	}
	ack, err := gw.SendAndAck(channelName, adtA01Message)
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if got, want := harness.NormalizeACK(ack), harness.NormalizeACK(adtA01AckAA); got != want {
		t.Errorf("ack = %q, want %q", got, want)
	}
	if !sink.WaitForReceived(1) {
		t.Error("destination never received the message")
	}
}
