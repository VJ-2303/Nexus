package balancer

import (
	"fmt"
	"net/http"

	"github.com/VJ-2303/Nexus/internal/backend"
)

type Balancer interface {
	Next(backends []*backend.Backend, r *http.Request) *backend.Backend
}

func New(algorithm string) (Balancer, error) {
	switch algorithm {
	case "roundrobin":
		return NewRoundRobin(), nil
	case "weighted_roundrobin":
		return NewWeightedRoundRobin(), nil
	case "leastconn":
		return NewLeastConn(), nil
	case "iphash":
		return NewIPHash(), nil
	default:
		return nil, fmt.Errorf("balancer: unknown algorithm %q", algorithm)
	}
}
