//go:build e2e

package e2e

import (
	"net/http"
	"testing"
	"time"

	"github.com/bltn/hl7gw/e2e/internal/harness"
)

func TestEnforcesConnectionLimitsPerChannel(t *testing.T) {
	t.Parallel()
	saturatedSink := harness.NewSink(t, http.StatusOK, "")
	openSink := harness.NewSink(t, http.StatusOK, "")

	gw, err := harness.StartGateway(t,
		harness.Channel{Name: "saturated", DestURL: saturatedSink.URL(), MaxTCPConns: 1},
		harness.Channel{Name: "open", DestURL: openSink.URL(), MaxTCPConns: 2},
	)
	if err != nil {
		t.Fatalf("start gateway: %v", err)
	}
	defer func() {
		if err := gw.Stop(); err != nil {
			t.Errorf("gateway exited uncleanly: %v", err)
		}
	}()

	held, err := gw.Dial("saturated")
	if err != nil {
		t.Fatalf("dial saturated: %v", err)
	}
	defer func() { _ = held.Close() }()

	if !gw.WaitForLog(5*time.Second, withAttr(isMsgOn(msgAtCapacity, "saturated"), "max", "1")) {
		t.Fatalf("channel saturated did not pause accepts at its limit")
	}

	queued, err := gw.Dial("saturated")
	if err != nil {
		t.Fatalf("dial queued conn: %v", err)
	}
	defer func() { _ = queued.Close() }()

	if err := queued.Send(adtA01Message); err != nil {
		t.Fatalf("send on queued conn: %v", err)
	}

	ack, err := gw.SendAndAck("open", adtA01Message)
	if err != nil {
		t.Fatalf("send on open channel while sibling saturated: %v", err)
	}
	if got, want := harness.NormalizeACK(ack), harness.NormalizeACK(adtA01AckAA); got != want {
		t.Errorf("open channel ACK mismatch:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}

	if queued.HasInboundWithin(quietWindow) {
		t.Fatalf("channel saturated served a connection beyond its limit")
	}
	if rcvd := saturatedSink.Received(); len(rcvd) != 0 {
		t.Fatalf("saturated sink received %d messages while the sender was queued, want 0", len(rcvd))
	}

	if err := held.Close(); err != nil {
		t.Fatalf("close held conn: %v", err)
	}

	ack, err = queued.ReadMessage()
	if err != nil {
		t.Fatalf("read ACK on queued conn after slot freed: %v", err)
	}
	if got, want := harness.NormalizeACK(ack), harness.NormalizeACK(adtA01AckAA); got != want {
		t.Errorf("queued conn ACK mismatch:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}

	if !gw.WaitForLog(5*time.Second, isMsgOn(msgBelowCapacity, "saturated")) {
		t.Fatalf("channel saturated did not resume accepts after a slot freed")
	}
	if rcvd := saturatedSink.Received(); len(rcvd) != 1 {
		t.Errorf("saturated sink received %d messages once the queued conn was served, want 1", len(rcvd))
	}
	if rcvd := openSink.Received(); len(rcvd) != 1 {
		t.Errorf("open sink received %d messages, want 1", len(rcvd))
	}
}
