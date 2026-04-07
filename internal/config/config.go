package config

import (
	"errors"
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

var validAlgos = map[string]bool{
	"roundrobin":          true,
	"leastconn":           true,
	"iphash":              true,
	"weighted_roundrobin": true,
}

var (
	ErrNoBackends        = errors.New("config: no backends configured")
	ErrInvalidAlgorithm  = errors.New("config: unsupported balancing algorithm")
	ErrInvalidListenAddr = errors.New("config: listen address is empty")
)

type Config struct {
	ListenAddr string          `yaml:"listen_addr"`
	Balancer   string          `yaml:"balancer"`
	Logging    LoggingConfig   `yaml:"logging"`
	Backends   []BackendConfig `yaml:"backends"`
	Health     HealthConfig    `yaml:"health"`
	Transport  TransportConfig `yaml:"transport"`
}

type LoggingConfig struct {
	Level     string `yaml:"level"`
	Format    string `yaml:"format"`
	AddSource bool   `yaml:"add_source"`
}

type BackendConfig struct {
	URL    string `yaml:"url"`
	Weight int    `yaml:"weight"`
}

type HealthConfig struct {
	Interval      time.Duration `yaml:"interval"`
	Timeout       time.Duration `yaml:"timeout"`
	PassThreshold int           `yaml:"pass_threshold"`
	FailThreshold int           `yaml:"fail_threshold"`
}

type TransportConfig struct {
	MaxIdleConns        int           `yaml:"max_idle_conns"`
	MaxIdleConnsPerHost int           `yaml:"max_idle_conns_per_host"`
	MaxConnsPerHost     int           `yaml:"max_conns_per_host"`
	IdleConnTimeout     time.Duration `yaml:"idle_conn_timeout"`
	ResponseTimeout     time.Duration `yaml:"response_timeout"`
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config file: %w", err)
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing yaml: %w", err)
	}

	cfg.applyDefaults()

	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("validating config: %w", err)
	}
	return &cfg, nil
}

// validate checks that the config is usable. Called after defaults are applied.
func (c *Config) validate() error {
	if c.ListenAddr == "" {
		return ErrInvalidListenAddr
	}
	if len(c.Backends) == 0 {
		return ErrNoBackends
	}
	if !validAlgos[c.Balancer] {
		return fmt.Errorf("%w: %q (valid: roundrobin, leastconn, iphash, weighted_roundrobin)", ErrInvalidAlgorithm, c.Balancer)
	}

	// Validate each backend has a URL
	for i, b := range c.Backends {
		if b.URL == "" {
			return fmt.Errorf("config: backend[%d] has empty URL", i)
		}
		if b.Weight < 0 {
			return fmt.Errorf("config: backend[%d] has negative weight", i)
		}
		if b.Weight == 0 {
			c.Backends[i].Weight = 1
		}
	}

	return nil
}

func (c *Config) applyDefaults() {
	if c.Balancer == "" {
		c.Balancer = "roundrobin"
	}
	if c.Logging.Level == "" {
		c.Logging.Level = "info"
	}
	if c.Logging.Format == "" {
		c.Logging.Format = "json"
	}

	if c.Health.Interval == 0 {
		c.Health.Interval = 10 * time.Second
	}
	if c.Health.Timeout == 0 {
		c.Health.Timeout = 5 * time.Second
	}
	if c.Health.PassThreshold < 1 {
		c.Health.PassThreshold = 2
	}
	if c.Health.FailThreshold < 1 {
		c.Health.FailThreshold = 3
	}

	if c.Transport.MaxIdleConns < 1 {
		c.Transport.MaxIdleConns = 100
	}
	if c.Transport.MaxIdleConnsPerHost < 1 {
		c.Transport.MaxIdleConnsPerHost = 10
	}
	if c.Transport.MaxConnsPerHost < 1 {
		c.Transport.MaxConnsPerHost = 50
	}
	if c.Transport.IdleConnTimeout == 0 {
		c.Transport.IdleConnTimeout = 90 * time.Second
	}
	if c.Transport.ResponseTimeout == 0 {
		c.Transport.ResponseTimeout = 30 * time.Second
	}
}
