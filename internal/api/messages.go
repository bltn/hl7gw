package api

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/bltn/hl7gw/internal/journal"
	"github.com/bltn/hl7gw/internal/store"
)

const (
	defaultPageSize = 50
	maxPageSize     = 500
	maxPageNumber   = 1_000_000_000
	milliRFC3339    = "2006-01-02T15:04:05.000Z07:00"
)

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

func toAttemptJSON(a journal.Attempt) attemptJSON {
	return attemptJSON{
		replayAttemptJSON: toReplayAttemptJSON(a),
		CreatedAt:         a.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:         a.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

type messagesPageJSON struct {
	Data       []messageJSON `json:"data"`
	Pagination pagination    `json:"pagination"`
	Links      links         `json:"links"`
}

type pagination struct {
	PageNumber int `json:"pageNumber"`
	PageSize   int `json:"pageSize"`
	Total      int `json:"total"`
}

type links struct {
	Self string `json:"self"`
	Next string `json:"next,omitempty"`
	Prev string `json:"prev,omitempty"`
}

func (a *api) queryChannel(r *http.Request) (string, error) {
	channel := r.URL.Query().Get("channel")
	return channel, a.resolveChannel(channel)
}

func (a *api) listMessages(w http.ResponseWriter, r *http.Request) {
	channel, err := a.queryChannel(r)
	switch {
	case errors.Is(err, errChannelMissing):
		writeProblem(w, http.StatusBadRequest, "channel query parameter is required")
		return
	case errors.Is(err, errChannelUnknown):
		writeProblem(w, http.StatusNotFound, fmt.Sprintf("no channel named %q", channel))
		return
	}
	pageNumber, pageSize, err := pageParams(r)
	if err != nil {
		writeProblem(w, http.StatusBadRequest, err.Error())
		return
	}

	page, err := a.stores[channel].ListMessages(r.Context(), pageNumber, pageSize)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "listing messages failed")
		return
	}

	body := messagesPageJSON{
		Data:       make([]messageJSON, 0, len(page.Messages)),
		Pagination: pagination{PageNumber: pageNumber, PageSize: pageSize, Total: page.Total},
		Links:      pageLinks(channel, pageNumber, pageSize, page.Total),
	}
	for _, m := range page.Messages {
		body.Data = append(body.Data, toMessageJSON(m))
	}
	writeJSON(w, body)
}

func (a *api) getMessage(w http.ResponseWriter, r *http.Request) {
	channel, err := a.queryChannel(r)
	switch {
	case errors.Is(err, errChannelMissing):
		writeProblem(w, http.StatusBadRequest, "channel query parameter is required")
		return
	case errors.Is(err, errChannelUnknown):
		writeProblem(w, http.StatusNotFound, fmt.Sprintf("no channel named %q", channel))
		return
	}
	raw := r.PathValue("id")
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id < 1 {
		writeProblem(w, http.StatusBadRequest, fmt.Sprintf("message id must be a positive integer, got %q", raw))
		return
	}

	m, err := a.stores[channel].GetMessage(r.Context(), id)
	switch {
	case errors.Is(err, store.ErrMessageNotFound):
		writeProblem(w, http.StatusNotFound, fmt.Sprintf("no message with id %d", id))
	case err != nil:
		writeProblem(w, http.StatusInternalServerError, "getting message failed")
	default:
		writeJSON(w, toMessageJSON(m))
	}
}

func toMessageJSON(m journal.Message) messageJSON {
	attempts := make([]attemptJSON, 0, len(m.Attempts))
	for _, a := range m.Attempts {
		attempts = append(attempts, toAttemptJSON(a))
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

func pageParams(r *http.Request) (pageNumber, pageSize int, err error) {
	pageNumber, pageSize = 1, defaultPageSize
	q := r.URL.Query()
	if raw := q.Get("pageNumber"); raw != "" {
		if pageNumber, err = strconv.Atoi(raw); err != nil || pageNumber < 1 || pageNumber > maxPageNumber {
			return 0, 0, fmt.Errorf("pageNumber must be a positive integer at most %d, got %q", maxPageNumber, raw)
		}
	}
	if raw := q.Get("pageSize"); raw != "" {
		if pageSize, err = strconv.Atoi(raw); err != nil || pageSize < 1 || pageSize > maxPageSize {
			return 0, 0, fmt.Errorf("pageSize must be a positive integer at most %d, got %q", maxPageSize, raw)
		}
	}
	return pageNumber, pageSize, nil
}

func pageLinks(channel string, pageNumber, pageSize, total int) links {
	l := links{Self: messagesURL(channel, pageNumber, pageSize)}
	if pageNumber*pageSize < total {
		l.Next = messagesURL(channel, pageNumber+1, pageSize)
	}
	if pageNumber > 1 {
		l.Prev = messagesURL(channel, pageNumber-1, pageSize)
	}
	return l
}

func messagesURL(channel string, pageNumber, pageSize int) string {
	q := url.Values{}
	q.Set("channel", channel)
	q.Set("pageNumber", strconv.Itoa(pageNumber))
	q.Set("pageSize", strconv.Itoa(pageSize))
	return "/messages?" + q.Encode()
}
