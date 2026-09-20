package analytics

import (
	"context"
	"testing"
	"time"
)

func TestLiveBrokerFiltersEvents(t *testing.T) {
	broker := NewLiveBroker()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := broker.Subscribe(ctx, LiveFilter{ClusterID: "cluster-1", SiteID: "site-1"}, 1)
	broker.Publish([]WebRequestLog{
		{ClusterID: "cluster-2", SiteID: "site-1"},
		{ClusterID: "cluster-1", SiteID: "site-1", NodeID: "node-1", Path: "/ok"},
	})
	select {
	case event := <-events:
		if event.Path != "/ok" || event.NodeID != "node-1" {
			t.Fatalf("unexpected live event: %#v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("matching live event was not published")
	}
}

func TestLiveFilterMatches(t *testing.T) {
	if !(LiveFilter{}).matches(LiveRequestLog{ClusterID: "any"}) {
		t.Fatal("empty filter must match every cluster")
	}
	if !(LiveFilter{ClusterID: "a", SiteID: "s", NodeID: "n"}).matches(LiveRequestLog{ClusterID: "a", SiteID: "s", NodeID: "n"}) {
		t.Fatal("exact filter rejected a matching event")
	}
	cases := map[string]struct {
		filter LiveFilter
		event  LiveRequestLog
	}{
		"cluster": {LiveFilter{ClusterID: "a"}, LiveRequestLog{ClusterID: "b"}},
		"site":    {LiveFilter{ClusterID: "a", SiteID: "s"}, LiveRequestLog{ClusterID: "a", SiteID: "other"}},
		"node":    {LiveFilter{ClusterID: "a", NodeID: "n"}, LiveRequestLog{ClusterID: "a", NodeID: "other"}},
	}
	for name, tc := range cases {
		if tc.filter.matches(tc.event) {
			t.Fatalf("%s filter matched a non-matching event", name)
		}
	}
}
