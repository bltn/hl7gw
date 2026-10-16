package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/bltn/hl7gw/internal/journal"
	"github.com/bltn/hl7gw/internal/processor"
)

type processRequestJSON struct {
	Channel string `json:"channel"`
	Message string `json:"message"`
}

type processMode string

const (
	modeFull   processMode = "full"
	modeDryRun processMode = "dry-run"
)

func processModeParam(r *http.Request) (processMode, error) {
	switch m := processMode(r.URL.Query().Get("mode")); m {
	case modeFull, modeDryRun:
		return m, nil
	default:
		return "", fmt.Errorf("mode query parameter must be %q or %q, got %q", modeFull, modeDryRun, m)
	}
}

func (a *api) processMessage(w http.ResponseWriter, r *http.Request) {
	mode, err := processModeParam(r)
	if err != nil {
		writeProblem(w, http.StatusBadRequest, err.Error())
		return
	}
	var req processRequestJSON
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
	if req.Message == "" {
		writeProblem(w, http.StatusBadRequest, "message field is required")
		return
	}

	if mode == modeDryRun {
		a.processMessageDryMode(w, r, req)
		return
	}
	a.processMessageFullMode(w, r, req)
}

func (a *api) processMessageFullMode(w http.ResponseWriter, r *http.Request, req processRequestJSON) {
	res := a.processors[req.Channel].ParseTransformDeliver(r.Context(), req.Message)
	switch res.Outcome {
	case processor.Uncorrelatable, processor.ParseFailed:
		writeProblem(w, http.StatusUnprocessableEntity, "message could not be parsed as HL7")
	case processor.DeliveryFailed:
		writeProblem(w, http.StatusBadGateway, "delivery failed")
	case processor.PersistenceFailed:
		writeProblem(w, http.StatusInternalServerError, "message persistence failed")
	case processor.Success:
		m, err := a.stores[req.Channel].GetMessage(r.Context(), res.MessageID)
		if err != nil {
			writeProblem(w, http.StatusInternalServerError, "getting message failed")
			return
		}
		writeJSON(w, toMessageJSON(m))
	}
}

func (a *api) processMessageDryMode(w http.ResponseWriter, r *http.Request, req processRequestJSON) {
	res := a.processors[req.Channel].ParseTransformDryRun(r.Context(), req.Message)
	switch res.Outcome {
	case processor.DryRunUncorrelatable, processor.DryRunParseFailed:
		writeProblem(w, http.StatusUnprocessableEntity, "message could not be parsed as HL7")
	case processor.DryRunSuccess:
		writeJSON(w, toDryRunMessageJSON(res.Message))
	}
}

type dryRunMessageJSON struct {
	ReceivedAt     string  `json:"received_at"`
	RawMessage     string  `json:"raw_message"`
	TransformedXML *string `json:"transformed_xml"`
	TransformError *string `json:"transform_error"`
}

func toDryRunMessageJSON(m journal.Message) dryRunMessageJSON {
	return dryRunMessageJSON{
		ReceivedAt:     m.ReceivedAt.UTC().Format(milliRFC3339),
		RawMessage:     m.Raw,
		TransformedXML: m.XML,
		TransformError: m.TransformError,
	}
}
