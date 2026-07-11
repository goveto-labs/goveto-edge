// Package edgeagent manages the embedded Caddy instance on an edge node.
package edgeagent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"goveto-edge/caddy/agentlog"
)

// Agent owns the lifecycle and configuration of the embedded Caddy instance.
type Agent struct {
	identityPath string
	dataDir      string
	configs      *ConfigManager
	nodeConfigs  *NodeConfigStore
	logs         *LogQueue
}

func New() *Agent {
	dataDir := envOr("EDGE_AGENT_DATA_DIR", "/opt/goveto-edge/agent/data")
	return &Agent{
		identityPath: envOr("EDGE_AGENT_IDENTITY_FILE", "/opt/goveto-edge/agent/identity.json"),
		dataDir:      dataDir,
		configs:      NewConfigManager(filepath.Join(dataDir, "sites.json"), envOr("EDGE_AGENT_LISTEN", ":80")),
		nodeConfigs:  NewNodeConfigStore(filepath.Join(dataDir, "node.json")),
	}
}

// Run waits for process shutdown. Node registration, configuration polling,
// heartbeat, and purge execution will be attached to this lifecycle later.
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
	agentlog.SetSink(agentLogSink{queue: a.logs})
	a.configs.SetAgentHost(identity.NodeID)
	setAgentHTTPHandler(newAgentServer(identity, a.configs, a.nodeConfigs, a.logs))
	if err := a.configs.Restore(); err != nil {
		return fmt.Errorf("restore site configs: %w", err)
	}
	go collectMetrics(ctx, a.logs, a.nodeConfigs)
	<-ctx.Done()
	return a.Stop()
}

func (a *Agent) Stop() error {
	if err := a.configs.Stop(); err != nil {
		return fmt.Errorf("stop caddy: %w", err)
	}
	if a.logs != nil {
		return a.logs.Close()
	}
	return nil
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

type agentLogSink struct{ queue *LogQueue }

func (s agentLogSink) WriteCaddyLog(payload []byte) error {
	_, err := s.queue.Append(LogRecord{Type: "caddy", Payload: payload})
	return err
}
