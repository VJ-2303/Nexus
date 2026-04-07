package proxy

import (
	"io"
	"net"
	"net/http"

	"github.com/VJ-2303/Nexus/internal/backend"
	"github.com/VJ-2303/Nexus/internal/balancer"
	"github.com/VJ-2303/Nexus/internal/config"
)

var hopByHopHeaders = []string{
	"Connection",
	"Keep-Alive",
	"Proxy-Authenticate",
	"Proxy-Authorization",
	"Te",
	"Trailer",
	"Transfer-Encoding",
	"Upgrade",
}

type Proxy struct {
	backends []*backend.Backend
	balancer balancer.Balancer
	client   *http.Client
}

func New(cfg *config.Config, backends []*backend.Backend, bal balancer.Balancer) *Proxy {
	transport := &http.Transport{
		MaxIdleConns:        cfg.Transport.MaxIdleConns,
		MaxIdleConnsPerHost: cfg.Transport.MaxIdleConnsPerHost,
		MaxConnsPerHost:     cfg.Transport.MaxConnsPerHost,
		IdleConnTimeout:     cfg.Transport.IdleConnTimeout,
	}
	client := &http.Client{
		Timeout:   cfg.Transport.ResponseTimeout,
		Transport: transport,
	}
	return &Proxy{
		backends: backends,
		balancer: bal,
		client:   client,
	}
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b := p.balancer.Next(p.backends, r)
	if b == nil {
		http.Error(w, "Service Unavailable: no healthy backends", http.StatusServiceUnavailable)
		return
	}
	b.IncrementConns()
	defer b.DecrementConns()

	targetURL := *b.URL
	targetURL.Path = r.URL.Path
	targetURL.RawQuery = r.URL.RawQuery

	proxyReq, err := http.NewRequestWithContext(r.Context(), r.Method, targetURL.String(), r.Body)
	if err != nil {
		http.Error(w, "internal Server Error", http.StatusInternalServerError)
		return
	}
	copyHeaders(proxyReq.Header, r.Header)

	for _, h := range hopByHopHeaders {
		proxyReq.Header.Del(h)
	}

	setForwardedHeaders(proxyReq, r)

	resp, err := p.client.Do(proxyReq)
	if err != nil {
		http.Error(w, "Bad Gateway", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	copyHeaders(w.Header(), resp.Header)

	for _, h := range hopByHopHeaders {
		proxyReq.Header.Del(h)
	}

	w.WriteHeader(resp.StatusCode)

	io.Copy(w, resp.Body)
}

func copyHeaders(dst, src http.Header) {
	for key, values := range src {
		for _, value := range values {
			dst.Add(key, value)
		}
	}
}

func setForwardedHeaders(proxyReq *http.Request, originalReq *http.
	Request) {
	clientIP := extractClientIP(originalReq)
	if prior := originalReq.Header.Get("X-Forwarded-For"); prior != "" {
		clientIP = prior + ", " + clientIP
	}
	proxyReq.Header.Set("X-Forwarded-For", clientIP)

	proxyReq.Header.Set("X-Real-IP", extractClientIP(originalReq))

	proto := "http"
	if originalReq.TLS != nil {
		proto = "https"
	}
	proxyReq.Header.Set("X-Forwarded-Proto", proto)

	proxyReq.Header.Set("X-Forwarded-Host", originalReq.Host)
}

// extractClientIP gets the client IP from RemoteAddr, stripping the port.
func extractClientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
