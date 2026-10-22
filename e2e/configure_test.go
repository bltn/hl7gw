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

//go:embed testdata/configure/defaults/session.txt
var defaultsSession string

//go:embed testdata/configure/defaults/hl7gw.json
var defaultsConfig string

//go:embed testdata/configure/none_auth/session.txt
var noneAuthSession string

//go:embed testdata/configure/none_auth/hl7gw.json
var noneAuthConfig string

//go:embed testdata/configure/shared_token/session.txt
var sharedTokenSession string

//go:embed testdata/configure/eof/session.txt
var eofSession string

const (
	subcommandConfigure = "configure"
	flagOutput          = "-output"
	configFileName      = "hl7gw.json"
	adminTokenFileName  = "admin.token"
	nextStepsHead       = "Next steps:\n  Admin API token: admin.token (generated)\n"
	nextStepsRun        = "  ./hl7gw run -config hl7gw.json\n"
	nextStepsFiles      = "  Put each destination's bearer token or password in its file:\n"
)

func runConfigure(t *testing.T, dir, session string) (harness.ExitResult, error) {
	t.Helper()
	return harness.RunUntilExit(dir, session, subcommandConfigure, flagOutput, configFileName)
}

func readConfigured(t *testing.T, dir, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, name)) //nolint:gosec // G304 -- test temp path
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
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
	if got := readConfigured(t, dir, configFileName); got != twoChannelsConfig {
		t.Errorf("written config\n--- got ---\n%s\n--- want ---\n%s", got, twoChannelsConfig)
	}
	want := nextStepsHead + nextStepsFiles + "    adt.token\n    lab.password\n" + nextStepsRun
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
	token := readConfigured(t, dir, adminTokenFileName)
	if _, err := hex.DecodeString(strings.TrimSuffix(token, "\n")); err != nil || len(token) != 65 {
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
	rejections := []string{
		`channel name "a/b" must be non-empty and contain only letters, digits, _ or -`,
		"must be a port between 1 and 65535, got 99999",
		"must be a whole number",
		"must be a positive duration",
		"must be a duration such as 30s, 1m or 2h",
		"must be a positive number",
		"answer y or n",
		`must be an absolute http(s) URL, got "/hl7"`,
		`must be an absolute http(s) URL, got "ftp://x/"`,
		"missing ']' in host",
		"must be one of none, bearer, basic",
		"channel adt already exists",
		"port 2575 is already in use",
		"must not be empty",
		"must be one of 1.2, 1.3",
		`must not contain ":", got "svc:lab"`,
		"must be a port between 1 and 65535, got 70000",
		"must be one of debug, info, warn, error",
		"must be one of auto, json, text",
	}
	for _, want := range rejections {
		if !strings.Contains(res.Stdout, want) {
			t.Errorf("stdout missing %q\n--- stdout ---\n%s", want, res.Stdout)
		}
	}
	if got := readConfigured(t, dir, configFileName); got != repromptConfig {
		t.Errorf("written config\n--- got ---\n%s\n--- want ---\n%s", got, repromptConfig)
	}
}

func TestConfigureNoneAuthNeedsNoSecretFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	res, err := runConfigure(t, dir, noneAuthSession)
	if err != nil {
		t.Fatalf("run configure: %v", err)
	}
	if res.Code != 0 {
		t.Fatalf("exit code = %d\n--- stderr ---\n%s", res.Code, res.Stderr)
	}
	if got := readConfigured(t, dir, configFileName); got != noneAuthConfig {
		t.Errorf("written config\n--- got ---\n%s\n--- want ---\n%s", got, noneAuthConfig)
	}
	if want := nextStepsHead + nextStepsRun; !strings.HasSuffix(res.Stdout, want) {
		t.Errorf("next steps should list no secret files\n--- stdout ---\n%s", res.Stdout)
	}
}

func TestConfigureListsEachSecretFileOnce(t *testing.T) {
	t.Parallel()
	res, err := runConfigure(t, t.TempDir(), sharedTokenSession)
	if err != nil {
		t.Fatalf("run configure: %v", err)
	}
	if res.Code != 0 {
		t.Fatalf("exit code = %d\n--- stderr ---\n%s", res.Code, res.Stderr)
	}
	if want := nextStepsHead + nextStepsFiles + "    adt.token\n" + nextStepsRun; !strings.HasSuffix(res.Stdout, want) {
		t.Errorf("secret file list not deduplicated\n--- stdout ---\n%s", res.Stdout)
	}
}

func TestConfigureAcceptsFinalLineWithoutNewline(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	res, err := runConfigure(t, dir, strings.TrimSuffix(defaultsSession, "\n")+"text")
	if err != nil {
		t.Fatalf("run configure: %v", err)
	}
	if res.Code != 0 {
		t.Errorf("exit code = %d\n--- stderr ---\n%s", res.Code, res.Stderr)
	}
	if got := readConfigured(t, dir, configFileName); !strings.Contains(got, `"format": "text"`) {
		t.Errorf("final answer without a newline was not applied\n--- config ---\n%s", got)
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
		name       string
		session    string
		wantCode   int
		wantStderr string
		kept       bool
	}{
		{"declined", "n\n", 1, "was not overwritten", true},
		{"blank declines", "\n", 1, "was not overwritten", true},
		{"input ends", "", 1, "input ended before the wizard finished", true},
		{"accepted", "y\n" + defaultsSession, 0, "", false},
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
			if !strings.Contains(res.Stderr, tc.wantStderr) {
				t.Errorf("stderr missing %q\n--- stderr ---\n%s", tc.wantStderr, res.Stderr)
			}
			got := readConfigured(t, dir, configFileName)
			if kept := got == "keep me"; kept != tc.kept {
				t.Errorf("kept = %v, want %v\n--- file ---\n%s", kept, tc.kept, got)
			}
			if tc.kept && strings.Contains(res.Stdout, "Channel 1") {
				t.Errorf("kept asking after declining\n--- stdout ---\n%s", res.Stdout)
			}
			if !tc.kept && got != defaultsConfig {
				t.Errorf("overwritten config\n--- got ---\n%s\n--- want ---\n%s", got, defaultsConfig)
			}
		})
	}
}

func TestConfigureReportsWriteFailure(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		output  func(dir string) string
		session string
	}{
		{"missing directory", func(dir string) string { return filepath.Join(dir, "missing", configFileName) }, defaultsSession},
		{"output path is a directory", func(dir string) string { return dir }, "y\n" + defaultsSession},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			res, err := harness.RunUntilExit(dir, tc.session, subcommandConfigure, flagOutput, tc.output(dir))
			if err != nil {
				t.Fatalf("run configure: %v", err)
			}
			if res.Code != 1 {
				t.Errorf("exit code = %d, want 1\n--- stderr ---\n%s", res.Code, res.Stderr)
			}
			if !strings.Contains(res.Stderr, errorPrefix) {
				t.Errorf("stderr missing %q\n--- stderr ---\n%s", errorPrefix, res.Stderr)
			}
		})
	}
}

func TestConfigureKeepsExistingAdminToken(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, adminTokenFileName), []byte("keep me\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := runConfigure(t, dir, defaultsSession)
	if err != nil {
		t.Fatalf("run configure: %v", err)
	}
	if res.Code != 0 {
		t.Fatalf("exit code = %d\n--- stderr ---\n%s", res.Code, res.Stderr)
	}
	if got := readConfigured(t, dir, adminTokenFileName); got != "keep me\n" {
		t.Errorf("admin token = %q, want untouched", got)
	}
	if !strings.Contains(res.Stdout, "Admin API token: admin.token (already existed, left unchanged)") {
		t.Errorf("stdout missing the existing token line\n--- stdout ---\n%s", res.Stdout)
	}
}

func TestConfiguredGatewayBoots(t *testing.T) {
	t.Parallel()
	ports, err := harness.FreePorts(4)
	if err != nil {
		t.Fatalf("find free ports: %v", err)
	}
	apiPort, adtPort, securePort, openPort := ports[0], ports[1], ports[2], ports[3]
	ct, err := harness.NewMTLS(t)
	if err != nil {
		t.Fatalf("generate mtls material: %v", err)
	}
	sink := harness.NewSink(t, http.StatusOK, "")
	openSink := harness.NewSink(t, http.StatusOK, "")
	dir := t.TempDir()
	for _, name := range []string{channelName, secureChannel} {
		if err := os.WriteFile(filepath.Join(dir, name+".token"), []byte("e2e-channel-token\n"), 0o600); err != nil {
			t.Fatalf("write %s token: %v", name, err)
		}
	}

	session := fmt.Sprintf("%s\n%d\n\n\n\n%s\n\n\ny\n%s\n%d\n\n\ny\n%s\n%s\n%s\n\n%s\n\n\ny\n%s\n%d\n\n\n\n%s\nnone\nn\n%d\n\n\n\n1s\n",
		channelName, adtPort, sink.URL(),
		secureChannel, securePort, ct.CertFile, ct.KeyFile, ct.ClientCAFile, sink.URL(),
		openChannel, openPort, openSink.URL(),
		apiPort)
	res, err := runConfigure(t, dir, session)
	if err != nil {
		t.Fatalf("run configure: %v", err)
	}
	if res.Code != 0 {
		t.Fatalf("exit code = %d\n--- stdout ---\n%s\n--- stderr ---\n%s", res.Code, res.Stdout, res.Stderr)
	}

	gw, err := harness.StartGatewayFromConfig(t, filepath.Join(dir, configFileName), apiPort, map[string]int{channelName: adtPort, secureChannel: securePort, openChannel: openPort})
	if err != nil {
		t.Fatalf("start gateway from the written config: %v", err)
	}
	for _, channel := range []string{channelName, openChannel} {
		ack, err := gw.SendAndAck(channel, adtA01Message)
		if err != nil {
			t.Fatalf("send on %s: %v", channel, err)
		}
		if got, want := harness.NormalizeACK(ack), harness.NormalizeACK(adtA01AckAA); got != want {
			t.Errorf("%s ack = %q, want %q", channel, got, want)
		}
	}
	if !sink.WaitForReceived(1) || !openSink.WaitForReceived(1) {
		t.Error("a destination never received its message")
	}
	if auths := sink.AuthHeaders(); len(auths) != 1 || auths[0] != "Bearer e2e-channel-token" {
		t.Errorf("bearer channel authorization headers = %q, want exactly one bearer token", auths)
	}
	if auths := openSink.AuthHeaders(); len(auths) != 1 || auths[0] != "" {
		t.Errorf("none channel authorization headers = %q, want exactly one empty header", auths)
	}

	token := readConfigured(t, dir, adminTokenFileName)
	resp, err := gw.APIGet("/channels", "Bearer "+strings.TrimSpace(token))
	if err != nil {
		t.Fatalf("get /channels with the generated token: %v", err)
	}
	if resp.Status != http.StatusOK {
		t.Errorf("status = %d with the generated admin token, want 200", resp.Status)
	}
}
