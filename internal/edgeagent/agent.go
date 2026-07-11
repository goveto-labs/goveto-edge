// Package edgeagent manages the embedded Caddy instance on an edge node.
package edgeagent

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/caddyserver/caddy/v2"
)

// Agent owns the lifecycle and configuration of the embedded Caddy instance.
type Agent struct {
	mu sync.Mutex
}

func New() *Agent {
	return &Agent{}
}

// Run waits for process shutdown. Node registration, configuration polling,
// heartbeat, and purge execution will be attached to this lifecycle later.
func (a *Agent) Run(ctx context.Context) error {
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
	return nil
}
