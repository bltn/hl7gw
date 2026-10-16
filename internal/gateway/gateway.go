package gateway

import (
	"context"
	"io"
	"runtime/debug"
	"time"

	"github.com/bltn/hl7gw/internal/api"
	"github.com/bltn/hl7gw/internal/config"
	"github.com/bltn/hl7gw/internal/console"
	"github.com/bltn/hl7gw/internal/metrics"
	"github.com/bltn/hl7gw/internal/processor"
	"github.com/bltn/hl7gw/internal/shutdown"
	"github.com/bltn/hl7gw/internal/source"
	"github.com/bltn/hl7gw/internal/store"
	"golang.org/x/sync/errgroup"
)

const shutdownDeadline = 20 * time.Second

func Run(ctx context.Context, configPath string, openapiSpec []byte, stdout io.Writer, term console.Terminal) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}
	term.Banner(stdout)
	log := cfg.Log.Logger(stdout, term)
	version, commit := buildVersion()
	log.Info("starting", "version", version, "commit", commit)
	m := metrics.New(version, commit)

	stores, err := store.OpenAll(ctx, cfg)
	if err != nil {
		return err
	}
	defer store.CloseAll(stores)

	bls, err := source.BindListeners(ctx, cfg, log, m)
	if err != nil {
		return err
	}

	g, ctx := errgroup.WithContext(ctx)

	drainCtx, expired, cleanup := shutdown.Begin(ctx, shutdownDeadline)
	defer cleanup()

	processors := processor.New(ctx.Done(), cfg, stores, log, m)

	if err := api.Serve(g, ctx, cfg, stores, processors, openapiSpec, log, m); err != nil {
		return err
	}

	g.Go(func() error {
		cfg.ReloadTLS(ctx, log, m)
		return nil
	})

	for _, boundListener := range bls {
		ch := boundListener.Channel()
		h := newHandler(processors[ch.Name])
		g.Go(func() error {
			return boundListener.Serve(ctx, drainCtx, h)
		})
	}

	err = g.Wait()
	if expired() {
		log.Warn("shutdown deadline reached, abandoned in-flight work")
	}
	log.Info("shutdown complete")
	return err
}

func buildVersion() (version, commit string) {
	info, _ := debug.ReadBuildInfo()
	for _, s := range info.Settings {
		if s.Key == "vcs.revision" {
			commit = s.Value
		}
	}
	return info.Main.Version, commit
}
