//go:build e2e

package e2e

import (
	_ "embed"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/bltn/hl7gw/e2e/internal/harness"
)

//go:embed testdata/adt_a01/pretty_dry_run.txt
var adtA01PrettyDryRun string

//go:embed testdata/adt_a01/pretty_process.txt
var adtA01PrettyProcess string

const (
	subcommandProcess = "process"
	flagDryRun        = "-dry-run"
	couldNotParse     = "could not be parsed as HL7"
)

func messageFile(t *testing.T, msg string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "message.hl7")
	if err := os.WriteFile(path, []byte(msg), 0o600); err != nil {
		t.Fatalf("write message file: %v", err)
	}
	return path
}

func readMessageRows(t *testing.T, gw *harness.Gateway) []harness.MessageRow {
	t.Helper()
	rows, err := harness.ReadMessages(gw.DBPath(channelName))
	if err != nil {
		t.Fatalf("reading messages: %v", err)
	}
	return rows
}

func TestProcessExitsOnInterruptWhileReadingStdin(t *testing.T) {
	t.Parallel()
	sink := harness.NewSink(t, http.StatusOK, "")
	gw, err := harness.StartGateway(t, harness.Channel{Name: channelName, DestURL: sink.URL()})
	if err != nil {
		t.Fatalf("start gateway: %v", err)
	}
	p, err := gw.Start(openStdin(t), subcommandProcess, channelName)
	if err != nil {
		t.Fatalf("start process: %v", err)
	}
	time.Sleep(200 * time.Millisecond)
	if err := p.Signal(syscall.SIGINT); err != nil {
		t.Fatalf("interrupt process: %v", err)
	}
	res, err := p.Wait()
	if err != nil {
		t.Fatalf("wait for process: %v", err)
	}
	if res.Signal != syscall.SIGINT {
		t.Errorf("exit code %d, signal %v, want killed by SIGINT\n--- stderr ---\n%s", res.Code, res.Signal, res.Stderr)
	}
	if rows := readMessageRows(t, gw); len(rows) != 0 {
		t.Errorf("persisted messages = %d, want 0", len(rows))
	}
}

func TestProcessRejectsBadArguments(t *testing.T) {
	t.Parallel()
	sink := harness.NewSink(t, http.StatusOK, sinkBody)
	gw, err := harness.StartGateway(t, harness.Channel{Name: channelName, DestURL: sink.URL()})
	if err != nil {
		t.Fatalf("start gateway: %v", err)
	}
	file := messageFile(t, adtA01Message)

	cases := []struct {
		name       string
		stdin      string
		args       []string
		wantCode   int
		wantStderr []string
	}{
		{"no channel", "", nil, 2, []string{errorPrefix, "process takes [-dry-run] <channel> [<file>]", usagePrefix}},
		{tooManyArguments, "", []string{channelName, file, file}, 2, []string{errorPrefix, "process takes [-dry-run] <channel> [<file>]", usagePrefix}},
		{"empty stdin", "", []string{channelName}, 2, []string{errorPrefix, "process: the message is empty", usagePrefix}},
		{unknownChannelCase, "", []string{unknownChannel, file}, 1, []string{errorPrefix, noChannelNamedNope}},
		{"missing file", "", []string{channelName, filepath.Join(t.TempDir(), "missing.hl7")}, 1, []string{errorPrefix, noSuchFile}},
		{"directory as file", "", []string{channelName, t.TempDir()}, 1, []string{errorPrefix, "reading the message"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			expectFailure(t, run(t, gw, tc.stdin, subcommandProcess, tc.args...), tc.wantCode, tc.wantStderr...)
		})
	}
	if rcvd := sink.Received(); len(rcvd) != 0 {
		t.Errorf("sink received %d requests, want none", len(rcvd))
	}
}

func TestProcessReportsParseFailure(t *testing.T) {
	t.Parallel()
	sink := harness.NewSink(t, http.StatusOK, sinkBody)
	gw, err := harness.StartGateway(t, harness.Channel{Name: channelName, DestURL: sink.URL()})
	if err != nil {
		t.Fatalf("start gateway: %v", err)
	}

	expectFailure(t, run(t, gw, "", subcommandProcess, channelName, messageFile(t, missingFieldSeparatorMessage)), 1,
		errorPrefix, "message 1 "+couldNotParse)
	expectFailure(t, run(t, gw, garbageMessage, subcommandProcess, channelName), 1,
		errorPrefix, "message "+couldNotParse)
	if n := gw.CountLogs(isMsg(msgDroppingUncorrelatable)); n != 1 {
		t.Errorf("gateway logged %q %d times, want 1\n--- logs ---\n%v", msgDroppingUncorrelatable, n, gw.Logs())
	}

	rows := readMessageRows(t, gw)
	if len(rows) != 2 {
		t.Fatalf("persisted messages = %+v, want 2", rows)
	}
	for _, r := range rows {
		if r.TransformError == nil || *r.TransformError != couldNotParseHL7 {
			t.Errorf("message %d transform_error = %v, want %q", r.ID, r.TransformError, couldNotParseHL7)
		}
	}
	if attempts := readAttempts(t, gw); len(attempts) != 0 {
		t.Errorf("delivery attempts = %+v, want none", attempts)
	}
	if rcvd := sink.Received(); len(rcvd) != 0 {
		t.Errorf("sink received %d requests, want none", len(rcvd))
	}
}

func TestProcessReportsDeliveryFailure(t *testing.T) {
	t.Parallel()
	sink := harness.NewSink(t, http.StatusInternalServerError, sinkBody)
	gw, err := harness.StartGateway(t, harness.Channel{Name: channelName, DestURL: sink.URL()})
	if err != nil {
		t.Fatalf("start gateway: %v", err)
	}

	expectFailure(t, run(t, gw, adtA01Message, subcommandProcess, channelName), 1,
		errorPrefix, "delivery failed (message 1 persisted)")
	if n := gw.CountLogs(withAttr(isMsg(msgDeliveryAttemptFailed), "origin", "inbound")); n != 2 {
		t.Errorf("gateway logged %d failed inbound attempts, want 2\n--- logs ---\n%v", n, gw.Logs())
	}

	if !sink.WaitForReceived(2) {
		t.Fatalf("sink received %d requests, want 2 (delivery retries once)", len(sink.Received()))
	}
	attempts := readAttempts(t, gw)
	if len(attempts) != 2 {
		t.Fatalf("delivery attempts = %+v, want 2 failed attempts", attempts)
	}
	for i, a := range attempts {
		if a.MessageID != 1 || a.Outcome != outcomeFailed || a.Origin != originInbound {
			t.Errorf("attempt %d = %+v, want FAILED INBOUND for message 1", i, a)
		}
		if a.ResponseStatusCode == nil || *a.ResponseStatusCode != http.StatusInternalServerError {
			t.Errorf("attempt %d response_status_code = %v, want 500", i, a.ResponseStatusCode)
		}
	}
}

func TestProcessReportsStoreFailure(t *testing.T) {
	t.Parallel()
	sink := harness.NewSink(t, http.StatusOK, sinkBody)
	gw, err := harness.StartGateway(t, harness.Channel{Name: channelName, DestURL: sink.URL()})
	if err != nil {
		t.Fatalf("start gateway: %v", err)
	}
	file := messageFile(t, adtA01Message)

	release, err := harness.HoldWriteLock(t, gw.DBPath(channelName))
	if err != nil {
		t.Fatalf("holding write lock: %v", err)
	}
	expectFailure(t, run(t, gw, "", subcommandProcess, channelName, file), 1, errorPrefix, "message persistence failed")
	if rcvd := sink.Received(); len(rcvd) != 0 {
		t.Errorf("sink received %d requests, want none (message never persisted)", len(rcvd))
	}
	release()

	runOK(t, gw, "", subcommandProcess, channelName, file)
}

func TestProcessReportsReadBackFailure(t *testing.T) {
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

	expectFailure(t, run(t, gw, adtA01Message, subcommandProcess, channelName), 1, errorPrefix, "getting message failed")
	if !sink.WaitForReceived(1) {
		t.Fatalf("sink received %d requests, want 1 (delivery happened before the read back failed)", len(sink.Received()))
	}
}

func TestProcessDeliversMessage(t *testing.T) {
	t.Parallel()
	sink := harness.NewSink(t, http.StatusOK, sinkBody)
	gw, err := harness.StartGateway(t, harness.Channel{Name: channelName, DestURL: sink.URL()})
	if err != nil {
		t.Fatalf("start gateway: %v", err)
	}

	out := runOK(t, gw, adtA01Message, subcommandProcess, channelName)
	var m cliMessage
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("decoding process output: %v\n--- stdout ---\n%s", err, out)
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
		t.Errorf("ack = %v/%v, want null (no MLLP ack when processed from the shell)", m.AckMessage, m.AckOutcome)
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
	if auths := sink.AuthHeaders(); len(auths) != 1 || auths[0] != "Bearer "+gw.ChannelToken() {
		t.Errorf("sink authorization headers = %q, want the channel's bearer token", auths)
	}
	if summaries := listMessages(t, gw, channelName); len(summaries) != 1 || summaries[0].ID != m.ID {
		t.Errorf("messages list = %+v, want the processed message with id %d", summaries, m.ID)
	}
}

func TestProcessPrettyPrints(t *testing.T) {
	t.Parallel()
	sink := harness.NewSink(t, http.StatusOK, sinkBody)
	gw, err := harness.StartGateway(t, harness.Channel{Name: channelName, DestURL: sink.URL()})
	if err != nil {
		t.Fatalf("start gateway: %v", err)
	}
	file := messageFile(t, adtA01Message)

	expectPretty(t, runOK(t, gw, "", subcommandProcess, flagPretty, flagDryRun, channelName, file), adtA01PrettyDryRun)
	expectPretty(t, runOK(t, gw, "", subcommandProcess, flagPretty, channelName, file), adtA01PrettyProcess)
}

func TestProcessDryRunReportsParseFailure(t *testing.T) {
	t.Parallel()
	sink := harness.NewSink(t, http.StatusOK, sinkBody)
	gw, err := harness.StartGateway(t, harness.Channel{Name: channelName, DestURL: sink.URL()})
	if err != nil {
		t.Fatalf("start gateway: %v", err)
	}

	for _, msg := range []string{missingFieldSeparatorMessage, garbageMessage} {
		expectFailure(t, run(t, gw, msg, subcommandProcess, flagDryRun, channelName), 1, errorPrefix, "message "+couldNotParse)
	}
	if rows := readMessageRows(t, gw); len(rows) != 0 {
		t.Errorf("persisted messages = %+v, want none in dry-run mode", rows)
	}
	if rcvd := sink.Received(); len(rcvd) != 0 {
		t.Errorf("sink received %d requests, want none", len(rcvd))
	}
}

func TestProcessDryRunTransformsWithoutSideEffects(t *testing.T) {
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

	out := runOK(t, gw, "", subcommandProcess, flagDryRun, channelName, messageFile(t, adtA01Message))
	var body map[string]any
	if err := json.Unmarshal([]byte(out), &body); err != nil {
		t.Fatalf("decoding dry-run output: %v\n--- stdout ---\n%s", err, out)
	}
	if _, ok := body["id"]; ok {
		t.Error("output has an id, want it absent in dry-run mode")
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

	if rows := readMessageRows(t, gw); len(rows) != 0 {
		t.Errorf("persisted messages = %+v, want none in dry-run mode", rows)
	}
	if attempts := readAttempts(t, gw); len(attempts) != 0 {
		t.Errorf("delivery attempts = %+v, want none in dry-run mode", attempts)
	}
	if rcvd := sink.Received(); len(rcvd) != 0 {
		t.Errorf("sink received %d requests, want none in dry-run mode", len(rcvd))
	}
}
