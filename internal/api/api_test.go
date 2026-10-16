package api

import (
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bltn/hl7gw/internal/config"
	"github.com/bltn/hl7gw/internal/metrics"
	"github.com/bltn/hl7gw/internal/store"
)

func TestIgnoreServerClosed(t *testing.T) {
	t.Parallel()
	if err := ignoreServerClosed(http.ErrServerClosed); err != nil {
		t.Errorf("ignoreServerClosed(ErrServerClosed) = %v, want nil", err)
	}
	boom := errors.New("boom")
	if err := ignoreServerClosed(boom); !errors.Is(err, boom) {
		t.Errorf("ignoreServerClosed(other error) = %v, want it back unchanged", err)
	}
}

func TestReadyzReportsUnavailableStore(t *testing.T) {
	t.Parallel()
	st, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "ch.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	h := newHandler([]config.Channel{{Name: "ch"}}, map[string]*store.Store{"ch": st}, nil, "token", nil, slog.New(slog.DiscardHandler), metrics.New("test", ""))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/readyz", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/problem+json" {
		t.Errorf("Content-Type = %q, want application/problem+json", ct)
	}
	if body := rec.Body.String(); !strings.Contains(body, "channel ch is not ready") {
		t.Errorf("body = %s, want it to name the channel", body)
	}
}
