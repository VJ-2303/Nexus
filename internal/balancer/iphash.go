package balancer

import (
	"hash/fnv"
	"net"
	"net/http"
	"strings"

	"github.com/VJ-2303/Nexus/internal/backend"
)

type IPHash struct{}

func NewIPHash() *IPHash {
	return &IPHash{}
}

func (ih *IPHash) Next(backends []*backend.Backend, r *http.Request) *backend.Backend {
	n := uint64(len(backends))

	if n == 0 {
		return nil
	}

	ip := extractClientIP(r)

	hash := hashIP(ip)

	for i := range n {
		idx := (hash + i) % n
		b := backends[idx]
		if b.IsAlive() {
			return b
		}
	}
	return nil
}

func extractClientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.SplitN(xff, ",", 2)
		clientIP := strings.TrimSpace(parts[0])

		if host, _, err := net.SplitHostPort(clientIP); err == nil {
			return host
		}
		return clientIP
	}
	if xri := r.Header.Get("X-Real-IP"); xri != "" {
		return xri
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func hashIP(ip string) uint64 {
	h := fnv.New64a()
	h.Write([]byte(ip))

	return h.Sum64()
}
