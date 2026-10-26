package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/bltn/hl7gw/internal/processor"
	"github.com/bltn/hl7gw/internal/record"
)

type replayRequestJSON struct {
	Channel   string `json:"channel"`
	MessageID int64  `json:"message_id"`
}

type replayAttemptJSON struct {
	AttemptedAt        string  `json:"attempted_at"`
	Outcome            string  `json:"outcome"`
	ResponseStatusCode *int64  `json:"response_status_code"`
	ResponseBody       *string `json:"response_body"`
	Payload            string  `json:"payload"`
	Origin             string  `json:"origin"`
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

func (a *api) replay(w http.ResponseWriter, r *http.Request) {
	var req replayRequestJSON
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeProblem(w, http.StatusBadRequest, "request body must be valid JSON")
		return
	}
	switch err := a.resolveChannel(req.Channel); {
	case errors.Is(err, errChannelMissing):
		writeProblem(w, http.StatusBadRequest, "channel field is required")
		return
	case errors.Is(err, errChannelUnknown):
		writeProblem(w, http.StatusNotFound, fmt.Sprintf("no channel named %q", req.Channel))
		return
	}

	res := a.processors[req.Channel].Replay(r.Context(), req.MessageID)
	switch res.Outcome {
	case processor.ReplayNotFound:
		writeProblem(w, http.StatusNotFound, fmt.Sprintf("no message with id %d", req.MessageID))
	case processor.ReplayNotYetDelivered:
		writeProblem(w, http.StatusTooManyRequests, fmt.Sprintf("message %d has no delivery attempts to replay", req.MessageID))
	case processor.ReplayDeliveryFailed:
		writeProblem(w, http.StatusBadGateway, "replay delivery failed")
	case processor.ReplayPersistenceFailed:
		writeProblem(w, http.StatusInternalServerError, "replay failed")
	case processor.Replayed:
		writeJSON(w, toReplayAttemptJSON(res.Attempt))
	}
}
