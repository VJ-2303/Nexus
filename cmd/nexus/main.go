package main

import (
	"context"
	"log"
	"os"

	"github.com/VJ-2303/Nexus/internal/backend"
	"github.com/VJ-2303/Nexus/internal/balancer"
	"github.com/VJ-2303/Nexus/internal/config"
	"github.com/VJ-2303/Nexus/internal/health"
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
		log.Fatal(err)
	}
	log.Printf("[nexus] Configuration loaded from %s", cfgPath)

	backends := make([]*backend.Backend, len(cfg.Backends))
	for i, bcfg := range cfg.Backends {
		b, err := backend.New(bcfg.URL, bcfg.Weight)
		if err != nil {
			log.Fatal(err)
		}
		backends[i] = b
		log.Printf("[nexus] Backend %d: %s (weight=%d)", i, bcfg.URL, bcfg.Weight)
	}

	bal, err := balancer.New(cfg.Balancer)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("[nexus] Balancer: %s", cfg.Balancer)

	px := proxy.New(cfg, backends, bal)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	checker := health.NewChecker(backends, cfg.Health)
	go checker.Run(ctx)

	log.Printf("[nexus] Starting Nexus on %s", cfg.ListenAddr)
	srv := server.New(cfg.ListenAddr, px)
	if err := srv.Run(); err != nil {
		log.Fatalf("Server error: %v", err)
	}
}
