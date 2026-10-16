//go:build e2e

package e2e

import (
	"net/http"
	"testing"
	"time"

	"github.com/bltn/hl7gw/e2e/internal/harness"
)

func TestClosesConnWhenFrameTrailerInvalid(t *testing.T) {
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

	conn, err := gw.Dial(channelName)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close() }()

	frame := harness.Frame(adtA01Message)
	frame[len(frame)-1] = 'X'
	if err := conn.WriteRaw(frame); err != nil {
		t.Fatalf("write frame with bad trailer: %v", err)
	}

	if !gw.WaitForLog(5*time.Second, withAttr(isMsg(msgConnectionClosed), "reason", "protocol_error")) {
		t.Fatalf("gateway did not log the bad trailer byte")
	}
	if !conn.ClosedWithin(5 * time.Second) {
		t.Fatal("gateway did not close the connection")
	}
	if rcvd := sink.Received(); len(rcvd) != 0 {
		t.Errorf("sink received %d messages from a malformed frame, want 0", len(rcvd))
	}
}

func TestSurvivesConnDroppedMidFrame(t *testing.T) {
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

	conn, err := gw.Dial(channelName)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}

	frame := harness.Frame(adtA01Message)
	if err := conn.WriteRaw(frame[:len(frame)-1]); err != nil {
		t.Fatalf("write frame without trailer: %v", err)
	}
	if err := conn.Close(); err != nil {
		t.Fatalf("close mid-frame: %v", err)
	}

	ack, err := gw.SendAndAck(channelName, adtA01Message)
	if err != nil {
		t.Fatalf("send on a fresh connection: %v", err)
	}
	if got, want := harness.NormalizeACK(ack), harness.NormalizeACK(adtA01AckAA); got != want {
		t.Errorf("ACK mismatch:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
	if rcvd := sink.Received(); len(rcvd) != 1 {
		t.Errorf("sink received %d messages, want 1 (the half-framed message must not be forwarded)", len(rcvd))
	}
}
