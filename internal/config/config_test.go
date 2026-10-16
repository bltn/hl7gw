package config

import (
	"bytes"
	"fmt"
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
