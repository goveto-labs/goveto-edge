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
	_ "goveto-edge/caddy/cachepurge"
	_ "goveto-edge/caddy/waf"
	"goveto-edge/internal/edgeagent"
)

func main() {
	if handled, err := edgeagent.RunHardwareBenchmarkCommand(os.Args[1:], os.Stdout); handled {
		if err != nil {
			slog.Error("benchmark node hardware", "error", err)
			os.Exit(1)
		}
		return
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	agent := edgeagent.New()
	if err := agent.Run(ctx); err != nil {
		slog.Error("run edge agent", "error", err)
		os.Exit(1)
	}
}
