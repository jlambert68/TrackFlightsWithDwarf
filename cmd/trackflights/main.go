package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/jlambert/trackflights/internal/api"
	"github.com/jlambert/trackflights/internal/config"
	"github.com/jlambert/trackflights/internal/flight"
	"github.com/jlambert/trackflights/internal/jobs"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	dataRoot := os.Getenv("TRACKFLIGHTS_DATA_ROOT")
	if dataRoot == "" {
		dataRoot = "data"
	}
	dataRoot, err := filepath.Abs(dataRoot)
	if err != nil {
		logger.Error("resolve data root", "error", err)
		os.Exit(1)
	}

	store := config.NewStore(filepath.Join(dataRoot, "config", "settings.json"))
	cfg, err := store.Load()
	if err != nil {
		logger.Error("load configuration", "error", err)
		os.Exit(1)
	}
	if !filepath.IsAbs(cfg.Flight.TrackingDir) {
		cfg.Flight.TrackingDir = filepath.Join(dataRoot, cfg.Flight.TrackingDir)
	}
	manager := jobs.NewManager()
	flights := flight.NewService(flight.Receiver{Name: cfg.Flight.HomeName, Latitude: cfg.Flight.Latitude, Longitude: cfg.Flight.Longitude})
	server := api.NewServer(logger, cfg, store, manager, flights, dataRoot)

	httpServer := &http.Server{
		Addr:              cfg.Server.ListenAddress,
		Handler:           server.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		logger.Info("TrackFlights server started", "address", cfg.Server.ListenAddress, "dataRoot", dataRoot)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("HTTP server failed", "error", err)
			stop()
		}
	}()
	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	manager.CancelAll()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		logger.Error("graceful shutdown failed", "error", err)
	}
}
