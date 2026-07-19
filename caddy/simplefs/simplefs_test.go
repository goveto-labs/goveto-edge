package simplefs

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/darkweak/storages/core"
	"go.uber.org/zap"
)

func TestWithinAutoLimitUsesWholeFilesystemTarget(t *testing.T) {
	tests := []struct {
		name     string
		total    uint64
		used     uint64
		incoming uint64
		percent  int
		want     bool
	}{
		{name: "at target", total: 100, used: 79, incoming: 1, percent: 80, want: true},
		{name: "write crosses target", total: 100, used: 79, incoming: 2, percent: 80},
		{name: "already over target", total: 100, used: 81, percent: 80},
		{name: "invalid percent defaults to 80", total: 100, used: 80, percent: 0, want: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := withinAutoLimit(test.total, test.used, test.incoming, test.percent); got != test.want {
				t.Fatalf("withinAutoLimit() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestFixedLimitEvictsOldestOrphan(t *testing.T) {
	directory := t.TempDir()
	old := filepath.Join(directory, "old-cache-entry")
	if err := os.WriteFile(old, []byte("12345678"), 0o600); err != nil {
		t.Fatal(err)
	}
	provider, err := newProvider(core.CacheProvider{
		Path: directory,
		Configuration: map[string]any{
			"auto_max_size":  false,
			"max_size_bytes": 10,
		},
	}, zap.NewNop().Sugar(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := provider.ensureSpace(5, provider.limits); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatalf("old cache entry was not evicted: %v", err)
	}
	if err := provider.ensureSpace(11, provider.limits); err == nil {
		t.Fatal("oversized cache entry should be rejected")
	}
}
