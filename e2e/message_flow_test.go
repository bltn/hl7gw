//go:build e2e

package e2e

import (
	_ "embed"
	"encoding/xml"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/bltn/hl7gw/e2e/internal/harness"
)

//go:embed testdata/adt_a01/message.txt
var adtA01Message string

//go:embed testdata/adt_a01/transformed.xml
var adtA01TransformedXML string

//go:embed testdata/adt_a01/ack_aa.txt
var adtA01AckAA string

//go:embed testdata/adt_a01/ack_ae.txt
var adtA01AckAE string

//go:embed testdata/escapes/message.txt
var escapesMessage string

//go:embed testdata/escapes/transformed.xml
var escapesTransformedXML string

//go:embed testdata/escapes/ack_aa.txt
var escapesAckAA string

//go:embed testdata/missing_field_separator/message.txt
var missingFieldSeparatorMessage string

//go:embed testdata/missing_field_separator/ack_ae.txt
var missingFieldSeparatorAckAE string

//go:embed testdata/missing_trigger_event/message.txt
var missingTriggerEventMessage string

//go:embed testdata/missing_trigger_event/ack_ae.txt
var missingTriggerEventAckAE string

//go:embed testdata/missing_msh12/message.txt
var missingMSH12Message string

//go:embed testdata/missing_msh12/ack_aa.txt
var missingMSH12AckAA string

//go:embed testdata/missing_message_code/message.txt
var missingMessageCodeMessage string

//go:embed testdata/missing_message_code/ack_ae.txt
var missingMessageCodeAckAE string

//go:embed testdata/empty_msh9/message.txt
var emptyMSH9Message string

//go:embed testdata/empty_msh9/ack_ae.txt
var emptyMSH9AckAE string

//go:embed testdata/missing_msh11/message.txt
var missingMSH11Message string

//go:embed testdata/missing_msh11/ack_aa.txt
var missingMSH11AckAA string

//go:embed testdata/missing_msh11_and_12/message.txt
var missingMSH11And12Message string

//go:embed testdata/missing_msh11_and_12/ack_aa.txt
var missingMSH11And12AckAA string

//go:embed testdata/bad_segment_id/message.txt
var badSegmentIDMessage string

//go:embed testdata/bad_segment_id/ack_ae.txt
var badSegmentIDAckAE string

//go:embed testdata/escaped_control_id/message.txt
var escapedControlIDMessage string

//go:embed testdata/escaped_control_id/transformed.xml
var escapedControlIDTransformedXML string

//go:embed testdata/escaped_control_id/ack_aa.txt
var escapedControlIDAckAA string

const (
	channelName               = "e2e"
	basicChannel              = "basic"
	openChannel               = "open"
	basicUsername             = "svc"
	clientSendBufferBytes     = 64 << 10
	spamFieldBytes            = 1 << 20
	spamFrameLimit            = 64
	quietWindow               = 250 * time.Millisecond
	unbufferableAckFieldBytes = 32 << 20
)

func TestAcksAAWhenSinkAccepts(t *testing.T) {
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

	ack, err := gw.SendAndAck(channelName, adtA01Message)
	if err != nil {
		t.Fatalf("send and ack: %v", err)
	}

	rcvd := sink.Received()
	if len(rcvd) != 1 {
		t.Fatalf("sink received %d messages, want 1", len(rcvd))
	}

	if rcvd[0] != adtA01TransformedXML {
		t.Errorf("XML mismatch:\n--- got ---\n%s\n--- want ---\n%s", rcvd[0], adtA01TransformedXML)
	}

	if auths := sink.AuthHeaders(); len(auths) != 1 || auths[0] != "Bearer "+gw.ChannelToken() {
		t.Errorf("authorization headers = %v, want exactly one %q", auths, "Bearer "+gw.ChannelToken())
	}

	if got, want := harness.NormalizeACK(ack), harness.NormalizeACK(adtA01AckAA); got != want {
		t.Errorf("ACK mismatch:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestDestinationAuthHeader(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		channel harness.Channel
		want    string
	}{
		{"bearer", harness.Channel{Name: channelName, Auth: &harness.ChannelAuth{Type: harness.AuthBearer}}, "Bearer harness-channel-token"},
		{"basic", harness.Channel{Name: basicChannel, Auth: &harness.ChannelAuth{Type: harness.AuthBasic, Username: basicUsername}}, "Basic c3ZjOmhhcm5lc3MtY2hhbm5lbC1wYXNzd29yZA=="},
		{"none", harness.Channel{Name: openChannel, Auth: &harness.ChannelAuth{Type: harness.AuthNone}}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sink := harness.NewSink(t, http.StatusOK, "")
			tc.channel.DestURL = sink.URL()
			gw, err := harness.StartGateway(t, tc.channel)
			if err != nil {
				t.Fatalf("start gateway: %v", err)
			}
			ack, err := gw.SendAndAck(tc.channel.Name, adtA01Message)
			if err != nil {
				t.Fatalf("send and ack: %v", err)
			}
			if got, want := harness.NormalizeACK(ack), harness.NormalizeACK(adtA01AckAA); got != want {
				t.Errorf("ACK mismatch:\n--- got ---\n%s\n--- want ---\n%s", got, want)
			}
			if auths := sink.AuthHeaders(); len(auths) != 1 || auths[0] != tc.want {
				t.Errorf("authorization headers = %q, want exactly one %q", auths, tc.want)
			}
		})
	}
}

func TestTransformsToMirthHL7MessageRoot(t *testing.T) {
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

	if _, err := gw.SendAndAck(channelName, adtA01Message); err != nil {
		t.Fatalf("send and ack: %v", err)
	}

	rcvd := sink.Received()
	if len(rcvd) != 1 {
		t.Fatalf("sink received %d messages, want 1", len(rcvd))
	}

	root, err := xmlRootElement(rcvd[0])
	if err != nil {
		t.Fatalf("parse transformed XML: %v", err)
	}
	if root != "HL7Message" {
		t.Errorf("root element = %q, want %q (Mirth wraps every ER7-to-XML message in HL7Message, not the message type e.g. ADT_A01)", root, "HL7Message")
	}
}

func xmlRootElement(doc string) (string, error) {
	dec := xml.NewDecoder(strings.NewReader(doc))
	for {
		tok, err := dec.Token()
		if err != nil {
			return "", err
		}
		if start, ok := tok.(xml.StartElement); ok {
			return start.Name.Local, nil
		}
	}
}

func TestAcksAEWhenSinkRejects(t *testing.T) {
	t.Parallel()
	sink := harness.NewSink(t, http.StatusInternalServerError, "")
	gw, err := harness.StartGateway(t, harness.Channel{Name: channelName, DestURL: sink.URL()})
	if err != nil {
		t.Fatalf("start gateway: %v", err)
	}
	defer func() {
		if err := gw.Stop(); err != nil {
			t.Errorf("gateway exited uncleanly: %v", err)
		}
	}()

	ack, err := gw.SendAndAck(channelName, adtA01Message)
	if err != nil {
		t.Fatalf("send and ack: %v", err)
	}

	rcvd := sink.Received()
	if len(rcvd) != 2 {
		t.Fatalf("sink received %d messages, want 2 (initial + one retry)", len(rcvd))
	}

	for i, got := range rcvd {
		if got != adtA01TransformedXML {
			t.Errorf("XML mismatch on POST %d:\n--- got ---\n%s\n--- want ---\n%s", i, got, adtA01TransformedXML)
		}
	}

	if got, want := harness.NormalizeACK(ack), harness.NormalizeACK(adtA01AckAE); got != want {
		t.Errorf("ACK mismatch:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestAcksAEWhenSinkUnavailable(t *testing.T) {
	t.Parallel()
	destURL, err := harness.DeadDestURL()
	if err != nil {
		t.Fatalf("dead destination url: %v", err)
	}
	gw, err := harness.StartGateway(t, harness.Channel{Name: channelName, DestURL: destURL})
	if err != nil {
		t.Fatalf("start gateway: %v", err)
	}
	defer func() {
		if err := gw.Stop(); err != nil {
			t.Errorf("gateway exited uncleanly: %v", err)
		}
	}()

	ack, err := gw.SendAndAck(channelName, adtA01Message)
	if err != nil {
		t.Fatalf("send and ack: %v", err)
	}

	if got, want := harness.NormalizeACK(ack), harness.NormalizeACK(adtA01AckAE); got != want {
		t.Errorf("ACK mismatch:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestDropsMessageWhenMSHUnreadable(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		msg  string
	}{
		{"garbage", garbageMessage},
		{"empty", ""},
		{"MSH header too short for delimiters", "MSH|^~&"},
		{"MSH-10 missing control ID", "MSH|^~\\&|SENDAPP|SENDFAC|RECVAPP|RECVFAC|20240101120000||ADT^A01||P|2.5"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
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

			if err := conn.Send(tc.msg); err != nil {
				t.Fatalf("send unreadable message: %v", err)
			}
			if !gw.WaitForLog(5*time.Second, isMsg(msgDroppingUncorrelatable)) {
				t.Fatal("gateway did not log the drop")
			}
			if conn.HasInboundWithin(quietWindow) {
				t.Fatal("gateway replied to a message with no readable MSH, want silence")
			}
			if rcvd := sink.Received(); len(rcvd) != 0 {
				t.Errorf("sink received %d messages, want 0 (no POST on unreadable message)", len(rcvd))
			}

			if err := conn.Send(adtA01Message); err != nil {
				t.Fatalf("send valid message on the same connection: %v", err)
			}
			ack, err := conn.ReadMessage()
			if err != nil {
				t.Fatalf("read ACK for the valid follow-up: %v", err)
			}
			if got, want := harness.NormalizeACK(ack), harness.NormalizeACK(adtA01AckAA); got != want {
				t.Errorf("follow-up ACK mismatch (connection should stay open and in sync):\n--- got ---\n%s\n--- want ---\n%s", got, want)
			}
		})
	}
}

func TestAcksAEWhenUnforwardableButCorrelatable(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		msg     string
		wantAck string
	}{
		{"segment missing field separator", missingFieldSeparatorMessage, missingFieldSeparatorAckAE},
		{"MSH-9 missing trigger event", missingTriggerEventMessage, missingTriggerEventAckAE},
		{"MSH-9 missing message code", missingMessageCodeMessage, missingMessageCodeAckAE},
		{"MSH-9 empty", emptyMSH9Message, emptyMSH9AckAE},
		{"segment ID malformed", badSegmentIDMessage, badSegmentIDAckAE},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
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

			ack, err := gw.SendAndAck(channelName, tc.msg)
			if err != nil {
				t.Fatalf("send and ack: %v", err)
			}

			if rcvd := sink.Received(); len(rcvd) != 0 {
				t.Errorf("sink received %d messages, want 0 (no POST on unforwardable message)", len(rcvd))
			}

			if got, want := harness.NormalizeACK(ack), harness.NormalizeACK(tc.wantAck); got != want {
				t.Errorf("ACK mismatch:\n--- got ---\n%s\n--- want ---\n%s", got, want)
			}
		})
	}
}

func TestAeEchoesControlIDWhenBodyUnparseable(t *testing.T) {
	t.Parallel()
	const controlID = "MSGBADBODY"
	malformed := harness.SetControlID(missingFieldSeparatorMessage, controlID)

	sink := harness.NewSink(t, http.StatusOK, "")
	gw, err := harness.StartGateway(t, harness.Channel{Name: channelName, DestURL: sink.URL()})
	if err != nil {
		t.Fatalf("start gateway: %v", err)
	}
	defer func() {
		if err := gw.Stop(); err != nil {
			t.Errorf("gateway exited uncleanly: %v\n", err)
		}
	}()

	ack, err := gw.SendAndAck(channelName, malformed)
	if err != nil {
		t.Fatalf("send and ack: %v", err)
	}

	if rcvd := sink.Received(); len(rcvd) != 0 {
		t.Errorf("sink received %d messages, want 0 (no POST on unparseable message)", len(rcvd))
	}

	wantAck := strings.Replace(missingFieldSeparatorAckAE, "MSA|AE|MSG00004", "MSA|AE|"+controlID, 1)
	if got, want := harness.NormalizeACK(ack), harness.NormalizeACK(wantAck); got != want {
		t.Errorf("ACK mismatch:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestForwardsAndLogsWhenRequiredMSHFieldsMissing(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name          string
		msg           string
		wantAck       string
		wantControlID string
		wantMissing   string
	}{
		{"missing MSH-12", missingMSH12Message, missingMSH12AckAA, "MSG00003", "[MSH-12]"},
		{"missing MSH-11", missingMSH11Message, missingMSH11AckAA, "MSG00008", "[MSH-11]"},
		{"missing MSH-11 and MSH-12", missingMSH11And12Message, missingMSH11And12AckAA, "MSG00009", "[MSH-11 MSH-12]"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
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

			ack, err := gw.SendAndAck(channelName, tc.msg)
			if err != nil {
				t.Fatalf("send and ack: %v", err)
			}

			if rcvd := sink.Received(); len(rcvd) != 1 {
				t.Errorf("sink received %d messages, want 1 (missing required MSH fields must not block forwarding)", len(rcvd))
			}

			if got, want := harness.NormalizeACK(ack), harness.NormalizeACK(tc.wantAck); got != want {
				t.Errorf("ACK mismatch:\n--- got ---\n%s\n--- want ---\n%s", got, want)
			}

			logged := withAttr(withAttr(isMsg(msgMissingRequiredFields), "control_id", tc.wantControlID), "missing", tc.wantMissing)
			if !gw.WaitForLog(5*time.Second, logged) {
				t.Errorf("gateway did not log the missing fields %s for %s\n--- stdout ---\n%s", tc.wantMissing, tc.wantControlID, gw.Stdout())
			}
		})
	}
}

func TestUnescapesFieldsWhenForwarding(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		msg     string
		wantXML string
		wantAck string
	}{
		{"escapes in body fields", escapesMessage, escapesTransformedXML, escapesAckAA},
		{"escapes in control ID", escapedControlIDMessage, escapedControlIDTransformedXML, escapedControlIDAckAA},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
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

			ack, err := gw.SendAndAck(channelName, tc.msg)
			if err != nil {
				t.Fatalf("send and ack: %v", err)
			}

			rcvd := sink.Received()
			if len(rcvd) != 1 {
				t.Fatalf("sink received %d messages, want 1", len(rcvd))
			}

			if rcvd[0] != tc.wantXML {
				t.Errorf("XML mismatch:\n--- got ---\n%s\n--- want ---\n%s", rcvd[0], tc.wantXML)
			}

			if got, want := harness.NormalizeACK(ack), harness.NormalizeACK(tc.wantAck); got != want {
				t.Errorf("ACK mismatch:\n--- got ---\n%s\n--- want ---\n%s", got, want)
			}
		})
	}
}

func TestClosesConnWhenAckWriteTimesOut(t *testing.T) {
	if testing.Short() {
		t.Skip("waits for the real write deadline")
	}
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

	conn, err := gw.Dial(channelName)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close() }()

	if err := conn.CapReceiveBuffer(2048); err != nil {
		t.Fatalf("cap receive buffer: %v", err)
	}

	if err := conn.Send(harness.GiantMessage(unbufferableAckFieldBytes)); err != nil {
		t.Fatalf("send giant message: %v", err)
	}

	if !gw.WaitForLog(90*time.Second, withAttr(isMsg(msgConnectionClosed), "reason", "write_error")) {
		t.Fatalf("gateway did not log the ack write timeout")
	}

	next, err := gw.Dial(channelName)
	if err != nil {
		t.Fatalf("dial after timeout: %v", err)
	}
	defer func() { _ = next.Close() }()

	if err := next.Send(adtA01Message); err != nil {
		t.Fatalf("send after timeout: %v", err)
	}
	if !next.HasInboundWithin(30 * time.Second) {
		t.Fatal("gateway did not free the timed-out connection's slot")
	}
	ack, err := next.ReadMessage()
	if err != nil {
		t.Fatalf("read ACK on the next connection: %v", err)
	}
	if got, want := harness.NormalizeACK(ack), harness.NormalizeACK(adtA01AckAA); got != want {
		t.Errorf("ACK mismatch on the next connection:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestClosesConnWhenReadTimesOut(t *testing.T) {
	if testing.Short() {
		t.Skip("waits for the real read deadline")
	}
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

	if err := conn.WriteRaw([]byte{harness.MLLPStart, 'M', 'S', 'H', '|'}); err != nil {
		t.Fatalf("write start of frame: %v", err)
	}

	if !gw.WaitForLog(45*time.Second, withAttr(isMsg(msgConnectionClosed), "reason", "timeout")) {
		t.Fatalf("gateway did not log the read timeout")
	}
	if !conn.ClosedWithin(45 * time.Second) {
		t.Fatal("gateway did not close the connection")
	}
}

func TestForwardsSeriallyWhenClientBursts(t *testing.T) {
	t.Parallel()
	sink, releaseOne := harness.NewGatedSink(t, http.StatusOK, "")
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

	burst := []string{"MSGBP001", "MSGBP002", "MSGBP003"}
	for _, id := range burst {
		if err := conn.Send(harness.SetControlID(adtA01Message, id)); err != nil {
			t.Fatalf("send %s: %v", id, err)
		}
	}

	if !sink.WaitForReceived(1) {
		t.Fatalf("sink never received the first message")
	}
	if conn.HasInboundWithin(quietWindow) {
		t.Fatal("gateway acked before the sink accepted the first message")
	}

	for i, id := range burst {
		forwarded := i + 1
		if !sink.WaitForReceived(forwarded) {
			t.Fatalf("sink never received %s", id)
		}

		atSink := sink.Received()
		if len(atSink) != forwarded {
			t.Fatalf("sink received %d messages before %s was acked, want %d", len(atSink), id, forwarded)
		}
		if !strings.Contains(atSink[i], id) {
			t.Fatalf("message %d at the sink is not %s:\n%s", i, id, atSink[i])
		}

		releaseOne()

		ack, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read ACK for %s: %v", id, err)
		}
		wantAck := strings.Replace(adtA01AckAA, "MSA|AA|MSG00001", "MSA|AA|"+id, 1)
		if got, want := harness.NormalizeACK(ack), harness.NormalizeACK(wantAck); got != want {
			t.Errorf("ACK mismatch for %s:\n--- got ---\n%s\n--- want ---\n%s", id, got, want)
		}
	}
}

func TestBlocksClientWritesWhenSinkStalls(t *testing.T) {
	t.Parallel()
	sink, releaseOne := harness.NewGatedSink(t, http.StatusOK, "")
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

	if err := conn.CapSendBuffer(clientSendBufferBytes); err != nil {
		t.Fatalf("cap send buffer: %v", err)
	}

	if err := conn.Send(adtA01Message); err != nil {
		t.Fatalf("send the message that stalls the pipeline: %v", err)
	}
	if !sink.WaitForReceived(1) {
		t.Fatalf("sink never received the stalling message")
	}

	spamFrame := harness.Frame(harness.GiantMessage(spamFieldBytes))
	queuedBehindStall, senderBlocked, err := conn.WriteRawUntilBlocked(spamFrame, spamFrameLimit)
	if err != nil {
		t.Fatalf("spam the stalled gateway: %v", err)
	}
	if !senderBlocked {
		t.Fatalf("client wrote %d frames without blocking; backpressure never reached the sender", queuedBehindStall)
	}
	if stalledAtSink := len(sink.Received()); stalledAtSink != 1 {
		t.Fatalf("sink received %d messages while the pipeline was stalled, want 1", stalledAtSink)
	}

	heldAtSink := 1
	awaitingAck := heldAtSink + queuedBehindStall

	spamAckAA := "MSH|^~\\&|RECVAPP|RECVFAC|" + strings.Repeat("A", spamFieldBytes) +
		"|SENDFAC|<TS>||ACK^A01^ACK|<CID>|P|2.5\rMSA|AA|MSG00001"

	for i := range awaitingAck {
		releaseOne()

		ack, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read ACK %d of %d while draining: %v", i+1, awaitingAck, err)
		}
		wantAck := adtA01AckAA
		if i > 0 {
			wantAck = spamAckAA
		}
		if got, want := harness.NormalizeACK(ack), harness.NormalizeACK(wantAck); got != want {
			t.Fatalf("ACK %d of %d while draining mismatched:\n--- got ---\n%.256s\n--- want ---\n%.256s", i+1, awaitingAck, got, want)
		}
	}

	if forwarded := len(sink.Received()); forwarded != awaitingAck {
		t.Fatalf("sink received %d messages once unstalled, want %d (every complete frame, exactly once)", forwarded, awaitingAck)
	}
}
