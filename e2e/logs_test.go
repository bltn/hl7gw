//go:build e2e

package e2e

import (
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/bltn/hl7gw/e2e/internal/harness"
)

const (
	msgStarting               = "starting"
	msgListening              = "listening"
	msgConnectionOpened       = "connection opened"
	msgConnectionClosed       = "connection closed"
	msgMessageProcessed       = "message processed"
	msgDroppingUncorrelatable = "dropping uncorrelatable message"
	msgMissingRequiredFields  = "accepted message with missing required msh fields"
	msgPersistenceFailed      = "message persistence failed"
	msgAckWriteFailed         = "ack write failed"
	msgAckRecordFailed        = "recording ack outcome failed"
	msgStoppedAccepting       = "stopped accepting connections"
	msgShutdownDeadline       = "shutdown deadline reached, abandoned in-flight work"
	msgAtCapacity             = "at tcp conn capacity, pausing accepts"
	msgBelowCapacity          = "below tcp conn capacity, resuming accepts"
	msgHandshakeFailed        = "tls handshake failed"
	msgClientCertAccepted     = "tls client cert accepted"
	msgCertReloaded           = "server cert reloaded"
	msgCAReloaded             = "client ca pool reloaded"
	msgCertReloadFailed       = "server cert reload failed, still serving previous cert"
	msgCAReloadFailed         = "client ca reload failed, still verifying against previous pool"
	msgMetricsListening       = "metrics listening"
	msgRetentionSweep         = "retention sweep"
	msgRetentionSweepFailed   = "retention sweep failed"
	msgRetentionOff           = "retention off"
)

var humanListeningLine = regexp.MustCompile(`(?m)^\d{2}:\d{2}:\d{2} INFO  listening channel=`)

type logPred = func(harness.LogRecord) bool

func isMsg(msg string) logPred {
	return func(r harness.LogRecord) bool { return r.Msg == msg }
}

func isMsgOn(msg, channel string) logPred {
	return withAttr(isMsg(msg), "channel", channel)
}

func withAttr(pred logPred, key, value string) logPred {
	return func(r harness.LogRecord) bool { return pred(r) && r.Attr(key) == value }
}

func TestLogsTextFormatAtDebugWhenConfigured(t *testing.T) {
	t.Parallel()
	sink := harness.NewSink(t, http.StatusOK, "")
	gw, err := harness.StartGatewayWithLog(t, harness.Log{Level: "debug", Format: "text"}, harness.Channel{Name: channelName, DestURL: sink.URL()})
	if err != nil {
		t.Fatalf("start gateway: %v", err)
	}

	if _, err := gw.SendAndAck(channelName, adtA01Message); err != nil {
		t.Fatalf("send and ack: %v", err)
	}

	if !gw.WaitForLog(5*time.Second, isMsgOn(msgConnectionOpened, channelName)) {
		t.Errorf("debug level did not surface the connection opened record\n--- stdout ---\n%s", gw.Stdout())
	}
	if !humanListeningLine.MatchString(gw.Stdout()) {
		t.Errorf("stdout is not in the human-readable text format\n--- stdout ---\n%s", gw.Stdout())
	}
	if strings.Contains(gw.Stdout(), "\x1b[") {
		t.Errorf("stdout carries ANSI colour codes off a terminal\n--- stdout ---\n%s", gw.Stdout())
	}
	if !gw.WaitForLog(5*time.Second, withAttr(isMsgOn(msgMessageProcessed, channelName), "outcome", "SUCCESS")) {
		t.Errorf("message processed record missing\n--- stdout ---\n%s", gw.Stdout())
	}
}

func TestLogsDefaultToJSONOffATerminal(t *testing.T) {
	t.Parallel()
	sink := harness.NewSink(t, http.StatusOK, "")
	gw, err := harness.StartGateway(t, harness.Channel{Name: channelName, DestURL: sink.URL()})
	if err != nil {
		t.Fatalf("start gateway: %v", err)
	}

	if !gw.WaitForLog(5*time.Second, isMsgOn(msgListening, channelName)) {
		t.Fatalf("listening record missing\n--- stdout ---\n%s", gw.Stdout())
	}
	for _, line := range strings.Split(strings.TrimSpace(gw.Stdout()), "\n") {
		if !strings.HasPrefix(line, "{") {
			t.Errorf("non-JSON line on stdout when no format is configured: %q", line)
		}
	}
}
