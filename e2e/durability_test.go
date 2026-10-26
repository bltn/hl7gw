//go:build e2e

package e2e

import (
	"net/http"
	"strings"
	"testing"

	"github.com/bltn/hl7gw/e2e/internal/harness"
)

func TestPersistedMessagesSurviveKillAndRestart(t *testing.T) {
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

	acked := []string{"MSGKILL01", "MSGKILL02", "MSGKILL03"}
	const held = "MSGKILL04"
	for _, id := range append(acked, held) {
		if err := conn.Send(harness.SetControlID(adtA01Message, id)); err != nil {
			t.Fatalf("send %s: %v", id, err)
		}
	}
	for i, id := range acked {
		if !sink.WaitForReceived(i + 1) {
			t.Fatalf("sink never received %s", id)
		}
		releaseOne()
		ack, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read ACK for %s: %v", id, err)
		}
		wantAck := strings.Replace(adtA01AckAA, "MSA|AA|MSG00001", "MSA|AA|"+id, 1)
		if got, want := harness.NormalizeACK(ack), harness.NormalizeACK(wantAck); got != want {
			t.Fatalf("ACK mismatch for %s:\n--- got ---\n%s\n--- want ---\n%s", id, got, want)
		}
	}
	if !sink.WaitForReceived(len(acked) + 1) {
		t.Fatalf("sink never received %s", held)
	}
	if conn.HasInboundWithin(quietWindow) {
		t.Fatalf("gateway acked %s while the sink was still holding it", held)
	}

	if err := gw.Kill(); err != nil {
		t.Fatalf("kill gateway: %v", err)
	}
	releaseOne()

	gw2, err := gw.Restart(t)
	if err != nil {
		t.Fatalf("restart gateway on the same data dir: %v", err)
	}
	resp, err := gw2.APIGet(readyzPath, "")
	if err != nil {
		t.Fatalf("get %s after restart: %v", readyzPath, err)
	}
	if resp.Status != http.StatusOK {
		t.Fatalf("%s after restart = %d, want 200; body %s", readyzPath, resp.Status, resp.Body)
	}

	const after = "MSGAFTER"
	conn2, err := gw2.Dial(channelName)
	if err != nil {
		t.Fatalf("dial after restart: %v", err)
	}
	defer func() { _ = conn2.Close() }()
	if err := conn2.Send(harness.SetControlID(adtA01Message, after)); err != nil {
		t.Fatalf("send after restart: %v", err)
	}
	if !sink.WaitForReceived(len(acked) + 2) {
		t.Fatalf("sink never received %s", after)
	}
	releaseOne()
	ack, err := conn2.ReadMessage()
	if err != nil {
		t.Fatalf("read ACK for %s: %v", after, err)
	}
	wantAck := strings.Replace(adtA01AckAA, "MSA|AA|MSG00001", "MSA|AA|"+after, 1)
	if got, want := harness.NormalizeACK(ack), harness.NormalizeACK(wantAck); got != want {
		t.Errorf("ACK after restart mismatch:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}

	rows, attempts := stopAndReadAll(t, gw2)
	wantIDs := append(append(append([]string{}, acked...), held), after)
	if len(rows) != len(wantIDs) {
		t.Fatalf("got %d message rows, want %d (%v)", len(rows), len(wantIDs), wantIDs)
	}
	byMessage := map[int64][]harness.DeliveryAttemptRow{}
	for _, a := range attempts {
		byMessage[a.MessageID] = append(byMessage[a.MessageID], a)
	}
	for i, row := range rows {
		id, as := wantIDs[i], byMessage[row.ID]
		if cid, _ := harness.Field(row.Raw, "MSH", 10); cid != id {
			t.Errorf("row %d holds %q, want %s", i, cid, id)
		}
		if row.XML == nil {
			t.Errorf("row %d (%s) has no transform", i, id)
		}
		delivered := len(as) == 1 && as[0].Outcome == outcomeSuccess && as[0].Origin == originInbound
		switch id {
		case held:
			if len(as) != 0 || row.Ack != nil {
				t.Errorf("%s was in flight at the kill: attempts = %+v, ack = %v; want none", id, as, row.Ack)
			}
		case after:
			if !delivered || row.AckOutcome == nil || *row.AckOutcome != outcomeSuccess {
				t.Errorf("%s: attempts = %+v, ack_outcome = %v; want delivered and acked", id, as, row.AckOutcome)
			}
		default:
			if !delivered {
				t.Errorf("%s was acked AA before the kill but attempts = %+v; want one successful inbound attempt", id, as)
			}
		}
	}
}
