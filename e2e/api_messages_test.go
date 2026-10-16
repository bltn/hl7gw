//go:build e2e

package e2e

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/bltn/hl7gw/e2e/internal/harness"
)

func TestAPIListsPersistedMessages(t *testing.T) {
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

	if page.Pagination.Total != 1 || len(page.Data) != 1 {
		t.Fatalf("page = %+v, want exactly one message", page)
	}
	m := page.Data[0]
	if m.RawMessage != adtA01Message {
		t.Errorf("raw_message mismatch:\n--- got ---\n%s\n--- want ---\n%s", m.RawMessage, adtA01Message)
	}
	if m.TransformedXML == nil || *m.TransformedXML != adtA01TransformedXML {
		t.Errorf("transformed_xml = %v, want the adt_a01 testdata XML", m.TransformedXML)
	}
	if m.TransformError != nil {
		t.Errorf("transform_error = %q, want null", *m.TransformError)
	}
	if m.AckMessage == nil || harness.NormalizeACK(*m.AckMessage) != harness.NormalizeACK(adtA01AckAA) {
		t.Errorf("ack_message = %v, want the adt_a01 AA ack", m.AckMessage)
	}
	if m.AckOutcome == nil || *m.AckOutcome != outcomeSuccess {
		t.Errorf("ack_outcome = %v, want %s", m.AckOutcome, outcomeSuccess)
	}
	for name, ts := range map[string]string{
		"created_at": m.CreatedAt, "updated_at": m.UpdatedAt, "received_at": m.ReceivedAt,
	} {
		if _, err := time.Parse(time.RFC3339, ts); err != nil {
			t.Errorf("message %s = %q, want RFC 3339: %v", name, ts, err)
		}
	}

	if len(m.DeliveryAttempts) != 1 {
		t.Fatalf("delivery_attempts = %+v, want exactly one", m.DeliveryAttempts)
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
	for name, ts := range map[string]string{
		"attempted_at": a.AttemptedAt, "created_at": a.CreatedAt, "updated_at": a.UpdatedAt,
	} {
		if _, err := time.Parse(time.RFC3339, ts); err != nil {
			t.Errorf("attempt %s = %q, want RFC 3339: %v", name, ts, err)
		}
	}

	if err := gw.Stop(); err != nil {
		t.Errorf("gateway exited uncleanly: %v", err)
	}
}

func TestAPIReportsUnparseableMessage(t *testing.T) {
	t.Parallel()
	sink := harness.NewSink(t, http.StatusOK, "")
	gw, err := harness.StartGateway(t, harness.Channel{Name: channelName, DestURL: sink.URL()})
	if err != nil {
		t.Fatalf("start gateway: %v", err)
	}

	ack, err := gw.SendAndAck(channelName, missingFieldSeparatorMessage)
	if err != nil {
		t.Fatalf("send and ack: %v", err)
	}
	if harness.NormalizeACK(ack) != harness.NormalizeACK(missingFieldSeparatorAckAE) {
		t.Fatalf("ack mismatch:\n--- got ---\n%s\n--- want ---\n%s", ack, missingFieldSeparatorAckAE)
	}

	page, ok := pollForAckedMessage(t, gw)
	if !ok {
		t.Fatal("message never appeared with a recorded ack")
	}

	m := page.Data[0]
	if m.TransformedXML != nil {
		t.Errorf("transformed_xml = %q, want null", *m.TransformedXML)
	}
	if m.TransformError == nil || *m.TransformError != couldNotParseHL7 {
		t.Errorf("transform_error = %v, want %s", m.TransformError, couldNotParseHL7)
	}
	if len(m.DeliveryAttempts) != 0 {
		t.Errorf("delivery_attempts = %+v, want empty", m.DeliveryAttempts)
	}
}

func TestAPIPaginatesMessages(t *testing.T) {
	t.Parallel()
	sink := harness.NewSink(t, http.StatusOK, "")
	gw, err := harness.StartGateway(t, harness.Channel{Name: channelName, DestURL: sink.URL()})
	if err != nil {
		t.Fatalf("start gateway: %v", err)
	}

	for range 3 {
		if _, err := gw.SendAndAck(channelName, adtA01Message); err != nil {
			t.Fatalf("send and ack: %v", err)
		}
	}

	first := getMessagesPage(t, gw, "/messages?channel="+channelName+"&pageSize=2")
	if len(first.Data) != 2 || first.Pagination.Total != 3 {
		t.Fatalf("page 1 = %d items of %d, want 2 of 3", len(first.Data), first.Pagination.Total)
	}
	if first.Links.Prev != "" || first.Links.Next == "" {
		t.Fatalf("page 1 links = %+v, want next and no prev", first.Links)
	}

	second := getMessagesPage(t, gw, first.Links.Next)
	if len(second.Data) != 1 || second.Pagination.PageNumber != 2 {
		t.Errorf("page 2 = %d items at page %d, want 1 item at page 2", len(second.Data), second.Pagination.PageNumber)
	}
	if second.Links.Next != "" || second.Links.Prev == "" {
		t.Errorf("page 2 links = %+v, want prev and no next", second.Links)
	}
	if second.Data[0].ID == first.Data[0].ID || second.Data[0].ID == first.Data[1].ID {
		t.Errorf("page 2 repeats an id from page 1: %+v vs %+v", second.Data, first.Data)
	}
}

func TestAPIScopesMessagesByChannel(t *testing.T) {
	t.Parallel()
	sink := harness.NewSink(t, http.StatusOK, "")
	gw, err := harness.StartGateway(t,
		harness.Channel{Name: alphaChannel, DestURL: sink.URL()},
		harness.Channel{Name: betaChannel, DestURL: sink.URL()})
	if err != nil {
		t.Fatalf("start gateway: %v", err)
	}

	if _, err := gw.SendAndAck(alphaChannel, adtA01Message); err != nil {
		t.Fatalf("send and ack: %v", err)
	}

	alpha := getMessagesPage(t, gw, "/messages?channel="+alphaChannel)
	beta := getMessagesPage(t, gw, "/messages?channel="+betaChannel)

	if alpha.Pagination.Total != 1 || len(alpha.Data) != 1 {
		t.Errorf("alpha = %+v, want the one sent message", alpha.Pagination)
	}
	if beta.Pagination.Total != 0 || len(beta.Data) != 0 {
		t.Errorf("beta = %+v, want no messages", beta.Pagination)
	}
}

func TestAPIGetsMessageByID(t *testing.T) {
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
	listed := page.Data[0]

	path := "/messages/" + strconv.FormatInt(listed.ID, 10) + "?channel=" + channelName
	resp, err := gw.APIGet(path, "Bearer "+gw.APIToken())
	if err != nil {
		t.Fatalf("get %s: %v", path, err)
	}
	if resp.Status != http.StatusOK || resp.ReqSpecErr != nil || resp.RespSpecErr != nil {
		t.Fatalf("get %s: status = %d, reqSpecErr = %v, respSpecErr = %v; want 200 and spec-clean; body %s",
			path, resp.Status, resp.ReqSpecErr, resp.RespSpecErr, resp.Body)
	}

	var got apiMessage
	if err := json.Unmarshal(resp.Body, &got); err != nil {
		t.Fatalf("decoding %s: %v", path, err)
	}
	if got.RawMessage != adtA01Message {
		t.Errorf("raw_message mismatch:\n--- got ---\n%s\n--- want ---\n%s", got.RawMessage, adtA01Message)
	}
	if got.TransformedXML == nil || *got.TransformedXML != adtA01TransformedXML {
		t.Errorf("transformed_xml = %v, want the adt_a01 testdata XML", got.TransformedXML)
	}
	if !reflect.DeepEqual(got, listed) {
		t.Errorf("message by id differs from the listed message:\n--- got ---\n%+v\n--- listed ---\n%+v", got, listed)
	}

	if err := gw.Stop(); err != nil {
		t.Errorf("gateway exited uncleanly: %v", err)
	}
}

const (
	dropMessages = "DROP TABLE messages"
	dropAttempts = "DROP TABLE delivery_attempts"
)

func TestAPIReportsStoreReadFailure(t *testing.T) {
	t.Parallel()
	poisons := []struct {
		name string
		sql  []string
	}{
		{name: "message query fails", sql: []string{
			dropAttempts, dropMessages,
		}},
		{name: "message scan fails", sql: []string{
			dropAttempts, dropMessages,
			"CREATE TABLE messages (id INTEGER PRIMARY KEY, received_at ANY, created_at ANY, updated_at ANY, raw ANY, xml ANY, transform_error ANY, ack ANY, ack_outcome ANY)",
			"INSERT INTO messages (id, received_at, created_at, updated_at, raw) VALUES (1, 'nope', 0, 0, 'raw')",
		}},
		{name: "attempts query fails", sql: []string{
			dropAttempts,
		}},
		{name: "attempt scan fails", sql: []string{
			dropAttempts,
			"CREATE TABLE delivery_attempts (id INTEGER PRIMARY KEY, message_id ANY, attempted_at ANY, created_at ANY, updated_at ANY, outcome ANY, response_status_code ANY, response_body ANY, payload ANY, origin ANY)",
			"INSERT INTO delivery_attempts (message_id, attempted_at, created_at, updated_at, outcome, payload, origin) VALUES (1, 'nope', 0, 0, 'SUCCESS', 'p', 'INBOUND')",
		}},
	}

	for _, tc := range poisons {
		t.Run(tc.name, func(t *testing.T) {
			sink := harness.NewSink(t, http.StatusOK, "")
			gw, err := harness.StartGateway(t, harness.Channel{Name: channelName, DestURL: sink.URL()})
			if err != nil {
				t.Fatalf("start gateway: %v", err)
			}

			if _, err := gw.SendAndAck(channelName, adtA01Message); err != nil {
				t.Fatalf("send and ack: %v", err)
			}
			if _, ok := pollForAckedMessage(t, gw); !ok {
				t.Fatal("message never appeared with a recorded ack")
			}
			if err := harness.ExecSQL(gw.DBPath(channelName), tc.sql...); err != nil {
				t.Fatalf("poisoning schema: %v", err)
			}

			for _, path := range []string{"/messages?channel=" + channelName, "/messages/1?channel=" + channelName} {
				resp, err := gw.APIGet(path, "Bearer "+gw.APIToken())
				if err != nil {
					t.Fatalf("get %s: %v", path, err)
				}
				if resp.Status != http.StatusInternalServerError {
					t.Errorf("get %s: status = %d, want 500; body %s", path, resp.Status, resp.Body)
				}
				if resp.ReqSpecErr != nil || resp.RespSpecErr != nil {
					t.Errorf("get %s: reqSpecErr = %v, respSpecErr = %v; want spec-clean", path, resp.ReqSpecErr, resp.RespSpecErr)
				}
			}
		})
	}
}

func TestAPIRejectsBadMessageParams(t *testing.T) {
	t.Parallel()
	sink := harness.NewSink(t, http.StatusOK, "")
	gw, err := harness.StartGateway(t, harness.Channel{Name: channelName, DestURL: sink.URL()})
	if err != nil {
		t.Fatalf("start gateway: %v", err)
	}

	tests := []struct {
		name           string
		path           string
		wantStatus     int
		wantReqSpecErr bool
	}{
		{name: "unknown channel", path: "/messages?channel=nope", wantStatus: http.StatusNotFound},
		{name: "missing channel", path: "/messages", wantStatus: http.StatusBadRequest, wantReqSpecErr: true},
		{name: "get unknown channel", path: "/messages/1?channel=nope", wantStatus: http.StatusNotFound},
		{name: "get unknown id", path: "/messages/424242?channel=" + channelName, wantStatus: http.StatusNotFound},
		{name: "get missing channel", path: "/messages/1", wantStatus: http.StatusBadRequest, wantReqSpecErr: true},
		{name: "get non-integer id", path: "/messages/abc?channel=" + channelName, wantStatus: http.StatusBadRequest, wantReqSpecErr: true},
		{name: "get id below one", path: "/messages/0?channel=" + channelName, wantStatus: http.StatusBadRequest, wantReqSpecErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := gw.APIGet(tc.path, "Bearer "+gw.APIToken())
			if err != nil {
				t.Fatalf("get %s: %v", tc.path, err)
			}
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
}
