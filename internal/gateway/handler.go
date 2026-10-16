package gateway

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/bltn/hl7gw/internal/hl7"
	"github.com/bltn/hl7gw/internal/processor"
)

type parseEncodeForwardHandler struct {
	proc    *processor.Processor
	counter atomic.Uint32
}

func newHandler(p *processor.Processor) *parseEncodeForwardHandler {
	return &parseEncodeForwardHandler{proc: p}
}

func (h *parseEncodeForwardHandler) Handle(ctx context.Context, msg string) (string, bool, func(context.Context, error)) {
	res := h.proc.ParseTransformDeliver(ctx, msg)

	var code hl7.AckCode
	switch res.Outcome {
	case processor.Uncorrelatable:
		return "", false, nil
	case processor.ParseFailed, processor.DeliveryFailed:
		code = hl7.Error
	case processor.PersistenceFailed:
		code = hl7.Reject
	case processor.Success:
		code = hl7.Accept
	}

	ack := res.Message.Ack(code, time.Now(), h.generateControlID())
	return ack, true, func(ctx context.Context, writeErr error) {
		h.proc.RecordAck(ctx, res.MessageID, ack, code, writeErr)
	}
}

func (h *parseEncodeForwardHandler) generateControlID() string {
	n := h.counter.Add(1)
	return fmt.Sprintf("%s%06d", time.Now().Format("20060102150405"), n%1000000)
}
