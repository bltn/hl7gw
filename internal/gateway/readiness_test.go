package gateway

import (
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/bltn/hl7gw/internal/config"
	"github.com/bltn/hl7gw/internal/store"
)

func TestReadinessNamesUnavailableChannel(t *testing.T) {
	t.Parallel()
	st, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "ch.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	cfg := config.Config{Channels: []config.Channel{{Name: "ch"}}}
	ready := readiness(cfg, map[string]*store.Store{"ch": st}, slog.New(slog.DiscardHandler))

	err = ready(t.Context())
	if err == nil || err.Error() != "channel ch is not ready" {
		t.Fatalf("ready() = %v, want it to name channel ch", err)
	}
}
