package ops

import (
	"encoding/json"
	"errors"
	"io"
	"time"

	"github.com/bltn/hl7gw/internal/hl7"
	"github.com/bltn/hl7gw/internal/record"
)

const milliRFC3339 = "2006-01-02T15:04:05.000Z07:00"

type summaryJSON struct {
	ID             int64   `json:"id"`
	ReceivedAt     string  `json:"received_at"`
	ControlID      string  `json:"control_id"`
	MessageType    string  `json:"message_type"`
	TransformError *string `json:"transform_error"`
	Attempts       int     `json:"attempts"`
	Delivery       *string `json:"delivery"`
	StatusCode     *int64  `json:"status_code"`
}

type messageJSON struct {
	ID               int64         `json:"id"`
	CreatedAt        string        `json:"created_at"`
	UpdatedAt        string        `json:"updated_at"`
	ReceivedAt       string        `json:"received_at"`
	RawMessage       string        `json:"raw_message"`
	TransformedXML   *string       `json:"transformed_xml"`
	TransformError   *string       `json:"transform_error"`
	AckMessage       *string       `json:"ack_message"`
	AckOutcome       *string       `json:"ack_outcome"`
	DeliveryAttempts []attemptJSON `json:"delivery_attempts"`
}

type attemptJSON struct {
	replayAttemptJSON
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

type replayAttemptJSON struct {
	AttemptedAt        string  `json:"attempted_at"`
	Outcome            string  `json:"outcome"`
	ResponseStatusCode *int64  `json:"response_status_code"`
	ResponseBody       *string `json:"response_body"`
	Payload            string  `json:"payload"`
	Origin             string  `json:"origin"`
}

type dryRunMessageJSON struct {
	ReceivedAt     string  `json:"received_at"`
	RawMessage     string  `json:"raw_message"`
	TransformedXML *string `json:"transformed_xml"`
	TransformError *string `json:"transform_error"`
}

func toSummaryJSON(m record.Message) summaryJSON {
	msg, err := hl7.ParseMessage(m.Raw)
	if cerr, ok := errors.AsType[*hl7.CorrelatableError](err); ok {
		msg.MSH = cerr.MSH
	}
	s := summaryJSON{
		ID:             m.ID,
		ReceivedAt:     m.ReceivedAt.UTC().Format(milliRFC3339),
		ControlID:      msg.MSH.ControlID(),
		MessageType:    msg.MSH.MessageType(),
		TransformError: m.TransformError,
		Attempts:       len(m.Attempts),
	}
	if n := len(m.Attempts); n > 0 {
		last := m.Attempts[n-1]
		outcome := string(last.Outcome)
		s.Delivery, s.StatusCode = &outcome, last.ResponseStatusCode
	}
	return s
}

func toMessageJSON(m record.Message) messageJSON {
	attempts := make([]attemptJSON, 0, len(m.Attempts))
	for _, a := range m.Attempts {
		attempts = append(attempts, attemptJSON{
			replayAttemptJSON: toReplayAttemptJSON(a),
			CreatedAt:         a.CreatedAt.UTC().Format(time.RFC3339),
			UpdatedAt:         a.UpdatedAt.UTC().Format(time.RFC3339),
		})
	}
	return messageJSON{
		ID:               m.ID,
		CreatedAt:        m.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:        m.UpdatedAt.UTC().Format(time.RFC3339),
		ReceivedAt:       m.ReceivedAt.UTC().Format(milliRFC3339),
		RawMessage:       m.Raw,
		TransformedXML:   m.XML,
		TransformError:   m.TransformError,
		AckMessage:       m.Ack,
		AckOutcome:       m.AckOutcome,
		DeliveryAttempts: attempts,
	}
}

func toReplayAttemptJSON(a record.Attempt) replayAttemptJSON {
	return replayAttemptJSON{
		AttemptedAt:        a.AttemptedAt.UTC().Format(milliRFC3339),
		Outcome:            string(a.Outcome),
		ResponseStatusCode: a.ResponseStatusCode,
		ResponseBody:       a.ResponseBody,
		Payload:            a.Payload,
		Origin:             string(a.Origin),
	}
}

func toDryRunMessageJSON(m record.Message) dryRunMessageJSON {
	return dryRunMessageJSON{
		ReceivedAt:     m.ReceivedAt.UTC().Format(milliRFC3339),
		RawMessage:     m.Raw,
		TransformedXML: m.XML,
		TransformError: m.TransformError,
	}
}

func encoder(w io.Writer) *json.Encoder {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	return enc
}

func writeJSON(w io.Writer, v any) {
	enc := encoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}
