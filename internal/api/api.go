package api

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/bltn/hl7gw/internal/config"
	"github.com/bltn/hl7gw/internal/metrics"
	"github.com/bltn/hl7gw/internal/processor"
	"github.com/bltn/hl7gw/internal/store"
	"golang.org/x/sync/errgroup"
)

const shutdownWait = 5 * time.Second

func Serve(g *errgroup.Group, ctx context.Context, cfg config.Config, stores map[string]*store.Store, processors map[string]*processor.Processor, openapiSpec []byte, log *slog.Logger, m *metrics.Set) error {
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", fmt.Sprintf(":%d", cfg.Admin.Port))
	if err != nil {
		return fmt.Errorf("binding admin api listener: %w", err)
	}

	h := newHandler(cfg.Channels, stores, processors, cfg.Admin.Token(), openapiSpec, log, m)
	srv := &http.Server{Handler: h, ReadHeaderTimeout: 5 * time.Second}
	g.Go(func() error {
		return ignoreServerClosed(srv.Serve(ln))
	})
	g.Go(func() error {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownWait)
		defer cancel()
		return srv.Shutdown(sctx)
	})
	log.Info("admin api listening", "addr", ln.Addr().String())

	return nil
}

func newHandler(channels []config.Channel, stores map[string]*store.Store, processors map[string]*processor.Processor, token string, openapiSpec []byte, log *slog.Logger, m *metrics.Set) http.Handler {
	a := &api{channels: channels, stores: stores, processors: processors, tokenHash: sha256.Sum256([]byte(token)), log: log}

	handle := func(mux *http.ServeMux, method, path string, h http.HandlerFunc) {
		mux.HandleFunc(method+" "+path, h)
		allow := method
		if method == http.MethodGet {
			allow = allowGetHead
		}
		mux.HandleFunc(path, methodNotAllowed(allow))
	}

	public := http.NewServeMux()
	public.Handle("GET /openapi.yaml", asset("application/yaml", openapiSpec))
	public.Handle("GET /docs", asset("text/html; charset=utf-8", docsHTML))
	public.Handle("GET /docs/redoc.standalone.js", asset("text/javascript; charset=utf-8", redocJS))
	handle(public, http.MethodGet, "/healthz", healthz)
	handle(public, http.MethodGet, "/readyz", a.readyz)
	handle(public, http.MethodGet, "/metrics", m.Handler().ServeHTTP)

	protected := http.NewServeMux()
	handle(protected, http.MethodGet, "/messages", a.listMessages)
	handle(protected, http.MethodGet, "/messages/{id}", a.getMessage)
	handle(protected, http.MethodGet, "/channels", a.listChannels)
	handle(protected, http.MethodPost, "/replay", a.replay)
	handle(protected, http.MethodPost, "/process-message", a.processMessage)
	protected.HandleFunc("/", noSuchEndpoint)
	logged := requestLogger(log, protected)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, pattern := public.Handler(r); pattern != "" {
			public.ServeHTTP(w, r)
			return
		}
		if !a.authorized(r) {
			log.Warn("admin api unauthorized", "method", r.Method, "path", r.URL.Path, "remote", r.RemoteAddr)
			writeProblem(w, http.StatusUnauthorized, "invalid or missing bearer token")
			return
		}
		logged.ServeHTTP(w, r)
	})
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
		log.Info("admin api request", "method", r.Method, "path", r.URL.Path, "status", rec.status, "duration_ms", time.Since(started).Seconds()*1000)
	})
}

func ignoreServerClosed(err error) error {
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

const allowGetHead = "GET, HEAD"

type api struct {
	channels   []config.Channel
	stores     map[string]*store.Store
	processors map[string]*processor.Processor
	tokenHash  [32]byte
	log        *slog.Logger
}

var (
	errChannelMissing = errors.New("channel missing")
	errChannelUnknown = errors.New("channel unknown")
)

func (a *api) resolveChannel(name string) error {
	if name == "" {
		return errChannelMissing
	}
	if _, ok := a.stores[name]; !ok {
		return errChannelUnknown
	}
	return nil
}

func healthz(w http.ResponseWriter, _ *http.Request) {
	writeOK(w)
}

func (a *api) readyz(w http.ResponseWriter, r *http.Request) {
	for _, ch := range a.channels {
		if err := a.stores[ch.Name].Ping(r.Context()); err != nil {
			a.log.Warn("readiness check failed", "channel", ch.Name, "err", err.Error())
			writeProblem(w, http.StatusServiceUnavailable, "channel "+ch.Name+" is not ready")
			return
		}
	}
	writeOK(w)
}

func writeOK(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = io.WriteString(w, "ok")
}

func (a *api) authorized(r *http.Request) bool {
	got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok {
		return false
	}
	gotHash := sha256.Sum256([]byte(got))
	return subtle.ConstantTimeCompare(a.tokenHash[:], gotHash[:]) == 1
}

type problem struct {
	Type   string `json:"type"`
	Title  string `json:"title"`
	Status int    `json:"status"`
	Detail string `json:"detail,omitempty"`
}

func methodNotAllowed(allow string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Allow", allow)
		writeProblem(w, http.StatusMethodNotAllowed, fmt.Sprintf("%s is not supported on %s", r.Method, r.URL.Path))
	}
}

func noSuchEndpoint(w http.ResponseWriter, r *http.Request) {
	writeProblem(w, http.StatusNotFound, fmt.Sprintf("no such endpoint: %s", r.URL.Path))
}

func writeProblem(w http.ResponseWriter, status int, detail string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(problem{Type: "about:blank", Title: http.StatusText(status), Status: status, Detail: detail})
}

func writeJSON(w http.ResponseWriter, body any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(body)
}
