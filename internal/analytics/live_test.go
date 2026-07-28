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
