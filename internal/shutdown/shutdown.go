package shutdown

import (
	"context"
	"sync/atomic"
	"time"
)

func Begin(ctx context.Context, limit time.Duration) (drain context.Context, expired func() bool, cleanup func()) {
	drain, cancel := context.WithCancel(context.WithoutCancel(ctx))

	var forced atomic.Bool
	stopWatch := context.AfterFunc(ctx, func() {
		select {
		case <-drain.Done():
		case <-time.After(limit):
			forced.Store(true)
			cancel()
		}
	})

	return drain, forced.Load, func() {
		stopWatch()
		cancel()
	}
}
