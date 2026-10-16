package hl7

import (
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

var ackTime = time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)

func ackEchoes(ack, wantMSH10, wantMSA2 string) error {
	lines, d, err := parseHeader(ack)
	if err != nil {
		return err
	}
	segs, err := parseSegments(lines, d)
	if err != nil {
		return err
	}
	if len(segs) != 2 || segs[1].ID != "MSA" {
		return fmt.Errorf("ack has %d segments, want MSH then MSA:\n%q", len(segs), ack)
	}
	if got := segs[0].component(10, 1); got != wantMSH10 {
		return fmt.Errorf("ack MSH-10 = %q, want %q", got, wantMSH10)
	}
	msa := segs[1]
	if len(msa.Fields) != 2 {
		return fmt.Errorf("MSA has %d fields, want 2:\n%q", len(msa.Fields), ack)
	}
	if f := msa.Fields[1]; len(f) != 1 || len(f[0]) != 1 || len(f[0][0]) != 1 {
		return fmt.Errorf("MSA-2 is not a single value:\n%q", ack)
	}
	if got := msa.component(2, 1); got != wantMSA2 {
		return fmt.Errorf("MSA-2 = %q, want %q", got, wantMSA2)
	}

	return nil
}

func wellFormed(doc string) error {
	dec := xml.NewDecoder(strings.NewReader(doc))
	for {
		if _, err := dec.Token(); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}
