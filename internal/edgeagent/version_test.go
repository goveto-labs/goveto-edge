package edgeagent

import "testing"

func TestCurrentAgentVersionIsAvailable(t *testing.T) {
	if version := currentAgentVersion(); version == "" {
		t.Fatal("agent version must not be empty")
	}
}
