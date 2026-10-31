//go:build e2e

package e2e

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bltn/hl7gw/e2e/internal/harness"
)

//go:embed testdata/adt_a01/pretty_list.txt
var adtA01PrettyList string

//go:embed testdata/adt_a01/pretty.txt
var adtA01Pretty string

//go:embed testdata/missing_field_separator/pretty.txt
var missingFieldSeparatorPretty string

//go:embed testdata/adt_a01/pretty_failed.txt
var adtA01PrettyFailed string

var millisecondStamp = regexp.MustCompile(`\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z`)

const (
	subcommandMessages = "messages"
	flagConfig         = "-config"
	flagPretty         = "-pretty"
	dropMessages       = "DROP TABLE messages"
	dropAttempts       = "DROP TABLE delivery_attempts"
	usagePrefix        = "usage:"
	adtA01Type         = "ADT^A01"
	tooManyArguments   = "too many arguments"
	unknownChannel     = "nope"
	unknownChannelCase = "unknown channel"
	noChannelNamedNope = `no channel named "nope"`
	badIDAbc           = `message id must be a positive integer, got "abc"`
	nonIntegerIDCase   = "non-integer id"
	flagLimit          = "-limit"
	flagBefore         = "-before"
)

type cliSummary struct {
	ID             int64   `json:"id"`
	ReceivedAt     string  `json:"received_at"`
	ControlID      string  `json:"control_id"`
	MessageType    string  `json:"message_type"`
	TransformError *string `json:"transform_error"`
	Attempts       int     `json:"attempts"`
	Delivery       *string `json:"delivery"`
	StatusCode     *int64  `json:"status_code"`
}

type cliAttempt struct {
	AttemptedAt        string  `json:"attempted_at"`
	Outcome            string  `json:"outcome"`
	ResponseStatusCode *int64  `json:"response_status_code"`
	ResponseBody       *string `json:"response_body"`
	Payload            string  `json:"payload"`
	Origin             string  `json:"origin"`
	CreatedAt          string  `json:"created_at"`
	UpdatedAt          string  `json:"updated_at"`
}

type cliMessage struct {
	ID               int64        `json:"id"`
	CreatedAt        string       `json:"created_at"`
	UpdatedAt        string       `json:"updated_at"`
	ReceivedAt       string       `json:"received_at"`
	RawMessage       string       `json:"raw_message"`
	TransformedXML   *string      `json:"transformed_xml"`
	TransformError   *string      `json:"transform_error"`
	AckMessage       *string      `json:"ack_message"`
	AckOutcome       *string      `json:"ack_outcome"`
	DeliveryAttempts []cliAttempt `json:"delivery_attempts"`
}

func run(t *testing.T, gw *harness.Gateway, stdin, subcommand string, args ...string) harness.ExitResult {
	t.Helper()
	res, err := gw.Run(stdin, subcommand, args...)
	if err != nil {
		t.Fatalf("run %s %v: %v", subcommand, args, err)
	}
	return res
}

func runOK(t *testing.T, gw *harness.Gateway, stdin, subcommand string, args ...string) string {
	t.Helper()
	res := run(t, gw, stdin, subcommand, args...)
	if res.Code != 0 {
		t.Fatalf("%s %v: exit code = %d, want 0\n--- stderr ---\n%s", subcommand, args, res.Code, res.Stderr)
	}
	return res.Stdout
}

func expectFailure(t *testing.T, res harness.ExitResult, wantCode int, wantStderr ...string) {
	t.Helper()
	if res.Code != wantCode {
		t.Errorf("exit code = %d, want %d\n--- stderr ---\n%s", res.Code, wantCode, res.Stderr)
	}
	for _, want := range wantStderr {
		if !strings.Contains(res.Stderr, want) {
			t.Errorf("stderr missing %q\n--- stderr ---\n%s", want, res.Stderr)
		}
	}
	if res.Stdout != "" {
		t.Errorf("unexpected stdout\n--- stdout ---\n%s", res.Stdout)
	}
}

func listMessages(t *testing.T, gw *harness.Gateway, args ...string) []cliSummary {
	t.Helper()
	return decodeSummaries(t, runOK(t, gw, "", subcommandMessages, args...))
}

func decodeSummaries(t *testing.T, out string) []cliSummary {
	t.Helper()
	var summaries []cliSummary
	for line := range strings.SplitSeq(strings.TrimSuffix(out, "\n"), "\n") {
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, `{"id":`) {
			t.Errorf("summary line should be one compact JSON object, got %q", line)
		}
		var s cliSummary
		if err := json.Unmarshal([]byte(line), &s); err != nil {
			t.Fatalf("decoding summary line %q: %v", line, err)
		}
		summaries = append(summaries, s)
	}
	return summaries
}

func getMessage(t *testing.T, gw *harness.Gateway, channel string, id int64) cliMessage {
	t.Helper()
	out := runOK(t, gw, "", subcommandMessages, channel, strconv.FormatInt(id, 10))
	if !strings.HasPrefix(out, "{\n  \"id\": ") {
		t.Errorf("record should be pretty-printed\n--- stdout ---\n%s", out)
	}
	var m cliMessage
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("decoding message: %v\n--- stdout ---\n%s", err, out)
	}
	if m.TransformedXML != nil && !strings.Contains(out, "<HL7Message>") {
		t.Errorf("transformed_xml should not be HTML-escaped\n--- stdout ---\n%s", out)
	}
	return m
}

func normalizePretty(s string) string {
	return harness.NormalizeACK(millisecondStamp.ReplaceAllString(s, "<TS>"))
}

func expectPretty(t *testing.T, got, want string) {
	t.Helper()
	if normalizePretty(got) != normalizePretty(want) {
		t.Errorf("pretty output mismatch:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func waitForMessages(t *testing.T, gw *harness.Gateway, channel string, n int, settled func(harness.MessageRow) bool) []harness.MessageRow {
	t.Helper()
	var rows []harness.MessageRow
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var err error
		rows, err = harness.ReadMessages(gw.DBPath(channel))
		if err == nil && len(rows) == n && allRows(rows, settled) {
			return rows
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%d settled messages never appeared on %s; rows = %+v", n, channel, rows)
	return nil
}

func allRows(rows []harness.MessageRow, pred func(harness.MessageRow) bool) bool {
	for _, r := range rows {
		if !pred(r) {
			return false
		}
	}
	return true
}

func acked(r harness.MessageRow) bool { return r.Ack != nil }

func transformRecorded(r harness.MessageRow) bool { return r.TransformError != nil || r.XML != nil }

func waitForAcks(t *testing.T, gw *harness.Gateway, channel string, n int) []harness.MessageRow {
	t.Helper()
	return waitForMessages(t, gw, channel, n, acked)
}

func sendControlIDs(t *testing.T, gw *harness.Gateway, channel string, controlIDs ...string) {
	t.Helper()
	for _, id := range controlIDs {
		if _, err := gw.SendAndAck(channel, harness.SetControlID(adtA01Message, id)); err != nil {
			t.Fatalf("send and ack %s: %v", id, err)
		}
	}
	waitForAcks(t, gw, channel, len(controlIDs))
}

func expectDeliveredSummary(t *testing.T, s cliSummary, wantID int64, wantControlID string) {
	t.Helper()
	if s.ID != wantID || s.ControlID != wantControlID || s.MessageType != adtA01Type {
		t.Errorf("summary = %+v, want id %d, control id %s and %s", s, wantID, wantControlID, adtA01Type)
	}
	if s.TransformError != nil {
		t.Errorf("transform_error = %q, want null", *s.TransformError)
	}
	if s.Attempts != 1 || s.Delivery == nil || *s.Delivery != outcomeSuccess {
		t.Errorf("attempts/delivery = %d/%v, want 1 and %s", s.Attempts, s.Delivery, outcomeSuccess)
	}
	if s.StatusCode == nil || *s.StatusCode != http.StatusOK {
		t.Errorf("status_code = %v, want 200", s.StatusCode)
	}
	if _, err := time.Parse(time.RFC3339, s.ReceivedAt); err != nil {
		t.Errorf("received_at = %q, want RFC 3339: %v", s.ReceivedAt, err)
	}
}

func TestMessagesListsNewestFirst(t *testing.T) {
	t.Parallel()
	sink := harness.NewSink(t, http.StatusOK, sinkBody)
	gw, err := harness.StartGateway(t, harness.Channel{Name: channelName, DestURL: sink.URL()})
	if err != nil {
		t.Fatalf("start gateway: %v", err)
	}
	sendControlIDs(t, gw, channelName, "MSG00001", "MSG00002")

	check := func(summaries []cliSummary) {
		t.Helper()
		if len(summaries) != 2 {
			t.Fatalf("summaries = %+v, want 2", summaries)
		}
		expectDeliveredSummary(t, summaries[0], 2, "MSG00002")
		expectDeliveredSummary(t, summaries[1], 1, "MSG00001")
	}
	check(listMessages(t, gw, channelName))

	if err := gw.Stop(); err != nil {
		t.Errorf("gateway exited uncleanly: %v", err)
	}
	expectFailure(t, run(t, gw, "", subcommandMessages, channelName), 1, errorPrefix, cannotReachGateway, gw.SocketPath())
}

func TestMessagesSummarisesUnparseableMessages(t *testing.T) {
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
	conn, err := gw.Dial(channelName)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close() }()
	if err := conn.Send(garbageMessage); err != nil {
		t.Fatalf("send garbage: %v", err)
	}
	waitForMessages(t, gw, channelName, 2, transformRecorded)

	summaries := listMessages(t, gw, channelName)
	if len(summaries) != 2 {
		t.Fatalf("summaries = %+v, want 2", summaries)
	}
	garbage, correlatable := summaries[0], summaries[1]
	if garbage.ID != 2 || garbage.ControlID != "" || garbage.MessageType != "" {
		t.Errorf("uncorrelatable summary = %+v, want id 2 with no control id or message type", garbage)
	}
	if correlatable.ID != 1 || correlatable.ControlID != "MSG00004" || correlatable.MessageType != adtA01Type {
		t.Errorf("correlatable summary = %+v, want id 1 with control id MSG00004 and %s", correlatable, adtA01Type)
	}
	for _, s := range summaries {
		if s.TransformError == nil || *s.TransformError != couldNotParseHL7 {
			t.Errorf("message %d transform_error = %v, want %s", s.ID, s.TransformError, couldNotParseHL7)
		}
		if s.Attempts != 0 || s.Delivery != nil || s.StatusCode != nil {
			t.Errorf("message %d = %+v, want no attempts, delivery or status code", s.ID, s)
		}
	}
	if rcvd := sink.Received(); len(rcvd) != 0 {
		t.Errorf("sink received %d requests, want none", len(rcvd))
	}
}

func TestMessagesPagesWithCursor(t *testing.T) {
	t.Parallel()
	sink := harness.NewSink(t, http.StatusOK, "")
	gw, err := harness.StartGateway(t, harness.Channel{Name: channelName, DestURL: sink.URL()})
	if err != nil {
		t.Fatalf("start gateway: %v", err)
	}
	sendControlIDs(t, gw, channelName, "MSG00001", "MSG00002", "MSG00003")

	ids := func(summaries []cliSummary) []int64 {
		out := make([]int64, 0, len(summaries))
		for _, s := range summaries {
			out = append(out, s.ID)
		}
		return out
	}
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"default lists everything newest first", nil, "[3 2 1]"},
		{"limit", []string{flagLimit, "2"}, "[3 2]"},
		{"limit with cursor", []string{flagLimit, "2", flagBefore, "2"}, "[1]"},
		{"cursor past the oldest", []string{flagBefore, "1"}, "[]"},
		{"limit one", []string{flagLimit, "1"}, "[3]"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ids(listMessages(t, gw, append(tc.args, channelName)...))
			if fmt.Sprint(got) != tc.want {
				t.Errorf("ids = %v, want %s", got, tc.want)
			}
		})
	}
}

func TestMessagesScopesToChannel(t *testing.T) {
	t.Parallel()
	sink := harness.NewSink(t, http.StatusOK, "")
	gw, err := harness.StartGateway(t,
		harness.Channel{Name: alphaChannel, DestURL: sink.URL()},
		harness.Channel{Name: betaChannel, DestURL: sink.URL()})
	if err != nil {
		t.Fatalf("start gateway: %v", err)
	}
	sendControlIDs(t, gw, alphaChannel, "MSG00001")

	if alpha := listMessages(t, gw, alphaChannel); len(alpha) != 1 {
		t.Errorf("alpha = %+v, want the one sent message", alpha)
	}
	if beta := listMessages(t, gw, betaChannel); len(beta) != 0 {
		t.Errorf("beta = %+v, want no messages", beta)
	}
}

func TestMessagesShowsFullRecord(t *testing.T) {
	t.Parallel()
	sink := harness.NewSink(t, http.StatusOK, sinkBody)
	gw, err := harness.StartGateway(t, harness.Channel{Name: channelName, DestURL: sink.URL()})
	if err != nil {
		t.Fatalf("start gateway: %v", err)
	}
	sendControlIDs(t, gw, channelName, "MSG00001")
	summaries := listMessages(t, gw, channelName)
	if len(summaries) != 1 {
		t.Fatalf("summaries = %+v, want 1", summaries)
	}

	m := getMessage(t, gw, channelName, summaries[0].ID)
	if m.ID != summaries[0].ID {
		t.Errorf("id = %d, want the listed id %d", m.ID, summaries[0].ID)
	}
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
	for name, ts := range map[string]string{"created_at": m.CreatedAt, "updated_at": m.UpdatedAt, "received_at": m.ReceivedAt} {
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
	for name, ts := range map[string]string{"attempted_at": a.AttemptedAt, "created_at": a.CreatedAt, "updated_at": a.UpdatedAt} {
		if _, err := time.Parse(time.RFC3339, ts); err != nil {
			t.Errorf("attempt %s = %q, want RFC 3339: %v", name, ts, err)
		}
	}
}

func TestMessagesPrettyPrintsListAndRecord(t *testing.T) {
	t.Parallel()
	sink := harness.NewSink(t, http.StatusOK, sinkBody)
	gw, err := harness.StartGateway(t, harness.Channel{Name: channelName, DestURL: sink.URL()})
	if err != nil {
		t.Fatalf("start gateway: %v", err)
	}
	sendControlIDs(t, gw, channelName, "MSG00001", "MSG00002")

	expectPretty(t, runOK(t, gw, "", subcommandMessages, flagPretty, channelName), adtA01PrettyList)
	expectPretty(t, runOK(t, gw, "", subcommandMessages, flagPretty, channelName, "1"), adtA01Pretty)
}

func TestMessagesPrettyPrintsUnparseableRecord(t *testing.T) {
	t.Parallel()
	sink := harness.NewSink(t, http.StatusOK, sinkBody)
	gw, err := harness.StartGateway(t, harness.Channel{Name: channelName, DestURL: sink.URL()})
	if err != nil {
		t.Fatalf("start gateway: %v", err)
	}
	if _, err := gw.SendAndAck(channelName, missingFieldSeparatorMessage); err != nil {
		t.Fatalf("send and ack: %v", err)
	}
	waitForMessages(t, gw, channelName, 1, func(r harness.MessageRow) bool { return acked(r) && transformRecorded(r) })

	expectPretty(t, runOK(t, gw, "", subcommandMessages, flagPretty, channelName, "1"), missingFieldSeparatorPretty)
}

func TestMessagesPrettyPrintsFailedDelivery(t *testing.T) {
	t.Parallel()
	sink := harness.NewSink(t, http.StatusInternalServerError, "")
	gw, err := harness.StartGateway(t, harness.Channel{Name: channelName, DestURL: sink.URL()})
	if err != nil {
		t.Fatalf("start gateway: %v", err)
	}
	if _, err := gw.SendAndAck(channelName, adtA01Message); err != nil {
		t.Fatalf("send and ack: %v", err)
	}
	waitForAcks(t, gw, channelName, 1)

	expectPretty(t, runOK(t, gw, "", subcommandMessages, flagPretty, channelName, "1"), adtA01PrettyFailed)

	if err := harness.ExecSQL(gw.DBPath(channelName), "UPDATE messages SET xml = NULL"); err != nil {
		t.Fatalf("strip transformed xml: %v", err)
	}
	out := runOK(t, gw, "", subcommandMessages, flagPretty, channelName, "1")
	if want := "--- transformed ---\n(none)\n"; !strings.Contains(out, want) {
		t.Errorf("pretty output missing %q\n--- got ---\n%s", want, out)
	}
}

func TestMessagesPrettyEscapesControlCharacters(t *testing.T) {
	t.Parallel()
	sink := harness.NewSink(t, http.StatusOK, sinkBody)
	gw, err := harness.StartGateway(t, harness.Channel{Name: channelName, DestURL: sink.URL()})
	if err != nil {
		t.Fatalf("start gateway: %v", err)
	}
	msg := strings.ReplaceAll(adtA01Message, "\r", "\n") + "\tX\x01"
	if _, err := gw.SendAndAck(channelName, msg); err != nil {
		t.Fatalf("send and ack: %v", err)
	}
	waitForMessages(t, gw, channelName, 1, transformRecorded)

	out := runOK(t, gw, "", subcommandMessages, flagPretty, channelName, "1")
	want := "--- raw ---\n" + strings.ReplaceAll(adtA01Message, "\r", `\n`) + `\tX\x01` + "\n"
	if !strings.Contains(out, want) {
		t.Errorf("pretty output missing %q\n--- got ---\n%s", want, out)
	}
}

func TestMessagesPrettyRejectsMalformedServerOutput(t *testing.T) {
	t.Parallel()
	socket := shortSocket(t)
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "unix", socket)
	if err != nil {
		t.Fatalf("listen on %s: %v", socket, err)
	}
	fake := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "not json\n")
	}))
	fake.Listener = ln
	fake.Start()
	t.Cleanup(fake.Close)

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(cfgPath, []byte(fmt.Sprintf(soloSocketConfig, socket)), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	for _, args := range [][]string{{"solo"}, {"solo", "1"}} {
		res, err := harness.RunUntilExit(dir, "", append([]string{subcommandMessages, flagConfig, cfgPath, flagPretty}, args...)...)
		if err != nil {
			t.Fatalf("run messages %v: %v", args, err)
		}
		expectFailure(t, res, 1, errorPrefix, "invalid character")
	}
}

func TestMessagesReportsStoreReadFailure(t *testing.T) {
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
			sendControlIDs(t, gw, channelName, "MSG00001")
			if err := harness.ExecSQL(gw.DBPath(channelName), tc.sql...); err != nil {
				t.Fatalf("poisoning schema: %v", err)
			}

			expectFailure(t, run(t, gw, "", subcommandMessages, channelName), 1, errorPrefix, "listing messages failed")
			expectFailure(t, run(t, gw, "", subcommandMessages, channelName, "1"), 1, errorPrefix, "getting message failed")
		})
	}
}

func TestMessagesRejectsBadArguments(t *testing.T) {
	t.Parallel()
	sink := harness.NewSink(t, http.StatusOK, "")
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
		{"no channel", nil, 2, []string{errorPrefix, "messages takes <channel> [<id>]", usagePrefix}},
		{tooManyArguments, []string{channelName, "1", "2"}, 2, []string{errorPrefix, "messages takes <channel> [<id>]", usagePrefix}},
		{nonIntegerIDCase, []string{channelName, "abc"}, 2, []string{errorPrefix, badIDAbc, usagePrefix}},
		{"id below one", []string{channelName, "0"}, 2, []string{errorPrefix, `message id must be a positive integer, got "0"`, usagePrefix}},
		{"limit below one", []string{flagLimit, "0", channelName}, 2, []string{errorPrefix, "-limit must be at least 1, got 0", usagePrefix}},
		{"limit above the cap", []string{flagLimit, "501", channelName}, 2, []string{errorPrefix, "-limit must be at most 500, got 501", usagePrefix}},
		{"negative before", []string{flagBefore, "-1", channelName}, 2, []string{errorPrefix, "-before must be a message id, got -1", usagePrefix}},
		{"unknown flag", []string{"-bogus", channelName}, 2, []string{"flag provided but not defined: -bogus"}},
		{unknownChannelCase, []string{unknownChannel}, 1, []string{errorPrefix, noChannelNamedNope}},
		{"unknown id", []string{channelName, "424242"}, 1, []string{errorPrefix, "no message with id 424242"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			expectFailure(t, run(t, gw, "", subcommandMessages, tc.args...), tc.wantCode, tc.wantStderr...)
		})
	}
}

func TestMessagesNeedsConfigAndGateway(t *testing.T) {
	t.Parallel()
	res, err := harness.RunUntilExit(t.TempDir(), "", subcommandMessages, channelName)
	if err != nil {
		t.Fatalf("run without a config: %v", err)
	}
	expectFailure(t, res, 1, errorPrefix, "no config file", "./hl7gw.json")

	dir := t.TempDir()
	socket := shortSocket(t)
	cfgPath := filepath.Join(dir, "config.json")
	cfg := fmt.Sprintf(`{"socket": %q, `+metricsOK+`, "channels": [{"name": "solo", `+sourceOK+`, `+destinationOK+`}]}`, socket)
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	res, err = harness.RunUntilExit(dir, "", subcommandMessages, flagConfig, cfgPath, "solo")
	if err != nil {
		t.Fatalf("run without a gateway: %v", err)
	}
	expectFailure(t, res, 1, errorPrefix, cannotReachGateway, socket)
}
