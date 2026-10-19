package configure

import (
	"bytes"
	_ "embed"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/bltn/hl7gw/internal/config"
	"github.com/bltn/hl7gw/internal/console"
)

//go:embed testdata/two_channels.json
var twoChannelsJSON []byte

const (
	adminSection    = "9000\nGW_ADMIN\n"
	generalSection  = "/var/lib/hl7gw\nDEBUG\njson\n"
	adtChannel      = "adt\n\n300\n10\n\nhttps://api.example.com/hl7\n\n"
	labChannel      = "lab-results\n\n\n\nMTLS\n/etc/hl7gw/server-cert.pem\n/etc/hl7gw/server-key.pem\n/etc/hl7gw/client-ca.pem\n1.3\nhttp://10.0.0.5/hl7\n\n"
	twoChannels     = adminSection + generalSection + adtChannel + "y\n" + labChannel + "n\n30\n"
	defaultsSession = "\n\n\n\n\nadt\n\n\n\n\nhttps://api.example.com/hl7\n\n\n"
	wantRunLine     = "./hl7gw run -config "
	rejectPort      = "must be a port between 1 and 65535"
	rejectPositive  = "must be a positive number"
	wantAdminPort   = `"port": 9000`
	wantLabURL      = `"url": "http://10.0.0.5/hl7"`
)

func runWizard(t *testing.T, in io.Reader, term console.Terminal) (path, out string, err error) {
	t.Helper()
	path = filepath.Join(t.TempDir(), "hl7gw.json")
	var b bytes.Buffer
	err = Run(path, in, &b, term)
	return path, b.String(), err
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
	path, out, err := runWizard(t, strings.NewReader(twoChannels), console.Terminal{})
	if err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, path); got != string(twoChannelsJSON) {
		t.Errorf("written config\n--- got ---\n%s\n--- want ---\n%s", got, twoChannelsJSON)
	}
	for _, want := range []string{
		"  export GW_ADMIN=...\n  export ADT_API_TOKEN=...\n  export LAB_RESULTS_API_TOKEN=...\n  " + wantRunLine + path + "\n",
		"Certificate reload interval in seconds [120]",
		"Wrote " + path,
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
	path, out, err := runWizard(t, strings.NewReader(defaultsSession), console.Terminal{})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HL7GW_ADMIN_TOKEN", "a")
	t.Setenv("ADT_API_TOKEN", "b")
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("load written config: %v", err)
	}
	ch := cfg.Channels[0]
	if cfg.AdminAPI.Port != 8081 || cfg.AdminAPI.TokenEnv != "HL7GW_ADMIN_TOKEN" || cfg.DataDir != "data" ||
		cfg.TLSReloadIntervalSeconds != 120 || cfg.Log.Level != "info" || cfg.Log.Format != "auto" ||
		ch.Name != "adt" || ch.TCPListener.Port != 2575 || ch.TCPListener.IdleTimeoutSeconds != 60 ||
		ch.TCPListener.MaxConnections != 64 || ch.TCPListener.TLS.Mode != "off" ||
		ch.Destination.Auth.TokenEnv != "ADT_API_TOKEN" {
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
		{"admin token env", strings.Replace(twoChannels, "GW_ADMIN\n", "not-valid!\nGW_ADMIN\n", 1), "must be an environment variable name", `"token_env": "GW_ADMIN"`},
		{"empty data dir", strings.Replace(twoChannels, "/var/lib/hl7gw\n", "\n", 1), "", `"data_dir": "data"`},
		{"log level", strings.Replace(twoChannels, "DEBUG\n", "loud\nDEBUG\n", 1), "must be one of debug, info, warn, error", `"level": "debug"`},
		{"channel name", strings.Replace(twoChannels, "adt\n", "a/b\nadt\n", 1), `channel name "a/b" must be non-empty`, `"name": "adt"`},
		{"duplicate channel name", strings.Replace(twoChannels, "lab-results\n", "adt\nlab-results\n", 1), "channel adt already exists", `"name": "lab-results"`},
		{"channel port too high", strings.Replace(twoChannels, "adt\n\n", "adt\n99999\n\n", 1), rejectPort, `"port": 2575`},
		{"channel port is the admin port", strings.Replace(twoChannels, "adt\n\n", "adt\n9000\n\n", 1), "port 9000 is already in use", `"port": 2575`},
		{"duplicate channel port", strings.Replace(twoChannels, "lab-results\n\n", "lab-results\n2575\n\n", 1), "port 2575 is already in use", `"port": 2576`},
		{"idle timeout", strings.Replace(twoChannels, "300\n", "0\n300\n", 1), rejectPositive, `"idle_timeout_seconds": 300`},
		{"max connections", strings.Replace(twoChannels, "10\n", "-3\n10\n", 1), rejectPositive, `"max_connections": 10`},
		{"tls mode", strings.Replace(twoChannels, "MTLS\n", "ssl\nMTLS\n", 1), "must be one of off, mtls", `"mode": "mtls"`},
		{"empty cert path", strings.Replace(twoChannels, "/etc/hl7gw/server-cert.pem\n", "\n/etc/hl7gw/server-cert.pem\n", 1), "must not be empty", `"server_cert_path": "/etc/hl7gw/server-cert.pem"`},
		{"min version", strings.Replace(twoChannels, "1.3\n", "1.1\n1.3\n", 1), "must be one of 1.2, 1.3", `"min_version": 1.3`},
		{"relative url", strings.Replace(twoChannels, "http://10.0.0.5/hl7\n", "not-a-url\nhttp://10.0.0.5/hl7\n", 1), "must be an absolute http(s) URL", wantLabURL},
		{"ftp url", strings.Replace(twoChannels, "http://10.0.0.5/hl7\n", "ftp://x/\nhttp://10.0.0.5/hl7\n", 1), "must be an absolute http(s) URL", wantLabURL},
		{"unparsable url", strings.Replace(twoChannels, "http://10.0.0.5/hl7\n", "http://[::1\nhttp://10.0.0.5/hl7\n", 1), "missing ']' in host", wantLabURL},
		{"destination token env", strings.Replace(twoChannels, "\n\ny\n", "\nlower-case!\n\ny\n", 1), "must be an environment variable name", `"token_env": "ADT_API_TOKEN"`},
		{"confirm", strings.Replace(twoChannels, "y\n", "maybe\nYES\n", 1), "answer y or n", `"name": "lab-results"`},
		{"reload interval", strings.Replace(twoChannels, "30\n", "0\n30\n", 1), rejectPositive, `"tls_reload_interval_seconds": 30`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path, out, err := runWizard(t, strings.NewReader(tc.session), console.Terminal{})
			if err != nil {
				t.Fatalf("%v\n--- output ---\n%s", err, out)
			}
			if !strings.Contains(out, tc.reject) {
				t.Errorf("output missing %q\n--- output ---\n%s", tc.reject, out)
			}
			if got := readFile(t, path); !strings.Contains(got, tc.want) {
				t.Errorf("config missing %s\n--- config ---\n%s", tc.want, got)
			}
		})
	}
}

func TestRunStopsWhenInputEnds(t *testing.T) {
	for name, session := range map[string]string{"nothing": "", "mid channel": adminSection + generalSection + "adt\n"} {
		t.Run(name, func(t *testing.T) {
			path, _, err := runWizard(t, strings.NewReader(session), console.Terminal{})
			if !errors.Is(err, ErrInputClosed) {
				t.Errorf("err = %v, want %v", err, ErrInputClosed)
			}
			if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("stat = %v, want not exist", err)
			}
		})
	}
}

func TestRunReportsReadErrors(t *testing.T) {
	boom := errors.New("boom")
	_, _, err := runWizard(t, iotest.ErrReader(boom), console.Terminal{})
	if !errors.Is(err, boom) {
		t.Errorf("err = %v, want %v", err, boom)
	}
}

func TestRunAcceptsFinalLineWithoutNewline(t *testing.T) {
	_, _, err := runWizard(t, strings.NewReader(strings.TrimSuffix(defaultsSession, "\n")+"no"), console.Terminal{})
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
			path := filepath.Join(t.TempDir(), "hl7gw.json")
			if err := os.WriteFile(path, []byte("keep me"), 0o600); err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			err := Run(path, strings.NewReader(tc.session), &out, console.Terminal{})
			if !errors.Is(err, tc.wantErr) {
				t.Errorf("err = %v, want %v", err, tc.wantErr)
			}
			if kept := readFile(t, path) == "keep me"; kept != tc.kept {
				t.Errorf("kept = %v, want %v", kept, tc.kept)
			}
			if tc.kept && strings.Contains(out.String(), "Admin API") {
				t.Errorf("kept asking after declining\n--- output ---\n%s", out.String())
			}
		})
	}
}

func TestRunReportsWriteFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", "hl7gw.json")
	err := Run(path, strings.NewReader(defaultsSession), &bytes.Buffer{}, console.Terminal{})
	if _, ok := errors.AsType[*fs.PathError](err); !ok {
		t.Errorf("err = %v, want *fs.PathError", err)
	}
}

func TestRunOnATerminal(t *testing.T) {
	_, out, err := runWizard(t, strings.NewReader(defaultsSession), console.Terminal{TTY: true, Color: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "MSH|^~\\&|") || !strings.Contains(out, "\x1b[2m8081\x1b[0m") || !strings.Contains(out, "\x1b[32mWrote ") {
		t.Errorf("missing banner or colour\n--- output ---\n%s", out)
	}
	if strings.Contains(out, "Port [\x1b[2m8081\x1b[0m]: \n") {
		t.Errorf("newline echoed after a prompt on a terminal\n--- output ---\n%s", out)
	}
}

func TestRunDedupesExportLines(t *testing.T) {
	session := strings.Replace(twoChannels, "\n\nn\n", "\nADT_API_TOKEN\nn\n", 1)
	_, out, err := runWizard(t, strings.NewReader(session), console.Terminal{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(out, "export ADT_API_TOKEN=") != 1 {
		t.Errorf("export line not deduplicated\n--- output ---\n%s", out)
	}
}

func TestRunOffersNoTokenEnvDefaultForDigitLedNames(t *testing.T) {
	session := strings.Replace(defaultsSession, "adt\n", "3m\n", 1)
	session = strings.Replace(session, "https://api.example.com/hl7\n\n", "https://api.example.com/hl7\n\nTHREE_M_TOKEN\n", 1)
	path, out, err := runWizard(t, strings.NewReader(session), console.Terminal{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Destination token environment variable: ") {
		t.Errorf("expected no default\n--- output ---\n%s", out)
	}
	if got := readFile(t, path); !strings.Contains(got, `"token_env": "THREE_M_TOKEN"`) {
		t.Errorf("config\n%s", got)
	}
}
