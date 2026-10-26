//go:build e2e

package e2e

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bltn/hl7gw/e2e/internal/harness"
)

const (
	retentionDefault    = ""
	retentionOff        = `"retention": "off", `
	retentionSevenDays  = `"retention": "7d", `
	retentionAdminToken = "e2e-retention-admin-token" //nolint:gosec // G101 -- placeholder token for a throwaway test process
	retentionAuth       = "Bearer " + retentionAdminToken
)

func startRetentionGateway(t *testing.T, retention string, channels ...string) *harness.Gateway {
	t.Helper()
	ports, err := harness.FreePorts(len(channels) + 1)
	if err != nil {
		t.Fatalf("find free ports: %v", err)
	}
	dir := t.TempDir()
	for name, token := range map[string]string{"admin.token": retentionAdminToken, "channel.token": "e2e-channel-token"} { //nolint:gosec // G101 -- placeholder tokens for a throwaway test process
		if err := os.WriteFile(filepath.Join(dir, name), []byte(token+"\n"), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	channelPorts := make(map[string]int, len(channels))
	chans := make([]string, 0, len(channels))
	for i, name := range channels {
		channelPorts[name] = ports[i+1]
		chans = append(chans, fmt.Sprintf(`{"name": %q, "source": {"port": %d, "idle_timeout": "24h", `+tlsOffOK+`}, `+destinationOK+`}`, name, ports[i+1]))
	}
	cfg := fmt.Sprintf(`{"admin": {"port": %d, "token_file": "admin.token"}, %s"channels": [%s]}`, ports[0], retention, strings.Join(chans, ", "))
	cfgPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	gw, err := harness.StartGatewayFromConfig(t, cfgPath, ports[0], channelPorts)
	if err != nil {
		t.Fatalf("start gateway: %v", err)
	}
	return gw
}

func stopForSeeding(t *testing.T, gw *harness.Gateway) {
	t.Helper()
	if err := gw.Stop(); err != nil {
		t.Fatalf("gateway did not shut down cleanly before seeding: %v", err)
	}
}

func seedMessage(t *testing.T, dbPath string, id int64, receivedAt time.Time) {
	t.Helper()
	ms := receivedAt.UnixMilli()
	err := harness.ExecSQL(dbPath,
		fmt.Sprintf("INSERT INTO messages (id, received_at, raw) VALUES (%d, %d, 'MSH|^~\\&|seed%d')", id, ms, id),
		fmt.Sprintf("INSERT INTO delivery_attempts (message_id, attempted_at, outcome, payload, origin) VALUES (%d, %d, 'SUCCESS', '<seed/>', 'INBOUND')", id, ms),
	)
	if err != nil {
		t.Fatalf("seed message %d: %v", id, err)
	}
}

func firstLog(logs []harness.LogRecord, pred logPred) int {
	for i, r := range logs {
		if pred(r) {
			return i
		}
	}
	return -1
}

func expectMessageStatus(t *testing.T, gw *harness.Gateway, channel string, id int64, want int) {
	t.Helper()
	path := fmt.Sprintf("/messages/%d?channel=%s", id, channel)
	resp, err := gw.APIGet(path, retentionAuth)
	if err != nil {
		t.Fatalf("get %s: %v", path, err)
	}
	if resp.Status != want || resp.ReqSpecErr != nil || resp.RespSpecErr != nil {
		t.Errorf("get %s: status = %d, reqSpecErr = %v, respSpecErr = %v; want %d and spec-clean; body %s",
			path, resp.Status, resp.ReqSpecErr, resp.RespSpecErr, want, resp.Body)
	}
}

func TestRetentionPrunesExpiredMessagesAtStartup(t *testing.T) {
	t.Parallel()
	channels := []string{alphaChannel, betaChannel}
	first := startRetentionGateway(t, retentionDefault, channels...)
	stopForSeeding(t, first)
	now := time.Now()
	for _, ch := range channels {
		seedMessage(t, first.DBPath(ch), 1, now.AddDate(0, 0, -31))
		seedMessage(t, first.DBPath(ch), 2, now.AddDate(0, 0, -29))
	}

	gw, err := first.Restart(t)
	if err != nil {
		t.Fatalf("restart gateway: %v", err)
	}

	logs := gw.Logs()
	listening := firstLog(logs, isMsg(msgListening))
	adminListening := firstLog(logs, isMsg(msgAdminAPIListening))
	for _, ch := range channels {
		sweep := firstLog(logs, isMsgOn(msgRetentionSweep, ch))
		if sweep < 0 {
			t.Fatalf("no %q record for channel %s\n--- stdout ---\n%s", msgRetentionSweep, ch, gw.Stdout())
		}
		if got := logs[sweep].Attr("deleted"); got != "1" {
			t.Errorf("channel %s: deleted = %q, want 1", ch, got)
		}
		if _, err := time.Parse(time.RFC3339, logs[sweep].Attr("cutoff")); err != nil {
			t.Errorf("channel %s: cutoff %q is not RFC 3339: %v", ch, logs[sweep].Attr("cutoff"), err)
		}
		if sweep > listening || sweep > adminListening {
			t.Errorf("channel %s: retention sweep logged at %d, after listening (%d) or admin api listening (%d)", ch, sweep, listening, adminListening)
		}
	}

	expectMessageStatus(t, gw, alphaChannel, 1, http.StatusNotFound)
	expectMessageStatus(t, gw, alphaChannel, 2, http.StatusOK)
	if body, ok := gw.WaitForMetricLine(`hl7gw_messages_pruned_total{channel="alpha"} 1`, 5*time.Second); !ok {
		t.Errorf("metrics missing the pruned counter\n--- body ---\n%s", body)
	}

	if err := gw.Stop(); err != nil {
		t.Fatalf("gateway did not shut down cleanly: %v", err)
	}
	for _, ch := range channels {
		msgs, attempts := readAll(t, gw, ch)
		if len(msgs) != 1 || msgs[0].ID != 2 {
			t.Errorf("channel %s: surviving messages = %+v, want only id 2", ch, msgs)
		}
		if len(attempts) != 1 || attempts[0].MessageID != 2 {
			t.Errorf("channel %s: surviving attempts = %+v, want only the one for message 2", ch, attempts)
		}
	}
}

func TestRetentionOffKeepsEverything(t *testing.T) {
	t.Parallel()
	first := startRetentionGateway(t, retentionOff, channelName)
	stopForSeeding(t, first)
	seedMessage(t, first.DBPath(channelName), 1, time.Now().AddDate(0, 0, -400))

	gw, err := first.Restart(t)
	if err != nil {
		t.Fatalf("restart gateway: %v", err)
	}

	if n := gw.CountLogs(isMsg(msgRetentionOff)); n != 1 {
		t.Errorf("%q records = %d, want 1", msgRetentionOff, n)
	}
	if n := gw.CountLogs(isMsg(msgRetentionSweep)); n != 0 {
		t.Errorf("%q records = %d, want 0", msgRetentionSweep, n)
	}
	expectMessageStatus(t, gw, channelName, 1, http.StatusOK)
	if body, ok := gw.WaitForMetricLine(`hl7gw_messages_pruned_total{channel="e2e"} 0`, 5*time.Second); !ok {
		t.Errorf("metrics missing the zero pruned counter\n--- body ---\n%s", body)
	}

	msgs, attempts := stopAndReadAll(t, gw)
	if len(msgs) != 1 || len(attempts) != 1 {
		t.Errorf("rows after restart: %d messages, %d attempts; want 1 and 1", len(msgs), len(attempts))
	}
}

func TestRetentionPrunesInBatches(t *testing.T) {
	t.Parallel()
	first := startRetentionGateway(t, retentionSevenDays, channelName)
	stopForSeeding(t, first)
	now := time.Now()
	err := harness.ExecSQL(first.DBPath(channelName),
		fmt.Sprintf("WITH RECURSIVE seq(n) AS (SELECT 1 UNION ALL SELECT n + 1 FROM seq WHERE n < 2500) INSERT INTO messages (received_at, raw) SELECT %d, 'old' FROM seq", now.AddDate(0, 0, -31).UnixMilli()),
		"INSERT INTO delivery_attempts (message_id, attempted_at, outcome, payload, origin) SELECT id, received_at, 'SUCCESS', '<old/>', 'INBOUND' FROM messages",
	)
	if err != nil {
		t.Fatalf("seed backlog: %v", err)
	}
	seedMessage(t, first.DBPath(channelName), 2501, now.AddDate(0, 0, -1))

	gw, err := first.Restart(t)
	if err != nil {
		t.Fatalf("restart gateway: %v", err)
	}

	logs := gw.Logs()
	sweep := firstLog(logs, isMsgOn(msgRetentionSweep, channelName))
	if sweep < 0 {
		t.Fatalf("no %q record\n--- stdout ---\n%s", msgRetentionSweep, gw.Stdout())
	}
	if got := logs[sweep].Attr("deleted"); got != "2500" {
		t.Errorf("deleted = %q, want 2500", got)
	}
	if body, ok := gw.WaitForMetricLine(`hl7gw_messages_pruned_total{channel="e2e"} 2500`, 5*time.Second); !ok {
		t.Errorf("metrics missing the pruned counter\n--- body ---\n%s", body)
	}

	msgs, attempts := stopAndReadAll(t, gw)
	if len(msgs) != 1 || msgs[0].ID != 2501 {
		t.Errorf("surviving messages = %+v, want only id 2501", msgs)
	}
	if len(attempts) != 1 || attempts[0].MessageID != 2501 {
		t.Errorf("surviving attempts = %+v, want only the one for message 2501", attempts)
	}
}

func TestRetentionSweepFailureKeepsGatewayUp(t *testing.T) {
	t.Parallel()
	channels := []string{alphaChannel, betaChannel}
	first := startRetentionGateway(t, retentionDefault, channels...)
	stopForSeeding(t, first)
	now := time.Now()
	for _, ch := range channels {
		seedMessage(t, first.DBPath(ch), 1, now.AddDate(0, 0, -31))
	}
	const refuseDeletes = "CREATE TRIGGER messages_read_only BEFORE DELETE ON messages BEGIN SELECT RAISE(ABORT, 'messages are read-only'); END"
	if err := harness.ExecSQL(first.DBPath(alphaChannel), refuseDeletes); err != nil {
		t.Fatalf("poison %s: %v", alphaChannel, err)
	}

	gw, err := first.Restart(t)
	if err != nil {
		t.Fatalf("restart gateway: %v", err)
	}

	logs := gw.Logs()
	failed := firstLog(logs, isMsgOn(msgRetentionSweepFailed, alphaChannel))
	if failed < 0 {
		t.Fatalf("no %q record for %s\n--- stdout ---\n%s", msgRetentionSweepFailed, alphaChannel, gw.Stdout())
	}
	if got := logs[failed].Attr("err"); !strings.Contains(got, "messages are read-only") {
		t.Errorf("err = %q, want the trigger's message", got)
	}
	if n := gw.CountLogs(isMsgOn(msgRetentionSweep, betaChannel)); n != 1 {
		t.Errorf("%q records for %s = %d, want 1", msgRetentionSweep, betaChannel, n)
	}
	resp, err := gw.APIGet(healthzPath, "")
	if err != nil {
		t.Fatalf("get %s: %v", healthzPath, err)
	}
	if resp.Status != http.StatusOK {
		t.Errorf("%s status = %d, want 200", healthzPath, resp.Status)
	}
	if err := gw.Stop(); err != nil {
		t.Errorf("gateway did not shut down cleanly: %v", err)
	}
}
