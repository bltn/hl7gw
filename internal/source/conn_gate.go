package source

import (
	"context"
	"sync"
)

type connGate struct {
	sem chan struct{}
	wg  sync.WaitGroup
}

func newConnGate(max int) *connGate {
	return &connGate{sem: make(chan struct{}, max)}
}

func (g *connGate) tryAcquire() bool {
	select {
	case g.sem <- struct{}{}:
		return true
	default:
		return false
	}
}

func (g *connGate) acquire(ctx context.Context) bool {
	select {
	case g.sem <- struct{}{}:
		return true
	case <-ctx.Done():
		return false
	}
}

func (g *connGate) release() {
	<-g.sem
}

func (g *connGate) run(fn func()) {
	g.wg.Go(func() {
		defer g.release()
		fn()
	})
}

func (g *connGate) wait() {
	g.wg.Wait()
}
