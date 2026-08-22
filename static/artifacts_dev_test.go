//go:build !agent_artifacts

package static

import (
	"errors"
	"testing"
)

func TestDevelopmentBuildOmitsAgentArtifacts(t *testing.T) {
	if _, err := AgentVersion(); !errors.Is(err, ErrAgentArtifactsUnavailable) {
		t.Fatalf("development AgentVersion error = %v", err)
	}
	if _, err := Agent("amd64"); !errors.Is(err, ErrAgentArtifactsUnavailable) {
		t.Fatalf("development Agent error = %v", err)
	}
}
