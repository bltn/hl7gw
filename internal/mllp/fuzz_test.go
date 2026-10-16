package mllp

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

func FuzzMLLPRoundTrip(f *testing.F) {
	for _, seed := range []string{"", "MSH|^~\\&|", "a\x1cb", "a\x1c\rb", "\x0b\x0b", "x\x1c", "\r"} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, body string) {
		var buf bytes.Buffer
		if err := WriteFrame(body, bufio.NewWriter(&buf)); err != nil {
			t.Fatalf("WriteFrame: %v", err)
		}
		r := bufio.NewReader(&buf)
		if err := ReadUntilStart(r); err != nil {
			t.Fatalf("ReadUntilStart: %v", err)
		}
		got, err := ReadFrame(r)

		prefix, rest, cut := strings.Cut(body, "\x1c")
		switch {
		case !cut:
			if err != nil || got != body {
				t.Fatalf("ReadFrame = %q, %v; want %q, nil", got, err, body)
			}
		case strings.HasPrefix(rest, "\r"):
			if err != nil || got != prefix {
				t.Fatalf("ReadFrame = %q, %v; want the body truncated to %q, nil", got, err, prefix)
			}
		default:
			if !errors.Is(err, ErrBadTrailer) {
				t.Fatalf("ReadFrame = %q, %v; want ErrBadTrailer", got, err)
			}
		}
	})
}

func FuzzReadFrame(f *testing.F) {
	for _, seed := range [][]byte{nil, []byte("\x0b"), []byte("MSH\x1c\r"), []byte("\x1cX"), []byte("\x1c"), []byte("\x0bMSH\x1c\r\x0b")} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		if err := ReadUntilStart(bufio.NewReader(bytes.NewReader(data))); err != nil && !errors.Is(err, io.EOF) {
			t.Fatalf("ReadUntilStart: %v", err)
		}
		if _, err := ReadFrame(bufio.NewReader(bytes.NewReader(data))); err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, ErrBadTrailer) {
			t.Fatalf("ReadFrame: %v", err)
		}
	})
}
