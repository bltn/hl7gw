package record

import "time"

type Message struct {
	ID             int64
	ReceivedAt     time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time
	Raw            string
	XML            *string
	TransformError *string
	Ack            *string
	AckOutcome     *string
	Attempts       []Attempt
}

type Attempt struct {
	AttemptedAt        time.Time
	CreatedAt          time.Time
	UpdatedAt          time.Time
	Outcome            Outcome
	ResponseStatusCode *int64
	ResponseBody       *string
	Payload            string
	Origin             Origin
}

func (m Message) DeliveryAttempted() bool {
	return len(m.Attempts) > 0
}

type Outcome string

const (
	Success Outcome = "SUCCESS"
	Failed  Outcome = "FAILED"
)

func OutcomeOf(success bool) Outcome {
	if success {
		return Success
	}
	return Failed
}

type Origin string

const (
	Inbound Origin = "INBOUND"
	Replay  Origin = "REPLAY"
)

type TransformError string

const CouldNotParseHL7 TransformError = "COULD_NOT_PARSE_HL7"
