//go:build e2e

package e2e

import (
	"net/http"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/bltn/hl7gw/e2e/internal/harness"
)

const propertyMessages = 200

func TestPropertyStreamedMessagesAckedInOrder(t *testing.T) {
	t.Parallel()
	seed := os.Getenv("HL7GW_PROP_SEED")
	if seed == "" {
		seed = strconv.FormatInt(time.Now().UnixNano(), 10)
	}
	t.Logf("seed %s (rerun with HL7GW_PROP_SEED=%s)", seed, seed)

	sink := harness.NewSink(t, http.StatusOK, sinkBody)
	gw, err := harness.StartGateway(t, harness.Channel{Name: channelName, DestURL: sink.URL()})
	if err != nil {
		t.Fatalf("start gateway: %v", err)
	}

	conn, err := gw.Dial(channelName)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close() }()

	inbound := conn.ReadMessages(propertyMessages, 60*time.Second)
	gen := harness.NewMessageGen(seed)
	sent := make([]harness.Generated, 0, propertyMessages)
	for range propertyMessages {
		g := gen.Next()
		if err := conn.Send(g.Raw); err != nil {
			t.Fatalf("send %s: %v", g.ControlID, err)
		}
		sent = append(sent, g)
	}

	acks := <-inbound
	if acks.Err != nil {
		t.Fatalf("read ACKs: %v", acks.Err)
	}
	for i, ack := range acks.Messages {
		code, _ := harness.Field(ack, "MSA", 1)
		echoed, _ := harness.Field(ack, "MSA", 2)
		if code != "AA" || echoed != sent[i].ControlID {
			t.Errorf("ACK %d: MSA|%s|%s, want MSA|AA|%s\n%s", i, code, echoed, sent[i].ControlID, sent[i].Raw)
		}
	}
	if conn.HasInboundWithin(quietWindow) {
		t.Error("gateway sent more ACKs than messages")
	}

	msgs, attempts := stopAndReadAll(t, gw)
	if len(msgs) != propertyMessages {
		t.Fatalf("got %d message rows, want %d", len(msgs), propertyMessages)
	}
	byMessage := map[int64][]harness.DeliveryAttemptRow{}
	for _, a := range attempts {
		byMessage[a.MessageID] = append(byMessage[a.MessageID], a)
	}
	for i, row := range msgs {
		cid, _ := harness.Field(row.Raw, "MSH", 10)
		if cid != sent[i].ControlID {
			t.Errorf("row %d journals control ID %q, want %q", i, cid, sent[i].ControlID)
		}
		if row.XML == nil {
			t.Errorf("row %d (%s) has no transform", i, cid)
		}
		if as := byMessage[row.ID]; len(as) != 1 || as[0].Outcome != outcomeSuccess || as[0].Origin != originInbound {
			t.Errorf("row %d (%s) attempts = %+v, want one successful inbound attempt", i, cid, as)
		}
	}
	if got := len(sink.Received()); got != propertyMessages {
		t.Errorf("sink received %d messages, want %d", got, propertyMessages)
	}
}
