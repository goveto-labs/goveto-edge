package edgeagent

import "testing"

func TestApplyConfigRejectsEmptyConfig(t *testing.T) {
	agent := New()
	if err := agent.ApplyConfig(nil); err == nil {
		t.Fatal("expected empty configuration to be rejected")
	}
}
