//go:build e2e

package e2e

import (
	_ "embed"
	"encoding/json"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/bltn/hl7gw/e2e/internal/harness"
)

//go:embed testdata/adt_a01/pretty_replay.txt
var adtA01PrettyReplay string

const (
	subcommandReplay         = "replay"
	replaySynopsis           = "replay takes <channel> <id>"
	originReplay             = "REPLAY"
	msgDeliveryAttemptFailed = "delivery attempt failed"
)

type cliReplayAttempt struct {
	AttemptedAt        string  `json:"attempted_at"`
	Outcome            string  `json:"outcome"`
	ResponseStatusCode *int64  `json:"response_status_code"`
	ResponseBody       *string `json:"response_body"`
	Payload            string  `json:"payload"`
	Origin             string  `json:"origin"`
}

func replay(t *testing.T, gw *harness.Gateway, id int64) harness.ExitResult {
	t.Helper()
	return run(t, gw, "", subcommandReplay, channelName, strconv.FormatInt(id, 10))
}

func expectReplayAttemptsLogged(t *testing.T, gw *harness.Gateway, want int) {
	t.Helper()
	if n := gw.CountLogs(withAttr(isMsg(msgDeliveryAttemptFailed), "origin", "replay")); n != want {
		t.Errorf("gateway logged %d failed replay attempts, want %d\n--- logs ---\n%v", n, want, gw.Logs())
	}
}

func readAttempts(t *testing.T, gw *harness.Gateway) []harness.DeliveryAttemptRow {
	t.Helper()
	rows, err := harness.ReadDeliveryAttempts(gw.DBPath(channelName))
	if err != nil {
		t.Fatalf("reading delivery attempts: %v", err)
	}
	return rows
}

func TestReplayRejectsBadArguments(t *testing.T) {
	t.Parallel()
	sink := harness.NewSink(t, http.StatusOK, sinkBody)
	gw, err := harness.StartGateway(t, harness.Channel{Name: channelName, DestURL: sink.URL()})
	if err != nil {
		t.Fatalf("start gateway: %v", err)
	}

	cases := []struct {
		name       string
		args       []string
		wantCode   int
		wantStderr []string
	}{
		{"no arguments", nil, 2, []string{errorPrefix, replaySynopsis, usagePrefix}},
		{"missing message id", []string{channelName}, 2, []string{errorPrefix, replaySynopsis, usagePrefix}},
		{tooManyArguments, []string{channelName, "1", "2"}, 2, []string{errorPrefix, replaySynopsis, usagePrefix}},
		{nonIntegerIDCase, []string{channelName, "abc"}, 2, []string{errorPrefix, badIDAbc, usagePrefix}},
		{unknownChannelCase, []string{unknownChannel, "1"}, 1, []string{errorPrefix, noChannelNamedNope}},
		{"unknown message id", []string{channelName, "999"}, 1, []string{errorPrefix, "no message with id 999"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			expectFailure(t, run(t, gw, "", subcommandReplay, tc.args...), tc.wantCode, tc.wantStderr...)
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
	rows := waitForAcks(t, gw, channelName, 1)

	expectFailure(t, replay(t, gw, rows[0].ID), 1, errorPrefix, "message 1 has no delivery attempts to replay")
	if attempts := readAttempts(t, gw); len(attempts) != 0 {
		t.Errorf("delivery attempts = %+v, want still none after the rejected replay", attempts)
	}
	if rcvd := sink.Received(); len(rcvd) != 0 {
		t.Errorf("sink received %d requests, want none", len(rcvd))
	}
}

func TestReplayPrettyPrintsAttempt(t *testing.T) {
	t.Parallel()
	sink := harness.NewSink(t, http.StatusOK, sinkBody)
	gw, err := harness.StartGateway(t, harness.Channel{Name: channelName, DestURL: sink.URL()})
	if err != nil {
		t.Fatalf("start gateway: %v", err)
	}
	if _, err := gw.SendAndAck(channelName, adtA01Message); err != nil {
		t.Fatalf("send and ack: %v", err)
	}
	rows := waitForAcks(t, gw, channelName, 1)

	out := runOK(t, gw, "", subcommandReplay, flagPretty, channelName, strconv.FormatInt(rows[0].ID, 10))
	expectPretty(t, out, adtA01PrettyReplay)
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
	rows := waitForAcks(t, gw, channelName, 1)

	out := runOK(t, gw, "", subcommandReplay, channelName, strconv.FormatInt(rows[0].ID, 10))
	var a cliReplayAttempt
	if err := json.Unmarshal([]byte(out), &a); err != nil {
		t.Fatalf("decoding replay output: %v\n--- stdout ---\n%s", err, out)
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
	for i, auth := range sink.AuthHeaders() {
		if auth != "Bearer "+gw.ChannelToken() {
			t.Errorf("sink request %d authorization = %q, want the channel's bearer token", i, auth)
		}
	}

	attempts := readAttempts(t, gw)
	if len(attempts) != 2 {
		t.Fatalf("attempt rows = %+v, want 2", attempts)
	}
	last := attempts[1]
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
	rows := waitForAcks(t, gw, channelName, 1)

	expectFailure(t, replay(t, gw, rows[0].ID), 1, errorPrefix, "replay delivery failed")
	expectReplayAttemptsLogged(t, gw, 2)

	attempts := readAttempts(t, gw)
	if len(attempts) != 4 {
		t.Fatalf("attempt rows = %+v, want 4 (2 inbound + 2 replay)", attempts)
	}
	for _, row := range attempts[2:] {
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
	rows := waitForAcks(t, gw, channelName, 1)

	expectFailure(t, replay(t, gw, rows[0].ID), 1, errorPrefix, "replay delivery failed")
	expectReplayAttemptsLogged(t, gw, 2)

	attempts := readAttempts(t, gw)
	if len(attempts) != 3 {
		t.Fatalf("attempt rows = %+v, want 3 (1 inbound + 2 replay)", attempts)
	}
	for _, row := range attempts[1:] {
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

	expectFailure(t, replay(t, gw, 1), 1, errorPrefix, "replay failed")
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
	rows := waitForAcks(t, gw, channelName, 1)

	if _, err := harness.HoldWriteLock(t, gw.DBPath(channelName)); err != nil {
		t.Fatalf("holding write lock: %v", err)
	}
	expectFailure(t, replay(t, gw, rows[0].ID), 1, errorPrefix, "replay failed")
}
