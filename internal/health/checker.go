package health

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/VJ-2303/Nexus/internal/backend"
	"github.com/VJ-2303/Nexus/internal/config"
)

type Checker struct {
	backends      []*backend.Backend
	client        *http.Client
	interval      time.Duration
	passThreshold int
	failThreshold int
	logger        *slog.Logger
}

func NewChecker(backends []*backend.Backend, cfg config.HealthConfig, logger *slog.Logger) *Checker {
	return &Checker{
		backends: backends,
		client: &http.Client{
			Timeout: cfg.Timeout,
		},
		interval:      cfg.Interval,
		passThreshold: cfg.PassThreshold,
		failThreshold: cfg.FailThreshold,
		logger:        logger,
	}
}

func (c *Checker) Run(ctx context.Context) {
	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()

	c.logger.Info("health checker started",
		"interval", c.interval.Seconds(),
	)

	c.checkAll()

	for {
		select {
		case <-ctx.Done():
			c.logger.Info("health checker stopped")
		case <-ticker.C:
			c.checkAll()
		}
	}
}

func (c *Checker) checkAll() {
	for _, b := range c.backends {
		passed := c.checkOne(b)
		changed := b.RecordHealthCheck(passed, c.passThreshold, c.failThreshold)

		if changed {
			status := "UP"
			if !b.IsAlive() {
				status = "DOWN"
			}
			c.logger.Warn("backend state change", "url", b.URL.String(), "status", status)
		}
	}
}

func (c *Checker) checkOne(b *backend.Backend) bool {
	checkURL := b.URL.String() + "/"

	resp, err := c.client.Get(checkURL)
	if err != nil {
		return false
	}
	resp.Body.Close()

	return true
}
