package console

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

var stamp = time.Date(2026, 1, 2, 15, 4, 5, 0, time.Local)

func TestDetectRegularFileIsNotATerminal(t *testing.T) {
	f, err := os.Create(filepath.Join(t.TempDir(), "out"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()

	if got := Detect(f); got != (Terminal{}) {
		t.Errorf("Detect(file) = %+v, want no tty and no colour", got)
	}
}

func TestBanner(t *testing.T) {
	cases := []struct {
		name string
		term Terminal
		want func(string) bool
	}{
		{"silent off a terminal", Terminal{}, func(s string) bool { return s == "" }},
		{"plain on a terminal", Terminal{TTY: true}, func(s string) bool {
			return strings.Contains(s, "MSH|^~\\&|") && !strings.Contains(s, "\x1b[") && strings.HasSuffix(s, "\n\n")
		}},
		{"coloured when allowed", Terminal{TTY: true, Color: true}, func(s string) bool {
			return strings.HasPrefix(s, ansiCyan) && strings.Contains(s, ansiReset)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var b bytes.Buffer
			tc.term.Banner(&b)
			if !tc.want(b.String()) {
				t.Errorf("banner output %q", b.String())
			}
		})
	}
}

func TestHandlerFormatsRecords(t *testing.T) {
	cases := []struct {
		name  string
		color bool
		log   func(*slog.Logger)
		want  string
	}{
		{
			name: "info with attrs",
			log:  func(l *slog.Logger) { l.Info("listening", "channel", "adt", "addr", ":3000") },
			want: "15:04:05 INFO  listening channel=adt addr=:3000\n",
		},
		{
			name: "debug",
			log:  func(l *slog.Logger) { l.Debug("connection opened", "remote", "10.0.0.5:1") },
			want: "15:04:05 DEBUG connection opened remote=10.0.0.5:1\n",
		},
		{
			name: "warn",
			log:  func(l *slog.Logger) { l.Warn("at capacity", "max", 64) },
			want: "15:04:05 WARN  at capacity max=64\n",
		},
		{
			name: "error",
			log:  func(l *slog.Logger) { l.Error("boom") },
			want: "15:04:05 ERROR boom\n",
		},
		{
			name: "quoting",
			log: func(l *slog.Logger) {
				l.Info("m", "space", "a b", "quote", `a"b`, "equals", "a=b", "empty", "", "ctrl", "a\tb", "del", "a\x7fb")
			},
			want: "15:04:05 INFO  m space=\"a b\" quote=\"a\\\"b\" equals=\"a=b\" empty=\"\" ctrl=\"a\\tb\" del=\"a\\x7fb\"\n",
		},
		{
			name: "time and duration kinds",
			log: func(l *slog.Logger) {
				l.Info("m", "at", time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), "took", 1500*time.Millisecond)
			},
			want: "15:04:05 INFO  m at=2026-01-02T03:04:05Z took=1.5s\n",
		},
		{
			name: "with attrs and groups",
			log: func(l *slog.Logger) {
				l.With("channel", "c", slog.Group("empty")).WithGroup("req").Info("m", "method", "GET",
					slog.Group("tls", "cn", "x"), slog.Group("", "inline", 1), slog.Attr{})
			},
			want: "15:04:05 INFO  m channel=c req.method=GET req.tls.cn=x req.inline=1\n",
		},
		{
			name: "resolves log valuers",
			log:  func(l *slog.Logger) { l.Info("m", "v", slog.AnyValue(valuer{})) },
			want: "15:04:05 INFO  m v=resolved\n",
		},
		{
			name:  "coloured",
			color: true,
			log:   func(l *slog.Logger) { l.Warn("tls handshake failed", "err", errors.New("bad cert")) },
			want: ansiDim + "15:04:05" + ansiReset + " " + ansiYellow + "WARN " + ansiReset + " tls handshake failed " +
				ansiDim + "err=" + ansiReset + ansiRed + "\"bad cert\"" + ansiReset + "\n",
		},
		{
			name:  "coloured levels",
			color: true,
			log: func(l *slog.Logger) {
				l.Debug("d")
				l.Info("i")
				l.Error("e")
			},
			want: ansiDim + "15:04:05" + ansiReset + " " + ansiDim + "DEBUG" + ansiReset + " d\n" +
				ansiDim + "15:04:05" + ansiReset + " " + ansiGreen + "INFO " + ansiReset + " i\n" +
				ansiDim + "15:04:05" + ansiReset + " " + ansiRed + "ERROR" + ansiReset + " e\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var b bytes.Buffer
			h := Terminal{TTY: true, Color: tc.color}.Handler(&b, slog.LevelDebug)
			tc.log(slog.New(fixedTime{h}))
			if b.String() != tc.want {
				t.Errorf("got  %q\nwant %q", b.String(), tc.want)
			}
		})
	}
}

func TestHandlerWithEmptyGroupNameIsIdentity(t *testing.T) {
	h := Terminal{}.Handler(&bytes.Buffer{}, slog.LevelInfo)
	if h.WithGroup("") != h {
		t.Error("WithGroup(\"\") did not return the receiver")
	}
}

func TestHandlerOmitsZeroTime(t *testing.T) {
	var b bytes.Buffer
	h := Terminal{}.Handler(&b, slog.LevelInfo)
	r := slog.NewRecord(time.Time{}, slog.LevelInfo, "no clock", 0)
	if err := h.Handle(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	if got := b.String(); got != "INFO  no clock\n" {
		t.Errorf("got %q", got)
	}
}

func TestHandlerHonoursLevel(t *testing.T) {
	var b bytes.Buffer
	l := slog.New(Terminal{}.Handler(&b, slog.LevelWarn))
	l.Info("dropped")
	l.Warn("kept")
	if got := b.String(); strings.Contains(got, "dropped") || !strings.Contains(got, "kept") {
		t.Errorf("got %q", got)
	}
}

func TestHandlerReportsWriteErrors(t *testing.T) {
	h := Terminal{}.Handler(failingWriter{}, slog.LevelInfo)
	err := h.Handle(context.Background(), slog.NewRecord(stamp, slog.LevelInfo, "m", 0))
	if !errors.Is(err, errWrite) {
		t.Errorf("err = %v, want %v", err, errWrite)
	}
}

func TestHandlerWritesWholeLinesConcurrently(t *testing.T) {
	var b lockedBuffer
	l := slog.New(Terminal{}.Handler(&b, slog.LevelInfo))
	var wg sync.WaitGroup
	for i := range 50 {
		wg.Go(func() { l.With("worker", i).Info("tick", "n", i) })
	}
	wg.Wait()
	lines := strings.Split(strings.TrimSuffix(b.String(), "\n"), "\n")
	if len(lines) != 50 {
		t.Fatalf("got %d lines, want 50", len(lines))
	}
	for _, line := range lines {
		if !strings.Contains(line, " INFO  tick worker=") {
			t.Errorf("mangled line %q", line)
		}
	}
}

type fixedTime struct{ slog.Handler }

func (f fixedTime) Handle(ctx context.Context, r slog.Record) error {
	r.Time = stamp
	return f.Handler.Handle(ctx, r)
}

func (f fixedTime) WithAttrs(attrs []slog.Attr) slog.Handler {
	return fixedTime{f.Handler.WithAttrs(attrs)}
}

func (f fixedTime) WithGroup(name string) slog.Handler {
	return fixedTime{f.Handler.WithGroup(name)}
}

type valuer struct{}

func (valuer) LogValue() slog.Value { return slog.StringValue("resolved") }

var errWrite = errors.New("write failed")

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errWrite }

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}
