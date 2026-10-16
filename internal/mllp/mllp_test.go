package mllp

import (
	"bufio"
	"errors"
	"strings"
	"testing"
)

func TestReadFrameRejectsBadTrailer(t *testing.T) {
	t.Parallel()
	_, err := ReadFrame(bufio.NewReader(strings.NewReader("MSH|^~\\&|\x1cX")))
	if !errors.Is(err, ErrBadTrailer) {
		t.Fatalf("err = %v, want ErrBadTrailer", err)
	}
	if !strings.Contains(err.Error(), "got 0x58") {
		t.Errorf("err = %q, want it to name the trailer byte", err)
	}
}
