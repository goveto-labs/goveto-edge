package jobretention

import (
	"strings"
	"testing"
)

func TestTerminalPublishCleanupProtectsLatestSucceededJob(t *testing.T) {
	query := terminalJobCleanupSQL("publish_jobs", "PUBLISH")
	for _, fragment := range []string{
		"status IN ('SUCCEEDED','FAILED','DEAD_LETTER','CANCELLED')",
		"latest.status='SUCCEEDED'",
		"ORDER BY latest.version DESC",
		"LIMIT $2 FOR UPDATE SKIP LOCKED",
		"DELETE FROM job_executions",
	} {
		if !strings.Contains(query, fragment) {
			t.Fatalf("publish retention query is missing %q", fragment)
		}
	}
}

func TestConfigVersionCleanupUsesHybridProtection(t *testing.T) {
	for _, fragment := range []string{
		"ranked.created_at < $1",
		"ranked.position > $2",
		"ranked.version <> s.version",
		"ranked.version <> ranked.latest_published",
		"NOT EXISTS (SELECT 1 FROM publish_jobs",
		"LIMIT $3",
	} {
		if !strings.Contains(configVersionCleanupSQL, fragment) {
			t.Fatalf("configuration retention query is missing %q", fragment)
		}
	}
}
