package balancer

import (
	"net/http"
	"sync/atomic"

	"github.com/VJ-2303/Nexus/internal/backend"
)

type RoundRobin struct {
	counter atomic.Uint64
}

func NewRoundRobin() *RoundRobin {
	return &RoundRobin{}
}

func (rr *RoundRobin) Next(backends []*backend.Backend, _ *http.Request) *backend.Backend {
	n := uint64(len(backends))
	if n == 0 {
		return nil
	}

	current := rr.counter.Add(1)

	for i := uint64(0); i < n; i++ {
		idx := (current + i) % n
		b := backends[idx]
		if b.IsAlive() {
			return b
		}
	}
	return nil
}
