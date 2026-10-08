package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"noters/internal/api"
	"noters/internal/config"
	"noters/internal/storage"
)

func main() {
	if err := run(); err != nil {
		slog.Error("Noters stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	slog.SetDefault(logger)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	db, err := pgxpool.NewWithConfig(ctx, cfg.Database)
	if err != nil {
		return err
	}
	defer db.Close()
	startup, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	for {
		if err = db.Ping(startup); err == nil {
			break
		}
		select {
		case <-startup.Done():
			return fmt.Errorf("database unavailable during startup: %w", err)
		case <-time.After(time.Second):
		}
	}
	store := storage.New(db)
	if err = store.Migrate(startup); err != nil {
		return fmt.Errorf("migrate database: %w", err)
	}
	server := &http.Server{
		Addr: cfg.HTTPAddr, Handler: api.New(store, logger, api.Options{
			SessionTTL: cfg.SessionTTL, SecureCookie: cfg.SecureCookie, AllowRegistration: cfg.AllowRegistration,
		}),
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second,
		WriteTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second,
		MaxHeaderBytes: 1 << 16,
	}
	done := make(chan error, 1)
	go func() { done <- server.ListenAndServe() }()
	logger.Info("Noters started", "address", cfg.HTTPAddr)
	select {
	case err = <-done:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
		logger.Info("shutting down")
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err = server.Shutdown(shutdown); err != nil {
			_ = server.Close()
			return err
		}
		if err = <-done; !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	}
	return nil
}
