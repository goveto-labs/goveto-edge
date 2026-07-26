package main

import (
	"context"
	"errors"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	clickhouseschema "goveto-edge/configs/clickhouse"
	"goveto-edge/internal/analytics"
	"goveto-edge/internal/auth"
	"goveto-edge/internal/certmanager"
	"goveto-edge/internal/config"
	"goveto-edge/internal/dnssync"
	"goveto-edge/internal/edgecontrol"
	"goveto-edge/internal/edgeprotocol"
	"goveto-edge/internal/httpapi"
	"goveto-edge/internal/node"
	"goveto-edge/internal/publisher"
	"goveto-edge/internal/purge"
	"goveto-edge/internal/storage"
	"goveto-edge/schema"
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

	schemaCtx, cancelSchema := context.WithTimeout(ctx, 5*time.Minute)
	schemaResult, err := storage.InitSchema(schemaCtx, db, schema.FS, cfg.DatabaseURL)
	cancelSchema()
	if err != nil {
		slog.Error("initialize database schema", "error", err)
		os.Exit(1)
	}
	if schemaResult.Noop {
		slog.Info("database schema is up to date", "models", schemaResult.ModelCount, "hash", schemaResult.SchemaHash)
	} else {
		slog.Info("database schema updated", "models", schemaResult.ModelCount, "changes", schemaResult.ChangeCount, "hash", schemaResult.SchemaHash)
	}

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
	authority, err := edgecontrol.NewAuthority(credentialCipher, cfg.AgentGatewayPublicAddress)
	if err != nil {
		slog.Error("initialize agent certificate authority", "error", err)
		os.Exit(1)
	}

	var analyticsStore *analytics.Store
	var analyticsIngest *analytics.Ingest
	if cfg.ClickHouseDSN != "" {
		clickhouseConn, clickhouseErr := storage.OpenClickHouse(ctx, cfg.ClickHouseDSN)
		if clickhouseErr != nil {
			slog.Error("connect ClickHouse", "error", clickhouseErr)
			os.Exit(1)
		}
		defer clickhouseConn.Close()

		clickhouseSchemaCtx, cancelClickHouseSchema := context.WithTimeout(ctx, 5*time.Minute)
		statementCount, clickhouseErr := storage.InitClickHouseSchema(clickhouseSchemaCtx, clickhouseConn, clickhouseschema.FS)
		cancelClickHouseSchema()
		if clickhouseErr != nil {
			slog.Error("initialize ClickHouse schema", "error", clickhouseErr)
			os.Exit(1)
		}
		slog.Info("ClickHouse schema is up to date", "statements", statementCount)

		analyticsStore = analytics.NewStore(clickhouseConn)
		analyticsIngest = analytics.NewIngest(orm, credentialCipher, analyticsStore)
		go analytics.NewDailyRollup(analyticsStore, clickhouseschema.FS).Run(ctx)
	}

	var publishService *publisher.Service
	var dnsService *dnssync.Service
	var consumeAgentLogs edgecontrol.LogConsumer
	if analyticsIngest != nil {
		consumeAgentLogs = analyticsIngest.Consume
	}
	onNodeStatusChange := func(callbackCtx context.Context, clusterID string) {
		callbackCtx = context.WithoutCancel(callbackCtx)
		go func() {
			if dnsService != nil {
				_, _ = dnsService.EnqueueNodeIPIfChanged(callbackCtx, clusterID)
			}
			if publishService != nil {
				if err := publishService.EnqueueCluster(callbackCtx, clusterID); err != nil {
					slog.Warn("republish cluster sites after node status change", "cluster_id", clusterID, "error", err)
				}
			}
		}()
	}
	gateway := edgecontrol.NewGateway(
		db,
		orm,
		authority,
		consumeAgentLogs,
		onNodeStatusChange,
	)
	go gateway.Run(ctx)

	publishService = publisher.New(orm, credentialCipher, gateway)
	go publishService.Run(ctx)
	certificateService := certmanager.New(orm, credentialCipher, publishService)
	go certificateService.Run(ctx)

	purgeService := purge.New(orm, gateway)
	go purgeService.Run(ctx)

	dnsService = dnssync.New(orm, credentialCipher)
	go dnsService.Run(ctx)

	installQueue := node.NewInstallQueue(orm)
	go node.NewInstallWorker(orm, installQueue, credentialCipher).Run(ctx)
	go node.NewLifecycle(orm, 45*time.Second, onNodeStatusChange).Run(ctx)

	agentListener, err := net.Listen("tcp", cfg.AgentGatewayAddress())
	if err != nil {
		slog.Error("listen for edge agents", "error", err)
		os.Exit(1)
	}
	agentServer := grpc.NewServer(
		grpc.Creds(credentials.NewTLS(authority.ServerTLSConfig())),
		grpc.ForceServerCodec(edgeprotocol.JSONCodec{}),
		grpc.MaxRecvMsgSize(32<<20),
		grpc.MaxSendMsgSize(32<<20),
	)
	edgeprotocol.RegisterManagementServer(agentServer, gateway)
	go func() {
		<-ctx.Done()
		stopped := make(chan struct{})
		go func() {
			agentServer.GracefulStop()
			close(stopped)
		}()
		timer := time.NewTimer(cfg.ShutdownTimeout)
		defer timer.Stop()
		select {
		case <-stopped:
		case <-timer.C:
			agentServer.Stop()
		}
	}()
	go func() {
		slog.Info("agent mTLS gateway listening", "address", cfg.AgentGatewayAddress(), "public_address", cfg.AgentGatewayPublicAddress)
		if serveErr := agentServer.Serve(agentListener); serveErr != nil && ctx.Err() == nil {
			slog.Error("serve agent mTLS gateway", "error", serveErr)
			stop()
		}
	}()

	server := &http.Server{
		Addr: cfg.HTTPAddress(),
		Handler: httpapi.New(
			db,
			orm,
			sessions,
			credentialCipher,
			authority,
			gateway,
			installQueue,
			publishService,
			certificateService,
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
