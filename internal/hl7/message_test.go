package hl7

import (
	"errors"
	"testing"
)

func TestCorrelatableErrorExposesItsCause(t *testing.T) {
	t.Parallel()
	cause := errors.New("boom")
	err := error(&CorrelatableError{Err: cause})

	if err.Error() != cause.Error() {
		t.Errorf("Error() = %q, want the underlying %q", err.Error(), cause.Error())
	}
	if !errors.Is(err, cause) {
		t.Errorf("error does not unwrap to its cause %v", cause)
	}
}

const adtA01 = "MSH|^~\\&|APP|FAC|||20240101000000||ADT^A01|MSG1|P|2.5\r"

func TestParseMessageRejectsNonASCIIDelimiters(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"field":           "MSH\x80^~\\&|APP|FAC|||20240101000000||ADT^A01|MSG1|P|2.5\r",
		"component":       "MSH|\x80~\\&|APP|FAC|||20240101000000||ADT^A01|MSG1|P|2.5\r",
		"repetition":      "MSH|^\x80\\&|APP|FAC|||20240101000000||ADT^A01|MSG1|P|2.5\r",
		"escape":          "MSH|^~\x80&|APP|FAC|||20240101000000||ADT^A01|MSG1|P|2.5\r",
		"subcomponent":    "MSH|^~\\\x80|APP|FAC|||20240101000000||ADT^A01|MSG1|P|2.5\r",
		"multibyte field": "MSH\xc3\xa9^~\\&|APP|FAC|||20240101000000||ADT^A01|MSG1|P|2.5\r",
	}
	for name, raw := range cases {
		_, err := ParseMessage(raw)
		if !errors.Is(err, ErrUncorrelatable) {
			t.Errorf("%s: err = %v, want ErrUncorrelatable", name, err)
		}
	}
	if _, err := ParseMessage(adtA01); err != nil {
		t.Errorf("ASCII delimiters: %v", err)
	}
}

func TestParseMessageRejectsMalformedSegmentIDs(t *testing.T) {
	t.Parallel()
	for _, id := range []string{"<x", "", "1AB", "A B", "pid", "AB", "ABCD"} {
		_, err := ParseMessage(adtA01 + id + "|1||DOE^JOHN\r")
		cerr, ok := errors.AsType[*CorrelatableError](err)
		if !ok {
			t.Errorf("segment ID %q: err = %v, want CorrelatableError", id, err)
			continue
		}
		if got := cerr.MSH.ControlID(); got != "MSG1" {
			t.Errorf("segment ID %q: recovered control ID = %q, want MSG1", id, got)
		}
	}

	msg, err := ParseMessage(adtA01 + "Z01|1||custom\r")
	if err != nil {
		t.Fatalf("Z-segment: %v", err)
	}
	if err := wellFormed(XML(msg)); err != nil {
		t.Errorf("XML of a Z-segment message: %v", err)
	}
}

func TestMSHMessageType(t *testing.T) {
	t.Parallel()
	msg, err := ParseMessage(adtA01)
	if err != nil {
		t.Fatalf("ParseMessage: %v", err)
	}
	if got := msg.MSH.MessageType(); got != "ADT^A01" {
		t.Errorf("MessageType() = %q, want ADT^A01", got)
	}
	if got := (MSH{}).MessageType(); got != "" {
		t.Errorf("MessageType() on an empty MSH = %q, want empty", got)
	}
}
