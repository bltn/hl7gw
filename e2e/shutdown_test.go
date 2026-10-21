//go:build e2e

package e2e

import (
	"errors"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/bltn/hl7gw/e2e/internal/harness"
)

func TestClosesTCPConnectionsOnShutdown(t *testing.T) {
	t.Parallel()
	sink := harness.NewSink(t, http.StatusOK, "")
	gw, err := harness.StartGatewayWithLog(t, harness.Log{Level: "debug"}, harness.Channel{Name: channelName, DestURL: sink.URL()})
	if err != nil {
		t.Fatalf("start gateway: %v", err)
	}

	conn, err := gw.Dial(channelName)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close() }()

	if !gw.WaitForLog(5*time.Second, isMsgOn(msgConnectionOpened, channelName)) {
		t.Fatal("gateway never accepted the connection")
	}

	if err := gw.Stop(); err != nil {
		t.Fatalf("gateway exited uncleanly: %v", err)
	}

	err = conn.ReadUntilError(5 * time.Second)
	if err == nil {
		t.Fatal("expected error, got none")
	}

	if !errors.Is(err, io.EOF) {
		t.Fatalf("expected EOF on shutdown, got %v", err)
	}
}

func TestRefusesNewConnectionsWhileDraining(t *testing.T) {
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
		t.Fatal("sink never received the POST")
	}

	if err := gw.StopNoWait(); err != nil {
		t.Fatalf("signal: %v", err)
	}
	if !gw.WaitForLog(5*time.Second, isMsgOn(msgStoppedAccepting, channelName)) {
		t.Fatal("gateway never logged that it stopped accepting connections")
	}

	if late, err := gw.Dial(channelName); err == nil {
		_ = late.Close()
		t.Fatal("dialed a new connection after shutdown began, want refusal")
	}

	releaseOne()
	ack, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read ACK for the in-flight message: %v", err)
	}
	if got, want := harness.NormalizeACK(ack), harness.NormalizeACK(adtA01AckAA); got != want {
		t.Errorf("ACK mismatch:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}

	if err := gw.Stop(); err != nil {
		t.Fatalf("gateway exited uncleanly: %v", err)
	}
}

func TestStopsReadingNewMessagesWhileDraining(t *testing.T) {
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
		t.Fatalf("send first message: %v", err)
	}
	if !sink.WaitForReceived(1) {
		t.Fatal("sink never received the first POST")
	}

	if err := gw.StopNoWait(); err != nil {
		t.Fatalf("signal: %v", err)
	}
	if !gw.WaitForLog(5*time.Second, isMsgOn(msgStoppedAccepting, channelName)) {
		t.Fatal("gateway never logged that it stopped accepting connections")
	}

	if err := conn.Send(harness.SetControlID(adtA01Message, "MSGSD002")); err != nil {
		t.Fatalf("send second message: %v", err)
	}

	releaseOne()
	ack, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read ACK for the in-flight message: %v", err)
	}
	if got, want := harness.NormalizeACK(ack), harness.NormalizeACK(adtA01AckAA); got != want {
		t.Errorf("ACK mismatch:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
	if !conn.ClosedWithin(5 * time.Second) {
		t.Fatal("gateway did not close the connection after acking the in-flight message")
	}

	msgs, _ := stopAndReadAll(t, gw)
	if len(msgs) != 1 {
		t.Errorf("got %d message rows, want 1 (the second message must never be read)", len(msgs))
	}
	if rcvd := sink.Received(); len(rcvd) != 1 {
		t.Errorf("sink received %d POSTs, want 1", len(rcvd))
	}
}

func TestDrainsInFlightDeliveryOnShutdown(t *testing.T) {
	t.Parallel()
	sink, releaseOne := harness.NewGatedSink(t, http.StatusOK, sinkBody)
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
		t.Fatal("sink never received the POST")
	}

	if err := gw.StopNoWait(); err != nil {
		t.Fatalf("signal: %v", err)
	}
	if !gw.WaitForLog(5*time.Second, isMsgOn(msgStoppedAccepting, channelName)) {
		t.Fatal("gateway never logged that it stopped accepting connections")
	}

	releaseOne()
	ack, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read ACK for the in-flight message: %v", err)
	}
	if got, want := harness.NormalizeACK(ack), harness.NormalizeACK(adtA01AckAA); got != want {
		t.Errorf("ACK mismatch:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
	if err := conn.ReadUntilError(5 * time.Second); !errors.Is(err, io.EOF) {
		t.Fatalf("expected EOF after the in-flight message was acked, got %v", err)
	}

	msgs, attempts := stopAndReadAll(t, gw)
	if len(msgs) != 1 {
		t.Fatalf("got %d message rows, want 1", len(msgs))
	}
	if msgs[0].XML == nil || *msgs[0].XML != adtA01TransformedXML {
		t.Errorf("xml = %v, want the transformed message", msgs[0].XML)
	}
	if msgs[0].Ack == nil || msgs[0].AckOutcome == nil || *msgs[0].AckOutcome != outcomeSuccess {
		t.Errorf("ack columns = (%v, %v), want the ACK recorded with outcome SUCCESS", msgs[0].Ack, msgs[0].AckOutcome)
	}
	if len(attempts) != 1 {
		t.Fatalf("got %d delivery attempts, want 1", len(attempts))
	}
	assertAttempt(t, attempts[0], msgs[0].ID, outcomeSuccess, http.StatusOK)
}

func TestFinishesInFlightFrameOnShutdown(t *testing.T) {
	if testing.Short() {
		t.Skip("waits past a real idle timeout to prove the frame is in flight")
	}
	t.Parallel()

	sink := harness.NewSink(t, http.StatusOK, sinkBody)
	gw, err := harness.StartGateway(t, harness.Channel{Name: channelName, DestURL: sink.URL(), IdleTimeout: 3 * time.Second})
	if err != nil {
		t.Fatalf("start gateway: %v", err)
	}

	conn, err := gw.Dial(channelName)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close() }()

	frame := harness.Frame(adtA01Message)
	if err := conn.WriteRaw(frame[:len(frame)/2]); err != nil {
		t.Fatalf("write partial frame: %v", err)
	}
	if conn.ClosedWithin(4 * time.Second) {
		t.Fatal("gateway closed the connection mid-frame before shutdown")
	}

	if err := gw.StopNoWait(); err != nil {
		t.Fatalf("signal: %v", err)
	}
	if !gw.WaitForLog(5*time.Second, isMsgOn(msgStoppedAccepting, channelName)) {
		t.Fatal("gateway never logged that it stopped accepting connections")
	}

	if err := conn.WriteRaw(frame[len(frame)/2:]); err != nil {
		t.Fatalf("write rest of frame: %v", err)
	}
	if !conn.HasInboundWithin(10 * time.Second) {
		t.Fatal("no ACK arrived for the frame finished during shutdown")
	}
	ack, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read ACK: %v", err)
	}
	if got, want := harness.NormalizeACK(ack), harness.NormalizeACK(adtA01AckAA); got != want {
		t.Errorf("ACK mismatch:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}

	msgs, attempts := stopAndReadAll(t, gw)
	if len(msgs) != 1 {
		t.Fatalf("got %d message rows, want 1", len(msgs))
	}
	if len(attempts) != 1 {
		t.Fatalf("got %d delivery attempts, want 1", len(attempts))
	}
	assertAttempt(t, attempts[0], msgs[0].ID, outcomeSuccess, http.StatusOK)
	if rcvd := sink.Received(); len(rcvd) != 1 {
		t.Errorf("sink received %d POSTs, want 1", len(rcvd))
	}
}

func TestForcesShutdownAtDeadline(t *testing.T) {
	if testing.Short() {
		t.Skip("waits for the full 20s shutdown deadline")
	}
	t.Parallel()

	sink := harness.NewSink(t, http.StatusOK, "")
	gw, err := harness.StartGateway(t, harness.Channel{Name: channelName, DestURL: sink.URL(), IdleTimeout: 3 * time.Second})
	if err != nil {
		t.Fatalf("start gateway: %v", err)
	}

	conn, err := gw.Dial(channelName)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close() }()

	frame := harness.Frame(adtA01Message)
	if err := conn.WriteRaw(frame[:len(frame)/2]); err != nil {
		t.Fatalf("write partial frame: %v", err)
	}
	if conn.ClosedWithin(4 * time.Second) {
		t.Fatal("gateway closed the connection mid-frame before shutdown")
	}

	start := time.Now()
	if err := gw.StopNoWait(); err != nil {
		t.Fatalf("signal: %v", err)
	}
	if err := gw.Stop(); err != nil {
		t.Fatalf("gateway exited uncleanly: %v", err)
	}

	elapsed := time.Since(start)
	if elapsed < 19*time.Second || elapsed > 25*time.Second {
		t.Errorf("gateway exited %v after the signal, want the 20s shutdown deadline (not the 30s frame read timeout)", elapsed)
	}
	if gw.CountLogs(isMsg(msgShutdownDeadline)) == 0 {
		t.Error("gateway never logged that the shutdown deadline forced it to abandon in-flight work")
	}
	if rcvd := sink.Received(); len(rcvd) != 0 {
		t.Errorf("sink received %d POSTs, want 0", len(rcvd))
	}
	msgs, err := harness.ReadMessages(gw.DBPath(channelName))
	if err != nil {
		t.Fatalf("read messages: %v", err)
	}
	if len(msgs) != 0 {
		t.Errorf("got %d message rows, want 0 (the frame never completed)", len(msgs))
	}
}

func TestShutdownSkipsDeliveryRetry(t *testing.T) {
	t.Parallel()
	sink := harness.NewSink(t, http.StatusInternalServerError, sinkBody)
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

	if err := gw.StopNoWait(); err != nil {
		t.Fatalf("signal: %v", err)
	}

	ack, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read ACK for the delivery abandoned by shutdown: %v", err)
	}
	if got, want := harness.NormalizeACK(ack), harness.NormalizeACK(adtA01AckAE); got != want {
		t.Errorf("ACK mismatch:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
	if err := conn.ReadUntilError(5 * time.Second); !errors.Is(err, io.EOF) {
		t.Fatalf("expected EOF after the in-flight message was acked, got %v", err)
	}

	msgs, attempts := stopAndReadAll(t, gw)
	if rcvd := sink.Received(); len(rcvd) != 1 {
		t.Errorf("sink received %d POSTs, want 1 (shutdown must skip the retry)", len(rcvd))
	}
	if len(msgs) != 1 {
		t.Fatalf("got %d message rows, want 1", len(msgs))
	}
	if msgs[0].AckOutcome == nil || *msgs[0].AckOutcome != outcomeSuccess {
		t.Errorf("ack_outcome = %v, want SUCCESS (the AE ACK was sent)", msgs[0].AckOutcome)
	}
	if len(attempts) != 1 {
		t.Fatalf("got %d delivery attempts, want 1", len(attempts))
	}
	assertAttempt(t, attempts[0], msgs[0].ID, outcomeFailed, http.StatusInternalServerError)
}

func TestBoundsHungDeliveryOnShutdown(t *testing.T) {
	if testing.Short() {
		t.Skip("waits for the full 10s delivery timeout")
	}
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
		t.Fatal("sink never received the POST")
	}

	start := time.Now()
	if err := gw.StopNoWait(); err != nil {
		t.Fatalf("signal: %v", err)
	}

	ack, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read ACK for the hung delivery: %v", err)
	}
	if got, want := harness.NormalizeACK(ack), harness.NormalizeACK(adtA01AckAE); got != want {
		t.Errorf("ACK mismatch:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}

	msgs, attempts := stopAndReadAll(t, gw)
	if elapsed := time.Since(start); elapsed > 15*time.Second {
		t.Errorf("shutdown took %v, want the hung delivery bounded by its 10s timeout, not the 20s deadline", elapsed)
	}
	if len(msgs) != 1 {
		t.Fatalf("got %d message rows, want 1", len(msgs))
	}
	if msgs[0].AckOutcome == nil || *msgs[0].AckOutcome != outcomeSuccess {
		t.Errorf("ack_outcome = %v, want SUCCESS (the AE ACK was sent)", msgs[0].AckOutcome)
	}
	if len(attempts) != 1 {
		t.Fatalf("got %d delivery attempts, want 1", len(attempts))
	}
	if attempts[0].Outcome != outcomeFailed {
		t.Errorf("attempt outcome = %q, want FAILED", attempts[0].Outcome)
	}
	if attempts[0].ResponseStatusCode != nil {
		t.Errorf("response_status_code = %v, want NULL (the delivery timed out without a response)", *attempts[0].ResponseStatusCode)
	}

	releaseOne()
}

func TestShutsDownWhileAtConnCapacity(t *testing.T) {
	t.Parallel()
	sink := harness.NewSink(t, http.StatusOK, "")
	gw, err := harness.StartGateway(t, harness.Channel{Name: channelName, DestURL: sink.URL(), MaxTCPConns: 3})
	if err != nil {
		t.Fatalf("start gateway: %v", err)
	}

	conns := make([]*harness.Conn, 3)
	for i := range conns {
		conn, err := gw.Dial(channelName)
		if err != nil {
			t.Fatalf("dial conn %d: %v", i, err)
		}
		defer func() { _ = conn.Close() }()
		conns[i] = conn
	}

	if !gw.WaitForLog(5*time.Second, withAttr(isMsg(msgAtCapacity), "max", "3")) {
		t.Fatal("gateway did not pause accepts at its limit")
	}

	if err := gw.Stop(); err != nil {
		t.Fatalf("gateway exited uncleanly: %v", err)
	}

	for i, conn := range conns {
		if err := conn.ReadUntilError(5 * time.Second); !errors.Is(err, io.EOF) {
			t.Errorf("conn %d: expected EOF on shutdown, got %v", i, err)
		}
	}
}
