package harness

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

type Sink struct {
	server   *httptest.Server
	mu       sync.Mutex
	statuses []int
	body     string
	served   int
	bodies   []string
	auths    []string
}

func NewSink(t *testing.T, status int, body string, more ...int) *Sink {
	return newSink(t, append([]int{status}, more...), body, nil)
}

func NewGatedSink(t *testing.T, status int, body string, more ...int) (*Sink, func()) {
	gate := make(chan struct{})
	t.Cleanup(func() { close(gate) })
	return newSink(t, append([]int{status}, more...), body, gate), func() { gate <- struct{}{} }
}

func newSink(t *testing.T, statuses []int, body string, gate chan struct{}) *Sink {
	s := &Sink{statuses: statuses, body: body}
	s.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.bodies = append(s.bodies, string(received))
		s.auths = append(s.auths, r.Header.Get("Authorization"))
		status := s.statuses[min(s.served, len(s.statuses)-1)]
		s.served++
		s.mu.Unlock()
		if gate != nil {
			<-gate
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(s.body))
	}))
	t.Cleanup(s.server.Close)
	return s
}

func (s *Sink) WaitForReceived(msgCount int) bool {
	return eventually(5*time.Second, func() bool {
		return len(s.Received()) >= msgCount
	})
}

func (s *Sink) URL() string { return s.server.URL }

func (s *Sink) Received() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string{}, s.bodies...)
}

func (s *Sink) AuthHeaders() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string{}, s.auths...)
}
