package analytics

import "testing"

func TestContinuousAggregateRefreshPolicies(t *testing.T) {
	expected := map[string]continuousAggregateRefreshPolicy{
		"analytics.request_usage_hourly":        {endOffset: "5 minutes", scheduleInterval: "5 minutes"},
		"analytics.request_method_hourly":       {endOffset: "5 minutes", scheduleInterval: "5 minutes"},
		"analytics.request_status_hourly":       {endOffset: "5 minutes", scheduleInterval: "5 minutes"},
		"analytics.request_extension_hourly":    {endOffset: "5 minutes", scheduleInterval: "5 minutes"},
		"analytics.request_hostname_hourly":     {endOffset: "5 minutes", scheduleInterval: "5 minutes"},
		"analytics.request_referer_hourly":      {endOffset: "5 minutes", scheduleInterval: "5 minutes"},
		"analytics.request_path_hourly":         {endOffset: "5 minutes", scheduleInterval: "5 minutes"},
		"analytics.request_client_ip_hourly":    {endOffset: "5 minutes", scheduleInterval: "5 minutes"},
		"analytics.request_country_hourly":      {endOffset: "5 minutes", scheduleInterval: "5 minutes"},
		"analytics.request_region_hourly":       {endOffset: "5 minutes", scheduleInterval: "5 minutes"},
		"analytics.request_usage_daily":         {endOffset: "1 hour", scheduleInterval: "1 hour"},
		"analytics.request_method_daily":        {endOffset: "1 hour", scheduleInterval: "1 hour"},
		"analytics.request_status_daily":        {endOffset: "1 hour", scheduleInterval: "1 hour"},
		"analytics.request_extension_daily":     {endOffset: "1 hour", scheduleInterval: "1 hour"},
		"analytics.request_hostname_daily":      {endOffset: "1 hour", scheduleInterval: "1 hour"},
		"analytics.request_referer_daily":       {endOffset: "1 hour", scheduleInterval: "1 hour"},
		"analytics.request_path_daily":          {endOffset: "1 hour", scheduleInterval: "1 hour"},
		"analytics.request_client_ip_daily":     {endOffset: "1 hour", scheduleInterval: "1 hour"},
		"analytics.request_country_daily":       {endOffset: "1 hour", scheduleInterval: "1 hour"},
		"analytics.request_region_daily":        {endOffset: "1 hour", scheduleInterval: "1 hour"},
		"analytics.node_traffic_metrics_minute": {endOffset: "1 minute", scheduleInterval: "1 minute"},
	}
	if len(continuousAggregateRefreshPolicies) != len(expected) {
		t.Fatalf("refresh policy count = %d, want %d", len(continuousAggregateRefreshPolicies), len(expected))
	}
	for _, policy := range continuousAggregateRefreshPolicies {
		want, ok := expected[policy.view]
		if !ok {
			t.Fatalf("unexpected refresh policy for %s", policy.view)
		}
		if policy.endOffset != want.endOffset || policy.scheduleInterval != want.scheduleInterval {
			t.Fatalf("refresh policy for %s = %#v, want %#v", policy.view, policy, want)
		}
		delete(expected, policy.view)
	}
}
