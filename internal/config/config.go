package config

import (
	"errors"
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

var validAlgos = map[string]bool{
	"roundrobin": true,
	"leastconn":  true,
	"iphash":     true,
}

var (
	ErrNoBackends       = errors.New("no backend configured")
	ErrInvalidAlgorithm = errors.New("invalid balancing algorithm")
	ErrInvalidistenAddr = errors.New("invalid listen address")
)

type Config struct {
	ListenAddr string          `yaml:"listen_addr"`
	AdminAddr  string          `yaml:"admin_addr"`
	Balancer   string          `yaml:"balancer"`
	LogLevel   string          `yaml:"log_level"`
	Backends   []BackendConfig `yaml:"backends"`
	Health     HealthConfig    `yaml:"health"`
	Transport  TransportConfig `yaml:"transport"`
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
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("validating config: %w", err)
	}
	return &cfg, nil
}

func (c *Config) Validate() error {
	if c.ListenAddr == "" {
		return ErrInvalidistenAddr
	}

	if len(c.Backends) == 0 {
		return ErrNoBackends
	}
	if !validAlgos[c.Balancer] {
		return ErrInvalidAlgorithm
	}
	if c.Health.PassThreshold < 1 {
		c.Health.PassThreshold = 2
	}
	if c.Health.FailThreshold < 1 {
		c.Health.FailThreshold = 3
	}
	if c.Transport.MaxConnsPerHost < 1 {
		c.Transport.MaxConnsPerHost = 50
	}
	return nil
}
