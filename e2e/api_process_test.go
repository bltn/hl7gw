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
	processMessageBody       = `{"channel":%q,"message":%q}`
	processMessagePath       = "/process-message?mode=full"
	processMessageDryRunPath = "/process-message?mode=dry-run"
)

func postProcess(t *testing.T, gw *harness.Gateway, pathAndQuery, body string) harness.APIResponse {
	t.Helper()
	resp, err := gw.APIPost(pathAndQuery, "Bearer "+gw.APIToken(), []byte(body))
	if err != nil {
		t.Fatalf("post %s: %v", pathAndQuery, err)
	}
	return resp
}

func TestProcessMessageRejectsBadRequests(t *testing.T) {
	t.Parallel()
	sink := harness.NewSink(t, http.StatusOK, sinkBody)
	gw, err := harness.StartGateway(t, harness.Channel{Name: channelName, DestURL: sink.URL()})
	if err != nil {
		t.Fatalf("start gateway: %v", err)
	}

	tests := []struct {
		name           string
		path           string
		body           string
		wantStatus     int
		wantReqSpecErr bool
	}{
		{name: "channel not supplied", path: processMessagePath, body: fmt.Sprintf(`{"message":%q}`, adtA01Message), wantStatus: http.StatusBadRequest, wantReqSpecErr: true},
		{name: "message not supplied", path: processMessagePath, body: fmt.Sprintf(`{"channel":%q}`, channelName), wantStatus: http.StatusBadRequest, wantReqSpecErr: true},
		{name: "message empty", path: processMessagePath, body: fmt.Sprintf(processMessageBody, channelName, ""), wantStatus: http.StatusBadRequest, wantReqSpecErr: true},
		{name: "channel unknown", path: processMessagePath, body: fmt.Sprintf(processMessageBody, "nope", adtA01Message), wantStatus: http.StatusNotFound},
		{name: "mode not supplied", path: "/process-message", body: fmt.Sprintf(processMessageBody, channelName, adtA01Message), wantStatus: http.StatusBadRequest, wantReqSpecErr: true},
		{name: "mode invalid", path: "/process-message?mode=sideways", body: fmt.Sprintf(processMessageBody, channelName, adtA01Message), wantStatus: http.StatusBadRequest, wantReqSpecErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resp := postProcess(t, gw, tc.path, tc.body)
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

func TestProcessMessageReportsParseFailure(t *testing.T) {
	t.Parallel()
	sink := harness.NewSink(t, http.StatusOK, sinkBody)
	gw, err := harness.StartGateway(t, harness.Channel{Name: channelName, DestURL: sink.URL()})
	if err != nil {
		t.Fatalf("start gateway: %v", err)
	}

	unparseables := []struct {
		name    string
		message string
	}{
		{name: "correlatable but unparseable", message: missingFieldSeparatorMessage},
		{name: "uncorrelatable garbage", message: garbageMessage},
	}
	for _, tc := range unparseables {
		t.Run(tc.name, func(t *testing.T) {
			resp := postProcess(t, gw, processMessagePath, fmt.Sprintf(processMessageBody, channelName, tc.message))
			if resp.Status != http.StatusUnprocessableEntity {
				t.Errorf("status = %d, want 422; body %s", resp.Status, resp.Body)
			}
			if resp.ReqSpecErr != nil || resp.RespSpecErr != nil {
				t.Errorf("reqSpecErr = %v, respSpecErr = %v; want spec-clean", resp.ReqSpecErr, resp.RespSpecErr)
			}
		})
	}

	page := getMessagesPage(t, gw, "/messages?channel="+channelName)
	if len(page.Data) != len(unparseables) {
		t.Fatalf("persisted messages = %d, want %d", len(page.Data), len(unparseables))
	}
	for i, m := range page.Data {
		if m.TransformError == nil || *m.TransformError != couldNotParseHL7 {
			t.Errorf("message %d transform_error = %v, want %q", i, m.TransformError, couldNotParseHL7)
		}
		if len(m.DeliveryAttempts) != 0 {
			t.Errorf("message %d delivery_attempts = %+v, want none", i, m.DeliveryAttempts)
		}
	}
	if rcvd := sink.Received(); len(rcvd) != 0 {
		t.Errorf("sink received %d requests, want none", len(rcvd))
	}
}

func TestProcessMessageReportsDeliveryFailure(t *testing.T) {
	t.Parallel()
	sink := harness.NewSink(t, http.StatusInternalServerError, sinkBody)
	gw, err := harness.StartGateway(t, harness.Channel{Name: channelName, DestURL: sink.URL()})
	if err != nil {
		t.Fatalf("start gateway: %v", err)
	}

	resp := postProcess(t, gw, processMessagePath, fmt.Sprintf(processMessageBody, channelName, adtA01Message))
	if resp.Status != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502; body %s", resp.Status, resp.Body)
	}
	if resp.ReqSpecErr != nil || resp.RespSpecErr != nil {
		t.Errorf("reqSpecErr = %v, respSpecErr = %v; want spec-clean", resp.ReqSpecErr, resp.RespSpecErr)
	}

	if !sink.WaitForReceived(2) {
		t.Fatalf("sink received %d requests, want 2 (inbound delivery retries once)", len(sink.Received()))
	}
	page := getMessagesPage(t, gw, "/messages?channel="+channelName)
	if len(page.Data) != 1 {
		t.Fatalf("persisted messages = %d, want 1", len(page.Data))
	}
	attempts := page.Data[0].DeliveryAttempts
	if len(attempts) != 2 {
		t.Fatalf("delivery_attempts = %+v, want 2 failed attempts", attempts)
	}
	for i, a := range attempts {
		if a.Outcome != outcomeFailed {
			t.Errorf("attempt %d outcome = %q, want %q", i, a.Outcome, outcomeFailed)
		}
		if a.ResponseStatusCode == nil || *a.ResponseStatusCode != http.StatusInternalServerError {
			t.Errorf("attempt %d response_status_code = %v, want 500", i, a.ResponseStatusCode)
		}
	}
}

func TestProcessMessageReportsStoreFailure(t *testing.T) {
	t.Parallel()
	sink := harness.NewSink(t, http.StatusOK, sinkBody)
	gw, err := harness.StartGateway(t, harness.Channel{Name: channelName, DestURL: sink.URL()})
	if err != nil {
		t.Fatalf("start gateway: %v", err)
	}

	release, err := harness.HoldWriteLock(t, gw.DBPath(channelName))
	if err != nil {
		t.Fatalf("holding write lock: %v", err)
	}

	resp := postProcess(t, gw, processMessagePath, fmt.Sprintf(processMessageBody, channelName, adtA01Message))
	if resp.Status != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body %s", resp.Status, resp.Body)
	}
	if resp.ReqSpecErr != nil || resp.RespSpecErr != nil {
		t.Errorf("reqSpecErr = %v, respSpecErr = %v; want spec-clean", resp.ReqSpecErr, resp.RespSpecErr)
	}
	if rcvd := sink.Received(); len(rcvd) != 0 {
		t.Errorf("sink received %d requests, want none (message never persisted)", len(rcvd))
	}
	release()

	resp = postProcess(t, gw, processMessagePath, fmt.Sprintf(processMessageBody, channelName, adtA01Message))
	if resp.Status != http.StatusOK {
		t.Errorf("status after releasing lock = %d, want 200; body %s", resp.Status, resp.Body)
	}
}

func TestProcessMessageReportsReadBackFailure(t *testing.T) {
	t.Parallel()
	sink := harness.NewSink(t, http.StatusOK, sinkBody)
	gw, err := harness.StartGateway(t, harness.Channel{Name: channelName, DestURL: sink.URL()})
	if err != nil {
		t.Fatalf("start gateway: %v", err)
	}

	poison := []string{
		dropAttempts,
		"CREATE TABLE delivery_attempts (id INTEGER PRIMARY KEY, message_id ANY, attempted_at ANY, created_at ANY, updated_at ANY, outcome ANY, response_status_code ANY, response_body ANY, payload ANY, origin ANY)",
		"INSERT INTO delivery_attempts (message_id, attempted_at, created_at, updated_at, outcome, payload, origin) VALUES (1, 'nope', 0, 0, 'SUCCESS', 'p', 'INBOUND')",
	}
	if err := harness.ExecSQL(gw.DBPath(channelName), poison...); err != nil {
		t.Fatalf("poisoning schema: %v", err)
	}

	resp := postProcess(t, gw, processMessagePath, fmt.Sprintf(processMessageBody, channelName, adtA01Message))
	if resp.Status != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (processed but could not be read back); body %s", resp.Status, resp.Body)
	}
	if resp.ReqSpecErr != nil || resp.RespSpecErr != nil {
		t.Errorf("reqSpecErr = %v, respSpecErr = %v; want spec-clean", resp.ReqSpecErr, resp.RespSpecErr)
	}
	if !sink.WaitForReceived(1) {
		t.Fatalf("sink received %d requests, want 1 (delivery happened before the read back failed)", len(sink.Received()))
	}
}

func TestProcessMessageDeliversMessage(t *testing.T) {
	t.Parallel()
	sink := harness.NewSink(t, http.StatusOK, sinkBody)
	gw, err := harness.StartGateway(t, harness.Channel{Name: channelName, DestURL: sink.URL()})
	if err != nil {
		t.Fatalf("start gateway: %v", err)
	}

	resp := postProcess(t, gw, processMessagePath, fmt.Sprintf(processMessageBody, channelName, adtA01Message))
	if resp.Status != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", resp.Status, resp.Body)
	}
	if resp.ReqSpecErr != nil || resp.RespSpecErr != nil {
		t.Errorf("reqSpecErr = %v, respSpecErr = %v; want spec-clean", resp.ReqSpecErr, resp.RespSpecErr)
	}

	var m apiMessage
	if err := json.Unmarshal(resp.Body, &m); err != nil {
		t.Fatalf("decoding process response: %v", err)
	}
	if m.RawMessage != adtA01Message {
		t.Errorf("raw_message mismatch:\n--- got ---\n%s\n--- want ---\n%s", m.RawMessage, adtA01Message)
	}
	if m.TransformedXML == nil || *m.TransformedXML != adtA01TransformedXML {
		t.Errorf("transformed_xml = %v, want the expected XML", m.TransformedXML)
	}
	if m.TransformError != nil {
		t.Errorf("transform_error = %q, want null", *m.TransformError)
	}
	if m.AckMessage != nil || m.AckOutcome != nil {
		t.Errorf("ack = %v/%v, want null (no MLLP ack on the API path)", m.AckMessage, m.AckOutcome)
	}
	if len(m.DeliveryAttempts) != 1 {
		t.Fatalf("delivery_attempts = %+v, want 1", m.DeliveryAttempts)
	}
	a := m.DeliveryAttempts[0]
	if a.Outcome != outcomeSuccess || a.Origin != originInbound || a.Payload != adtA01TransformedXML {
		t.Errorf("attempt = %+v, want SUCCESS INBOUND with the transformed XML payload", a)
	}
	if a.ResponseStatusCode == nil || *a.ResponseStatusCode != http.StatusOK {
		t.Errorf("response_status_code = %v, want 200", a.ResponseStatusCode)
	}
	if a.ResponseBody == nil || *a.ResponseBody != sinkBody {
		t.Errorf("response_body = %v, want %q", a.ResponseBody, sinkBody)
	}

	if !sink.WaitForReceived(1) {
		t.Fatalf("sink received %d requests, want 1", len(sink.Received()))
	}
	if got := sink.Received()[0]; got != adtA01TransformedXML {
		t.Errorf("sink request mismatch:\n--- got ---\n%s\n--- want ---\n%s", got, adtA01TransformedXML)
	}

	byID := getMessagesPage(t, gw, "/messages?channel="+channelName)
	if len(byID.Data) != 1 || byID.Data[0].ID != m.ID {
		t.Errorf("messages list = %+v, want the processed message with id %d", byID.Data, m.ID)
	}
}

func TestProcessMessageDryRunReportsParseFailure(t *testing.T) {
	t.Parallel()
	sink := harness.NewSink(t, http.StatusOK, sinkBody)
	gw, err := harness.StartGateway(t, harness.Channel{Name: channelName, DestURL: sink.URL()})
	if err != nil {
		t.Fatalf("start gateway: %v", err)
	}

	unparseables := []struct {
		name    string
		message string
	}{
		{name: "correlatable but unparseable", message: missingFieldSeparatorMessage},
		{name: "uncorrelatable garbage", message: garbageMessage},
	}
	for _, tc := range unparseables {
		t.Run(tc.name, func(t *testing.T) {
			resp := postProcess(t, gw, processMessageDryRunPath, fmt.Sprintf(processMessageBody, channelName, tc.message))
			if resp.Status != http.StatusUnprocessableEntity {
				t.Errorf("status = %d, want 422; body %s", resp.Status, resp.Body)
			}
			if resp.ReqSpecErr != nil || resp.RespSpecErr != nil {
				t.Errorf("reqSpecErr = %v, respSpecErr = %v; want spec-clean", resp.ReqSpecErr, resp.RespSpecErr)
			}
		})
	}

	page := getMessagesPage(t, gw, "/messages?channel="+channelName)
	if len(page.Data) != 0 {
		t.Errorf("persisted messages = %+v, want none in dry-run mode", page.Data)
	}
	if rcvd := sink.Received(); len(rcvd) != 0 {
		t.Errorf("sink received %d requests, want none", len(rcvd))
	}
}

func TestProcessMessageDryRunTransformsWithoutSideEffects(t *testing.T) {
	t.Parallel()
	sink := harness.NewSink(t, http.StatusInternalServerError, sinkBody)
	gw, err := harness.StartGateway(t, harness.Channel{Name: channelName, DestURL: sink.URL()})
	if err != nil {
		t.Fatalf("start gateway: %v", err)
	}

	release, err := harness.HoldWriteLock(t, gw.DBPath(channelName))
	if err != nil {
		t.Fatalf("holding write lock: %v", err)
	}

	resp := postProcess(t, gw, processMessageDryRunPath, fmt.Sprintf(processMessageBody, channelName, adtA01Message))
	if resp.Status != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", resp.Status, resp.Body)
	}
	if resp.ReqSpecErr != nil || resp.RespSpecErr != nil {
		t.Errorf("reqSpecErr = %v, respSpecErr = %v; want spec-clean", resp.ReqSpecErr, resp.RespSpecErr)
	}

	var body map[string]any
	if err := json.Unmarshal(resp.Body, &body); err != nil {
		t.Fatalf("decoding dry-run response: %v", err)
	}
	if _, ok := body["id"]; ok {
		t.Error("body has an id, want it absent in dry-run responses")
	}
	if body["raw_message"] != adtA01Message {
		t.Errorf("raw_message mismatch:\n--- got ---\n%v\n--- want ---\n%s", body["raw_message"], adtA01Message)
	}
	if body["transformed_xml"] != adtA01TransformedXML {
		t.Errorf("transformed_xml = %v, want the expected XML", body["transformed_xml"])
	}
	if body["transform_error"] != nil {
		t.Errorf("transform_error = %v, want null", body["transform_error"])
	}
	receivedAt, ok := body["received_at"].(string)
	if !ok {
		t.Errorf("received_at = %v, want a timestamp string", body["received_at"])
	} else if _, err := time.Parse(time.RFC3339, receivedAt); err != nil {
		t.Errorf("received_at = %q, want RFC 3339: %v", receivedAt, err)
	}
	release()

	page := getMessagesPage(t, gw, "/messages?channel="+channelName)
	if len(page.Data) != 0 {
		t.Errorf("persisted messages = %+v, want none in dry-run mode", page.Data)
	}
	attempts, err := harness.ReadDeliveryAttempts(gw.DBPath(channelName))
	if err != nil {
		t.Fatalf("reading delivery attempts: %v", err)
	}
	if len(attempts) != 0 {
		t.Errorf("delivery attempts = %+v, want none in dry-run mode", attempts)
	}
	if rcvd := sink.Received(); len(rcvd) != 0 {
		t.Errorf("sink received %d requests, want none", len(rcvd))
	}
}
