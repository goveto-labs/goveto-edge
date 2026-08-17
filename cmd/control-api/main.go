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
	"strconv"
	"strings"
	"syscall"
	"time"

	analyticsschema "goveto-edge/configs/analytics"
	"goveto-edge/internal/analytics"
	"goveto-edge/internal/auth"
	"goveto-edge/internal/certmanager"
	"goveto-edge/internal/config"
	"goveto-edge/internal/dnssync"
	"goveto-edge/internal/edgecontrol"
	"goveto-edge/internal/edgeprotocol"
	"goveto-edge/internal/httpapi"
	clusterapi "goveto-edge/internal/httpapi/clusters"
	"goveto-edge/internal/httpsecurity"
	"goveto-edge/internal/jobretention"
	"goveto-edge/internal/logpush"
	"goveto-edge/internal/node"
	"goveto-edge/internal/outboundhttp"
	"goveto-edge/internal/publisher"
	"goveto-edge/internal/purge"
	"goveto-edge/internal/settings"
	"goveto-edge/internal/storage"
	"goveto-edge/internal/telemetry"
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
	if cfg.MetricsEnabled {
		telemetry.RegisterDBStats("control", db)
	}

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

	settingStore := settings.New(orm, nil)
	instanceInitialized, err := settingStore.Initialized(ctx)
	if err != nil {
		slog.Error("read instance initialization status", "error", err)
		os.Exit(1)
	}
	agentGatewayPublicAddress, addressConfigured, err := settingStore.AgentGatewayPublicAddress(ctx)
	if err != nil {
		slog.Error("read agent gateway public address", "error", err)
		os.Exit(1)
	}
	if !addressConfigured {
		if instanceInitialized {
			slog.Error("agent gateway public address is not configured; complete instance initialization")
			os.Exit(1)
		}
		agentGatewayPublicAddress = net.JoinHostPort("127.0.0.1", strconv.Itoa(cfg.AgentGatewayPort))
		slog.Warn("using temporary agent gateway address until instance initialization", "public_address", agentGatewayPublicAddress)
	}
	httpProxyConfig, proxyConfigured, err := settingStore.HTTPProxy(ctx)
	if err != nil {
		slog.Error("read HTTP proxy settings", "error", err)
		os.Exit(1)
	}
	if !proxyConfigured {
		httpProxyConfig = settings.HTTPProxyConfig{
			ClientIPHeaders: append([]string(nil), settings.DefaultClientIPHeaders...),
		}
		if err = settingStore.SetHTTPProxy(ctx, httpProxyConfig); err != nil {
			slog.Error("initialize HTTP proxy settings", "error", err)
			os.Exit(1)
		}
	}

	redisClient, err := storage.OpenRedis(ctx, cfg.RedisURL)
	if err != nil {
		slog.Error("connect redis", "error", err)
		os.Exit(1)
	}
	defer redisClient.Close()

	sessions := auth.NewSessionStore(redisClient, orm, cfg.SessionCookieName, cfg.SessionTTL, cfg.SessionCookieSecure)

	ipExtractor, err := httpsecurity.ProxyIPExtractor(httpProxyConfig)
	if err != nil {
		slog.Error("configure client IP forwarding headers", "error", err)
		os.Exit(1)
	}

	credentialCipher, err := node.NewCredentialCipherKeyring(cfg.NodeCredentialMasterKey, cfg.NodeCredentialPreviousKeys...)
	if err != nil {
		slog.Error("initialize node credential encryption", "error", err)
		os.Exit(1)
	}
	certificatePreviousKeys := append(append([]string(nil), cfg.CertificatePreviousKeys...), cfg.NodeCredentialMasterKey)
	certificatePreviousKeys = append(certificatePreviousKeys, cfg.NodeCredentialPreviousKeys...)
	certificateCipher, err := node.NewCredentialCipherKeyring(cfg.CertificateMasterKey, certificatePreviousKeys...)
	if err != nil {
		slog.Error("initialize certificate encryption", "error", err)
		os.Exit(1)
	}
	dnsPreviousKeys := append(append([]string(nil), cfg.DNSCredentialPreviousKeys...), cfg.NodeCredentialMasterKey)
	dnsPreviousKeys = append(dnsPreviousKeys, cfg.NodeCredentialPreviousKeys...)
	dnsCipher, err := node.NewCredentialCipherKeyring(cfg.DNSCredentialMasterKey, dnsPreviousKeys...)
	if err != nil {
		slog.Error("initialize DNS credential encryption", "error", err)
		os.Exit(1)
	}
	notificationPreviousKeys := append(append([]string(nil), cfg.NotificationPreviousKeys...), cfg.NodeCredentialMasterKey)
	notificationPreviousKeys = append(notificationPreviousKeys, cfg.NodeCredentialPreviousKeys...)
	notificationCipher, err := node.NewCredentialCipherKeyring(cfg.NotificationMasterKey, notificationPreviousKeys...)
	if err != nil {
		slog.Error("initialize notification credential encryption", "error", err)
		os.Exit(1)
	}
	totpCipher, err := node.NewCredentialCipherKeyring(cfg.TOTPMasterKey, cfg.TOTPPreviousKeys...)
	if err != nil {
		slog.Error("initialize TOTP encryption", "error", err)
		os.Exit(1)
	}
	authority, err := edgecontrol.NewAuthorityWithCAKey(cfg.AgentCAMasterKey, agentGatewayPublicAddress)
	if err != nil {
		slog.Error("initialize agent certificate authority", "error", err)
		os.Exit(1)
	}
	if err = edgecontrol.PinAgentCA(cfg.DataDir, authority, cfg.AgentCAMasterKeyPinned); err != nil {
		slog.Error("agent certificate authority identity check failed", "error", err)
		os.Exit(1)
	}

	analyticsPool, err := storage.OpenAnalyticsPostgreSQL(ctx, cfg.AnalyticsDatabaseURL, int32(cfg.AnalyticsDBMaxConns))
	if err != nil {
		slog.Error("connect analytics database", "error", err)
		os.Exit(1)
	}
	defer analyticsPool.Close()
	if cfg.MetricsEnabled {
		telemetry.RegisterPGXPoolStats("analytics", analyticsPool)
	}
	analyticsSchemaCtx, cancelAnalyticsSchema := context.WithTimeout(ctx, 5*time.Minute)
	migrationCount, err := storage.InitAnalyticsSchema(analyticsSchemaCtx, analyticsPool, analyticsschema.FS)
	cancelAnalyticsSchema()
	if err != nil {
		slog.Error("initialize analytics schema", "error", err)
		os.Exit(1)
	}
	slog.Info("analytics schema is up to date", "migrations_applied", migrationCount)
	analyticsStore := analytics.NewStore(analyticsPool, cfg.AnalyticsQueryTimeout)
	if err = analyticsStore.ConfigureRawRetention(ctx, cfg.AnalyticsRawRetentionDays); err != nil {
		slog.Error("configure analytics retention", "error", err)
		os.Exit(1)
	}
	analyticsIngest := analytics.NewIngestWithConcurrency(orm, analyticsStore, cfg.AnalyticsIngestConcurrency)
	analyticsIngest.ConfigureGeoIP(cfg.GeoIPDatabasePath, cfg.GeoIPASNDatabasePath)
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

	if cfg.LogpushEnabled {
		logpushDispatcher := logpush.NewDispatcher(orm, notificationCipher, cfg.LogpushQueueSize, cfg.LogpushBatchLinger)
		analyticsIngest.SetLogpush(logpushDispatcher)
		clusterapi.ConfigureLogpushDispatcher(logpushDispatcher)
		defer logpushDispatcher.Close()
	}

	var publishService *publisher.Service
	dnsService := dnssync.New(orm, dnsCipher, dnssync.Options{
		Scheduler: dnssync.SchedulerPolicy{
			MinHealthyTime:  cfg.DNSMinHealthyTime,
			MaxRemovalRatio: cfg.DNSMaxRemovalRatio,
			MinPublished:    cfg.DNSMinPublishedNodes,
		},
	})
	var consumeAgentLogs edgecontrol.LogConsumer = analyticsIngest.Consume
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
	publishService = publisher.NewWithCiphers(orm, certificateCipher, credentialCipher, gateway)
	gateway.ConfigureGeoIP(cfg.GeoIPDatabasePath, cfg.GeoIPDatabasePollInterval, func(callbackCtx context.Context) error {
		if publishService != nil {
			return publishService.EnqueueAll(context.WithoutCancel(callbackCtx))
		}
		return nil
	})
	certificateService := certmanager.New(orm, certificateCipher, publishService)
	rewrapSkip, _ := strconv.ParseBool(strings.TrimSpace(os.Getenv("NODE_REWRAP_SKIP")))
	rewrapCtx, cancelRewrap := context.WithTimeout(ctx, 2*time.Minute)
	if rewrapSkip {
		slog.Warn("skipping startup secret rewrap because NODE_REWRAP_SKIP is set; secrets encrypted with unavailable keys will fail when accessed")
		err = nil
	} else {
		if err = node.RewrapStoredSecrets(rewrapCtx, orm, credentialCipher); err == nil {
			err = settingStore.RewrapAuthProviderSecrets(rewrapCtx, credentialCipher)
		}
		if err == nil {
			err = settingStore.RewrapCaptchaSecret(rewrapCtx, credentialCipher)
		}
		if err == nil {
			err = dnsService.RewrapSecrets(rewrapCtx)
		}
		if err == nil {
			err = clusterapi.RewrapNotificationSecrets(rewrapCtx, orm, notificationCipher)
		}
		if err == nil {
			err = clusterapi.RewrapLogpushSecrets(rewrapCtx, orm, notificationCipher)
		}
		if err == nil {
			err = certificateService.RewrapSecrets(rewrapCtx)
		}
		if err == nil {
			var totpRewrap auth.TOTPRewrapResult
			totpRewrap, err = auth.RewrapTOTPSecrets(rewrapCtx, orm, totpCipher)
			for _, failure := range totpRewrap.Skipped {
				slog.Warn("skip unavailable TOTP secret during startup rewrap", "user_id", failure.UserID, "error", failure.Err)
			}
		}
	}
	cancelRewrap()
	if err != nil {
		slog.Error("rewrap encrypted secrets", "error", err)
		os.Exit(1)
	}
	go gateway.Run(ctx)
	go publishService.Run(ctx)
	go certificateService.Run(ctx)

	purgeService := purge.New(orm, gateway)
	go purgeService.Run(ctx)

	go dnsService.Run(ctx)
	go jobretention.New(orm, settingStore).Run(ctx)

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
		slog.Info("agent mTLS gateway listening", "address", cfg.AgentGatewayAddress(), "public_address", agentGatewayPublicAddress)
		if serveErr := agentServer.Serve(agentListener); serveErr != nil && ctx.Err() == nil {
			slog.Error("serve agent mTLS gateway", "error", serveErr)
			stop()
		}
	}()

	clusterapi.ConfigureNotificationOutbound(outboundhttp.NewPolicyWithAllowlist(cfg.OutboundPrivateAllowlist))
	slog.Info("notification destination allowlist configured", "cidrs", cfg.OutboundPrivateAllowlist,
		"note", "loopback/link-local (cloud metadata) always blocked")

	server := &http.Server{
		Addr: cfg.HTTPAddress(),
		Handler: httpapi.New(
			db,
			orm,
			sessions,
			httpapi.SecretCiphers{General: credentialCipher, DNS: dnsCipher, Notification: notificationCipher, TOTP: totpCipher},
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
			func() {
				slog.Info("control plane restart requested after admin settings update")
				stop()
			},
			cfg.MetricsEnabled,
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
