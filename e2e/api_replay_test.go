//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/bltn/hl7gw/e2e/internal/harness"
)

const (
	originReplay = "REPLAY"
	replayBody   = `{"channel":%q,"message_id":%d}`
)

type replayAttempt struct {
	AttemptedAt        string  `json:"attempted_at"`
	Outcome            string  `json:"outcome"`
	ResponseStatusCode *int64  `json:"response_status_code"`
	ResponseBody       *string `json:"response_body"`
	Payload            string  `json:"payload"`
	Origin             string  `json:"origin"`
}

func postReplay(t *testing.T, gw *harness.Gateway, body string) harness.APIResponse {
	t.Helper()
	resp, err := gw.APIPost("/replay", "Bearer "+gw.APIToken(), []byte(body))
	if err != nil {
		t.Fatalf("post /replay: %v", err)
	}
	return resp
}

func TestReplayRejectsBadRequests(t *testing.T) {
	t.Parallel()
	sink := harness.NewSink(t, http.StatusOK, sinkBody)
	gw, err := harness.StartGateway(t, harness.Channel{Name: channelName, DestURL: sink.URL()})
	if err != nil {
		t.Fatalf("start gateway: %v", err)
	}

	tests := []struct {
		name           string
		body           string
		wantStatus     int
		wantReqSpecErr bool
	}{
		{name: "missing channel", body: `{"message_id":1}`, wantStatus: http.StatusBadRequest, wantReqSpecErr: true},
		{name: "missing message id", body: fmt.Sprintf(`{"channel":%q}`, channelName), wantStatus: http.StatusNotFound, wantReqSpecErr: true},
		{name: "unknown channel", body: fmt.Sprintf(replayBody, "nope", 1), wantStatus: http.StatusNotFound},
		{name: "unknown message id", body: fmt.Sprintf(replayBody, channelName, 999), wantStatus: http.StatusNotFound},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resp := postReplay(t, gw, tc.body)
			if resp.Status != tc.wantStatus {
				t.Errorf("status = %d, want %d; body %s", resp.Status, tc.wantStatus, resp.Body)
			}
			if resp.RespSpecErr != nil {
				t.Errorf("response violates openapi.yaml: %v", resp.RespSpecErr)
			}
			if gotErr := resp.ReqSpecErr != nil; gotErr != tc.wantReqSpecErr {
				t.Errorf("request spec validation error = %v, want error: %v", resp.ReqSpecErr, tc.wantReqSpecErr)
			}
		})
	}

	if rcvd := sink.Received(); len(rcvd) != 0 {
		t.Errorf("sink received %d requests, want none", len(rcvd))
	}
}

func TestReplayRejectsUndeliveredMessage(t *testing.T) {
	t.Parallel()
	sink := harness.NewSink(t, http.StatusOK, sinkBody)
	gw, err := harness.StartGateway(t, harness.Channel{Name: channelName, DestURL: sink.URL()})
	if err != nil {
		t.Fatalf("start gateway: %v", err)
	}

	if _, err := gw.SendAndAck(channelName, missingFieldSeparatorMessage); err != nil {
		t.Fatalf("send and ack: %v", err)
	}
	page, ok := pollForAckedMessage(t, gw)
	if !ok {
		t.Fatal("message never appeared with a recorded ack")
	}

	resp := postReplay(t, gw, fmt.Sprintf(replayBody, channelName, page.Data[0].ID))
	if resp.Status != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429; body %s", resp.Status, resp.Body)
	}
	if resp.ReqSpecErr != nil || resp.RespSpecErr != nil {
		t.Errorf("reqSpecErr = %v, respSpecErr = %v; want spec-clean", resp.ReqSpecErr, resp.RespSpecErr)
	}

	after := getMessagesPage(t, gw, "/messages?channel="+channelName)
	if attempts := after.Data[0].DeliveryAttempts; len(attempts) != 0 {
		t.Errorf("delivery_attempts = %+v, want still empty after rejected replay", attempts)
	}
	if rcvd := sink.Received(); len(rcvd) != 0 {
		t.Errorf("sink received %d requests, want none", len(rcvd))
	}
}

func TestReplayRedeliversMessage(t *testing.T) {
	t.Parallel()
	sink := harness.NewSink(t, http.StatusOK, sinkBody)
	gw, err := harness.StartGateway(t, harness.Channel{Name: channelName, DestURL: sink.URL()})
	if err != nil {
		t.Fatalf("start gateway: %v", err)
	}

	if _, err := gw.SendAndAck(channelName, adtA01Message); err != nil {
		t.Fatalf("send and ack: %v", err)
	}
	page, ok := pollForAckedMessage(t, gw)
	if !ok {
		t.Fatal("message never appeared with a recorded ack")
	}

	resp := postReplay(t, gw, fmt.Sprintf(replayBody, channelName, page.Data[0].ID))
	if resp.Status != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", resp.Status, resp.Body)
	}
	if resp.ReqSpecErr != nil || resp.RespSpecErr != nil {
		t.Errorf("reqSpecErr = %v, respSpecErr = %v; want spec-clean", resp.ReqSpecErr, resp.RespSpecErr)
	}

	var a replayAttempt
	if err := json.Unmarshal(resp.Body, &a); err != nil {
		t.Fatalf("decoding replay response: %v", err)
	}
	if a.Outcome != outcomeSuccess || a.Origin != originReplay || a.Payload != adtA01TransformedXML {
		t.Errorf("attempt = %+v, want SUCCESS REPLAY with the transformed XML payload", a)
	}
	if a.ResponseStatusCode == nil || *a.ResponseStatusCode != http.StatusOK {
		t.Errorf("response_status_code = %v, want 200", a.ResponseStatusCode)
	}
	if a.ResponseBody == nil || *a.ResponseBody != sinkBody {
		t.Errorf("response_body = %v, want %q", a.ResponseBody, sinkBody)
	}
	if _, err := time.Parse(time.RFC3339, a.AttemptedAt); err != nil {
		t.Errorf("attempted_at = %q, want RFC 3339: %v", a.AttemptedAt, err)
	}

	if !sink.WaitForReceived(2) {
		t.Fatalf("sink received %d requests, want 2", len(sink.Received()))
	}
	for i, got := range sink.Received() {
		if got != adtA01TransformedXML {
			t.Errorf("sink request %d mismatch:\n--- got ---\n%s\n--- want ---\n%s", i, got, adtA01TransformedXML)
		}
	}

	if err := gw.Stop(); err != nil {
		t.Errorf("gateway exited uncleanly: %v", err)
	}
	rows, err := harness.ReadDeliveryAttempts(gw.DBPath(channelName))
	if err != nil {
		t.Fatalf("reading delivery attempts: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("attempt rows = %+v, want 2", rows)
	}
	last := rows[1]
	if last.Outcome != outcomeSuccess || last.Origin != originReplay {
		t.Errorf("replay row = %+v, want SUCCESS REPLAY", last)
	}
	if last.ResponseStatusCode == nil || *last.ResponseStatusCode != http.StatusOK {
		t.Errorf("replay row status = %v, want 200", last.ResponseStatusCode)
	}
}

func TestReplayReportsTransportError(t *testing.T) {
	t.Parallel()
	dead, err := harness.DeadDestURL()
	if err != nil {
		t.Fatalf("dead destination url: %v", err)
	}
	gw, err := harness.StartGateway(t, harness.Channel{Name: channelName, DestURL: dead})
	if err != nil {
		t.Fatalf("start gateway: %v", err)
	}

	if _, err := gw.SendAndAck(channelName, adtA01Message); err != nil {
		t.Fatalf("send and ack: %v", err)
	}
	page, ok := pollForAckedMessage(t, gw)
	if !ok {
		t.Fatal("message never appeared with a recorded ack")
	}

	resp := postReplay(t, gw, fmt.Sprintf(replayBody, channelName, page.Data[0].ID))
	if resp.Status != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502; body %s", resp.Status, resp.Body)
	}
	if resp.ReqSpecErr != nil || resp.RespSpecErr != nil {
		t.Errorf("reqSpecErr = %v, respSpecErr = %v; want spec-clean", resp.ReqSpecErr, resp.RespSpecErr)
	}

	if err := gw.Stop(); err != nil {
		t.Errorf("gateway exited uncleanly: %v", err)
	}
	rows, err := harness.ReadDeliveryAttempts(gw.DBPath(channelName))
	if err != nil {
		t.Fatalf("reading delivery attempts: %v", err)
	}
	if len(rows) != 4 {
		t.Fatalf("attempt rows = %+v, want 4 (2 inbound + 2 replay)", rows)
	}
	for _, row := range rows[2:] {
		if row.Outcome != outcomeFailed || row.Origin != originReplay {
			t.Errorf("replay row = %+v, want FAILED REPLAY", row)
		}
		if row.ResponseStatusCode != nil || row.ResponseBody != nil {
			t.Errorf("replay row response = %v/%v, want null (no HTTP response)", row.ResponseStatusCode, row.ResponseBody)
		}
	}
}

func TestReplayReportsSinkRejection(t *testing.T) {
	t.Parallel()
	sink := harness.NewSink(t, http.StatusOK, sinkBody, http.StatusInternalServerError)
	gw, err := harness.StartGateway(t, harness.Channel{Name: channelName, DestURL: sink.URL()})
	if err != nil {
		t.Fatalf("start gateway: %v", err)
	}

	if _, err := gw.SendAndAck(channelName, adtA01Message); err != nil {
		t.Fatalf("send and ack: %v", err)
	}
	page, ok := pollForAckedMessage(t, gw)
	if !ok {
		t.Fatal("message never appeared with a recorded ack")
	}

	resp := postReplay(t, gw, fmt.Sprintf(replayBody, channelName, page.Data[0].ID))
	if resp.Status != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502; body %s", resp.Status, resp.Body)
	}
	if resp.ReqSpecErr != nil || resp.RespSpecErr != nil {
		t.Errorf("reqSpecErr = %v, respSpecErr = %v; want spec-clean", resp.ReqSpecErr, resp.RespSpecErr)
	}

	if err := gw.Stop(); err != nil {
		t.Errorf("gateway exited uncleanly: %v", err)
	}
	rows, err := harness.ReadDeliveryAttempts(gw.DBPath(channelName))
	if err != nil {
		t.Fatalf("reading delivery attempts: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("attempt rows = %+v, want 3 (1 inbound + 2 replay)", rows)
	}
	for _, row := range rows[1:] {
		if row.Outcome != outcomeFailed || row.Origin != originReplay {
			t.Errorf("replay row = %+v, want FAILED REPLAY", row)
		}
		if row.ResponseStatusCode == nil || *row.ResponseStatusCode != http.StatusInternalServerError {
			t.Errorf("replay row status = %v, want 500", row.ResponseStatusCode)
		}
	}
}

func TestReplayReportsMessageReadFailure(t *testing.T) {
	t.Parallel()
	sink := harness.NewSink(t, http.StatusOK, sinkBody)
	gw, err := harness.StartGateway(t, harness.Channel{Name: channelName, DestURL: sink.URL()})
	if err != nil {
		t.Fatalf("start gateway: %v", err)
	}

	if err := harness.ExecSQL(gw.DBPath(channelName), dropAttempts, dropMessages); err != nil {
		t.Fatalf("poisoning schema: %v", err)
	}

	resp := postReplay(t, gw, fmt.Sprintf(replayBody, channelName, 1))
	if resp.Status != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body %s", resp.Status, resp.Body)
	}
	if resp.ReqSpecErr != nil || resp.RespSpecErr != nil {
		t.Errorf("reqSpecErr = %v, respSpecErr = %v; want spec-clean", resp.ReqSpecErr, resp.RespSpecErr)
	}
	if rcvd := sink.Received(); len(rcvd) != 0 {
		t.Errorf("sink received %d requests, want none", len(rcvd))
	}
}

func TestReplayReportsStoreFailure(t *testing.T) {
	t.Parallel()
	sink := harness.NewSink(t, http.StatusOK, sinkBody)
	gw, err := harness.StartGateway(t, harness.Channel{Name: channelName, DestURL: sink.URL()})
	if err != nil {
		t.Fatalf("start gateway: %v", err)
	}

	if _, err := gw.SendAndAck(channelName, adtA01Message); err != nil {
		t.Fatalf("send and ack: %v", err)
	}
	page, ok := pollForAckedMessage(t, gw)
	if !ok {
		t.Fatal("message never appeared with a recorded ack")
	}

	release, err := harness.HoldWriteLock(t, gw.DBPath(channelName))
	if err != nil {
		t.Fatalf("holding write lock: %v", err)
	}

	resp := postReplay(t, gw, fmt.Sprintf(replayBody, channelName, page.Data[0].ID))
	if resp.Status != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body %s", resp.Status, resp.Body)
	}
	if resp.ReqSpecErr != nil || resp.RespSpecErr != nil {
		t.Errorf("reqSpecErr = %v, respSpecErr = %v; want spec-clean", resp.ReqSpecErr, resp.RespSpecErr)
	}
	if rcvd := sink.Received(); len(rcvd) != 2 {
		t.Errorf("sink received %d requests, want 2 (the replay post happened)", len(rcvd))
	}
	release()

	if err := gw.Stop(); err != nil {
		t.Errorf("gateway exited uncleanly: %v", err)
	}
	rows, err := harness.ReadDeliveryAttempts(gw.DBPath(channelName))
	if err != nil {
		t.Fatalf("reading delivery attempts: %v", err)
	}
	if len(rows) != 1 {
		t.Errorf("attempt rows = %+v, want only the inbound attempt", rows)
	}
}
