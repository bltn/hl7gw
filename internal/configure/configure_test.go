package configure

import (
	"bytes"
	_ "embed"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	"github.com/bltn/hl7gw/internal/config"
	"github.com/bltn/hl7gw/internal/console"
)

//go:embed testdata/two_channels.json
var twoChannelsJSON []byte

const (
	configName      = "hl7gw.json"
	adtChannel      = "adt\n\n5m\n10\n\nhttps://api.example.com/hl7\n\n"
	labChannel      = "lab-results\n\n\n\ny\n/etc/hl7gw/server-cert.pem\n/etc/hl7gw/server-key.pem\n/etc/hl7gw/client-ca.pem\n1.3\nhttp://10.0.0.5/hl7\n\n"
	adminSection    = "9000\n"
	generalSection  = "/var/lib/hl7gw\nDEBUG\njson\n"
	twoChannels     = adtChannel + "y\n" + labChannel + "n\n" + adminSection + generalSection + "30s\n"
	defaultsSession = "adt\n\n\n\n\nhttps://api.example.com/hl7\n\n\n\n\n\n\n"
	wantRunLine     = "./hl7gw run -config " + configName
	rejectPort      = "must be a port between 1 and 65535"
	rejectPositive  = "must be a positive number"
	wantAdminPort   = `"port": 9000`
	wantLabURL      = `"url": "http://10.0.0.5/hl7"`
)

func runWizard(t *testing.T, in io.Reader, term console.Terminal) (out string, err error) {
	t.Helper()
	t.Chdir(t.TempDir())
	var b bytes.Buffer
	err = Run(configName, in, &b, term)
	return b.String(), err
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path) //nolint:gosec // G304 -- test temp path
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestRunWritesEveryAnswer(t *testing.T) {
	out, err := runWizard(t, strings.NewReader(twoChannels), console.Terminal{})
	if err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, configName); got != string(twoChannelsJSON) {
		t.Errorf("written config\n--- got ---\n%s\n--- want ---\n%s", got, twoChannelsJSON)
	}
	for _, want := range []string{
		"Next steps:\n  Admin API token: admin.token (generated)\n  Put each destination's bearer token in its file:\n    adt.token\n    lab-results.token\n  " + wantRunLine + "\n",
		"Certificate reload interval [2m]",
		"Wrote " + configName,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\n--- output ---\n%s", want, out)
		}
	}
	if strings.Contains(out, "\x1b[") {
		t.Errorf("escape codes off a terminal\n--- output ---\n%s", out)
	}
}

func TestRunAcceptsDefaultsAndLoads(t *testing.T) {
	out, err := runWizard(t, strings.NewReader(defaultsSession), console.Terminal{})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("adt.token", []byte("b\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(configName)
	if err != nil {
		t.Fatalf("load written config: %v", err)
	}
	ch := cfg.Channels[0]
	if cfg.Admin.Port != 8081 || cfg.Admin.TokenFile != adminTokenName || len(cfg.Admin.Token()) != 2*adminTokenBytes ||
		cfg.DataDir != "data" || cfg.CertReloadInterval != config.DefaultCertReloadInterval ||
		cfg.Log.Level != "info" || cfg.Log.Format != "auto" ||
		ch.Name != "adt" || ch.Source.Port != 2575 || ch.Source.IdleTimeout != config.Duration(time.Minute) ||
		ch.Source.MaxConnections != 64 || ch.Source.TLS.Mode != "off" ||
		ch.Destination.Auth.TokenFile != "adt.token" || ch.Destination.Auth.Token() != "b" {
		t.Errorf("defaults not applied: %+v", cfg)
	}
	if strings.Contains(out, "reload interval") {
		t.Errorf("asked for the reload interval without mtls\n--- output ---\n%s", out)
	}
}

func TestRunRepromptsUntilValid(t *testing.T) {
	cases := []struct {
		name    string
		session string
		reject  string
		want    string
	}{
		{"admin port not a number", strings.Replace(twoChannels, "9000\n", "abc\n9000\n", 1), "must be a whole number", wantAdminPort},
		{"admin port zero", strings.Replace(twoChannels, "9000\n", "0\n9000\n", 1), rejectPort, wantAdminPort},
		{"admin port too high", strings.Replace(twoChannels, "9000\n", "70000\n9000\n", 1), rejectPort, wantAdminPort},
		{"admin port is a channel port", strings.Replace(twoChannels, "9000\n", "2575\n9000\n", 1), "port 2575 is already in use", wantAdminPort},
		{"empty data dir", strings.Replace(twoChannels, "/var/lib/hl7gw\n", "\n", 1), "", `"data_dir": "data"`},
		{"log level", strings.Replace(twoChannels, "DEBUG\n", "loud\nDEBUG\n", 1), "must be one of debug, info, warn, error", `"level": "debug"`},
		{"channel name", strings.Replace(twoChannels, "adt\n", "a/b\nadt\n", 1), `channel name "a/b" must be non-empty`, `"name": "adt"`},
		{"duplicate channel name", strings.Replace(twoChannels, "lab-results\n", "adt\nlab-results\n", 1), "channel adt already exists", `"name": "lab-results"`},
		{"channel port too high", strings.Replace(twoChannels, "adt\n\n", "adt\n99999\n\n", 1), rejectPort, `"port": 2575`},
		{"duplicate channel port", strings.Replace(twoChannels, "lab-results\n\n", "lab-results\n2575\n\n", 1), "port 2575 is already in use", `"port": 2576`},
		{"idle timeout zero", strings.Replace(twoChannels, "5m\n", "0s\n5m\n", 1), "must be a positive duration", `"idle_timeout": "5m"`},
		{"idle timeout without unit", strings.Replace(twoChannels, "5m\n", "300\n5m\n", 1), "must be a duration such as 30s, 1m or 2h", `"idle_timeout": "5m"`},
		{"max connections", strings.Replace(twoChannels, "10\n", "-3\n10\n", 1), rejectPositive, `"max_connections": 10`},
		{"mtls confirm", strings.Replace(twoChannels, "y\n/etc", "maybe\ny\n/etc", 1), "answer y or n", `"mode": "mtls"`},
		{"empty cert path", strings.Replace(twoChannels, "/etc/hl7gw/server-cert.pem\n", "\n/etc/hl7gw/server-cert.pem\n", 1), "must not be empty", `"cert_file": "/etc/hl7gw/server-cert.pem"`},
		{"min version", strings.Replace(twoChannels, "1.3\n", "1.1\n1.3\n", 1), "must be one of 1.2, 1.3", `"min_version": "1.3"`},
		{"relative url", strings.Replace(twoChannels, "http://10.0.0.5/hl7\n", "not-a-url\nhttp://10.0.0.5/hl7\n", 1), "must be an absolute http(s) URL", wantLabURL},
		{"ftp url", strings.Replace(twoChannels, "http://10.0.0.5/hl7\n", "ftp://x/\nhttp://10.0.0.5/hl7\n", 1), "must be an absolute http(s) URL", wantLabURL},
		{"unparsable url", strings.Replace(twoChannels, "http://10.0.0.5/hl7\n", "http://[::1\nhttp://10.0.0.5/hl7\n", 1), "missing ']' in host", wantLabURL},
		{"confirm", strings.Replace(twoChannels, "y\n", "maybe\nYES\n", 1), "answer y or n", `"name": "lab-results"`},
		{"reload interval", strings.Replace(twoChannels, "30s\n", "0s\n30s\n", 1), "must be a positive duration", `"cert_reload_interval": "30s"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := runWizard(t, strings.NewReader(tc.session), console.Terminal{})
			if err != nil {
				t.Fatalf("%v\n--- output ---\n%s", err, out)
			}
			if !strings.Contains(out, tc.reject) {
				t.Errorf("output missing %q\n--- output ---\n%s", tc.reject, out)
			}
			if got := readFile(t, configName); !strings.Contains(got, tc.want) {
				t.Errorf("config missing %s\n--- config ---\n%s", tc.want, got)
			}
		})
	}
}

func TestRunStopsWhenInputEnds(t *testing.T) {
	for name, session := range map[string]string{"nothing": "", "mid channel": "adt\n\n"} {
		t.Run(name, func(t *testing.T) {
			_, err := runWizard(t, strings.NewReader(session), console.Terminal{})
			if !errors.Is(err, ErrInputClosed) {
				t.Errorf("err = %v, want %v", err, ErrInputClosed)
			}
			for _, path := range []string{configName, adminTokenName} {
				if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
					t.Errorf("stat %s = %v, want not exist", path, err)
				}
			}
		})
	}
}

func TestRunReportsReadErrors(t *testing.T) {
	boom := errors.New("boom")
	_, err := runWizard(t, iotest.ErrReader(boom), console.Terminal{})
	if !errors.Is(err, boom) {
		t.Errorf("err = %v, want %v", err, boom)
	}
}

func TestRunAcceptsFinalLineWithoutNewline(t *testing.T) {
	_, err := runWizard(t, strings.NewReader(strings.TrimSuffix(defaultsSession, "\n")+"text"), console.Terminal{})
	if err != nil {
		t.Error(err)
	}
}

func TestRunOverwrite(t *testing.T) {
	cases := []struct {
		name    string
		session string
		wantErr error
		kept    bool
	}{
		{"declined", "n\n", ErrDeclined, true},
		{"blank declines", "\n", ErrDeclined, true},
		{"input ends", "", ErrInputClosed, true},
		{"accepted", "y\n" + defaultsSession, nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			if err := os.WriteFile(configName, []byte("keep me"), 0o600); err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			err := Run(configName, strings.NewReader(tc.session), &out, console.Terminal{})
			if !errors.Is(err, tc.wantErr) {
				t.Errorf("err = %v, want %v", err, tc.wantErr)
			}
			if kept := readFile(t, configName) == "keep me"; kept != tc.kept {
				t.Errorf("kept = %v, want %v", kept, tc.kept)
			}
			if tc.kept && strings.Contains(out.String(), "Channel 1") {
				t.Errorf("kept asking after declining\n--- output ---\n%s", out.String())
			}
		})
	}
}

func TestRunReportsWriteFailure(t *testing.T) {
	cases := []struct {
		name    string
		path    func(t *testing.T) string
		session string
	}{
		{"missing directory", func(t *testing.T) string { return filepath.Join(t.TempDir(), "missing", configName) }, defaultsSession},
		{"output path is a directory", func(t *testing.T) string { return t.TempDir() }, "y\n" + defaultsSession},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := Run(tc.path(t), strings.NewReader(tc.session), &bytes.Buffer{}, console.Terminal{})
			if _, ok := errors.AsType[*fs.PathError](err); !ok {
				t.Errorf("err = %v, want *fs.PathError", err)
			}
		})
	}
}

func TestRunOnATerminal(t *testing.T) {
	out, err := runWizard(t, strings.NewReader(defaultsSession), console.Terminal{TTY: true, Color: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "MSH|^~\\&|") || !strings.Contains(out, "\x1b[2m2575\x1b[0m") || !strings.Contains(out, "\x1b[32mWrote ") {
		t.Errorf("missing banner or colour\n--- output ---\n%s", out)
	}
	if strings.Contains(out, "Listen port (MLLP) [\x1b[2m2575\x1b[0m]: \n") {
		t.Errorf("newline echoed after a prompt on a terminal\n--- output ---\n%s", out)
	}
}

func TestRunDedupesTokenFiles(t *testing.T) {
	session := strings.Replace(twoChannels, "http://10.0.0.5/hl7\n\n", "http://10.0.0.5/hl7\nadt.token\n", 1)
	out, err := runWizard(t, strings.NewReader(session), console.Terminal{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "in its file:\n    adt.token\n  "+wantRunLine) {
		t.Errorf("token file list not deduplicated\n--- output ---\n%s", out)
	}
}

func TestRunGeneratesAdminToken(t *testing.T) {
	out, err := runWizard(t, strings.NewReader(defaultsSession), console.Terminal{})
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(adminTokenName)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", info.Mode().Perm())
	}
	token := readFile(t, adminTokenName)
	if _, err := hex.DecodeString(strings.TrimSuffix(token, "\n")); err != nil || len(token) != 2*adminTokenBytes+1 {
		t.Errorf("token = %q, want %d hex characters and a newline", token, 2*adminTokenBytes)
	}
	if !strings.Contains(out, "Admin API token: admin.token (generated)") {
		t.Errorf("output missing the generated token line\n--- output ---\n%s", out)
	}
}

func TestRunKeepsExistingAdminToken(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.WriteFile(adminTokenName, []byte("keep me\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := Run(configName, strings.NewReader(defaultsSession), &out, console.Terminal{}); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, adminTokenName); got != "keep me\n" {
		t.Errorf("token file = %q, want untouched", got)
	}
	if !strings.Contains(out.String(), "Admin API token: admin.token (already existed, left unchanged)") {
		t.Errorf("output missing the existing token line\n--- output ---\n%s", out.String())
	}
}
