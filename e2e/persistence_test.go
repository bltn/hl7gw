//go:build e2e

package e2e

import (
	_ "embed"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bltn/hl7gw/e2e/internal/harness"
)

//go:embed testdata/adt_a01/ack_ar.txt
var adtA01AckAR string

//go:embed testdata/missing_field_separator/ack_ar.txt
var missingFieldSeparatorAckAR string

const (
	createdAtColumn  = "created_at"
	updatedAtColumn  = "updated_at"
	outcomeSuccess   = "SUCCESS"
	outcomeFailed    = "FAILED"
	originInbound    = "INBOUND"
	sinkBody         = "sink response"
	couldNotParseHL7 = "COULD_NOT_PARSE_HL7"
	garbageMessage   = "this is not a valid HL7 message"
)

func TestCreatesPerChannelDatabases(t *testing.T) {
	t.Parallel()
	gw, err := harness.StartGateway(t,
		harness.Channel{Name: "alpha", DestURL: "http://127.0.0.1:1/"},
		harness.Channel{Name: "beta", DestURL: "http://127.0.0.1:1/"},
	)
	if err != nil {
		t.Fatalf("start gateway: %v", err)
	}

	for _, name := range []string{"alpha", "beta"} {
		if _, err := os.Stat(gw.DBPath(name)); err != nil {
			t.Errorf("database file %s: %v", name, err)
		}
		if _, err := os.Stat(gw.DBPath(name) + "-wal"); err != nil {
			t.Errorf("WAL sidecar for %s missing while the gateway runs (store not in WAL mode?): %v", name, err)
		}
	}

	if err := gw.Stop(); err != nil {
		t.Fatalf("gateway did not shut down cleanly: %v", err)
	}

	leftovers, err := filepath.Glob(filepath.Join(gw.DataDir(), "*.db-*"))
	if err != nil {
		t.Fatalf("globbing data dir: %v", err)
	}
	if len(leftovers) != 0 {
		t.Errorf("WAL sidecar files survived clean shutdown: %v", leftovers)
	}
}

func TestExitsWhenDataDirCannotBeCreated(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("write blocker file: %v", err)
	}

	cfg := fmt.Sprintf(`{"data_dir": %q, "admin_api": {"port": 9990, "token_env": "`+harness.AdminTokenEnv+`"}, "channels": []}`, filepath.Join(blocker, "data"))
	expectStartupFailure(t, cfg, harness.StartupEnv(), errorPrefix, "creating data dir", "not a directory")
}

func TestUnwindsStoresOnOpenFailure(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")
	if err := os.MkdirAll(dataDir, 0o750); err != nil {
		t.Fatalf("create data dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "second.db"), []byte("this is not a sqlite database"), 0o600); err != nil {
		t.Fatalf("write corrupt db: %v", err)
	}

	cfg := fmt.Sprintf(`{"data_dir": %q, "admin_api": {"port": 9990, "token_env": "`+harness.AdminTokenEnv+`"}, "channels": [
		{"name": "first", `+listener9998OK+`, `+destinationOK+`},
		{"name": "second", `+listenerOK+`, `+destinationOK+`}
	]}`, dataDir)
	expectStartupFailure(t, cfg, harness.StartupEnv(), errorPrefix, "channel second", "not a database")
}

func TestStoresReceivedMessage(t *testing.T) {
	t.Parallel()
	sink := harness.NewSink(t, http.StatusOK, "")
	gw, err := harness.StartGateway(t, harness.Channel{Name: channelName, DestURL: sink.URL()})
	if err != nil {
		t.Fatalf("start gateway: %v", err)
	}

	before := time.Now().UnixMilli()
	if _, err := gw.SendAndAck(channelName, adtA01Message); err != nil {
		t.Fatalf("send and ack: %v", err)
	}
	after := time.Now().UnixMilli()

	msgs, _ := stopAndReadAll(t, gw)
	if len(msgs) != 1 {
		t.Fatalf("got %d message rows, want 1", len(msgs))
	}
	if msgs[0].Raw != adtA01Message {
		t.Errorf("raw mismatch:\n--- got ---\n%s\n--- want ---\n%s", msgs[0].Raw, adtA01Message)
	}
	if msgs[0].ReceivedAt < before || msgs[0].ReceivedAt > after {
		t.Errorf("received_at = %d, want within [%d, %d]", msgs[0].ReceivedAt, before, after)
	}
	now := time.Now().Unix()
	for name, got := range map[string]int64{createdAtColumn: msgs[0].CreatedAt, updatedAtColumn: msgs[0].UpdatedAt} {
		if got < now-60 || got > now+60 {
			t.Errorf("%s = %d, want within a minute of %d (unix seconds)", name, got, now)
		}
	}
}

func TestPersistsAcrossRestart(t *testing.T) {
	t.Parallel()
	sink := harness.NewSink(t, http.StatusOK, "")
	gw, err := harness.StartGateway(t, harness.Channel{Name: channelName, DestURL: sink.URL()})
	if err != nil {
		t.Fatalf("start gateway: %v", err)
	}

	if _, err := gw.SendAndAck(channelName, adtA01Message); err != nil {
		t.Fatalf("send before restart: %v", err)
	}
	if err := gw.Stop(); err != nil {
		t.Fatalf("gateway did not shut down cleanly: %v", err)
	}

	gw2, err := gw.Restart(t)
	if err != nil {
		t.Fatalf("restart gateway: %v", err)
	}

	second := harness.SetControlID(adtA01Message, "MSGRESTART")
	ack, err := gw2.SendAndAck(channelName, second)
	if err != nil {
		t.Fatalf("send after restart: %v", err)
	}
	wantAck := strings.Replace(adtA01AckAA, "MSA|AA|MSG00001", "MSA|AA|MSGRESTART", 1)
	if got, want := harness.NormalizeACK(ack), harness.NormalizeACK(wantAck); got != want {
		t.Errorf("ACK after restart mismatch:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}

	msgs, _ := stopAndReadAll(t, gw2)
	if len(msgs) != 2 {
		t.Fatalf("got %d message rows after restart, want 2 (both runs persisted in the same database)", len(msgs))
	}
	if msgs[0].Raw != adtA01Message {
		t.Errorf("first row raw mismatch:\n--- got ---\n%s\n--- want ---\n%s", msgs[0].Raw, adtA01Message)
	}
	if msgs[1].Raw != second {
		t.Errorf("second row raw mismatch:\n--- got ---\n%s\n--- want ---\n%s", msgs[1].Raw, second)
	}
}

func TestExitsWhenSchemaVersionTooNew(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")
	if err := os.MkdirAll(dataDir, 0o750); err != nil {
		t.Fatalf("create data dir: %v", err)
	}
	if err := harness.ExecSQL(filepath.Join(dataDir, "solo.db"), "PRAGMA user_version = 999"); err != nil {
		t.Fatalf("stamping future schema version: %v", err)
	}

	cfg := fmt.Sprintf(`{"data_dir": %q, "admin_api": {"port": 9990, "token_env": "`+harness.AdminTokenEnv+`"}, "channels": [
		{"name": "solo", `+listenerOK+`, `+destinationOK+`}
	]}`, dataDir)
	expectStartupFailure(t, cfg, harness.StartupEnv(), errorPrefix, "channel solo", "newer than this binary supports")
}

func TestTouchesUpdatedAtOnUpdate(t *testing.T) {
	t.Parallel()
	sink, releaseOne := harness.NewGatedSink(t, http.StatusOK, "")
	gw, err := harness.StartGateway(t, harness.Channel{Name: channelName, DestURL: sink.URL()})
	if err != nil {
		t.Fatalf("start gateway: %v", err)
	}

	conn, err := gw.Dial(channelName)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close() }()

	if err := conn.Send(adtA01Message); err != nil {
		t.Fatalf("send: %v", err)
	}
	if !sink.WaitForReceived(1) {
		t.Fatal("sink never received the message")
	}
	if err := harness.ExecSQL(gw.DBPath(channelName), "UPDATE messages SET updated_at = 5"); err != nil {
		t.Fatalf("rewinding messages.updated_at: %v", err)
	}
	releaseOne()
	if _, err := conn.ReadMessage(); err != nil {
		t.Fatalf("read ack: %v", err)
	}

	msgs, _ := stopAndReadAll(t, gw)
	if len(msgs) != 1 {
		t.Fatalf("got %d message rows, want 1", len(msgs))
	}
	now := time.Now().Unix()
	if msgs[0].UpdatedAt < now-60 {
		t.Errorf("messages.updated_at = %d after the ack was recorded, want touched to roughly %d", msgs[0].UpdatedAt, now)
	}

	rewinds := []string{
		"UPDATE delivery_attempts SET updated_at = 5",
		"UPDATE delivery_attempts SET response_body = 'touched'",
	}
	if err := harness.ExecSQL(gw.DBPath(channelName), rewinds...); err != nil {
		t.Fatalf("rewinding and updating delivery_attempts: %v", err)
	}
	attempts, err := harness.ReadDeliveryAttempts(gw.DBPath(channelName))
	if err != nil {
		t.Fatalf("read delivery attempts: %v", err)
	}
	if len(attempts) != 1 {
		t.Fatalf("got %d delivery attempts, want 1", len(attempts))
	}
	if attempts[0].UpdatedAt < now-60 {
		t.Errorf("delivery_attempts.updated_at = %d after an update, want touched to roughly %d", attempts[0].UpdatedAt, now)
	}
}

func TestAcksARWhenStoreUnavailableAndBodyUnparseable(t *testing.T) {
	t.Parallel()
	sink := harness.NewSink(t, http.StatusOK, "")
	gw, err := harness.StartGateway(t, harness.Channel{Name: channelName, DestURL: sink.URL()})
	if err != nil {
		t.Fatalf("start gateway: %v", err)
	}
	defer func() {
		if err := gw.Stop(); err != nil {
			t.Errorf("gateway exited uncleanly: %v", err)
		}
	}()

	release, err := harness.HoldWriteLock(t, gw.DBPath(channelName))
	if err != nil {
		t.Fatalf("hold write lock: %v", err)
	}
	defer release()

	ack, err := gw.SendAndAck(channelName, missingFieldSeparatorMessage)
	if err != nil {
		t.Fatalf("send and ack: %v", err)
	}
	if got, want := harness.NormalizeACK(ack), harness.NormalizeACK(missingFieldSeparatorAckAR); got != want {
		t.Errorf("ACK mismatch (persistence failure must win over the parse failure):\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
	if rcvd := sink.Received(); len(rcvd) != 0 {
		t.Errorf("sink received %d messages, want 0", len(rcvd))
	}
	if !gw.WaitForLog(5*time.Second, withAttr(isMsg(msgPersistenceFailed), "operation", "create_message")) {
		t.Error("gateway never logged the persistence failure")
	}
}

func TestStoresTransformedXML(t *testing.T) {
	t.Parallel()
	sink := harness.NewSink(t, http.StatusOK, "")
	gw, err := harness.StartGateway(t, harness.Channel{Name: channelName, DestURL: sink.URL()})
	if err != nil {
		t.Fatalf("start gateway: %v", err)
	}

	if _, err := gw.SendAndAck(channelName, adtA01Message); err != nil {
		t.Fatalf("send and ack: %v", err)
	}

	msgs, _ := stopAndReadAll(t, gw)
	if len(msgs) != 1 {
		t.Fatalf("got %d message rows, want 1", len(msgs))
	}
	if msgs[0].XML == nil {
		t.Fatal("xml column is NULL, want the transformed message")
	}
	if *msgs[0].XML != adtA01TransformedXML {
		t.Errorf("xml mismatch:\n--- got ---\n%s\n--- want ---\n%s", *msgs[0].XML, adtA01TransformedXML)
	}
	if msgs[0].TransformError != nil {
		t.Errorf("transform_error = %q, want NULL", *msgs[0].TransformError)
	}
}

func TestStoresTransformErrorWhenUnparseable(t *testing.T) {
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
	if got, want := harness.NormalizeACK(ack), harness.NormalizeACK(missingFieldSeparatorAckAE); got != want {
		t.Errorf("ACK mismatch:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}

	msgs, _ := stopAndReadAll(t, gw)
	if len(msgs) != 1 {
		t.Fatalf("got %d message rows, want 1", len(msgs))
	}
	if msgs[0].XML != nil {
		t.Errorf("xml = %q, want NULL", *msgs[0].XML)
	}
	if msgs[0].TransformError == nil || *msgs[0].TransformError != couldNotParseHL7 {
		t.Errorf("transform_error = %v, want %s", msgs[0].TransformError, couldNotParseHL7)
	}
	if msgs[0].Ack == nil || *msgs[0].Ack != ack {
		t.Errorf("ack column = %v, want the wire ack %q", msgs[0].Ack, ack)
	}
	if msgs[0].AckOutcome == nil || *msgs[0].AckOutcome != outcomeSuccess {
		t.Errorf("ack_outcome = %v, want SUCCESS", msgs[0].AckOutcome)
	}
}

func TestStoresDeliveryAttemptOnSuccess(t *testing.T) {
	t.Parallel()
	sink := harness.NewSink(t, http.StatusOK, sinkBody)
	gw, err := harness.StartGateway(t, harness.Channel{Name: channelName, DestURL: sink.URL()})
	if err != nil {
		t.Fatalf("start gateway: %v", err)
	}

	if _, err := gw.SendAndAck(channelName, adtA01Message); err != nil {
		t.Fatalf("send and ack: %v", err)
	}

	msgs, attempts := stopAndReadAll(t, gw)
	if len(msgs) != 1 {
		t.Fatalf("got %d message rows, want 1", len(msgs))
	}
	if len(attempts) != 1 {
		t.Fatalf("got %d delivery attempts, want 1", len(attempts))
	}
	assertAttempt(t, attempts[0], msgs[0].ID, outcomeSuccess, http.StatusOK)
}

func TestStoresDeliveryAttemptsWhenSinkFails(t *testing.T) {
	t.Parallel()
	sink := harness.NewSink(t, http.StatusInternalServerError, sinkBody)
	gw, err := harness.StartGateway(t, harness.Channel{Name: channelName, DestURL: sink.URL()})
	if err != nil {
		t.Fatalf("start gateway: %v", err)
	}

	ack, err := gw.SendAndAck(channelName, adtA01Message)
	if err != nil {
		t.Fatalf("send and ack: %v", err)
	}
	if got, want := harness.NormalizeACK(ack), harness.NormalizeACK(adtA01AckAE); got != want {
		t.Errorf("ACK mismatch:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}

	msgs, attempts := stopAndReadAll(t, gw)
	if len(msgs) != 1 {
		t.Fatalf("got %d message rows, want 1", len(msgs))
	}
	if len(attempts) != 2 {
		t.Fatalf("got %d delivery attempts, want 2 (initial + one retry)", len(attempts))
	}
	for i, a := range attempts {
		t.Run(fmt.Sprintf("attempt_%d", i), func(t *testing.T) {
			assertAttempt(t, a, msgs[0].ID, outcomeFailed, http.StatusInternalServerError)
		})
	}
}

func TestStoresDeliveryAttemptsOnRetrySuccess(t *testing.T) {
	t.Parallel()
	sink := harness.NewSink(t, http.StatusInternalServerError, sinkBody, http.StatusOK)
	gw, err := harness.StartGateway(t, harness.Channel{Name: channelName, DestURL: sink.URL()})
	if err != nil {
		t.Fatalf("start gateway: %v", err)
	}

	ack, err := gw.SendAndAck(channelName, adtA01Message)
	if err != nil {
		t.Fatalf("send and ack: %v", err)
	}
	if got, want := harness.NormalizeACK(ack), harness.NormalizeACK(adtA01AckAA); got != want {
		t.Errorf("ACK mismatch:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}

	msgs, attempts := stopAndReadAll(t, gw)
	if len(msgs) != 1 {
		t.Fatalf("got %d message rows, want 1", len(msgs))
	}
	if len(attempts) != 2 {
		t.Fatalf("got %d delivery attempts, want 2 (failure + retry)", len(attempts))
	}
	assertAttempt(t, attempts[0], msgs[0].ID, outcomeFailed, http.StatusInternalServerError)
	assertAttempt(t, attempts[1], msgs[0].ID, outcomeSuccess, http.StatusOK)
}

func TestStoresDeliveryAttemptsOnTransportError(t *testing.T) {
	t.Parallel()
	dest, err := harness.DeadDestURL()
	if err != nil {
		t.Fatalf("dead dest url: %v", err)
	}
	gw, err := harness.StartGateway(t, harness.Channel{Name: channelName, DestURL: dest})
	if err != nil {
		t.Fatalf("start gateway: %v", err)
	}

	ack, err := gw.SendAndAck(channelName, adtA01Message)
	if err != nil {
		t.Fatalf("send and ack: %v", err)
	}
	if got, want := harness.NormalizeACK(ack), harness.NormalizeACK(adtA01AckAE); got != want {
		t.Errorf("ACK mismatch:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}

	msgs, attempts := stopAndReadAll(t, gw)
	if len(msgs) != 1 {
		t.Fatalf("got %d message rows, want 1", len(msgs))
	}
	if len(attempts) != 2 {
		t.Fatalf("got %d delivery attempts, want 2", len(attempts))
	}
	for i, a := range attempts {
		t.Run(fmt.Sprintf("attempt_%d", i), func(t *testing.T) {
			if a.MessageID != msgs[0].ID {
				t.Errorf("message_id = %d, want %d", a.MessageID, msgs[0].ID)
			}
			if a.Outcome != outcomeFailed {
				t.Errorf("outcome = %q, want %q", a.Outcome, outcomeFailed)
			}
			if a.ResponseStatusCode != nil {
				t.Errorf("response_status_code = %d, want NULL for a transport error", *a.ResponseStatusCode)
			}
			if a.ResponseBody != nil {
				t.Errorf("response_body = %q, want NULL for a transport error", *a.ResponseBody)
			}
		})
	}
}

func TestStoresDroppedMessage(t *testing.T) {
	t.Parallel()
	sink := harness.NewSink(t, http.StatusOK, "")
	gw, err := harness.StartGateway(t, harness.Channel{Name: channelName, DestURL: sink.URL()})
	if err != nil {
		t.Fatalf("start gateway: %v", err)
	}

	conn, err := gw.Dial(channelName)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close() }()

	garbage := garbageMessage
	if err := conn.Send(garbage); err != nil {
		t.Fatalf("send: %v", err)
	}
	if !gw.WaitForLog(5*time.Second, isMsg(msgDroppingUncorrelatable)) {
		t.Fatal("gateway never logged the drop")
	}

	msgs, attempts := stopAndReadAll(t, gw)
	if len(msgs) != 1 {
		t.Fatalf("got %d message rows, want 1", len(msgs))
	}
	if msgs[0].Raw != garbage {
		t.Errorf("raw = %q, want %q", msgs[0].Raw, garbage)
	}
	if msgs[0].TransformError == nil || *msgs[0].TransformError != couldNotParseHL7 {
		t.Errorf("transform_error = %v, want %s", msgs[0].TransformError, couldNotParseHL7)
	}
	if msgs[0].XML != nil {
		t.Errorf("xml = %q, want NULL", *msgs[0].XML)
	}
	if msgs[0].Ack != nil || msgs[0].AckSentAt != nil || msgs[0].AckOutcome != nil {
		t.Errorf("ack columns = (%v, %v, %v), want all NULL for a dropped message", msgs[0].Ack, msgs[0].AckSentAt, msgs[0].AckOutcome)
	}
	if len(attempts) != 0 {
		t.Errorf("got %d delivery attempts, want 0", len(attempts))
	}
}

func TestStoresAckWhenAckSendFails(t *testing.T) {
	t.Parallel()
	sink, releaseOne := harness.NewGatedSink(t, http.StatusOK, "")
	gw, err := harness.StartGateway(t, harness.Channel{Name: channelName, DestURL: sink.URL()})
	if err != nil {
		t.Fatalf("start gateway: %v", err)
	}

	conn, err := gw.Dial(channelName)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}

	if err := conn.Send(adtA01Message); err != nil {
		t.Fatalf("send: %v", err)
	}
	if !sink.WaitForReceived(1) {
		t.Fatal("sink never received the message")
	}
	if err := conn.Abort(); err != nil {
		t.Fatalf("abort connection: %v", err)
	}
	releaseOne()
	if !gw.WaitForLog(5*time.Second, isMsg(msgAckWriteFailed)) {
		t.Fatal("gateway never logged the failed ack write")
	}

	msgs, attempts := stopAndReadAll(t, gw)
	if len(msgs) != 1 {
		t.Fatalf("got %d message rows, want 1", len(msgs))
	}
	if msgs[0].Ack == nil {
		t.Fatal("ack column is NULL, want the unsendable ack stored")
	}
	if got, want := harness.NormalizeACK(*msgs[0].Ack), harness.NormalizeACK(adtA01AckAA); got != want {
		t.Errorf("stored ack mismatch:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
	if msgs[0].AckOutcome == nil || *msgs[0].AckOutcome != outcomeFailed {
		t.Errorf("ack_outcome = %v, want FAILED", msgs[0].AckOutcome)
	}
	if msgs[0].AckSentAt == nil {
		t.Error("ack_sent_at is NULL, want the attempt time")
	}
	if len(attempts) != 1 || attempts[0].Outcome != outcomeSuccess {
		t.Errorf("attempts = %+v, want one SUCCESS row", attempts)
	}
}

func TestAcksARWhenStoreUnavailable(t *testing.T) {
	t.Parallel()
	sink := harness.NewSink(t, http.StatusOK, "")
	gw, err := harness.StartGateway(t, harness.Channel{Name: channelName, DestURL: sink.URL()})
	if err != nil {
		t.Fatalf("start gateway: %v", err)
	}

	release, err := harness.HoldWriteLock(t, gw.DBPath(channelName))
	if err != nil {
		t.Fatalf("hold write lock: %v", err)
	}

	conn, err := gw.Dial(channelName)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close() }()

	if err := conn.Send(adtA01Message); err != nil {
		t.Fatalf("send: %v", err)
	}
	ack, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read ack: %v", err)
	}
	if got, want := harness.NormalizeACK(ack), harness.NormalizeACK(adtA01AckAR); got != want {
		t.Errorf("ACK mismatch:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
	if rcvd := sink.Received(); len(rcvd) != 0 {
		t.Errorf("sink received %d messages, want 0 (no forward on persistence failure)", len(rcvd))
	}
	if !gw.WaitForLog(5*time.Second, withAttr(isMsg(msgPersistenceFailed), "operation", "create_message")) {
		t.Error("gateway never logged the persistence failure")
	}

	release()
	if err := conn.Send(adtA01Message); err != nil {
		t.Fatalf("send after release: %v", err)
	}
	ack, err = conn.ReadMessage()
	if err != nil {
		t.Fatalf("read ack after release: %v", err)
	}
	if got, want := harness.NormalizeACK(ack), harness.NormalizeACK(adtA01AckAA); got != want {
		t.Errorf("ACK after release mismatch:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}

	msgs, _ := stopAndReadAll(t, gw)
	if len(msgs) != 1 {
		t.Fatalf("got %d message rows, want 1 (the rejected message must not have a row)", len(msgs))
	}
	if msgs[0].AckOutcome == nil || *msgs[0].AckOutcome != outcomeSuccess {
		t.Errorf("ack_outcome = %v, want SUCCESS for the post-release message", msgs[0].AckOutcome)
	}
}

func TestAcksARWhenStoreFailsMidMessage(t *testing.T) {
	t.Parallel()
	sink, releaseOne := harness.NewGatedSink(t, http.StatusInternalServerError, "", http.StatusOK)
	gw, err := harness.StartGateway(t, harness.Channel{Name: channelName, DestURL: sink.URL()})
	if err != nil {
		t.Fatalf("start gateway: %v", err)
	}

	conn, err := gw.Dial(channelName)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close() }()

	if err := conn.Send(adtA01Message); err != nil {
		t.Fatalf("send: %v", err)
	}
	if !sink.WaitForReceived(1) {
		t.Fatal("sink never received the first POST")
	}

	release, err := harness.HoldWriteLock(t, gw.DBPath(channelName))
	if err != nil {
		t.Fatalf("hold write lock: %v", err)
	}
	releaseOne()
	if !sink.WaitForReceived(2) {
		t.Fatal("sink never received the retry POST")
	}
	releaseOne()

	ack, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read ack: %v", err)
	}
	if got, want := harness.NormalizeACK(ack), harness.NormalizeACK(adtA01AckAR); got != want {
		t.Errorf("ACK mismatch:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
	if !gw.WaitForLog(10*time.Second, isMsg(msgAckRecordFailed)) {
		t.Error("gateway never logged the failed ack record")
	}

	release()
	msgs, attempts := stopAndReadAll(t, gw)
	if len(msgs) != 1 {
		t.Fatalf("got %d message rows, want 1", len(msgs))
	}
	if msgs[0].XML == nil || *msgs[0].XML != adtA01TransformedXML {
		t.Errorf("xml = %v, want the transformed message (recorded before the store failed)", msgs[0].XML)
	}
	if msgs[0].Ack != nil || msgs[0].AckSentAt != nil || msgs[0].AckOutcome != nil {
		t.Errorf("ack columns = (%v, %v, %v), want all NULL when recording the ack failed", msgs[0].Ack, msgs[0].AckSentAt, msgs[0].AckOutcome)
	}
	if len(attempts) != 0 {
		t.Errorf("got %d delivery attempts, want 0 (attempt records hit the locked store)", len(attempts))
	}
	if rcvd := sink.Received(); len(rcvd) != 2 {
		t.Errorf("sink received %d messages, want 2", len(rcvd))
	}
}

func stopAndReadAll(t *testing.T, gw *harness.Gateway) ([]harness.MessageRow, []harness.DeliveryAttemptRow) {
	t.Helper()
	if err := gw.Stop(); err != nil {
		t.Fatalf("gateway did not shut down cleanly: %v", err)
	}
	msgs, err := harness.ReadMessages(gw.DBPath(channelName))
	if err != nil {
		t.Fatalf("read messages: %v", err)
	}
	attempts, err := harness.ReadDeliveryAttempts(gw.DBPath(channelName))
	if err != nil {
		t.Fatalf("read delivery attempts: %v", err)
	}
	return msgs, attempts
}

func assertAttempt(t *testing.T, a harness.DeliveryAttemptRow, messageID int64, outcome string, status int64) {
	t.Helper()
	if a.MessageID != messageID {
		t.Errorf("message_id = %d, want %d", a.MessageID, messageID)
	}
	if a.Outcome != outcome {
		t.Errorf("outcome = %q, want %q", a.Outcome, outcome)
	}
	if a.ResponseStatusCode == nil || *a.ResponseStatusCode != status {
		t.Errorf("response_status_code = %v, want %d", a.ResponseStatusCode, status)
	}
	if a.ResponseBody == nil || *a.ResponseBody != sinkBody {
		t.Errorf("response_body = %v, want %q", a.ResponseBody, sinkBody)
	}
	if a.Payload != adtA01TransformedXML {
		t.Errorf("payload mismatch:\n--- got ---\n%s\n--- want ---\n%s", a.Payload, adtA01TransformedXML)
	}
	if a.Origin != originInbound {
		t.Errorf("origin = %q, want INBOUND", a.Origin)
	}
	now := time.Now().Unix()
	for name, got := range map[string]int64{createdAtColumn: a.CreatedAt, updatedAtColumn: a.UpdatedAt} {
		if got < now-60 || got > now+60 {
			t.Errorf("%s = %d, want within a minute of %d (unix seconds)", name, got, now)
		}
	}
}
