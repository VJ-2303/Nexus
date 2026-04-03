package proxy

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"

	"github.com/VJ-2303/Nexus/internal/config"
)

type Backend struct {
	URL    *url.URL
	Alive  bool
	Weight int
}

type Proxy struct {
	backends []*Backend
	mu       sync.RWMutex
	client   *http.Client
}

func New(cfg *config.Config) (*Proxy, error) {
	if len(cfg.Backends) == 0 {
		return nil, fmt.Errorf("no backend provided")
	}
	backends := make([]*Backend, len(cfg.Backends))
	for i, backend := range cfg.Backends {
		u, err := url.Parse(backend.URL)
		if err != nil {
			return nil, fmt.Errorf("parsing backend URL %q: %w", backend.URL, err)
		}
		backends[i] = &Backend{
			URL:    u,
			Alive:  true,
			Weight: backend.Weight,
		}
	}
	client := &http.Client{
		Timeout: cfg.Transport.ResponseTimeout,
		Transport: &http.Transport{
			MaxIdleConns:        cfg.Transport.MaxIdleConns,
			MaxIdleConnsPerHost: cfg.Transport.MaxIdleConnsPerHost,
			IdleConnTimeout:     cfg.Transport.IdleConnTimeout,
		},
	}
	return &Proxy{
		backends: backends,
		client:   client,
	}, nil
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	backend := p.getNextBackend()
	if backend == nil {
		http.Error(w, "no healthy backends", http.StatusServiceUnavailable)
		return
	}
	targetURL := backend.URL.ResolveReference(r.URL)

	proxyReq, err := http.NewRequest(r.Method, targetURL.String(), r.Body)
	if err != nil {
		http.Error(w, "failed to create request", http.StatusInternalServerError)
		return
	}

	for key, values := range r.Header {
		for _, value := range values {
			proxyReq.Header.Add(key, value)
		}
	}
	proxyReq.Header.Set("X-Forwarded-For", r.RemoteAddr)
	proxyReq.Header.Set("X-Real_IP", r.RemoteAddr)

	if r.TLS != nil {
		proxyReq.Header.Set("X-Forwarded-Proto", "https")
	} else {
		proxyReq.Header.Set("X-Forwarded-Proto", "http")
	}

	// Strip hop-by-hop headers
	proxyReq.Header.Del("Connection")
	proxyReq.Header.Del("Keep-Alive")
	proxyReq.Header.Del("Proxy-Authorization")
	proxyReq.Header.Del("Proxy-Authenticate")
	proxyReq.Header.Del("Trailer")
	proxyReq.Header.Del("Transfer-Encoding")
	proxyReq.Header.Del("Upgrade")

	resp, err := p.client.Do(proxyReq)
	if err != nil {
		http.Error(w, "Bad Gateway", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	for key, values := range resp.Header {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}

	w.WriteHeader(resp.StatusCode)

	io.Copy(w, resp.Body)
}

func (p *Proxy) getNextBackend() *Backend {
	p.mu.RLock()
	defer p.mu.RUnlock()

	for _, backend := range p.backends {
		if backend.Alive {
			return backend
		}
	}
	return nil
}
