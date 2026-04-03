package main

import (
	"fmt"
	"log"

	"github.com/VJ-2303/Nexus/internal/config"
)

func main() {
	cfg, err := config.Load("config.yaml")
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	fmt.Printf("Loaded config:\n")
	fmt.Printf("  Listen: %s\n", cfg.ListenAddr)
	fmt.Printf("  Admin: %s\n", cfg.AdminAddr)
	fmt.Printf("  Balancer: %s\n", cfg.Balancer)
	fmt.Printf("  Backends: %d\n", len(cfg.Backends))

	for i, b := range cfg.Backends {
		fmt.Printf("    [%d] %s (weight: %d)\n", i, b.URL, b.Weight)
	}

	fmt.Printf("  Health check interval: %v\n", cfg.Health.Interval)
	fmt.Printf("  Max conns per host: %d\n", cfg.Transport.MaxConnsPerHost)
}
