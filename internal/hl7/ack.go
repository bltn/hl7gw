package hl7

import (
	"strings"
	"time"
)

type AckCode string

const (
	Accept AckCode = "AA"
	Error  AckCode = "AE"
	Reject AckCode = "AR"
)

var ackEscape = defaultDelimiters.escaper()

func (m Message) Ack(code AckCode, now time.Time, controlID string) string {
	src, esc := m.MSH, ackEscape.Replace

	msh := []string{
		mshID,
		defaultDelimiters.encodingCharacters(),
		esc(src.receivingApp()),
		esc(src.receivingFacility()),
		esc(src.sendingApp()),
		esc(src.sendingFacility()),
		now.UTC().Format("20060102150405"),
		"",
		"ACK^" + esc(src.triggerEvent()) + "^ACK",
		esc(controlID),
		esc(src.processingID()),
		esc(src.version()),
	}
	msa := []string{"MSA", string(code), esc(src.ControlID())}

	return renderSegment(msh) + renderSegment(msa)
}

func renderSegment(fields []string) string {
	return strings.Join(fields, defaultDelimiters.Field) + "\r"
}
