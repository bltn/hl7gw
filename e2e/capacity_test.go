//go:build e2e

package e2e

import (
	"net/http"
	"testing"
	"time"

	"github.com/bltn/hl7gw/e2e/internal/harness"
)

func TestQueuesExcessConnectionsAtCapacity(t *testing.T) {
	t.Parallel()
	sink := harness.NewSink(t, http.StatusOK, "")
	gw, err := harness.StartGateway(t, harness.Channel{Name: channelName, DestURL: sink.URL(), MaxTCPConns: 1})
	if err != nil {
		t.Fatalf("start gateway: %v", err)
	}
	defer func() {
		if err := gw.Stop(); err != nil {
			t.Errorf("gateway exited uncleanly: %v", err)
		}
	}()

	conn1, err := gw.Dial(channelName)
	if err != nil {
		t.Fatalf("dial first conn: %v", err)
	}
	defer func() { _ = conn1.Close() }()

	if !gw.WaitForLog(5*time.Second, withAttr(isMsg(msgAtCapacity), "max", "1")) {
		t.Fatalf("gateway did not log that accepts are paused")
	}

	conn2, err := gw.Dial(channelName)
	if err != nil {
		t.Fatalf("dial second conn while at capacity: %v", err)
	}
	defer func() { _ = conn2.Close() }()

	if err := conn2.Send(adtA01Message); err != nil {
		t.Fatalf("send on queued conn: %v", err)
	}
	if conn2.HasInboundWithin(quietWindow) {
		t.Fatal("gateway served the queued connection while at capacity")
	}
	if rcvd := sink.Received(); len(rcvd) != 0 {
		t.Fatalf("sink received %d messages while the sender was queued, want 0", len(rcvd))
	}

	if err := conn1.Close(); err != nil {
		t.Fatalf("close first conn: %v", err)
	}

	ack, err := conn2.ReadMessage()
	if err != nil {
		t.Fatalf("read ACK on queued conn after slot freed: %v", err)
	}
	if got, want := harness.NormalizeACK(ack), harness.NormalizeACK(adtA01AckAA); got != want {
		t.Errorf("ACK mismatch:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
	if rcvd := sink.Received(); len(rcvd) != 1 {
		t.Errorf("sink received %d messages once the queued conn was served, want 1", len(rcvd))
	}
}
