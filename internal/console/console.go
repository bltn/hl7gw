package console

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	ansiReset  = "\x1b[0m"
	ansiDim    = "\x1b[2m"
	ansiRed    = "\x1b[31m"
	ansiGreen  = "\x1b[32m"
	ansiYellow = "\x1b[33m"
	ansiCyan   = "\x1b[36m"
)

const banner = "" +
	" _     _ _____\n" +
	"| |__ | |___  |  __ _ __      __\n" +
	"| '_ \\| |  / /  / _` |\\ \\ /\\ / /\n" +
	"| | | | | / /  | (_| | \\ V  V /\n" +
	"|_| |_|_|/_/    \\__, |  \\_/\\_/\n" +
	"                |___/\n" +
	"\n" +
	"MSH|^~\\&|  MLLP in, HTTPS out\n"

type Terminal struct {
	TTY   bool
	Color bool
}

func Detect(f *os.File) Terminal {
	fi, err := f.Stat()
	tty := err == nil && fi.Mode()&os.ModeCharDevice != 0
	return Terminal{
		TTY:   tty,
		Color: tty && os.Getenv("NO_COLOR") == "" && os.Getenv("TERM") != "dumb",
	}
}

func (t Terminal) Banner(w io.Writer) {
	if !t.TTY {
		return
	}
	_, _ = io.WriteString(w, paint(t.Color, ansiCyan, banner)+"\n")
}

func (t Terminal) Handler(w io.Writer, level slog.Level) slog.Handler {
	return &handler{w: w, mu: &sync.Mutex{}, level: level, color: t.Color}
}

type handler struct {
	w      io.Writer
	mu     *sync.Mutex
	level  slog.Level
	color  bool
	attrs  string
	groups string
}

func (h *handler) Enabled(_ context.Context, l slog.Level) bool {
	return l >= h.level
}

func (h *handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	var b bytes.Buffer
	for _, a := range attrs {
		h.appendAttr(&b, h.groups, a)
	}
	c := *h
	c.attrs += b.String()
	return &c
}

func (h *handler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	c := *h
	c.groups += name + "."
	return &c
}

func (h *handler) Handle(_ context.Context, r slog.Record) error {
	var b bytes.Buffer
	if !r.Time.IsZero() {
		b.WriteString(paint(h.color, ansiDim, r.Time.Format("15:04:05")))
		b.WriteByte(' ')
	}
	b.WriteString(paint(h.color, levelColor(r.Level), fmt.Sprintf("%-5s", r.Level)))
	b.WriteByte(' ')
	b.WriteString(r.Message)
	b.WriteString(h.attrs)
	r.Attrs(func(a slog.Attr) bool {
		h.appendAttr(&b, h.groups, a)
		return true
	})
	b.WriteByte('\n')

	h.mu.Lock()
	defer h.mu.Unlock()
	_, err := h.w.Write(b.Bytes())
	return err
}

func (h *handler) appendAttr(b *bytes.Buffer, prefix string, a slog.Attr) {
	a.Value = a.Value.Resolve()
	if a.Equal(slog.Attr{}) {
		return
	}
	if a.Value.Kind() == slog.KindGroup {
		attrs := a.Value.Group()
		if len(attrs) == 0 {
			return
		}
		if a.Key != "" {
			prefix += a.Key + "."
		}
		for _, ga := range attrs {
			h.appendAttr(b, prefix, ga)
		}
		return
	}
	b.WriteByte(' ')
	b.WriteString(paint(h.color, ansiDim, prefix+a.Key+"="))
	value := formatValue(a.Value)
	if a.Key == "err" {
		value = paint(h.color, ansiRed, value)
	}
	b.WriteString(value)
}

func paint(on bool, code, s string) string {
	if !on {
		return s
	}
	return code + s + ansiReset
}

func formatValue(v slog.Value) string {
	s := v.String()
	if v.Kind() == slog.KindTime {
		s = v.Time().Format(time.RFC3339)
	}
	if needsQuoting(s) {
		return strconv.Quote(s)
	}
	return s
}

func needsQuoting(s string) bool {
	if s == "" {
		return true
	}
	return strings.ContainsFunc(s, func(r rune) bool {
		return r == ' ' || r == '"' || r == '=' || r < ' ' || r == 0x7f
	})
}

func levelColor(l slog.Level) string {
	switch {
	case l >= slog.LevelError:
		return ansiRed
	case l >= slog.LevelWarn:
		return ansiYellow
	case l >= slog.LevelInfo:
		return ansiGreen
	default:
		return ansiDim
	}
}
