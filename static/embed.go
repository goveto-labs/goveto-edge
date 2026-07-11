// Package static contains artifacts embedded into the control-plane binary.
package static

import (
	"embed"
	"fmt"
)

//go:embed agent/agent-linux-amd64 agent/agent-linux-arm64
var artifacts embed.FS

func AgentBinary(goarch string) ([]byte, error) {
	data, err := artifacts.ReadFile("agent/agent-linux-" + goarch)
	if err != nil {
		return nil, fmt.Errorf("embedded edge agent for %s: %w", goarch, err)
	}
	return data, nil
}
