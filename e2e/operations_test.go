//go:build e2e

package e2e

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/bltn/hl7gw/e2e/internal/harness"
)

const (
	healthzPath = "/healthz"
	readyzPath  = "/readyz"
	metricsPath = "/metrics"
)

func TestProbesAnswer(t *testing.T) {
	t.Parallel()
	sink := harness.NewSink(t, http.StatusOK, "")
	gw, err := harness.StartGateway(t, harness.Channel{Name: channelName, DestURL: sink.URL()})
	if err != nil {
		t.Fatalf("start gateway: %v", err)
	}

	for _, path := range []string{healthzPath, readyzPath} {
		t.Run(path, func(t *testing.T) {
			resp, err := gw.HTTPDo(http.MethodGet, path)
			if err != nil {
				t.Fatalf("get %s: %v", path, err)
			}
			if resp.Status != http.StatusOK {
				t.Errorf("status = %d, want 200; body %s", resp.Status, resp.Body)
			}
			if string(resp.Body) != "ok" {
				t.Errorf("body = %q, want ok", resp.Body)
			}
			if ct := resp.Header.Get("Content-Type"); ct != "text/plain; charset=utf-8" {
				t.Errorf("Content-Type = %q, want text/plain", ct)
			}
		})
	}

	resp, err := gw.HTTPDo(http.MethodGet, "/messages")
	if err != nil {
		t.Fatalf("get /messages: %v", err)
	}
	if resp.Status != http.StatusNotFound {
		t.Errorf("/messages status = %d, want 404 (the metrics port serves no data)", resp.Status)
	}
	resp, err = gw.HTTPDo(http.MethodPost, healthzPath)
	if err != nil {
		t.Fatalf("post %s: %v", healthzPath, err)
	}
	if resp.Status != http.StatusMethodNotAllowed || resp.Header.Get("Allow") != "GET, HEAD" {
		t.Errorf("POST %s = %d with Allow %q, want 405 and GET, HEAD", healthzPath, resp.Status, resp.Header.Get("Allow"))
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

	resp, err := gw.HTTPDo(http.MethodGet, metricsPath)
	if err != nil {
		t.Fatalf("get /metrics: %v", err)
	}
	if resp.Status != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", resp.Status, resp.Body)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/plain; version=0.0.4") {
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
