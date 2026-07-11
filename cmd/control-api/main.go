package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"goveto-edge/internal/auth"
	"goveto-edge/internal/config"
	"goveto-edge/internal/httpapi"
	"goveto-edge/internal/node"
	"goveto-edge/internal/publisher"
	"goveto-edge/internal/purge"
	"goveto-edge/internal/storage"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("load config", "error", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	db, orm, err := storage.OpenPostgreSQL(ctx, cfg.DatabaseURL)
	if err != nil {
		slog.Error("connect database", "error", err)
		os.Exit(1)
	}
	defer db.Close()
	defer orm.Close()
	redisClient, err := storage.OpenRedis(ctx, cfg.RedisURL)
	if err != nil {
		slog.Error("connect redis", "error", err)
		os.Exit(1)
	}
	defer redisClient.Close()
	sessions := auth.NewSessionStore(redisClient, cfg.SessionCookieName, cfg.SessionTTL, cfg.SessionCookieSecure)
	credentialCipher, err := node.NewCredentialCipher(cfg.NodeCredentialMasterKey)
	if err != nil {
		slog.Error("initialize node credential encryption", "error", err)
		os.Exit(1)
	}
	publishService := publisher.New(orm, credentialCipher)
	go publishService.Run(ctx)
	purgeService := purge.New(orm, credentialCipher)
	go purgeService.Run(ctx)

	server := &http.Server{
		Addr:              cfg.HTTPAddress(),
		Handler:           httpapi.New(db, orm, redisClient, sessions, credentialCipher, publishService, purgeService),
		ReadHeaderTimeout: cfg.HTTPReadHeaderTimeout,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			slog.Error("shutdown server", "error", err)
		}
	}()

	slog.Info("control API listening", "address", server.Addr, "environment", cfg.AppEnv)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("serve control API", "error", err)
		os.Exit(1)
	}
}
