package ops

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"time"

	"github.com/bltn/hl7gw/internal/httpserve"
	"github.com/bltn/hl7gw/internal/processor"
	"github.com/bltn/hl7gw/internal/store"
	"golang.org/x/sync/errgroup"
)

const (
	maxSocketPath   = 103
	maxMessageBytes = 16 << 20
	socketMode      = 0o660
	defaultLimit    = 50
	maxLimit        = 500
	paramChannel    = "channel"
)

func Listen(ctx context.Context, path string) (net.Listener, error) {
	if len(path) > maxSocketPath {
		return nil, fmt.Errorf("socket path %q is too long (%d bytes, limit %d); set \"socket\" to a shorter path", path, len(path), maxSocketPath)
	}
	if fi, err := os.Lstat(path); err == nil {
		if fi.Mode()&os.ModeSocket == 0 {
			return nil, fmt.Errorf("%s exists and is not a socket", path)
		}
		d := net.Dialer{Timeout: time.Second}
		if conn, err := d.DialContext(ctx, "unix", path); err == nil {
			_ = conn.Close()
			return nil, fmt.Errorf("another hl7gw is already serving %s", path)
		}
		_ = os.Remove(path)
	}
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "unix", path)
	if err != nil {
		return nil, fmt.Errorf("binding socket: %w", err)
	}
	_ = os.Chmod(path, socketMode)
	return ln, nil
}

func Serve(g *errgroup.Group, ctx context.Context, ln net.Listener, stores map[string]*store.Store, processors map[string]*processor.Processor, log *slog.Logger) {
	s := &server{stores: stores, processors: processors}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /messages", handle(s.list))
	mux.HandleFunc("GET /messages/{id}", handle(s.get))
	mux.HandleFunc("POST /replay", handle(s.replay))
	mux.HandleFunc("POST /process", handle(s.process))
	httpserve.Run(g, ctx, ln, requestLogger(log, mux))
	log.Info("ops listening", "socket", ln.Addr().String())
}

type server struct {
	stores     map[string]*store.Store
	processors map[string]*processor.Processor
}

type httpError struct {
	status int
	msg    string
}

func handle(fn func(http.ResponseWriter, *http.Request) *httpError) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if herr := fn(w, r); herr != nil {
			httpserve.Text(w, herr.status, herr.msg)
		}
	}
}

func (s *server) channel(q url.Values) (string, *httpError) {
	name := q.Get(paramChannel)
	if name == "" {
		return "", &httpError{http.StatusBadRequest, "channel query parameter is required"}
	}
	if _, ok := s.stores[name]; !ok {
		return "", &httpError{http.StatusNotFound, fmt.Sprintf("no channel named %q", name)}
	}
	return name, nil
}

func queryInt(q url.Values, name string, def, floor int64) (int64, *httpError) {
	raw := q.Get(name)
	n, err := def, error(nil)
	if raw != "" {
		n, err = strconv.ParseInt(raw, 10, 64)
	}
	if err != nil || n < floor {
		return 0, &httpError{http.StatusBadRequest, fmt.Sprintf("%s must be an integer of at least %d, got %q", name, floor, raw)}
	}
	return n, nil
}

func (s *server) list(w http.ResponseWriter, r *http.Request) *httpError {
	q := r.URL.Query()
	name, herr := s.channel(q)
	if herr != nil {
		return herr
	}
	limit, herr := queryInt(q, "limit", defaultLimit, 1)
	if herr != nil {
		return herr
	}
	if limit > maxLimit {
		return &httpError{http.StatusBadRequest, fmt.Sprintf("limit must be at most %d, got %d", maxLimit, limit)}
	}
	before, herr := queryInt(q, "before", 0, 0)
	if herr != nil {
		return herr
	}
	msgs, err := s.stores[name].ListMessages(r.Context(), before, int(limit))
	if err != nil {
		return &httpError{http.StatusInternalServerError, "listing messages failed: " + err.Error()}
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	enc := encoder(w)
	for _, m := range msgs {
		_ = enc.Encode(toSummaryJSON(m))
	}
	return nil
}

func (s *server) get(w http.ResponseWriter, r *http.Request) *httpError {
	name, herr := s.channel(r.URL.Query())
	if herr != nil {
		return herr
	}
	id, ok := parseID(r.PathValue("id"))
	if !ok {
		return &httpError{http.StatusBadRequest, fmt.Sprintf("message id must be a positive integer, got %q", r.PathValue("id"))}
	}
	return s.writeMessage(w, r.Context(), name, id)
}

func (s *server) writeMessage(w http.ResponseWriter, ctx context.Context, name string, id int64) *httpError {
	m, err := s.stores[name].GetMessage(ctx, id)
	switch {
	case errors.Is(err, store.ErrMessageNotFound):
		return &httpError{http.StatusNotFound, fmt.Sprintf("no message with id %d", id)}
	case err != nil:
		return &httpError{http.StatusInternalServerError, "getting message failed: " + err.Error()}
	}
	writeJSON(w, toMessageJSON(m))
	return nil
}

func (s *server) replay(w http.ResponseWriter, r *http.Request) *httpError {
	q := r.URL.Query()
	name, herr := s.channel(q)
	if herr != nil {
		return herr
	}
	id, herr := queryInt(q, "id", 0, 1)
	if herr != nil {
		return herr
	}
	res := s.processors[name].Replay(r.Context(), id)
	switch res.Outcome {
	case processor.ReplayNotFound:
		herr = &httpError{http.StatusNotFound, fmt.Sprintf("no message with id %d", id)}
	case processor.ReplayNotYetDelivered:
		herr = &httpError{http.StatusBadRequest, fmt.Sprintf("message %d has no delivery attempts to replay", id)}
	case processor.ReplayDeliveryFailed:
		herr = &httpError{http.StatusBadGateway, "replay delivery failed"}
	case processor.ReplayPersistenceFailed:
		herr = &httpError{http.StatusInternalServerError, "replay failed"}
	case processor.Replayed:
		writeJSON(w, toReplayAttemptJSON(res.Attempt))
	}
	return herr
}

func (s *server) process(w http.ResponseWriter, r *http.Request) *httpError {
	q := r.URL.Query()
	name, herr := s.channel(q)
	if herr != nil {
		return herr
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxMessageBytes))
	if err != nil {
		return &httpError{http.StatusBadRequest, "reading the message: " + err.Error()}
	}
	if len(raw) == 0 {
		return &httpError{http.StatusBadRequest, "the message is empty"}
	}
	if q.Get("dry_run") == "1" {
		return s.dryRun(w, r.Context(), name, string(raw))
	}
	res := s.processors[name].ParseTransformDeliver(r.Context(), string(raw))
	switch res.Outcome {
	case processor.Uncorrelatable:
		herr = &httpError{http.StatusBadRequest, "message could not be parsed as HL7"}
	case processor.ParseFailed:
		herr = &httpError{http.StatusBadRequest, fmt.Sprintf("message %d could not be parsed as HL7", res.MessageID)}
	case processor.DeliveryFailed:
		herr = &httpError{http.StatusBadGateway, fmt.Sprintf("delivery failed (message %d persisted)", res.MessageID)}
	case processor.PersistenceFailed:
		herr = &httpError{http.StatusInternalServerError, "message persistence failed"}
	case processor.Success:
		herr = s.writeMessage(w, r.Context(), name, res.MessageID)
	}
	return herr
}

func (s *server) dryRun(w http.ResponseWriter, ctx context.Context, name, raw string) *httpError {
	res := s.processors[name].ParseTransformDryRun(ctx, raw)
	switch res.Outcome {
	case processor.DryRunUncorrelatable, processor.DryRunParseFailed:
		return &httpError{http.StatusBadRequest, "message could not be parsed as HL7"}
	case processor.DryRunSuccess:
		writeJSON(w, toDryRunMessageJSON(res.Message))
	}
	return nil
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func requestLogger(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		log.Info("ops request", "method", r.Method, "path", r.URL.Path, "query", r.URL.RawQuery, "status", rec.status, "duration_ms", time.Since(started).Seconds()*1000)
	})
}
