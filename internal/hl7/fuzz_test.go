package hl7

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func FuzzParseMessage(f *testing.F) {
	for _, seed := range []string{
		"",
		"MSH",
		"MSH|^~\\&",
		"\r\r",
		adtA01,
		adtA01 + "PID\r",
		"MSH|^~\\&|APP|FAC|||20240101000000||ADT^A01|M\\X0D\\MSA|P|2.5\r",
		"MSHM^~\\&MAPPMFACMMM20240101000000MMADT^A01MMSG1MPM2.5\r",
	} {
		f.Add(seed)
	}
	for _, seed := range e2eMessages(f) {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, msg string) {
		m, err := ParseMessage(msg)
		if err != nil {
			uncorrelatable := errors.Is(err, ErrUncorrelatable)
			cerr, correlatable := errors.AsType[*CorrelatableError](err)
			if uncorrelatable == correlatable {
				t.Fatalf("error is not exactly one of the declared kinds: %v", err)
			}
			if correlatable && cerr.MSH.ControlID() == "" {
				t.Fatalf("correlatable error carries no control ID: %v", err)
			}
			return
		}

		cid := m.MSH.ControlID()
		if cid == "" {
			t.Fatal("parsed message has no control ID")
		}
		if err := wellFormed(XML(m)); err != nil {
			t.Fatalf("XML is not well-formed: %v", err)
		}
		for _, code := range []AckCode{Accept, Error, Reject} {
			if err := ackEchoes(m.Ack(code, ackTime, "ACK1"), "ACK1", cid); err != nil {
				t.Fatalf("%s ACK: %v", code, err)
			}
		}
	})
}

func FuzzUnescape(f *testing.F) {
	for _, seed := range []string{"", "a\\F\\b", "\\", "a\\b", "\\X0D\\", "\\XZZ\\", "é|^~\\&", "\x00\x1c\x7f"} {
		f.Add(seed)
	}
	esc := defaultDelimiters.escaper().Replace

	f.Fuzz(func(t *testing.T, s string) {
		if got := unescape(esc(s), defaultDelimiters); got != s {
			t.Fatalf("unescape(escape(%q)) = %q", s, got)
		}
		if got := unescape(s, defaultDelimiters); !strings.Contains(s, defaultDelimiters.Escape) && got != s {
			t.Fatalf("unescape(%q) = %q without an escape character present", s, got)
		}
	})
}

func e2eMessages(f *testing.F) []string {
	paths, err := filepath.Glob("../../e2e/testdata/*/message.txt")
	if err != nil || len(paths) == 0 {
		f.Fatalf("no e2e message files found: %v", err)
	}
	seeds := make([]string, 0, len(paths))
	for _, path := range paths {
		data, err := os.ReadFile(path) //nolint:gosec // G304 -- fixed glob under the repo's own testdata
		if err != nil {
			f.Fatalf("read %s: %v", path, err)
		}
		seeds = append(seeds, string(data))
	}

	return seeds
}
