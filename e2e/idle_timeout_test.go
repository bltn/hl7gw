//go:build e2e

package e2e

import (
	"testing"
	"time"

	"github.com/bltn/hl7gw/e2e/internal/harness"
)

func TestClosesIdleConnsPerChannelTimeout(t *testing.T) {
	if testing.Short() {
		t.Skip("waits for real idle timeouts")
	}
	t.Parallel()

	destURL, err := harness.DeadDestURL()
	if err != nil {
		t.Fatalf("dead destination url: %v", err)
	}
	gw, err := harness.StartGateway(t,
		harness.Channel{Name: "fast", DestURL: destURL, TCPIdleTimeoutSecs: 5},
		harness.Channel{Name: "slow", DestURL: destURL, TCPIdleTimeoutSecs: 10},
	)
	if err != nil {
		t.Fatalf("start gateway: %v", err)
	}

	start := time.Now()
	fast, err := gw.Dial("fast")
	if err != nil {
		t.Fatalf("dial fast: %v", err)
	}
	defer func() { _ = fast.Close() }()
	slow, err := gw.Dial("slow")
	if err != nil {
		t.Fatalf("dial slow: %v", err)
	}
	defer func() { _ = slow.Close() }()

	if fast.ClosedWithin(4 * time.Second) {
		t.Fatal("fast channel closed its connection before the 5s idle timeout")
	}
	if !fast.ClosedWithin(5 * time.Second) {
		t.Fatal("fast channel did not close its idle connection")
	}
	if slow.ClosedWithin(9*time.Second - time.Since(start)) {
		t.Fatal("slow channel closed its connection before the 10s idle timeout")
	}
	if !slow.ClosedWithin(5 * time.Second) {
		t.Fatal("slow channel did not close its idle connection")
	}

	if !gw.WaitForLog(5*time.Second, withAttr(isMsgOn(msgConnectionClosed, "slow"), "reason", "timeout")) {
		t.Errorf("gateway did not log an i/o timeout")
	}
}
