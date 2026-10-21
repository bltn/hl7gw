package source

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"time"

	"github.com/bltn/hl7gw/internal/config"
	"github.com/bltn/hl7gw/internal/metrics"
	"github.com/bltn/hl7gw/internal/mllp"
)

var (
	errDraining = errors.New("draining")
	errAckWrite = errors.New("ack write")
)

const (
	readTimeout           = 30 * time.Second
	writeTimeout          = 15 * time.Second
	handshakeTimeout      = 10 * time.Second
	tcpAcceptFailureLimit = 10
)

type BoundListener struct {
	ch     config.Channel
	ln     net.Listener
	tlsCfg *tls.Config
	log    *slog.Logger
	m      *metrics.Channel
}

type Handler interface {
	Handle(ctx context.Context, msg string) (ack string, shouldAck bool, afterAck func(context.Context, error))
}

func BindListeners(ctx context.Context, cfg config.Config, log *slog.Logger, m *metrics.Set) ([]*BoundListener, error) {
	var listeners []*BoundListener
	lc := net.ListenConfig{
		KeepAliveConfig: net.KeepAliveConfig{
			Enable:   true,
			Idle:     30 * time.Second,
			Interval: 10 * time.Second,
			Count:    3,
		},
	}

	for _, ch := range cfg.Channels {
		ln, err := lc.Listen(ctx, "tcp", fmt.Sprintf(":%d", ch.Source.Port))
		if err != nil {
			for _, b := range listeners {
				_ = b.ln.Close()
			}
			return nil, fmt.Errorf("channel %s: %w", ch.Name, err)
		}
		listeners = append(listeners, &BoundListener{ch: ch, ln: ln, tlsCfg: ch.Source.TLS.ServerConfig(), log: log.With("channel", ch.Name), m: m.Channel(ch.Name)})
	}

	return listeners, nil
}

func (bl *BoundListener) Channel() config.Channel {
	return bl.ch
}

func (bl *BoundListener) Serve(ctx, drain context.Context, h Handler) error {
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(ctx, func() {
		_ = bl.ln.Close()
		bl.log.Info("stopped accepting connections")
	})

	defer cancel()
	defer stop()

	bl.logListening()

	return bl.acceptLoop(ctx, drain, h)
}

func (bl *BoundListener) logListening() {
	attrs := []any{"addr", bl.ln.Addr().String(), "tls", bl.ch.Source.TLS.Mode}
	if bl.tlsCfg != nil {
		leaf := bl.ch.Source.TLS.ServerLeaf()
		bl.m.TLSCertExpiry.Set(float64(leaf.NotAfter.Unix()))
		attrs = append(attrs, "serial", leaf.SerialNumber.String(), "expires", leaf.NotAfter.UTC().Format(time.RFC3339))
	}
	bl.log.Info("listening", attrs...)
}

func (bl *BoundListener) acceptLoop(ctx, drain context.Context, h Handler) error {
	gate := newConnGate(bl.ch.Source.MaxConnections)
	defer gate.wait()

	for {
		if !gate.tryAcquire() {
			bl.m.AcceptPaused.Set(1)
			bl.log.Warn("at tcp conn capacity, pausing accepts", "max", bl.ch.Source.MaxConnections)
			if !gate.acquire(ctx) {
				return nil
			}
			bl.m.AcceptPaused.Set(0)
			bl.log.Info("below tcp conn capacity, resuming accepts")
		}

		conn, err := bl.accept(ctx)
		if err != nil {
			gate.release()
			if ctx.Err() != nil {
				return nil
			}

			return err
		}

		gate.run(func() { bl.serveConn(ctx, drain, conn, h) })
	}
}

func (bl *BoundListener) serveConn(ctx, drain context.Context, conn net.Conn, h Handler) {
	defer func() { _ = conn.Close() }()
	bl.m.ConnectionsOpen.Inc()
	defer bl.m.ConnectionsOpen.Dec()
	started := time.Now()
	cl := bl.log.With("remote", conn.RemoteAddr().String())
	cl.Debug("connection opened")

	guard := &msgGuard{}
	stopGuard := context.AfterFunc(ctx, func() { guard.drain(conn) })
	defer stopGuard()
	stopKill := context.AfterFunc(drain, func() { _ = conn.Close() })
	defer stopKill()

	sconn, leaf, err := bl.secure(ctx, conn)
	if err != nil {
		bl.m.TLSHandshakeFailures.Inc()
		bl.m.ConnectionsTotal.WithLabelValues("tls_handshake_failed").Inc()
		cl.Warn("tls handshake failed", "err", err.Error())
		return
	}
	if leaf != nil {
		cl.Info("tls client cert accepted", "cn", leaf.Subject.CommonName, "issuer", leaf.Issuer.String(),
			"serial", leaf.SerialNumber.String(), "expires", leaf.NotAfter.UTC().Format(time.RFC3339))
	}

	messages, err := bl.handleConn(drain, guard, h, sconn)
	reason, detail := closeReason(err)
	bl.m.ConnectionsTotal.WithLabelValues(reason).Inc()
	attrs := []any{"reason", reason, "messages", messages, "duration_ms", time.Since(started).Seconds() * 1000}
	if detail != nil {
		attrs = append(attrs, "err", detail.Error())
	}
	cl.Info("connection closed", attrs...)
}

func closeReason(err error) (string, error) {
	switch {
	case errors.Is(err, io.EOF):
		return "peer_closed", nil
	case errors.Is(err, errDraining), errors.Is(err, net.ErrClosed):
		return "drain", nil
	case errors.Is(err, errAckWrite):
		return "write_error", err
	case errors.Is(err, os.ErrDeadlineExceeded):
		return "timeout", err
	case errors.Is(err, mllp.ErrBadTrailer):
		return "protocol_error", err
	}
	return "error", err
}

func (bl *BoundListener) secure(ctx context.Context, conn net.Conn) (net.Conn, *x509.Certificate, error) {
	if bl.tlsCfg == nil {
		return conn, nil, nil
	}

	tlsConn := tls.Server(conn, bl.tlsCfg)
	hctx, cancel := context.WithTimeout(ctx, handshakeTimeout)
	defer cancel()
	if err := tlsConn.HandshakeContext(hctx); err != nil {
		return nil, nil, err
	}

	return tlsConn, tlsConn.ConnectionState().PeerCertificates[0], nil
}

func (bl *BoundListener) accept(ctx context.Context) (net.Conn, error) {
	backoff := 5 * time.Millisecond
	failures := 0

	for {
		conn, err := bl.ln.Accept()

		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}

			failures++
			if failures >= tcpAcceptFailureLimit {
				return nil, fmt.Errorf("accept failed %d times, giving up: %w", failures, err)
			}

			bl.log.Warn("accept failed, retrying", "backoff", backoff.String(), "err", err.Error())
			select {
			case <-time.After(backoff):
				backoff *= 2
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		} else {
			return conn, nil
		}
	}
}

func (bl *BoundListener) handleConn(ctx context.Context, guard *msgGuard, h Handler, conn net.Conn) (int, error) {
	r := bufio.NewReader(conn)
	w := bufio.NewWriter(conn)
	idle := time.Duration(bl.ch.Source.IdleTimeout)

	for messages := 0; ; messages++ {
		_ = conn.SetReadDeadline(time.Now().Add(idle))
		if err := mllp.ReadUntilStart(r); err != nil {
			return messages, err
		}
		guard.begin()
		bl.m.MessagesInFlight.Inc()
		started := time.Now()

		frame, err := readFrame(r, conn)
		if err != nil {
			bl.m.MessagesInFlight.Dec()
			return messages, err
		}

		ack, shouldAck, afterAck := h.Handle(ctx, frame)
		var werr error
		if shouldAck {
			werr = writeFrame(ack, w, conn)
			afterAck(ctx, werr)
		}

		bl.m.MessageDuration.Observe(time.Since(started).Seconds())
		bl.m.MessagesInFlight.Dec()
		draining := guard.end()
		if werr != nil {
			return messages + 1, fmt.Errorf("%w: %w", errAckWrite, werr)
		}
		if draining {
			return messages + 1, errDraining
		}
	}
}

func readFrame(r *bufio.Reader, conn net.Conn) (string, error) {
	defer func() { _ = conn.SetReadDeadline(time.Time{}) }()
	_ = conn.SetReadDeadline(time.Now().Add(readTimeout))

	frame, err := mllp.ReadFrame(r)

	return frame, err
}

func writeFrame(body string, w *bufio.Writer, conn net.Conn) error {
	defer func() { _ = conn.SetWriteDeadline(time.Time{}) }()
	_ = conn.SetWriteDeadline(time.Now().Add(writeTimeout))

	return mllp.WriteFrame(body, w)
}
