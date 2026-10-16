package hl7

import (
	"math"
	"testing"
)

func TestAckEscapesControlIDRoundTrip(t *testing.T) {
	t.Parallel()
	ids := make([]string, 0, math.MaxUint8+8)
	ids = append(ids, "MSG00001", "MSG|1", "A^B~C&D\\E", "X\rMSA|AA|Z", "a\x1cb", "\x7f", "\xff", "é")
	for b := range byte(math.MaxUint8) {
		ids = append(ids, string(b))
	}
	esc := defaultDelimiters.escaper().Replace

	for _, id := range ids {
		if got := unescape(esc(id), defaultDelimiters); got != id {
			t.Errorf("unescape(escape(%q)) = %q", id, got)
		}

		msg, err := ParseMessage("MSH|^~\\&|APP|FAC|||20240101000000||ADT^A01|" + esc(id) + "|P|2.5\r")
		if err != nil {
			t.Errorf("%q: ParseMessage: %v", id, err)
			continue
		}
		if got := msg.MSH.ControlID(); got != id {
			t.Errorf("%q: ControlID() = %q", id, got)
		}
		for _, code := range []AckCode{Accept, Error, Reject} {
			if err := ackEchoes(msg.Ack(code, ackTime, "ACK"+id), "ACK"+id, id); err != nil {
				t.Errorf("%q, %s: %v", id, code, err)
			}
		}
	}
}
