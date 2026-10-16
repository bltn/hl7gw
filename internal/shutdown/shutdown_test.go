package shutdown_test

import (
	"context"
	"testing"
	"time"

	"github.com/bltn/hl7gw/internal/shutdown"
)

type ctxKey struct{}

func TestDrainOutlivesTheStopSignal(t *testing.T) {
	t.Parallel()

	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	drain, expired, cleanup := shutdown.Begin(ctx, time.Hour)

	stop()
	<-ctx.Done()

	if err := drain.Err(); err != nil {
		t.Fatalf("drain context was cancelled with the stop signal: %v", err)
	}
	if expired() {
		t.Error("expired reported true while work was still within the limit")
	}

	cleanup()

	select {
	case <-drain.Done():
	case <-time.After(time.Second):
		t.Fatal("drain context still open after cleanup")
	}
	if expired() {
		t.Error("expired reported true after a drain that finished within the limit")
	}
}

func TestLimitForcesTheDrain(t *testing.T) {
	t.Parallel()

	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	drain, expired, cleanup := shutdown.Begin(ctx, time.Millisecond)
	defer cleanup()

	stop()

	select {
	case <-drain.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("drain context never closed after the limit elapsed")
	}
	if !expired() {
		t.Error("expired reported false after the limit forced the drain")
	}
}

func TestLimitDoesNotRunBeforeTheStopSignal(t *testing.T) {
	t.Parallel()

	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	drain, expired, cleanup := shutdown.Begin(ctx, time.Millisecond)
	defer cleanup()

	time.Sleep(50 * time.Millisecond)

	if err := drain.Err(); err != nil {
		t.Errorf("drain context cancelled without a stop signal: %v", err)
	}
	if expired() {
		t.Error("expired reported true without a stop signal")
	}
}

func TestDrainKeepsParentValues(t *testing.T) {
	t.Parallel()

	ctx := context.WithValue(context.Background(), ctxKey{}, "carried")
	drain, _, cleanup := shutdown.Begin(ctx, time.Hour)
	defer cleanup()

	if got := drain.Value(ctxKey{}); got != "carried" {
		t.Errorf("drain context value = %v, want %q", got, "carried")
	}
}
