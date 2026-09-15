package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/nerve-labs/nerve/internal/config"
	"github.com/nerve-labs/nerve/internal/httpapi"
	"github.com/nerve-labs/nerve/internal/platform/database"
)

var version = "dev"

func main() {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("invalid configuration", "error", err)
		os.Exit(1)
	}
	if len(os.Args) == 2 && os.Args[1] == "traffic-check" {
		os.Exit(checkTraffic(cfg))
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	slog.SetDefault(logger)

	startupContext, startupCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer startupCancel()
	pool, err := database.Open(startupContext, cfg.DatabaseURL)
	if err != nil {
		logger.Error("database startup failed", "error", err)
		os.Exit(1)
	}
	defer pool.Close()
	if err := database.Migrate(startupContext, pool, cfg.MigrationsDir); err != nil {
		logger.Error("database migration failed", "error", err)
		os.Exit(1)
	}

	api := httpapi.New(pool, cfg, logger, version)
	sensorServer := &http.Server{Addr: cfg.SensorAddr, Handler: api.SensorHandler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second}
	if cfg.SensorAddr != "" {
		go func() {
			if err := sensorServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				logger.Error("sensor listener unavailable")
			}
		}()
	}
	server := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           api.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      0, // SSE responses remain open; handlers enforce their own bounded work.
		IdleTimeout:       60 * time.Second,
	}

	shutdownContext, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-shutdownContext.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = sensorServer.Shutdown(ctx)
		if err := server.Shutdown(ctx); err != nil {
			logger.Error("http shutdown failed", "error", err)
		}
	}()

	logger.Info("NERVE core started", "address", cfg.HTTPAddr, "version", version)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("http server failed", "error", err)
		os.Exit(1)
	}
}
