package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	_ "github.com/caddyserver/caddy/v2/modules/standard"
	_ "github.com/darkweak/souin/plugins/caddy"

	"goveto-edge/internal/edgeagent"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	agent := edgeagent.New()
	if err := agent.Run(ctx); err != nil {
		slog.Error("run edge agent", "error", err)
		os.Exit(1)
	}
}
