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

	"goveto-edge/internal/analytics"
	"goveto-edge/internal/auth"
	"goveto-edge/internal/config"
	"goveto-edge/internal/dnssync"
	"goveto-edge/internal/httpapi"
	"goveto-edge/internal/node"
	"goveto-edge/internal/publisher"
	"goveto-edge/internal/purge"
	"goveto-edge/internal/storage"
	"goveto-edge/internal/storage/gen/model"
)

// @title Goveto Edge Control API
// @version 0.1.0
// @description Control-plane API for managing edge clusters, nodes, sites, certificates, publish, purge and analytics.
// @BasePath /
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

	var analyticsStore *analytics.Store
	if cfg.ClickHouseDSN != "" {
		clickhouseConn, clickhouseErr := storage.OpenClickHouse(ctx, cfg.ClickHouseDSN)
		if clickhouseErr != nil {
			slog.Error("connect ClickHouse", "error", clickhouseErr)
			os.Exit(1)
		}
		defer clickhouseConn.Close()

		analyticsStore = analytics.NewStore(clickhouseConn)
		go analytics.NewIngest(orm, credentialCipher, analyticsStore).Run(ctx)
	}

	publishService := publisher.New(orm, credentialCipher)
	go publishService.Run(ctx)

	purgeService := purge.New(orm, credentialCipher)
	go purgeService.Run(ctx)

	dnsService := dnssync.New(orm, credentialCipher)
	go dnsService.Run(ctx)

	installQueue := node.NewInstallQueue(redisClient, 0)
	go node.NewInstallWorker(orm, installQueue).Run(ctx)
	go node.NewLifecycle(
		orm,
		credentialCipher,
		45*time.Second,
		func(callbackCtx context.Context, clusterID string) {
			go func() {
				_, _ = dnsService.EnqueueIfConfigured(
					callbackCtx,
					clusterID,
					nil,
					model.DNSSyncActionRECONCILE,
				)
			}()
		},
	).Run(ctx)

	server := &http.Server{
		Addr: cfg.HTTPAddress(),
		Handler: httpapi.New(
			db,
			orm,
			sessions,
			credentialCipher,
			installQueue,
			publishService,
			purgeService,
			dnsService,
			analyticsStore,
		),
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
