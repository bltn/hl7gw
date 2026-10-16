package metrics

import (
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
)

func TestHandlerServesEveryInstrument(t *testing.T) {
	t.Parallel()
	s := New("v1", "abc")
	s.Channel("adt").MessagesTotal.WithLabelValues("success").Inc()
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/metrics", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain; version=0.0.4") {
		t.Errorf("Content-Type = %q, want the Prometheus text format", ct)
	}
	body := rec.Body.String()
	for _, want := range []string{
		`hl7gw_build_info{commit="abc",go_version="` + runtime.Version() + `",version="v1"} 1`,
		`hl7gw_messages_total{channel="adt",outcome="success"} 1`,
		"\ngo_goroutines ",
		"\ngo_memstats_heap_alloc_bytes ",
		"\nprocess_start_time_seconds ",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q\n--- body ---\n%s", want, body)
		}
	}
}
