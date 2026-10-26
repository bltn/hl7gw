package retention

import (
	"context"
	"log/slog"
	"maps"
	"slices"
	"time"

	"github.com/bltn/hl7gw/internal/config"
	"github.com/bltn/hl7gw/internal/metrics"
	"github.com/bltn/hl7gw/internal/store"
	"github.com/prometheus/client_golang/prometheus"
)

type target struct {
	store  *store.Store
	log    *slog.Logger
	pruned prometheus.Counter
}

type Sweeper struct {
	targets  []target
	days     int
	interval time.Duration
	log      *slog.Logger
}

func New(stores map[string]*store.Store, policy config.Retention, interval time.Duration, log *slog.Logger, m *metrics.Set) *Sweeper {
	targets := make([]target, 0, len(stores))
	for _, name := range slices.Sorted(maps.Keys(stores)) {
		targets = append(targets, target{store: stores[name], log: log.With("channel", name), pruned: m.Channel(name).MessagesPruned})
	}
	return &Sweeper{targets: targets, days: policy.Days(), interval: interval, log: log}
}

func (s *Sweeper) Sweep(ctx context.Context) {
	if s.days == 0 {
		s.log.Info("retention off")
		return
	}
	cutoff := time.Now().AddDate(0, 0, -s.days)
	for _, t := range s.targets {
		started := time.Now()
		deleted, err := t.store.Prune(ctx, cutoff)
		t.pruned.Add(float64(deleted))
		if err != nil {
			t.log.Error("retention sweep failed", "err", err.Error())
			continue
		}
		t.log.Info("retention sweep", "deleted", deleted, "cutoff", cutoff.UTC().Format(time.RFC3339), "duration_ms", time.Since(started).Seconds()*1000)
	}
}

func (s *Sweeper) Run(ctx context.Context) {
	if s.days == 0 {
		return
	}
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.Sweep(ctx)
		}
	}
}
