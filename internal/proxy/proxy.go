package proxy

import (
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"time"
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

func new(backendURLs []string) (*Proxy, error) {
	if len(backendURLs) == 0 {
		return nil, fmt.Errorf("no backend provided")
	}
	backends := make([]*Backend, len(backendURLs))
	for i, urlStr := range backendURLs {
		u, err := url.Parse(urlStr)
		if err != nil {
			return nil, fmt.Errorf("parsing backend URL %q: %w", urlStr, err)
		}
		backends[i] = &Backend{
			URL:    u,
			Alive:  true,
			Weight: 1,
		}
	}
	client := &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			MaxIdleConns:        100,
			MaxIdleConnsPerHost: 10,
			IdleConnTimeout:     90 * time.Second,
		},
	}
	return &Proxy{
		backends: backends,
		client:   client,
	}, nil
}
