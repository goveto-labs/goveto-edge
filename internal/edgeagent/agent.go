// Package edgeagent manages the embedded Caddy instance on an edge node.
package edgeagent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"goveto-edge/caddy/agentlog"
)

// Agent owns the lifecycle and configuration of the embedded Caddy instance.
type Agent struct {
	identityPath string
	dataDir      string
	configs      *ConfigManager
	nodeConfigs  *NodeConfigStore
	logs         *LogQueue
	geoIP        *GeoIPStore
	stopOnce     sync.Once
	stopErr      error
}

func New() *Agent {
	dataDir := envOr("EDGE_AGENT_DATA_DIR", "/opt/goveto-edge/agent/data")
	agent := &Agent{
		identityPath: envOr("EDGE_AGENT_IDENTITY_FILE", "/opt/goveto-edge/agent/identity.json"),
		dataDir:      dataDir,
		configs:      NewConfigManager(filepath.Join(dataDir, "sites.json"), envOr("EDGE_USER_LISTEN", ":80")),
		nodeConfigs:  NewNodeConfigStore(filepath.Join(dataDir, "node.json")),
	}
	agent.geoIP = NewGeoIPStore(dataDir, agent.configs)
	return agent
}

// Run waits for process shutdown.
func (a *Agent) Run(ctx context.Context) error {
	identity, err := LoadIdentity(a.identityPath)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(a.dataDir, 0700); err != nil {
		return err
	}
	a.logs, err = OpenLogQueue(filepath.Join(a.dataDir, "logs.db"), envUint64("EDGE_AGENT_LOG_MAX_BYTES", 2<<30))
	if err != nil {
		return fmt.Errorf("open log queue: %w", err)
	}
	if err := a.logs.StartAccessPipeline(logPolicyFromEnv(), accessLogConfigFromEnv()); err != nil {
		_ = a.logs.Close()
		return fmt.Errorf("start access log pipeline: %w", err)
	}
	agentlog.SetSink(agentLogSink{queue: a.logs})
	if err := a.configs.SetNodeConfig(a.nodeConfigs.Get()); err != nil {
		return errors.Join(fmt.Errorf("apply node cache config: %w", err), a.Stop())
	}
	if err := a.configs.Restore(); err != nil && !errors.Is(err, ErrGeoIPUnavailable) {
		return errors.Join(fmt.Errorf("restore site configs: %w", err), a.Stop())
	}
	if benchmarkListen := os.Getenv("EDGE_AGENT_BENCHMARK_LISTEN"); benchmarkListen != "" {
		go serveBenchmarkMetrics(ctx, benchmarkListen, a.logs, a.nodeConfigs)
	}
	go collectMetrics(ctx, a.logs, a.nodeConfigs)
	channelErrors := make(chan error, 1)
	go func() {
		channelErrors <- (&channelClient{
			identityPath: a.identityPath, identity: identity, configs: a.configs,
			nodeConfigs: a.nodeConfigs, logs: a.logs, geoIP: a.geoIP,
		}).Run(ctx)
	}()
	select {
	case <-ctx.Done():
		return a.Stop()
	case err := <-channelErrors:
		if stopErr := a.Stop(); stopErr != nil {
			return errors.Join(err, stopErr)
		}
		return err
	}
}

func (a *Agent) Stop() error {
	a.stopOnce.Do(func() {
		a.stopErr = a.stop()
	})
	return a.stopErr
}

func (a *Agent) stop() error {
	var result error
	if err := a.configs.Stop(); err != nil {
		result = errors.Join(result, fmt.Errorf("stop caddy: %w", err))
	}
	agentlog.SetSink(nil)
	if a.logs != nil {
		flushContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := a.logs.ShutdownAccess(flushContext); err != nil {
			result = errors.Join(result, err)
		}
		cancel()
		if err := a.logs.Close(); err != nil {
			result = errors.Join(result, fmt.Errorf("close log queue: %w", err))
		}
	}
	return result
}

func (a *Agent) AppendLog(record LogRecord) (uint64, error) {
	if a.logs == nil {
		return 0, errors.New("log queue is not open")
	}
	return a.logs.Append(record)
}
func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func envUint64(key string, fallback uint64) uint64 {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseUint(value, 10, 64)
	if err != nil || parsed == 0 {
		return fallback
	}
	return parsed
}

func envBool(key string, fallback bool) bool {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func envInt(key string, fallback int) int {
	value := os.Getenv(key)
	parsed, err := strconv.Atoi(value)
	if value == "" || err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}

func envDuration(key string, fallback time.Duration) time.Duration {
	value := os.Getenv(key)
	parsed, err := time.ParseDuration(value)
	if value == "" || err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}

func accessLogConfigFromEnv() AccessLogConfig {
	return AccessLogConfig{
		BufferBytes:   envUint64("EDGE_AGENT_LOG_BUFFER_BYTES", defaultAccessLogBufferBytes),
		BufferRecords: envInt("EDGE_AGENT_LOG_BUFFER_RECORDS", defaultAccessLogBufferRecords),
		BatchBytes:    envUint64("EDGE_AGENT_LOG_BATCH_BYTES", defaultAccessLogBatchBytes),
		BatchRecords:  envInt("EDGE_AGENT_LOG_BATCH_RECORDS", defaultAccessLogBatchRecords),
		FlushInterval: envDuration("EDGE_AGENT_LOG_FLUSH_INTERVAL", defaultAccessLogFlushInterval),
	}
}

type agentLogSink struct {
	queue *LogQueue
}

func (s agentLogSink) WriteCaddyLog(siteID string, configVersion uint64, receivedAt time.Time, payload []byte) error {
	record := LogRecord{Type: "caddy", SiteID: siteID, ConfigVersion: configVersion, CreatedAt: receivedAt, Payload: payload}
	if siteID != "" {
		record.Type = "access"
		s.queue.EnqueueAccess(record)
		return nil
	}
	_, err := s.queue.Append(record)
	return err
}
