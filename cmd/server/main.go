package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/phonevoxel/internal/config"
	"github.com/phonevoxel/internal/geo"
	"github.com/phonevoxel/internal/pipeline"
	"github.com/phonevoxel/internal/server"
	"github.com/phonevoxel/internal/voxel"
	web "github.com/phonevoxel/web"
)

func main() {
	cfg := config.Load()

	// Geo converter — origin set from first client if mode is "auto"
	var conv *geo.Converter
	if cfg.Grid.OriginMode == "manual" {
		conv = geo.NewConverter(cfg.Grid.OriginLat, cfg.Grid.OriginLon, cfg.Grid.OriginAlt)
		log.Printf("ENU origin (manual): %.6f, %.6f, %.2f",
			cfg.Grid.OriginLat, cfg.Grid.OriginLon, cfg.Grid.OriginAlt)
	} else {
		conv = geo.NewAutoConverter() // origin set on first client connection
	}

	// Voxel grid
	grid := voxel.NewGrid(voxel.Config{
		MinE: cfg.Grid.MinE, MaxE: cfg.Grid.MaxE,
		MinN: cfg.Grid.MinN, MaxN: cfg.Grid.MaxN,
		MinU: cfg.Grid.MinU, MaxU: cfg.Grid.MaxU,
		Resolution:    cfg.Grid.Resolution,
		DecayFactor:   cfg.Grid.DecayFactor,
		DecayInterval: cfg.Grid.DecayDuration(),
	})

	// Pipeline
	pl := pipeline.New(grid, conv, cfg.Pipeline)

	// Static assets
	staticFS := web.StaticFS()

	// HTTP server
	srv := server.New(cfg, pl, grid, conv, staticFS)

	// Graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
		<-sig
		log.Println("Shutting down…")
		cancel()
		os.Exit(0)
	}()

	// Start grid decay loop
	go grid.RunDecay(ctx)

	log.Printf("PhoneVoxel starting on %s", cfg.Server.Addr)
	nx, ny, nz := grid.Dims()
	log.Printf("Grid: %dx%dx%d voxels at %.1fm resolution", nx, ny, nz, cfg.Grid.Resolution)

	if err := srv.ListenAndServeTLS(ctx); err != nil {
		log.Fatalf("Server error: %v", err)
	}
}
