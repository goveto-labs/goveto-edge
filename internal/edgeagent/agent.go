// Package edgeagent manages the embedded Caddy instance on an edge node.
package edgeagent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/caddyserver/caddy/v2"
	"goveto-edge/caddy/agentlog"
)

// Agent owns the lifecycle and configuration of the embedded Caddy instance.
type Agent struct {
	mu           sync.Mutex
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
		nodeConfigs:  NewNodeConfigStore(),
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
	a.logs, err = OpenLogQueue(filepath.Join(a.dataDir, "logs.db"))
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

// ApplyConfig atomically loads a Caddy JSON configuration received from the
// control plane. Caddy keeps the previous working configuration if loading the
// new configuration fails.
func (a *Agent) ApplyConfig(configJSON []byte) error {
	if len(configJSON) == 0 {
		return errors.New("caddy config is empty")
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	if err := caddy.Load(configJSON, true); err != nil {
		return fmt.Errorf("load caddy config: %w", err)
	}
	return nil
}

func (a *Agent) Stop() error {
	a.mu.Lock()
	defer a.mu.Unlock()

	if err := caddy.Stop(); err != nil {
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

type agentLogSink struct{ queue *LogQueue }

func (s agentLogSink) WriteCaddyLog(payload []byte) error {
	_, err := s.queue.Append(LogRecord{Type: "caddy", Payload: payload})
	return err
}
