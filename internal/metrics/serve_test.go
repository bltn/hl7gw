package metrics

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestReadyzReportsNotReady(t *testing.T) {
	t.Parallel()
	h := New("test", "").handler(func(context.Context) error { return errors.New("channel ch is not ready") })

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/readyz", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/plain; charset=utf-8" {
		t.Errorf("Content-Type = %q, want text/plain", ct)
	}
	if body := rec.Body.String(); !strings.Contains(body, "channel ch is not ready") {
		t.Errorf("body = %s, want it to name the channel", body)
	}
}
