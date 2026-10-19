package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
	cfg := Config{
		AdminAPI:                 AdminAPI{Port: 8081, TokenEnv: "ROUNDTRIP_ADMIN"},
		DataDir:                  "data",
		TLSReloadIntervalSeconds: 120,
		Log:                      Log{Level: "info", Format: LogFormatAuto},
		Channels: []Channel{{
			Name:        "adt",
			TCPListener: TCPListener{MaxConnections: 64, Port: 2575, IdleTimeoutSeconds: 60, TLS: &TLS{Mode: TLSModeOff}},
			Destination: Destination{URL: dest, Auth: &Auth{Type: "bearer", TokenEnv: "ROUNDTRIP_ADT"}}, //nolint:gosec // G101 -- env var name, not a credential
		}},
	}
	want, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "hl7gw.json")
	if err := os.WriteFile(path, want, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ROUNDTRIP_ADMIN", "a")
	t.Setenv("ROUNDTRIP_ADT", "b")

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
}
