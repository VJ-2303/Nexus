package balancer

import (
	"net/http"
	"sync/atomic"

	"github.com/VJ-2303/Nexus/internal/backend"
)

type WeightedRoundRobin struct {
	counter atomic.Uint64
}

func NewWeightedRoundRobin() *WeightedRoundRobin {
	return &WeightedRoundRobin{}
}

func (wrr *WeightedRoundRobin) Next(backends []*backend.Backend, _ *http.Request) *backend.Backend {
	if len(backends) == 0 {
		return nil
	}

	alive := make([]*backend.Backend, 0, len(backends))
	totalWeight := 0
	for _, b := range backends {
		if b.IsAlive() {
			alive = append(alive, b)
			totalWeight += b.Weight
		}
	}

	if len(alive) == 0 || totalWeight == 0 {
		return nil
	}

	current := wrr.counter.Add(1)
	pos := int(current % uint64(totalWeight))

	for _, b := range alive {
		if pos < b.Weight {
			return b
		}
		pos -= b.Weight
	}

	return alive[0]
}
