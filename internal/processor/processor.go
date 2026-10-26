package processor

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/bltn/hl7gw/internal/config"
	"github.com/bltn/hl7gw/internal/delivery"
	"github.com/bltn/hl7gw/internal/hl7"
	"github.com/bltn/hl7gw/internal/metrics"
	"github.com/bltn/hl7gw/internal/record"
	"github.com/bltn/hl7gw/internal/store"
)

const (
	opCreateMessage         = "create_message"
	opRecordTransform       = "record_transform"
	opRecordDeliveryAttempt = "record_delivery_attempt"
	opRecordAck             = "record_ack"
	opGetMessage            = "get_message"
)

type Outcome string

const (
	Uncorrelatable    Outcome = "UNCORRELATABLE"
	PersistenceFailed Outcome = "PERSISTENCE_FAILED"
	ParseFailed       Outcome = "PARSE_FAILED"
	DeliveryFailed    Outcome = "DELIVERY_FAILED"
	Success           Outcome = "SUCCESS"
)

type Result struct {
	Outcome   Outcome
	MessageID int64
	Message   hl7.Message
	Attempts  []record.Attempt
}

type Processor struct {
	ch        config.Channel
	deliverer *delivery.Deliverer
	store     *store.Store
	log       *slog.Logger
	m         *metrics.Channel
}

func New(stopping <-chan struct{}, cfg config.Config, stores map[string]*store.Store, log *slog.Logger, m *metrics.Set) map[string]*Processor {
	processors := make(map[string]*Processor, len(cfg.Channels))
	for _, ch := range cfg.Channels {
		cl, cm := log.With("channel", ch.Name), m.Channel(ch.Name)
		p := &Processor{
			ch:        ch,
			deliverer: delivery.New(ch, stores[ch.Name], stopping, cl, cm),
			store:     stores[ch.Name],
			log:       cl,
			m:         cm,
		}
		processors[ch.Name] = p
	}

	return processors
}

func (p *Processor) ParseTransformDeliver(ctx context.Context, msg string) Result {
	r := p.begin(ctx, msg)

	ev := evaluate(msg)
	outcome := Success
	var attempts []record.Attempt
	switch ev.class {
	case uncorrelatable:
		r.transform(ctx, "", record.CouldNotParseHL7)
		r.log.Warn("dropping uncorrelatable message")
		return r.finish(Uncorrelatable, hl7.Message{}, nil)
	case parseFailed:
		r.transform(ctx, "", record.CouldNotParseHL7)
		outcome = ParseFailed
	case parsed:
		r.transform(ctx, ev.xml, "")
		if r.err == nil {
			logMissingRequiredFields(r.log, ev)

			var err error
			attempts, err = p.deliverer.Deliver(ctx, r.id, ev.xml, record.Inbound)
			switch {
			case errors.Is(err, delivery.ErrDeliveryFailed):
				outcome = DeliveryFailed
			case err != nil:
				r.note(opRecordDeliveryAttempt, err)
			}
		}
	}

	if r.err != nil {
		outcome = PersistenceFailed
	}
	return r.finish(outcome, ev.msg, attempts)
}

type DryRunOutcome string

const (
	DryRunUncorrelatable DryRunOutcome = "UNCORRELATABLE"
	DryRunParseFailed    DryRunOutcome = "PARSE_FAILED"
	DryRunSuccess        DryRunOutcome = "SUCCESS"
)

type DryRunResult struct {
	Outcome DryRunOutcome
	Message record.Message
}

func (p *Processor) ParseTransformDryRun(_ context.Context, msg string) DryRunResult {
	ev := evaluate(msg)
	res := DryRunResult{Outcome: DryRunSuccess, Message: record.Message{ReceivedAt: time.Now(), Raw: msg}}
	switch ev.class {
	case uncorrelatable:
		p.log.Warn("dropping uncorrelatable message")
		res.Outcome = DryRunUncorrelatable
	case parseFailed:
		terr := string(record.CouldNotParseHL7)
		res.Outcome = DryRunParseFailed
		res.Message.TransformError = &terr
	case parsed:
		logMissingRequiredFields(p.log, ev)
		res.Message.XML = &ev.xml
	}
	p.log.Info("message dry run", "outcome", res.Outcome, "control_id", ev.msg.MSH.ControlID(), "message_type", ev.msg.MSH.MessageType())
	return res
}

type ReplayOutcome string

const (
	ReplayNotFound          ReplayOutcome = "NOT_FOUND"
	ReplayNotYetDelivered   ReplayOutcome = "NOT_YET_DELIVERED"
	ReplayDeliveryFailed    ReplayOutcome = "DELIVERY_FAILED"
	ReplayPersistenceFailed ReplayOutcome = "PERSISTENCE_FAILED"
	Replayed                ReplayOutcome = "REPLAYED"
)

type ReplayResult struct {
	Outcome ReplayOutcome
	Attempt record.Attempt
}

func (p *Processor) Replay(ctx context.Context, messageID int64) ReplayResult {
	ml := p.log.With("message_id", messageID)
	res := p.replay(ctx, ml, messageID)
	ml.Info("replay finished", "outcome", res.Outcome)
	return res
}

func (p *Processor) replay(ctx context.Context, ml *slog.Logger, messageID int64) ReplayResult {
	m, err := p.store.GetMessage(ctx, messageID)
	switch {
	case errors.Is(err, store.ErrMessageNotFound):
		return ReplayResult{Outcome: ReplayNotFound}
	case err != nil:
		return p.replayPersistenceFailed(ml, opGetMessage, err)
	}
	if !m.DeliveryAttempted() {
		return ReplayResult{Outcome: ReplayNotYetDelivered}
	}

	attempts, err := p.deliverer.Deliver(ctx, messageID, *m.XML, record.Replay)
	switch {
	case errors.Is(err, delivery.ErrDeliveryFailed):
		return ReplayResult{Outcome: ReplayDeliveryFailed}
	case err != nil:
		return p.replayPersistenceFailed(ml, opRecordDeliveryAttempt, err)
	}
	return ReplayResult{Outcome: Replayed, Attempt: attempts[len(attempts)-1]}
}

func (p *Processor) replayPersistenceFailed(ml *slog.Logger, op string, err error) ReplayResult {
	p.m.PersistenceErrorsTotal.WithLabelValues(op).Inc()
	ml.Error("message persistence failed", "operation", op, "err", err.Error())
	return ReplayResult{Outcome: ReplayPersistenceFailed}
}

func (p *Processor) RecordAck(ctx context.Context, id int64, ack string, code hl7.AckCode, writeErr error) {
	p.m.AcksTotal.WithLabelValues(strings.ToLower(string(code))).Inc()
	ml := p.log.With("message_id", id)
	if writeErr != nil {
		ml.Warn("ack write failed", "err", writeErr.Error())
	} else {
		ml.Debug("ack sent", "code", code)
	}
	if id == 0 {
		return
	}
	if err := p.store.RecordAck(ctx, id, ack, time.Now(), writeErr == nil); err != nil {
		p.m.PersistenceErrorsTotal.WithLabelValues(opRecordAck).Inc()
		ml.Error("recording ack outcome failed", "err", err.Error())
	}
}

func logMissingRequiredFields(l *slog.Logger, ev evaluation) {
	if len(ev.missing) == 0 {
		return
	}
	l.Warn("accepted message with missing required msh fields", "control_id", ev.msg.MSH.ControlID(), "missing", ev.missing)
}

type class int

const (
	uncorrelatable class = iota
	parseFailed
	parsed
)

type evaluation struct {
	class   class
	msg     hl7.Message
	xml     string
	missing []string
}

func evaluate(msg string) evaluation {
	parsedMsg, err := hl7.ParseMessage(msg)
	cerr, correlatable := errors.AsType[*hl7.CorrelatableError](err)
	switch {
	case err != nil && !correlatable:
		return evaluation{class: uncorrelatable}
	case correlatable:
		return evaluation{class: parseFailed, msg: hl7.Message{MSH: cerr.MSH}}
	default:
		return evaluation{
			class:   parsed,
			msg:     parsedMsg,
			xml:     hl7.XML(parsedMsg),
			missing: parsedMsg.MSH.MissingRequiredFields(),
		}
	}
}

type recorder struct {
	p       *Processor
	log     *slog.Logger
	started time.Time
	id      int64
	err     error
}

func (p *Processor) begin(ctx context.Context, raw string) *recorder {
	r := &recorder{p: p, started: time.Now()}
	id, err := p.store.CreateMessage(ctx, r.started, raw)
	r.id = id
	r.log = p.log.With("message_id", id)
	r.note(opCreateMessage, err)
	r.log.Debug("message received")
	return r
}

func (r *recorder) finish(outcome Outcome, msg hl7.Message, attempts []record.Attempt) Result {
	r.p.m.MessagesTotal.WithLabelValues(strings.ToLower(string(outcome))).Inc()
	attrs := []any{
		"control_id", msg.MSH.ControlID(), "message_type", msg.MSH.MessageType(), "outcome", outcome,
		"attempts", len(attempts), "duration_ms", time.Since(r.started).Seconds() * 1000,
	}
	if n := len(attempts); n > 0 && attempts[n-1].ResponseStatusCode != nil {
		attrs = append(attrs, "status_code", *attempts[n-1].ResponseStatusCode)
	}
	r.log.Info("message processed", attrs...)
	return Result{Outcome: outcome, MessageID: r.id, Message: msg, Attempts: attempts}
}

func (r *recorder) transform(ctx context.Context, xml string, terr record.TransformError) {
	if r.err != nil {
		return
	}
	r.note(opRecordTransform, r.p.store.RecordTransform(ctx, r.id, xml, terr))
}

func (r *recorder) note(op string, err error) {
	if err == nil {
		return
	}
	r.err = err
	r.p.m.PersistenceErrorsTotal.WithLabelValues(op).Inc()
	r.log.Error("message persistence failed", "operation", op, "err", err.Error())
}
