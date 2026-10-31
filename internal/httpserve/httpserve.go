package httpserve

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"time"

	"golang.org/x/sync/errgroup"
)

const shutdownWait = 5 * time.Second

func Run(g *errgroup.Group, ctx context.Context, ln net.Listener, h http.Handler) {
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
}

func Text(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}

func ignoreServerClosed(err error) error {
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
