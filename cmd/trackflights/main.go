package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
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
	go server.RunNostr(ctx, logger)
	go func() {
		logger.Info("TrackFlights server started", "address", cfg.Server.ListenAddress, "dataRoot", dataRoot)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("HTTP server failed", "error", err)
			stop()
		}
	}()
	if sdrURL, callback := os.Getenv("TRACKFLIGHTS_SDR_URL"), os.Getenv("TRACKFLIGHTS_CALLBACK_URL"); sdrURL != "" && callback != "" {
		go subscribeSDR(ctx, server, sdrURL, callback, logger)
	} else if sdrURL != "" || callback != "" {
		logger.Error("both TRACKFLIGHTS_SDR_URL and TRACKFLIGHTS_CALLBACK_URL are required")
	}
	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	manager.CancelAll()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		logger.Error("graceful shutdown failed", "error", err)
	}
}

// subscribeSDR retries after loss of the receiver's callback heartbeat.
func subscribeSDR(ctx context.Context, server *api.Server, base, callback string, logger *slog.Logger) {
	client := &http.Client{Timeout: 5 * time.Second}
	defer client.CloseIdleConnections()
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	var lastAttempt time.Time
	for {
		if time.Since(server.SDRLastContact()) > 15*time.Second && time.Since(lastAttempt) > 10*time.Second {
			lastAttempt = time.Now()
			body, _ := json.Marshal(map[string]string{"url": strings.TrimRight(callback, "/") + "/api/v1/sdr/frames"})
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(base, "/")+"/api/v1/subscription", bytes.NewReader(body))
			if err == nil {
				req.Header.Set("Content-Type", "application/json")
				resp, e := client.Do(req)
				err = e
				if e == nil {
					io.Copy(io.Discard, resp.Body)
					resp.Body.Close()
					if resp.StatusCode/100 != 2 {
						logger.Warn("SDR subscription rejected", "status", resp.Status)
					}
				}
			}
			if err != nil && ctx.Err() == nil {
				logger.Warn("SDR subscription failed", "error", err)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
