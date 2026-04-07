package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/VJ-2303/Nexus/internal/backend"
	"github.com/VJ-2303/Nexus/internal/balancer"
	"github.com/VJ-2303/Nexus/internal/config"
	"github.com/VJ-2303/Nexus/internal/health"
	"github.com/VJ-2303/Nexus/internal/logger"
	"github.com/VJ-2303/Nexus/internal/middleware"
	"github.com/VJ-2303/Nexus/internal/proxy"
	"github.com/VJ-2303/Nexus/internal/server"
)

func main() {
	cfgPath := "config.yaml"

	if len(os.Args) > 1 {
		cfgPath = os.Args[1]
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		slog.Error("failed to load config", "error", err)
		os.Exit(1)
	}

	log, err := logger.New(cfg.Logging)
	if err != nil {
		slog.Error("failed to create logger", "error", err)
		os.Exit(1)
	}

	log.Info("configuration loaded",
		"path", cfgPath,
		"listen_addr", cfg.ListenAddr,
	)

	backends := make([]*backend.Backend, len(cfg.Backends))
	for i, bcfg := range cfg.Backends {
		b, err := backend.New(bcfg.URL, bcfg.Weight)
		if err != nil {
			log.Error("failed to create backend",
				"index", i,
				"url", bcfg.URL,
				"error", err,
			)
			os.Exit(1)
		}
		backends[i] = b
		log.Info("backend initialized",
			"index", i,
			"url", bcfg.URL,
			"weight", bcfg.Weight,
		)
	}

	bal, err := balancer.New(cfg.Balancer)
	if err != nil {
		log.Error("failed to create balancer", "algorithm", cfg.Balancer, "error", err)
		os.Exit(1)
	}

	log.Info("balancer initialized", "algorithm", cfg.Balancer)

	px := proxy.New(cfg, backends, bal, log)

	handler := middleware.RequestLogger(log)(px)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	checker := health.NewChecker(backends, cfg.Health, log)
	go checker.Run(ctx)

	srv := server.New(cfg.ListenAddr, handler, log)
	if err := srv.Run(); err != nil {
		log.Error("server error", "error", err)
		os.Exit(1)
	}
}
