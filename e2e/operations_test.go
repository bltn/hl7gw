//go:build e2e

package e2e

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/bltn/hl7gw/e2e/internal/harness"
)

func TestProbesAnswerWithoutAuth(t *testing.T) {
	t.Parallel()
	sink := harness.NewSink(t, http.StatusOK, "")
	gw, err := harness.StartGateway(t, harness.Channel{Name: channelName, DestURL: sink.URL()})
	if err != nil {
		t.Fatalf("start gateway: %v", err)
	}

	for _, path := range []string{healthzPath, readyzPath} {
		t.Run(path, func(t *testing.T) {
			resp, err := gw.APIGet(path, "")
			if err != nil {
				t.Fatalf("get %s: %v", path, err)
			}
			if resp.Status != http.StatusOK {
				t.Errorf("status = %d, want 200; body %s", resp.Status, resp.Body)
			}
			if string(resp.Body) != "ok" {
				t.Errorf("body = %q, want ok", resp.Body)
			}
			if resp.ReqSpecErr != nil || resp.RespSpecErr != nil {
				t.Errorf("reqSpecErr = %v, respSpecErr = %v; want spec-clean", resp.ReqSpecErr, resp.RespSpecErr)
			}
		})
	}
}

func TestMetricsCountTheMessageFlow(t *testing.T) {
	t.Parallel()
	sink := harness.NewSink(t, http.StatusOK, "")
	gw, err := harness.StartGateway(t, harness.Channel{Name: channelName, DestURL: sink.URL()})
	if err != nil {
		t.Fatalf("start gateway: %v", err)
	}

	if _, err := gw.SendAndAck(channelName, adtA01Message); err != nil {
		t.Fatalf("send and ack: %v", err)
	}

	resp, err := gw.APIGet(metricsPath, "")
	if err != nil {
		t.Fatalf("get /metrics: %v", err)
	}
	if resp.Status != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", resp.Status, resp.Body)
	}
	if resp.ReqSpecErr != nil || resp.RespSpecErr != nil {
		t.Errorf("reqSpecErr = %v, respSpecErr = %v; want spec-clean", resp.ReqSpecErr, resp.RespSpecErr)
	}
	raw, err := gw.APIRaw(http.MethodGet, metricsPath, "", nil)
	if err != nil {
		t.Fatalf("raw get /metrics: %v", err)
	}
	if ct := raw.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/plain; version=0.0.4") {
		t.Errorf("Content-Type = %q, want the Prometheus text format", ct)
	}

	for _, want := range []string{
		`hl7gw_messages_total{channel="e2e",outcome="success"} 1`,
		`hl7gw_delivery_attempts_total{channel="e2e",origin="inbound",result="success",status_code="200"} 1`,
		`hl7gw_acks_total{channel="e2e",code="aa"} 1`,
		`hl7gw_message_duration_seconds_count{channel="e2e"} 1`,
		`hl7gw_connections_open{channel="e2e"} 0`,
		`hl7gw_connections_total{channel="e2e",close_reason="peer_closed"} 1`,
		"hl7gw_build_info{commit=",
		"\nprocess_start_time_seconds ",
		"\ngo_goroutines ",
	} {
		if body, ok := gw.WaitForMetricLine(want, 5*time.Second); !ok {
			t.Errorf("metrics missing %q\n--- body ---\n%s", want, body)
		}
	}
}
