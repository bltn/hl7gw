package hl7

import (
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

const mshID = "MSH"

// ErrUncorrelatable reports a message the gateway cannot ACK: the MSH could
// not be recovered far enough to echo a control ID, so the sender has no
// correlation handle to match a reply against. Such messages are dropped.
var ErrUncorrelatable = errors.New("hl7: no correlatable MSH")

// CorrelatableError reports a message whose body failed to parse or validate
// but whose MSH was recovered far enough to correlate. The gateway can still
// return an AE ACK keyed on the embedded MSH.
type CorrelatableError struct {
	MSH MSH
	Err error
}

func (e *CorrelatableError) Error() string { return e.Err.Error() }
func (e *CorrelatableError) Unwrap() error { return e.Err }

type Message struct {
	MSH      MSH
	segments []segment
}

type segment struct {
	ID     string
	Fields []field
}

type (
	field      []repetition
	repetition []component
	component  []string
)

func ParseMessage(msg string) (Message, error) {
	lines, delims, err := parseHeader(msg)
	if err != nil {
		return Message{}, fmt.Errorf("%w: %w", ErrUncorrelatable, err)
	}

	msh, err := mshFromHeader(lines, delims)
	if err != nil {
		return Message{}, fmt.Errorf("%w: %w", ErrUncorrelatable, err)
	}

	segments, err := parseSegments(lines, delims)
	if err != nil {
		return Message{}, &CorrelatableError{MSH: msh, Err: err}
	}

	if err := msh.validateMessageType(); err != nil {
		return Message{}, &CorrelatableError{MSH: msh, Err: err}
	}

	return Message{MSH: msh, segments: segments[1:]}, nil
}

func mshFromHeader(lines []string, delims delimiters) (MSH, error) {
	return correlatableMSH(parseMSH(strings.Split(lines[0], delims.Field), delims))
}

func parseHeader(msg string) ([]string, delimiters, error) {
	lines := splitSegments(msg)
	if len(lines) == 0 {
		return nil, delimiters{}, errors.New("hl7: empty message")
	}

	delims, err := parseDelimiters(lines[0])
	if err != nil {
		return nil, delimiters{}, err
	}

	return lines, delims, nil
}

func splitSegments(msg string) []string {
	return strings.FieldsFunc(msg, func(r rune) bool {
		return r == '\r'
	})
}

func parseSegments(lines []string, delims delimiters) ([]segment, error) {
	segments := make([]segment, len(lines))
	for i, line := range lines {
		seg, err := parseSegment(line, delims)
		if err != nil {
			return nil, err
		}
		segments[i] = seg
	}

	return segments, nil
}

var segmentIDShape = regexp.MustCompile(`^[A-Z][A-Z0-9]{2}$`)

func parseSegment(line string, d delimiters) (segment, error) {
	parts := strings.Split(line, d.Field)
	if len(parts) <= 1 {
		return segment{}, errors.New("hl7: malformed segment, missing field separator")
	}
	if !segmentIDShape.MatchString(parts[0]) {
		return segment{}, fmt.Errorf("hl7: malformed segment ID %q", parts[0])
	}

	if parts[0] == mshID {
		return parseMSH(parts, d), nil
	}

	return segment{ID: parts[0], Fields: parseFields(parts[1:], d)}, nil
}

func parseMSH(parts []string, d delimiters) segment {
	fields := make([]field, 0, len(parts))
	fields = append(fields, literalField(d.Field), literalField(parts[1]))
	fields = append(fields, parseFields(parts[2:], d)...)

	return segment{ID: mshID, Fields: fields}
}

func parseFields(raw []string, d delimiters) []field {
	fields := make([]field, len(raw))
	for i, f := range raw {
		fields[i] = parseField(f, d)
	}

	return fields
}

func parseField(s string, d delimiters) field {
	reps := strings.Split(s, d.Repetition)
	out := make(field, len(reps))
	for i, r := range reps {
		out[i] = parseRepetition(r, d)
	}

	return out
}

func parseRepetition(s string, d delimiters) repetition {
	comps := strings.Split(s, d.Component)
	out := make(repetition, len(comps))
	for i, c := range comps {
		out[i] = parseComponent(c, d)
	}

	return out
}

func parseComponent(s string, d delimiters) component {
	subs := strings.Split(s, d.Subcomponent)
	out := make(component, len(subs))
	for i, sub := range subs {
		out[i] = unescape(sub, d)
	}

	return out
}

func literalField(value string) field {
	return field{repetition{component{value}}}
}

func unescape(s string, d delimiters) string {
	parts := strings.Split(s, d.Escape)
	if len(parts) == 1 {
		return s
	}

	result := ""
	for i, p := range parts {
		isEscaped := i%2 != 0
		if isEscaped {
			result += resolveEscape(p, d)
		} else {
			result += p
		}
	}

	return result
}

func resolveEscape(seq string, d delimiters) string {
	switch seq {
	case "F":
		return d.Field
	case "S":
		return d.Component
	case "T":
		return d.Subcomponent
	case "R":
		return d.Repetition
	case "E":
		return d.Escape
	}

	hasHexEscape := len(seq) > 1 && (seq[0] == 'X' || seq[0] == 'x')
	if hasHexEscape {
		decoded, err := hex.DecodeString(seq[1:])
		if err == nil {
			return string(decoded)
		}
	}

	return d.Escape + seq + d.Escape
}
