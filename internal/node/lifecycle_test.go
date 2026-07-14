package node

import (
	"context"
	"testing"
)

func TestHealthRequestUsesNodeIDAsHost(t *testing.T) {
	target := healthTarget{
		NodeID:  "550e8400-e29b-41d4-a716-446655440000",
		Address: "192.0.2.10",
	}
	request, err := newHealthRequest(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	if request.URL.String() != "http://192.0.2.10:80/v1/health" {
		t.Fatalf("URL=%q", request.URL.String())
	}
	if request.Host != target.NodeID {
		t.Fatalf("Host=%q, want node ID", request.Host)
	}
}
