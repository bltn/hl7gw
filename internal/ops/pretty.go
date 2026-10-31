package ops

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/bltn/hl7gw/internal/console"
	"github.com/bltn/hl7gw/internal/record"
)

type output struct {
	w      io.Writer
	term   console.Terminal
	pretty bool
}

func (o output) summaries(r io.Reader) error {
	if !o.pretty {
		_, err := io.Copy(o.w, r)
		return err
	}
	var b bytes.Buffer
	tw := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "ID\tRECEIVED\tTYPE\tCONTROL ID\tTRANSFORM\tATTEMPTS\tDELIVERY\tSTATUS")
	dec := json.NewDecoder(r)
	for {
		var s summaryJSON
		err := dec.Decode(&s)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\t%d\t%s\t%s\n",
			s.ID, s.ReceivedAt, orDash(&s.MessageType), orDash(&s.ControlID), orDash(s.TransformError),
			s.Attempts, orDash(s.Delivery), orDash(s.StatusCode))
	}
	_ = tw.Flush()
	_, err := o.w.Write(b.Bytes())
	return err
}

func (o output) message(r io.Reader) error {
	return render(o, r, func(b *bytes.Buffer, m messageJSON) {
		head(b, fmt.Sprintf("message %d  received %s", m.ID, m.ReceivedAt), m.RawMessage, m.TransformedXML, m.TransformError)
		if m.AckMessage == nil {
			section(b, "ack")
			b.WriteString("(none)\n")
		} else {
			section(b, "ack  send "+o.outcome(*m.AckOutcome))
			b.WriteString(segments(*m.AckMessage))
		}
		if len(m.DeliveryAttempts) == 0 {
			section(b, "attempts")
			b.WriteString("(none)\n")
		}
		for i, a := range m.DeliveryAttempts {
			o.attemptBlock(b, fmt.Sprintf("attempt %d of %d", i+1, len(m.DeliveryAttempts)), a.replayAttemptJSON, m.TransformedXML)
		}
	})
}

func (o output) attempt(r io.Reader) error {
	return render(o, r, func(b *bytes.Buffer, a replayAttemptJSON) {
		o.attemptBlock(b, "attempt", a, nil)
	})
}

func (o output) dryRun(r io.Reader) error {
	return render(o, r, func(b *bytes.Buffer, m dryRunMessageJSON) {
		head(b, "dry run  received "+m.ReceivedAt, m.RawMessage, m.TransformedXML, m.TransformError)
	})
}

func render[T any](o output, r io.Reader, pretty func(*bytes.Buffer, T)) error {
	if !o.pretty {
		_, err := io.Copy(o.w, r)
		return err
	}
	var v T
	if err := json.NewDecoder(r).Decode(&v); err != nil {
		return err
	}
	var b bytes.Buffer
	pretty(&b, v)
	_, err := o.w.Write(b.Bytes())
	return err
}

func (o output) attemptBlock(b *bytes.Buffer, title string, a replayAttemptJSON, transformed *string) {
	status := "no response"
	if a.ResponseStatusCode != nil {
		status = "HTTP " + strconv.FormatInt(*a.ResponseStatusCode, 10)
	}
	section(b, fmt.Sprintf("%s  %s  %s  %s  %s", title, o.outcome(a.Outcome), status, a.Origin, a.AttemptedAt))
	if transformed != nil && a.Payload == *transformed {
		b.WriteString("payload: same as transformed\n")
	} else {
		b.WriteString("payload:\n" + withNewline(a.Payload))
	}
	if a.ResponseBody == nil || *a.ResponseBody == "" {
		b.WriteString("response: (none)\n")
	} else {
		b.WriteString("response:\n" + withNewline(*a.ResponseBody))
	}
}

func (o output) outcome(s string) string {
	if s == string(record.Success) {
		return o.term.Success(s)
	}
	return o.term.Alert(s)
}

func head(b *bytes.Buffer, title, raw string, xml, transformError *string) {
	b.WriteString(title + "\n")
	section(b, "raw")
	b.WriteString(segments(raw))
	switch {
	case transformError != nil:
		section(b, "transform error")
		b.WriteString(*transformError + "\n")
	case xml == nil:
		section(b, "transformed")
		b.WriteString("(none)\n")
	default:
		section(b, "transformed")
		b.WriteString(withNewline(*xml))
	}
}

func section(b *bytes.Buffer, title string) {
	if b.Len() > 0 {
		b.WriteByte('\n')
	}
	b.WriteString("--- " + title + " ---\n")
}

func segments(hl7 string) string {
	var b strings.Builder
	for _, r := range hl7 {
		switch {
		case r == '\r':
			b.WriteByte('\n')
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\t':
			b.WriteString(`\t`)
		case r < ' ' || r == 0x7f:
			fmt.Fprintf(&b, `\x%02x`, r)
		default:
			b.WriteRune(r)
		}
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
}

func withNewline(s string) string {
	if strings.HasSuffix(s, "\n") {
		return s
	}
	return s + "\n"
}

func orDash[T comparable](p *T) string {
	var zero T
	if p == nil || *p == zero {
		return "-"
	}
	return fmt.Sprint(*p)
}
