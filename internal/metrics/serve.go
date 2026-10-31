package metrics

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"

	"github.com/bltn/hl7gw/internal/httpserve"
	"golang.org/x/sync/errgroup"
)

func (s *Set) Serve(g *errgroup.Group, ctx context.Context, port int, ready func(context.Context) error, log *slog.Logger) error {
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return fmt.Errorf("binding metrics listener: %w", err)
	}
	httpserve.Run(g, ctx, ln, s.handler(ready))
	log.Info("metrics listening", "addr", ln.Addr().String())
	return nil
}

func (s *Set) handler(ready func(context.Context) error) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		httpserve.Text(w, http.StatusOK, "ok")
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		if err := ready(r.Context()); err != nil {
			httpserve.Text(w, http.StatusServiceUnavailable, err.Error())
			return
		}
		httpserve.Text(w, http.StatusOK, "ok")
	})
	mux.Handle("GET /metrics", s.Handler())
	return mux
}
