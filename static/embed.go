// Package static contains artifacts embedded into the control-plane binary.
package static

import (
	"embed"
	"fmt"
)

//go:embed all:agent
var artifacts embed.FS

func AgentBinary(goarch string) ([]byte, error) {
	data, err := artifacts.ReadFile("agent/agent-linux-" + goarch)
	if err != nil {
		return nil, fmt.Errorf("embedded edge agent for %s (run script/build_agent.sh): %w", goarch, err)
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("embedded edge agent for %s is empty (run script/build_agent.sh)", goarch)
	}
	return data, nil
}
