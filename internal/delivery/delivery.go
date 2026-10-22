package delivery

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"strings"

	"github.com/bltn/hl7gw/internal/config"
	"github.com/bltn/hl7gw/internal/journal"
	"github.com/bltn/hl7gw/internal/metrics"
	"github.com/bltn/hl7gw/internal/store"
)

var ErrDeliveryFailed = errors.New("delivery failed")

type Deliverer struct {
	client *client
	store  *store.Store
	log    *slog.Logger
	m      *metrics.Channel
}

func New(ch config.Channel, st *store.Store, stopping <-chan struct{}, log *slog.Logger, m *metrics.Channel) *Deliverer {
	return &Deliverer{
		client: newClient(ch.Destination.URL.URL, ch.Destination.Auth.Authorization(), stopping),
		store:  st,
		log:    log,
		m:      m,
	}
}

func (d *Deliverer) Deliver(ctx context.Context, messageID int64, xml string, origin journal.Origin) ([]journal.Attempt, error) {
	attempts, postErr := d.client.postHL7Message(ctx, xml)
	recorded := make([]journal.Attempt, 0, len(attempts))
	originLabel := strings.ToLower(string(origin))
	for i, a := range attempts {
		ja := journal.Attempt{
			AttemptedAt: a.at,
			Outcome:     journal.OutcomeOf(a.err == nil),
			Payload:     xml,
			Origin:      origin,
		}
		statusLabel := ""
		if a.statusCode != 0 {
			status := int64(a.statusCode)
			ja.ResponseStatusCode, ja.ResponseBody = &status, &a.body
			statusLabel = strconv.Itoa(a.statusCode)
		}
		d.m.DeliveryAttemptsTotal.WithLabelValues(originLabel, strings.ToLower(string(ja.Outcome)), statusLabel).Inc()
		d.m.DeliveryDuration.Observe(a.elapsed.Seconds())
		if a.err != nil {
			attrs := []any{"message_id", messageID, "origin", originLabel, "attempt", i + 1, "err", a.err.Error()}
			if a.statusCode != 0 {
				attrs = append(attrs, "status_code", a.statusCode)
			}
			d.log.Warn("delivery attempt failed", attrs...)
		}
		if err := d.store.RecordDeliveryAttempt(ctx, messageID, ja); err != nil {
			return recorded, err
		}
		recorded = append(recorded, ja)
	}
	if postErr != nil {
		return recorded, ErrDeliveryFailed
	}
	return recorded, nil
}
