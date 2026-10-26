package retention

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/bltn/hl7gw/internal/config"
	"github.com/bltn/hl7gw/internal/metrics"
	"github.com/bltn/hl7gw/internal/store"
)

func TestRunSweepsEveryTickUntilCancelled(t *testing.T) {
	t.Parallel()
	st, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "ch.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = st.Close() }()
	id, err := st.CreateMessage(t.Context(), time.Now().AddDate(0, 0, -31), "MSH|^~\\&|old")
	if err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}

	s := New(map[string]*store.Store{"ch": st}, config.DefaultRetention, 10*time.Millisecond, slog.New(slog.DiscardHandler), metrics.New("test", ""))
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.Run(ctx)
	}()

	deadline := time.Now().Add(5 * time.Second)
	for {
		_, err := st.GetMessage(t.Context(), id)
		if errors.Is(err, store.ErrMessageNotFound) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("message %d still present after 5s of ticks (err=%v)", id, err)
		}
		time.Sleep(10 * time.Millisecond)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
}
