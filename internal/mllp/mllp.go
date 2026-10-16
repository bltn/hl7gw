package mllp

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
)

var ErrBadTrailer = errors.New("expected carriage return")

const (
	startBlock     = 0x0B
	endBlock       = 0x1C
	carriageReturn = 0x0D
)

func ReadUntilStart(r *bufio.Reader) error {
	_, err := r.ReadBytes(startBlock)
	return err
}

func ReadFrame(r *bufio.Reader) (string, error) {
	msg, err := r.ReadBytes(endBlock)
	if err != nil {
		return "", err
	}
	msg = msg[:len(msg)-1]

	finalByte, err := r.ReadByte()
	if err != nil {
		return "", err
	}
	if finalByte != carriageReturn {
		return "", fmt.Errorf("%w, got %#x", ErrBadTrailer, finalByte)
	}

	return string(msg), nil
}

func WriteFrame(body string, w *bufio.Writer) error {
	var buf bytes.Buffer
	buf.WriteByte(startBlock)
	buf.WriteString(body)
	buf.WriteByte(endBlock)
	buf.WriteByte(carriageReturn)

	_, err := w.Write(buf.Bytes())
	if err != nil {
		return err
	}

	err = w.Flush()
	return err
}
