package gateway

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"runtime/debug"
	"time"

	"github.com/bltn/hl7gw/internal/config"
	"github.com/bltn/hl7gw/internal/console"
	"github.com/bltn/hl7gw/internal/metrics"
	"github.com/bltn/hl7gw/internal/ops"
	"github.com/bltn/hl7gw/internal/processor"
	"github.com/bltn/hl7gw/internal/retention"
	"github.com/bltn/hl7gw/internal/shutdown"
	"github.com/bltn/hl7gw/internal/source"
	"github.com/bltn/hl7gw/internal/store"
	"golang.org/x/sync/errgroup"
)

const (
	shutdownDeadline  = 20 * time.Second
	retentionInterval = time.Hour
)

func Run(ctx context.Context, configPath string, stdout io.Writer, term console.Terminal) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}
	term.Banner(stdout)
	log := cfg.Log.Logger(stdout, term)
	version, commit := buildVersion()
	log.Info("starting", "version", version, "commit", commit, "config", cfg.Path)
	m := metrics.New(version, commit)

	stores, err := store.OpenAll(ctx, cfg)
	if err != nil {
		return err
	}
	defer store.CloseAll(stores)

	socket, err := ops.Listen(ctx, cfg.Socket)
	if err != nil {
		return err
	}
	defer func() { _ = socket.Close() }()

	sweeper := retention.New(stores, cfg.Retention, retentionInterval, log, m)
	sweeper.Sweep(ctx)

	bls, err := source.BindListeners(ctx, cfg, log, m)
	if err != nil {
		return err
	}

	g, ctx := errgroup.WithContext(ctx)

	drainCtx, expired, cleanup := shutdown.Begin(ctx, shutdownDeadline)
	defer cleanup()

	processors := processor.New(ctx.Done(), cfg, stores, log, m)

	ops.Serve(g, ctx, socket, stores, processors, log)
	if err := m.Serve(g, ctx, cfg.Metrics.Port, readiness(cfg, stores, log), log); err != nil {
		return err
	}

	g.Go(func() error {
		cfg.ReloadTLS(ctx, log, m)
		return nil
	})
	g.Go(func() error {
		sweeper.Run(ctx)
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

func readiness(cfg config.Config, stores map[string]*store.Store, log *slog.Logger) func(context.Context) error {
	return func(ctx context.Context) error {
		for _, ch := range cfg.Channels {
			if err := stores[ch.Name].Ping(ctx); err != nil {
				log.Warn("readiness check failed", "channel", ch.Name, "err", err.Error())
				return fmt.Errorf("channel %s is not ready", ch.Name)
			}
		}
		return nil
	}
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
