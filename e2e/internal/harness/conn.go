package harness

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"time"
)

const (
	MLLPStart byte = 0x0B

	mllpEnd = 0x1C
	mllpCR  = 0x0D
)

type Conn struct {
	conn net.Conn
	r    *bufio.Reader
}

func (c *Conn) Close() error {
	return c.conn.Close()
}

func (c *Conn) startTLS(cfg *tls.Config) (*Conn, error) {
	tlsConn := tls.Client(c.conn, cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := tlsConn.HandshakeContext(ctx); err != nil {
		return nil, err
	}
	return &Conn{conn: tlsConn, r: bufio.NewReader(tlsConn)}, nil
}

func (c *Conn) PeerSerial() (*big.Int, bool) {
	tlsConn, ok := c.conn.(*tls.Conn)
	if !ok {
		return nil, false
	}
	return tlsConn.ConnectionState().PeerCertificates[0].SerialNumber, true
}

func (c *Conn) Abort() error {
	tcp, ok := c.conn.(*net.TCPConn)
	if !ok {
		return fmt.Errorf("expected *net.TCPConn, got %T", c.conn)
	}
	if err := tcp.SetLinger(0); err != nil {
		return err
	}
	return tcp.Close()
}

func Frame(msg string) []byte {
	frame := append([]byte{MLLPStart}, msg...)
	return append(frame, mllpEnd, mllpCR)
}

func (c *Conn) Send(msg string) error {
	if err := c.WriteRaw(Frame(msg)); err != nil {
		return fmt.Errorf("write MLLP: %w", err)
	}
	return nil
}

func (c *Conn) ReadMessage() (string, error) {
	if _, err := c.r.ReadBytes(MLLPStart); err != nil {
		return "", fmt.Errorf("read start: %w", err)
	}
	body, err := c.r.ReadBytes(mllpEnd)
	if err != nil {
		return "", fmt.Errorf("read frame: %w", err)
	}
	body = body[:len(body)-1]
	if _, err := c.r.ReadByte(); err != nil {
		return "", fmt.Errorf("read trailer: %w", err)
	}
	return string(body), nil
}

type Inbound struct {
	Messages []string
	Err      error
}

func (c *Conn) ReadMessages(n int, within time.Duration) <-chan Inbound {
	done := make(chan Inbound, 1)
	go func() {
		_ = c.conn.SetReadDeadline(time.Now().Add(within))
		in := Inbound{Messages: make([]string, 0, n)}
		for len(in.Messages) < n {
			msg, err := c.ReadMessage()
			if err != nil {
				in.Err = fmt.Errorf("after %d of %d messages: %w", len(in.Messages), n, err)
				break
			}
			in.Messages = append(in.Messages, msg)
		}
		_ = c.conn.SetReadDeadline(time.Time{})
		done <- in
	}()
	return done
}

func (c *Conn) WriteRaw(p []byte) error {
	_ = c.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	_, err := c.conn.Write(p)
	return err
}

func (c *Conn) WriteRawUntilBlocked(frame []byte, maxFrames int) (complete int, blocked bool, err error) {
	for range maxFrames {
		_ = c.conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
		_, writeErr := c.conn.Write(frame)
		if writeErr == nil {
			complete++
			continue
		}

		var nerr net.Error
		if errors.As(writeErr, &nerr) && nerr.Timeout() {
			return complete, true, nil
		}
		return complete, false, fmt.Errorf("write frame %d: %w", complete, writeErr)
	}
	return complete, false, nil
}

func (c *Conn) CapSendBuffer(size int) error {
	tcp, ok := c.conn.(*net.TCPConn)
	if !ok {
		return fmt.Errorf("expected *net.TCPConn, got %T", c.conn)
	}
	return tcp.SetWriteBuffer(size)
}

func (c *Conn) CapReceiveBuffer(size int) error {
	tcp, ok := c.conn.(*net.TCPConn)
	if !ok {
		return fmt.Errorf("expected *net.TCPConn, got %T", c.conn)
	}
	return tcp.SetReadBuffer(size)
}

func (c *Conn) HasInboundWithin(within time.Duration) bool {
	_ = c.conn.SetReadDeadline(time.Now().Add(within))
	defer func() { _ = c.conn.SetReadDeadline(time.Time{}) }()

	_, err := c.r.Peek(1)
	return err == nil
}

func (c *Conn) ClosedWithin(within time.Duration) bool {
	_ = c.conn.SetReadDeadline(time.Now().Add(within))
	_, err := io.Copy(io.Discard, c.r)
	if err == nil {
		return true
	}
	var nerr net.Error
	return !errors.As(err, &nerr) || !nerr.Timeout()
}

func (c *Conn) ReadUntilError(within time.Duration) error {
	_ = c.conn.SetReadDeadline(time.Now().Add(within))
	buf := make([]byte, 4096)
	for {
		if _, err := c.r.Read(buf); err != nil {
			return err
		}
	}
}
