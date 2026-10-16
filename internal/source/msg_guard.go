package source

import (
	"net"
	"sync"
)

type msgGuard struct {
	mu       sync.Mutex
	inMsg    bool
	draining bool
}

func (g *msgGuard) drain(conn net.Conn) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.draining = true
	if !g.inMsg {
		_ = conn.Close()
	}
}

func (g *msgGuard) begin() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.inMsg = true
}

func (g *msgGuard) end() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.inMsg = false
	return g.draining
}
