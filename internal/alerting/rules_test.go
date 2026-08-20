package alerting

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"goveto-edge/internal/storage/gen/model"
)

type captureAnalyticsQuery struct {
	query string
}

func (c *captureAnalyticsQuery) QueryAnalytics(_ context.Context, query string, _ func(AnalyticsRow) error, _ ...any) error {
	c.query = query
	return nil
}

func TestRuleParametersRejectInvalidTypesAndRanges(t *testing.T) {
	spec := Spec(KindOriginErrorRate)
	if spec == nil {
		t.Fatal("origin error rate spec is missing")
	}
	for _, raw := range []string{
		`{"threshold":"0.5"}`,
		`{"threshold":0}`,
		`{"threshold":-1}`,
		`{"threshold":1.1}`,
		`{"window_minutes":0}`,
		`{"window_minutes":1.5}`,
		`{"unknown":1}`,
	} {
		params, err := spec.DecodeParams(json.RawMessage(raw))
		if err == nil {
			t.Errorf("DecodeParams(%s) succeeded, want validation error", raw)
		}
		if got := params.Float("threshold", -1); got != spec.Params["threshold"].Default {
			t.Errorf("DecodeParams(%s) threshold = %v, want usable default", raw, got)
		}
	}
}

func TestNodeResourceUsageObservationOmitsUnavailableCPU(t *testing.T) {
	memory := 95.0
	observation := nodeResourceUsageObservation(nodeResourceUsageRow{
		NodeID: "node-1", Memory: &memory,
	}, "edge-1", 10, 90, 92)
	if strings.Contains(observation.Title, "CPU") {
		t.Fatalf("title includes unavailable CPU: %q", observation.Title)
	}
	if _, found := observation.Detail["cpu_percent"]; found {
		t.Fatalf("detail includes unavailable CPU: %+v", observation.Detail)
	}
	if got := observation.Detail["memory_percent"]; got != memory {
		t.Fatalf("memory_percent = %v, want %v", got, memory)
	}
}

func TestNodeResourceUsageQueryPreservesUnavailableCPU(t *testing.T) {
	analytics := &captureAnalyticsQuery{}
	_, err := evaluateNodeResourceUsage(
		context.Background(), nil, "cluster-1", Spec(KindNodeResourceUsage).DefaultParams(), analytics,
	)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(analytics.query, "COALESCE(AVG(cpu_usage_percent)") {
		t.Fatalf("CPU aggregation coerces NULL to a measurement: %s", analytics.query)
	}
	if !strings.Contains(analytics.query, "AVG(cpu_usage_percent) AS cpu") {
		t.Fatalf("CPU aggregation is not nullable: %s", analytics.query)
	}
}

func TestMergeParamsRepairsDirtyLegacyStateAndStoresSparseOverrides(t *testing.T) {
	spec := Spec(KindOriginErrorRate)
	merged, err := spec.MergeParams(
		json.RawMessage(`{"threshold":-1,"window_minutes":null,"unknown":4}`),
		map[string]any{"window_minutes": 15},
	)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(merged), `{"window_minutes":15}`; got != want {
		t.Fatalf("merged params = %s, want %s", got, want)
	}
	params, decodeErr := spec.DecodeParams(merged)
	if decodeErr != nil {
		t.Fatal(decodeErr)
	}
	if params.Int("window_minutes", 0) != 15 || params.Float("threshold", -1) != spec.Params["threshold"].Default {
		t.Fatalf("repaired params = %+v", params)
	}
}

func TestMergeParamsOmitsCurrentDefaults(t *testing.T) {
	spec := Spec(KindOriginErrorRate)
	merged, err := spec.MergeParams(nil, map[string]any{
		"threshold": spec.Params["threshold"].Default,
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(merged) != `{}` {
		t.Fatalf("default persisted as override: %s", merged)
	}
}

func TestChannelsForFiltersAndFailsClosed(t *testing.T) {
	channels := []model.NotificationChannel{{Id: "one"}, {Id: "two"}}
	byCluster := map[string][]model.NotificationChannel{"cluster": channels}
	rule := &model.AlertRule{ClusterId: "cluster", ChannelsJson: json.RawMessage(`["two"]`)}
	selected, err := channelsFor(rule, byCluster)
	if err != nil {
		t.Fatal(err)
	}
	if len(selected) != 1 || selected[0].Id != "two" {
		t.Fatalf("selected channels = %+v, want channel two", selected)
	}
	rule.ChannelsJson = json.RawMessage(`{broken`)
	if selected, err = channelsFor(rule, byCluster); err == nil || selected != nil {
		t.Fatalf("invalid channel JSON returned %+v, err=%v", selected, err)
	}
}

func TestMergeParamsNullResetsDefault(t *testing.T) {
	spec := Spec(KindOriginErrorRate)
	merged, err := spec.MergeParams(
		json.RawMessage(`{"threshold":0.9,"window_minutes":10}`),
		map[string]any{"threshold": nil},
	)
	if err != nil {
		t.Fatal(err)
	}
	params, err := spec.DecodeParams(merged)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := params.Float("threshold", -1), spec.Params["threshold"].Default; got != want {
		t.Fatalf("threshold = %v, want default %v", got, want)
	}
	if got := params.Int("window_minutes", -1); got != 10 {
		t.Fatalf("window_minutes = %d, want 10", got)
	}
}

func TestValidateRuleTimers(t *testing.T) {
	for _, test := range []struct {
		forSeconds, cooldownSeconds int
		valid                       bool
	}{
		{0, 60, true}, {86400, 86400, true},
		{-1, 60, false}, {86401, 60, false}, {0, 0, false}, {0, 59, false}, {0, 86401, false},
	} {
		err := ValidateRuleTimers(test.forSeconds, test.cooldownSeconds)
		if (err == nil) != test.valid {
			t.Errorf("ValidateRuleTimers(%d, %d) error = %v, valid=%t", test.forSeconds, test.cooldownSeconds, err, test.valid)
		}
	}
}
