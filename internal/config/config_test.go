package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bltn/hl7gw/internal/console"
)

func TestLoggerPicksHandlerByFormatAndTerminal(t *testing.T) {
	cases := []struct {
		format   string
		tty      bool
		wantJSON bool
	}{
		{LogFormatAuto, false, true},
		{LogFormatAuto, true, false},
		{LogFormatJSON, true, true},
		{LogFormatText, false, false},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("%s/tty=%t", tc.format, tc.tty), func(t *testing.T) {
			var b bytes.Buffer
			log := Log{Level: "debug", Format: tc.format}.Logger(&b, console.Terminal{TTY: tc.tty})
			log.Debug("probe", "k", "v")
			if got := strings.HasPrefix(b.String(), "{"); got != tc.wantJSON {
				t.Errorf("json=%v, want %v: %q", got, tc.wantJSON, b.String())
			}
		})
	}
}

func TestDestURLMarshalText(t *testing.T) {
	var empty DestURL
	if got, _ := empty.MarshalText(); string(got) != "" {
		t.Errorf("empty = %q", got)
	}
	var d DestURL
	if err := d.UnmarshalText([]byte("https://api.example.com/hl7")); err != nil {
		t.Fatal(err)
	}
	if got, _ := d.MarshalText(); string(got) != "https://api.example.com/hl7" {
		t.Errorf("set = %q", got)
	}
}

func TestDurationFormatsTersely(t *testing.T) {
	cases := map[Duration]string{
		Duration(time.Minute):                  "1m",
		Duration(time.Hour):                    "1h",
		Duration(2*time.Hour + 30*time.Minute): "2h30m",
		Duration(time.Minute + 30*time.Second): "1m30s",
		Duration(10 * time.Second):             "10s",
		0:                                      "0s",
	}
	for d, want := range cases {
		if got, _ := d.MarshalText(); string(got) != want {
			t.Errorf("%d = %q, want %q", time.Duration(d), got, want)
		}
		var back Duration
		if err := back.UnmarshalText([]byte(want)); err != nil || back != d {
			t.Errorf("%q parsed to %v, %v; want %v", want, back, err, d)
		}
	}
	var d Duration
	if err := d.UnmarshalText([]byte("60")); err == nil {
		t.Error("unitless duration accepted")
	}
}

func TestTLSOffOmitsMaterial(t *testing.T) {
	got, err := json.Marshal(&TLS{Mode: TLSModeOff})
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `{"mode":"off"}` {
		t.Errorf("got %s", got)
	}
}

func TestConfigRoundTripsThroughLoad(t *testing.T) {
	var dest DestURL
	if err := dest.UnmarshalText([]byte("https://api.example.com/hl7")); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	adminToken := filepath.Join(dir, "admin.token")
	adtToken := filepath.Join(dir, "adt.token")
	for path, token := range map[string]string{adminToken: "a\n", adtToken: " b "} {
		if err := os.WriteFile(path, []byte(token), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cfg := Config{
		Admin:              Admin{Port: 8081, TokenFile: adminToken},
		DataDir:            "data",
		CertReloadInterval: DefaultCertReloadInterval,
		Log:                Log{Level: "info", Format: LogFormatAuto},
		Channels: []Channel{{
			Name:        "adt",
			Source:      Source{MaxConnections: 64, Port: 2575, IdleTimeout: Duration(time.Minute), TLS: &TLS{Mode: TLSModeOff}},
			Destination: Destination{URL: dest, Auth: &Auth{Type: "bearer", TokenFile: adtToken}},
		}},
	}
	want, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "hl7gw.json")
	if err := os.WriteFile(path, want, 0o600); err != nil {
		t.Fatal(err)
	}

	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := json.MarshalIndent(loaded, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("round trip changed the config\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
	if loaded.Admin.Token() != "a" || loaded.Channels[0].Destination.Auth.Token() != "b" {
		t.Errorf("tokens = %q, %q; want trimmed file contents", loaded.Admin.Token(), loaded.Channels[0].Destination.Auth.Token())
	}
}
