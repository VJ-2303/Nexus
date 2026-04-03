package backend

import (
	"net/url"
	"sync"
	"sync/atomic"
)

type Backend struct {
	URL    *url.URL
	Weight int

	alive       atomic.Bool
	connections atomic.Int64

	mu              sync.RWMutex
	consecutivePass int
	consecutiveFail int
}

func New(rawURL string, weight int) (*Backend, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	b := &Backend{
		URL:    u,
		Weight: weight,
	}
	b.alive.Store(true)

	return b, nil
}

func (b *Backend) IsAlive() bool {
	return b.alive.Load()
}

func (b *Backend) SetAlive(alive bool) {
	b.alive.Store(alive)
}

func (b *Backend) ActiveConnections() int64 {
	return b.connections.Load()
}

func (b *Backend) IncrementConns() {
	b.connections.Add(1)
}

func (b *Backend) DecrementConns() {
	b.connections.Add(-1)
}

func (b *Backend) RecordHealthCheck(passed bool, passThreshold, failThreshold int) (changed bool) {
	b.mu.Lock()

	defer b.mu.Unlock()

	wasAlive := b.alive.Load()

	if passed {
		b.consecutiveFail = 0
		b.consecutivePass++
		if b.consecutivePass >= passThreshold {
			b.SetAlive(true)
		}
	} else {
		b.consecutivePass = 0
		b.consecutiveFail++
		if b.consecutiveFail >= failThreshold {
			b.SetAlive(false)
		}
	}

	return wasAlive != b.IsAlive()
}

func (b *Backend) ResetHealth() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.consecutivePass = 0
	b.consecutiveFail = 0
}
