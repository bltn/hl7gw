package hl7

import (
	"errors"
	"strconv"
	"strings"
	"testing"
)

var delimiterTable = []delimiters{
	defaultDelimiters,
	{Field: "#", Component: "$", Repetition: "%", Escape: "!", Subcomponent: "*"},
	{Field: "/", Component: "@", Repetition: ";", Escape: "+", Subcomponent: "="},
}

var xmlText = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&#34;", "'", "&#39;")

func buildMessage(d delimiters, app, cid string, nseg uint8) string {
	esc := d.escaper().Replace
	var b strings.Builder
	b.WriteString(mshID + d.Field + d.encodingCharacters())
	for _, f := range []string{esc(app), "FAC", "RAPP", "RFAC", "20240101000000", "", "ADT" + d.Component + "A01", esc(cid), "P", "2.5"} {
		b.WriteString(d.Field + f)
	}
	b.WriteString("\r")
	for i := range int(nseg) {
		b.WriteString("OBX" + d.Field + strconv.Itoa(i+1) + d.Field + esc(app) + d.Repetition + esc(cid) + "\r")
	}

	return b.String()
}

func FuzzMessageProperties(f *testing.F) {
	f.Add("MSG1", "APP", uint8(2), byte(0))
	f.Add("", "APP", uint8(0), byte(1))
	f.Add("a|b^c~d&e\\f\rg", "x\x1cy", uint8(5), byte(2))
	f.Add("#$%!*", "/@;+=", uint8(1), byte(1))
	f.Add("<&\"'>", "é", uint8(1), byte(0))

	f.Fuzz(func(t *testing.T, cid, app string, nseg uint8, which byte) {
		d := delimiterTable[int(which)%len(delimiterTable)]
		raw := buildMessage(d, app, cid, nseg)

		m, err := ParseMessage(raw)
		if cid == "" {
			if !errors.Is(err, ErrUncorrelatable) {
				t.Fatalf("empty control ID: err = %v, want ErrUncorrelatable", err)
			}
			return
		}
		if err != nil {
			t.Fatalf("ParseMessage(%q): %v", raw, err)
		}
		if got := m.MSH.ControlID(); got != cid {
			t.Fatalf("ControlID() = %q, want %q", got, cid)
		}
		if got := m.MSH.sendingApp(); got != app {
			t.Fatalf("sendingApp() = %q, want %q", got, app)
		}
		if len(m.segments) != int(nseg) {
			t.Fatalf("parsed %d segments, want %d", len(m.segments), nseg)
		}
		for i, seg := range m.segments {
			reps := seg.Fields[1]
			if seg.ID != "OBX" || len(seg.Fields) != 2 || len(reps) != 2 {
				t.Fatalf("segment %d = %+v, want OBX with two fields and two repetitions", i, seg)
			}
			if reps[0][0][0] != app || reps[1][0][0] != cid {
				t.Fatalf("segment %d repetitions = %q, %q; want %q, %q", i, reps[0][0][0], reps[1][0][0], app, cid)
			}
		}

		for _, code := range []AckCode{Accept, Error, Reject} {
			if err := ackEchoes(m.Ack(code, ackTime, "ACK1"), "ACK1", cid); err != nil {
				t.Fatalf("%s ACK: %v", code, err)
			}
		}

		doc := XML(m)
		if err := wellFormed(doc); err != nil {
			t.Fatalf("XML is not well-formed: %v", err)
		}
		if want := "<MSH.10.1>" + xmlText.Replace(cid) + "</MSH.10.1>"; printableASCII(cid) && !strings.Contains(doc, want) {
			t.Fatalf("XML does not carry the control ID verbatim, want %s in:\n%s", want, doc)
		}
	})
}

func printableASCII(s string) bool {
	return !strings.ContainsFunc(s, func(r rune) bool { return r < ' ' || r > '~' })
}
