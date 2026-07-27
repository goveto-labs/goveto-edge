package nodes

import (
	"testing"

	"goveto-edge/internal/edgeprotocol"
)

func TestValidateCacheConfigInputDiskUsageBoundary(t *testing.T) {
	for _, test := range []struct {
		name    string
		percent int
		wantErr bool
	}{
		{name: "minimum", percent: 1},
		{name: "maximum", percent: 90},
		{name: "below minimum", percent: 0, wantErr: true},
		{name: "above hard maximum", percent: 91, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := edgeprotocol.NodeCacheConfig{
				CacheDirectory:      "  /var/cache/goveto-edge  ",
				MaxDiskUsagePercent: test.percent,
			}
			err := validateCacheConfigInput(&input)
			if (err != nil) != test.wantErr {
				t.Fatalf("validateCacheConfigInput() error = %v, wantErr %v", err, test.wantErr)
			}
			if !test.wantErr && input.CacheDirectory != "/var/cache/goveto-edge" {
				t.Fatalf("cache directory was not normalized: %q", input.CacheDirectory)
			}
		})
	}
}
