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
	"strings"
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
	"goveto-edge/internal/httpsecurity"
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

	sessions := auth.NewSessionStore(redisClient, orm, cfg.SessionCookieName, cfg.SessionTTL, cfg.SessionCookieSecure)

	ipExtractor, err := httpsecurity.TrustedProxyIPExtractor(cfg.HTTPTrustedProxies)
	if err != nil {
		slog.Error("configure trusted proxies", "error", err)
		os.Exit(1)
	}

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
		if clickhouseErr = analyticsStore.ConfigureRawRetention(ctx, cfg.AnalyticsRawRetentionDays); clickhouseErr != nil {
			slog.Error("configure analytics retention", "error", clickhouseErr)
			os.Exit(1)
		}
		analyticsIngest = analytics.NewIngestWithConcurrency(
			orm, analyticsStore, cfg.AnalyticsIngestConcurrency,
		)
		if cfg.AnalyticsArchiveS3Endpoint != "" {
			archiveStore, archiveErr := analytics.NewS3ObjectStore(analytics.S3Options{
				Endpoint:     cfg.AnalyticsArchiveS3Endpoint,
				Bucket:       cfg.AnalyticsArchiveS3Bucket,
				Region:       cfg.AnalyticsArchiveS3Region,
				AccessKey:    cfg.AnalyticsArchiveS3AccessKey,
				SecretKey:    cfg.AnalyticsArchiveS3SecretKey,
				SessionToken: cfg.AnalyticsArchiveS3SessionToken,
			})
			if archiveErr != nil {
				slog.Error("configure S3 analytics archive", "error", archiveErr)
				os.Exit(1)
			}
			analyticsIngest.SetArchive(analytics.NewGzipNDJSONArchive(archiveStore, "access-logs"))
		} else if cfg.AnalyticsArchiveDir != "" {
			analyticsIngest.SetArchive(analytics.NewGzipNDJSONArchive(
				analytics.NewFileObjectStore(cfg.AnalyticsArchiveDir), "access-logs",
			))
		}
		go analytics.NewDailyRollup(analyticsStore, clickhouseschema.FS).Run(ctx)
	}

	var publishService *publisher.Service
	dnsService := dnssync.New(orm, credentialCipher)
	var consumeAgentLogs edgecontrol.LogConsumer
	if analyticsIngest != nil {
		consumeAgentLogs = analyticsIngest.Consume
	}
	onNodeStatusChange := func(callbackCtx context.Context, clusterID string) {
		callbackCtx = context.WithoutCancel(callbackCtx)
		go func() {
			if _, enqueueErr := dnsService.EnqueueNodeIPIfChanged(callbackCtx, clusterID); enqueueErr != nil {
				slog.Warn("reconcile node DNS after status change", "cluster_id", clusterID, "error", enqueueErr)
			}
			go func() {
				timer := time.NewTimer(dnssync.NodeDNSOfflineGracePeriod)
				defer timer.Stop()
				select {
				case <-ctx.Done():
					return
				case <-timer.C:
				}
				recheckCtx, cancelRecheck := context.WithTimeout(ctx, 30*time.Second)
				defer cancelRecheck()
				if _, recheckErr := dnsService.EnqueueNodeIPIfChanged(recheckCtx, clusterID); recheckErr != nil {
					slog.Warn("reconcile node DNS after offline grace period", "cluster_id", clusterID, "error", recheckErr)
				}
			}()
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
	publishService = publisher.New(orm, credentialCipher, gateway)
	gateway.ConfigureGeoIP(cfg.GeoIPDatabasePath, cfg.GeoIPDatabasePollInterval, func(callbackCtx context.Context) error {
		if publishService != nil {
			return publishService.EnqueueAll(context.WithoutCancel(callbackCtx))
		}
		return nil
	})
	go gateway.Run(ctx)
	go publishService.Run(ctx)
	certificateService := certmanager.New(orm, credentialCipher, publishService)
	go certificateService.Run(ctx)

	purgeService := purge.New(orm, gateway)
	go purgeService.Run(ctx)

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
			redisClient,
			httpsecurity.Options{
				MaxBodyBytes: cfg.HTTPMaxBodyBytes, MaxUploadBytes: cfg.HTTPMaxUploadBytes,
				MaxHeaderCount: 100, HSTS: strings.EqualFold(cfg.AppEnv, "production"),
				IPExtractor: ipExtractor,
			},
			analyticsStore,
		),
		ReadHeaderTimeout: cfg.HTTPReadHeaderTimeout,
		ReadTimeout:       cfg.HTTPReadTimeout,
		WriteTimeout:      cfg.HTTPWriteTimeout,
		IdleTimeout:       cfg.HTTPIdleTimeout,
		MaxHeaderBytes:    cfg.HTTPMaxHeaderBytes,
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
