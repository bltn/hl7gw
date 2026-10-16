package source

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bltn/hl7gw/internal/config"
	"github.com/bltn/hl7gw/internal/metrics"
	"github.com/bltn/hl7gw/internal/mllp"
)

type acceptStep struct {
	conn net.Conn
	err  error
}

type fakeListener struct {
	mu    sync.Mutex
	steps []acceptStep
}

func (l *fakeListener) Accept() (net.Conn, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	step := l.steps[0]
	if len(l.steps) > 1 {
		l.steps = l.steps[1:]
	}
	return step.conn, step.err
}

func (l *fakeListener) Close() error   { return nil }
func (l *fakeListener) Addr() net.Addr { return &net.TCPAddr{} }

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func newTestListener(steps []acceptStep) (*BoundListener, *lockedBuffer) {
	stderr := &lockedBuffer{}
	return &BoundListener{
		ch:  config.Channel{Name: "test", TCPListener: config.TCPListener{MaxConnections: 1}},
		m:   metrics.New("test", "").Channel("test"),
		ln:  &fakeListener{steps: steps},
		log: slog.New(slog.NewTextHandler(stderr, nil)),
	}, stderr
}

func TestAcceptRetriesWithBackoffThenSucceeds(t *testing.T) {
	t.Parallel()

	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	defer func() { _ = server.Close() }()

	acceptErr := errors.New("accept tcp: too many open files")
	bl, stderr := newTestListener([]acceptStep{
		{err: acceptErr},
		{err: acceptErr},
		{err: acceptErr},
		{conn: server},
	})

	conn, err := bl.accept(context.Background())
	if err != nil {
		t.Fatalf("accept returned error after transient failures: %v", err)
	}
	if conn != server {
		t.Fatalf("accept returned %v, want the scripted conn", conn)
	}

	logged := stderr.String()
	lines := strings.Split(strings.TrimSpace(logged), "\n")
	if len(lines) != 3 {
		t.Fatalf("got %d retry lines, want 3\n--- stderr ---\n%s", len(lines), logged)
	}
	for i, wantDelay := range []string{"5ms", "10ms", "20ms"} {
		want := `level=WARN msg="accept failed, retrying" backoff=` + wantDelay + ` err="` + acceptErr.Error() + `"`
		if !strings.HasSuffix(lines[i], want) {
			t.Errorf("retry line %d = %q, want suffix %q", i, lines[i], want)
		}
	}
}

func TestAcceptLoopShutsDownAfterAcceptFailureLimit(t *testing.T) {
	t.Parallel()

	acceptErr := errors.New("accept tcp: too many open files")
	bl, stderr := newTestListener([]acceptStep{{err: acceptErr}})

	err := bl.acceptLoop(context.Background(), context.Background(), nil)
	if err == nil {
		t.Fatal("acceptLoop returned nil, want give-up error")
	}
	if !strings.Contains(err.Error(), "accept failed 10 times, giving up") {
		t.Errorf("error = %q, want it to contain the give-up message", err)
	}
	if !errors.Is(err, acceptErr) {
		t.Errorf("error %q does not wrap the accept error", err)
	}

	retries := strings.Count(stderr.String(), `msg="accept failed, retrying"`)
	if retries != 9 {
		t.Errorf("got %d retry lines before giving up, want 9\n--- stderr ---\n%s", retries, stderr.String())
	}
}

func TestAcceptStopsBackoffWhenContextCancelled(t *testing.T) {
	t.Parallel()

	acceptErr := errors.New("accept tcp: too many open files")
	bl, stderr := newTestListener([]acceptStep{{err: acceptErr}})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	type result struct {
		conn net.Conn
		err  error
	}
	done := make(chan result, 1)
	go func() {
		conn, err := bl.accept(ctx)
		done <- result{conn, err}
	}()

	deadline := time.After(5 * time.Second)
	for !strings.Contains(stderr.String(), "backoff=160ms") {
		select {
		case <-deadline:
			t.Fatalf("never saw the 160ms retry line\n--- stderr ---\n%s", stderr.String())
		case <-time.After(time.Millisecond):
		}
	}
	cancel()

	select {
	case res := <-done:
		if res.conn != nil {
			t.Errorf("accept returned a conn after cancellation, want nil")
		}
		if !errors.Is(res.err, context.Canceled) {
			t.Errorf("accept error = %v, want context.Canceled", res.err)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("accept did not return promptly after context cancellation")
	}
}

func TestCloseReasonClassifiesEveryOutcome(t *testing.T) {
	t.Parallel()
	other := errors.New("reset by peer")
	cases := []struct {
		err        error
		wantReason string
		wantDetail bool
	}{
		{err: io.EOF, wantReason: "peer_closed"},
		{err: errDraining, wantReason: "drain"},
		{err: net.ErrClosed, wantReason: "drain"},
		{err: fmt.Errorf("%w: %w", errAckWrite, os.ErrDeadlineExceeded), wantReason: "write_error", wantDetail: true},
		{err: os.ErrDeadlineExceeded, wantReason: "timeout", wantDetail: true},
		{err: fmt.Errorf("%w, got 0x58", mllp.ErrBadTrailer), wantReason: "protocol_error", wantDetail: true},
		{err: other, wantReason: "error", wantDetail: true},
	}
	for _, c := range cases {
		reason, detail := closeReason(c.err)
		if reason != c.wantReason {
			t.Errorf("closeReason(%v) = %q, want %q", c.err, reason, c.wantReason)
		}
		if (detail != nil) != c.wantDetail {
			t.Errorf("closeReason(%v) detail = %v, want present=%v", c.err, detail, c.wantDetail)
		}
	}
}
