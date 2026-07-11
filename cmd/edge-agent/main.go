package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	_ "github.com/caddyserver/caddy/v2/modules/standard"
	_ "github.com/darkweak/souin/plugins/caddy"

	_ "goveto-edge/caddy/cacheheaders"
	_ "goveto-edge/caddy/cachematch"
	"goveto-edge/internal/edgeagent"
)

func main() {
	identityPath := os.Getenv("EDGE_AGENT_IDENTITY_FILE")
	if identityPath == "" {
		identityPath = "/opt/goveto-edge/agent/identity.json"
	}
	if nodeID, key := os.Getenv("EDGE_NODE_ID"), os.Getenv("EDGE_COMMUNICATION_KEY"); nodeID != "" && key != "" {
		if err := edgeagent.WriteIdentity(identityPath, edgeagent.Identity{NodeID: nodeID, CommunicationKey: key}); err != nil {
			slog.Error("write agent identity", "error", err)
			os.Exit(1)
		}
		_ = os.Unsetenv("EDGE_COMMUNICATION_KEY")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	agent := edgeagent.New()
	if err := agent.Run(ctx); err != nil {
		slog.Error("run edge agent", "error", err)
		os.Exit(1)
	}
}
