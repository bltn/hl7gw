//go:build e2e

package e2e

import (
	_ "embed"
	"encoding/hex"
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
	adminTokenFileName  = "admin.token"
)

func runConfigure(t *testing.T, dir, session string) (harness.ExitResult, error) {
	t.Helper()
	return harness.RunUntilExit(dir, session, subcommandConfigure, flagOutput, configFileName)
}

func TestConfigureWritesConfig(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	res, err := runConfigure(t, dir, twoChannelsSession)
	if err != nil {
		t.Fatalf("run configure: %v", err)
	}
	if res.Code != 0 {
		t.Fatalf("exit code = %d\n--- stderr ---\n%s", res.Code, res.Stderr)
	}
	got, err := os.ReadFile(filepath.Join(dir, configFileName)) //nolint:gosec // G304 -- test temp path
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != twoChannelsConfig {
		t.Errorf("written config\n--- got ---\n%s\n--- want ---\n%s", got, twoChannelsConfig)
	}
	want := "Next steps:\n  Admin API token: admin.token (generated)\n  Put each destination's bearer token in its file:\n    adt.token\n    lab.token\n  ./hl7gw run -config hl7gw.json\n"
	if !strings.HasSuffix(res.Stdout, want) {
		t.Errorf("stdout missing next steps\n--- stdout ---\n%s", res.Stdout)
	}
	info, err := os.Stat(filepath.Join(dir, adminTokenFileName))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("admin token mode = %v, want 0600", info.Mode().Perm())
	}
	token, err := os.ReadFile(filepath.Join(dir, adminTokenFileName)) //nolint:gosec // G304 -- test temp path
	if err != nil {
		t.Fatal(err)
	}
	if _, err := hex.DecodeString(strings.TrimSuffix(string(token), "\n")); err != nil || len(token) != 65 {
		t.Errorf("admin token = %q, want 64 hex characters and a newline", token)
	}
}

func TestConfigureRepromptsOnInvalidInput(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	res, err := runConfigure(t, dir, repromptSession)
	if err != nil {
		t.Fatalf("run configure: %v", err)
	}
	if res.Code != 0 {
		t.Fatalf("exit code = %d\n--- stderr ---\n%s", res.Code, res.Stderr)
	}
	for _, want := range []string{"must be a whole number", "port 2575 is already in use", `must be an absolute http(s) URL, got "/hl7"`} {
		if !strings.Contains(res.Stdout, want) {
			t.Errorf("stdout missing %q\n--- stdout ---\n%s", want, res.Stdout)
		}
	}
	got, err := os.ReadFile(filepath.Join(dir, configFileName)) //nolint:gosec // G304 -- test temp path
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != repromptConfig {
		t.Errorf("written config\n--- got ---\n%s\n--- want ---\n%s", got, repromptConfig)
	}
}

func TestConfigureStopsWhenInputEnds(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	res, err := runConfigure(t, dir, eofSession)
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
	for _, name := range []string{configFileName, adminTokenFileName} {
		if _, err := os.Stat(filepath.Join(dir, name)); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("stat %s = %v, want not exist", name, err)
		}
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
			dir := t.TempDir()
			path := filepath.Join(dir, configFileName)
			if err := os.WriteFile(path, []byte("keep me"), 0o600); err != nil {
				t.Fatal(err)
			}
			res, err := runConfigure(t, dir, tc.session)
			if err != nil {
				t.Fatalf("run configure: %v", err)
			}
			if res.Code != tc.wantCode {
				t.Errorf("exit code = %d, want %d\n--- stderr ---\n%s", res.Code, tc.wantCode, res.Stderr)
			}
			if !strings.Contains(res.Stdout, configFileName+" already exists. Overwrite? [y/N]") {
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
	dir := t.TempDir()
	for _, name := range []string{channelName, secureChannel} {
		if err := os.WriteFile(filepath.Join(dir, name+".token"), []byte("e2e-channel-token\n"), 0o600); err != nil {
			t.Fatalf("write %s token: %v", name, err)
		}
	}

	session := fmt.Sprintf("%s\n%d\n\n\n\n%s\n\ny\n%s\n%d\n\n\ny\n%s\n%s\n%s\n\n%s\n\nn\n%d\n\n\n\n1s\n",
		channelName, adtPort, sink.URL(),
		secureChannel, securePort, ct.CertFile, ct.KeyFile, ct.ClientCAFile, sink.URL(),
		apiPort)
	res, err := runConfigure(t, dir, session)
	if err != nil {
		t.Fatalf("run configure: %v", err)
	}
	if res.Code != 0 {
		t.Fatalf("exit code = %d\n--- stdout ---\n%s\n--- stderr ---\n%s", res.Code, res.Stdout, res.Stderr)
	}

	gw, err := harness.StartGatewayFromConfig(t, filepath.Join(dir, configFileName), apiPort, map[string]int{channelName: adtPort, secureChannel: securePort})
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

	token, err := os.ReadFile(filepath.Join(dir, adminTokenFileName)) //nolint:gosec // G304 -- test temp path
	if err != nil {
		t.Fatal(err)
	}
	resp, err := gw.APIGet("/channels", "Bearer "+strings.TrimSpace(string(token)))
	if err != nil {
		t.Fatalf("get /channels with the generated token: %v", err)
	}
	if resp.Status != http.StatusOK {
		t.Errorf("status = %d with the generated admin token, want 200", resp.Status)
	}
}
