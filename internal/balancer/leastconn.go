package balancer

import (
	"net/http"

	"github.com/VJ-2303/Nexus/internal/backend"
)

type LeastConn struct{}

func NewLeastConn() *LeastConn {
	return &LeastConn{}
}

func (lc *LeastConn) Next(backends []*backend.Backend, _ *http.Request) *backend.Backend {
	var best *backend.Backend
	var bestConns int64 = -1

	for _, b := range backends {
		if !b.IsAlive() {
			continue
		}
		conns := b.ActiveConnections()

		if best == nil || conns < bestConns {
			best = b
			bestConns = conns
		}
	}
	return best
}
