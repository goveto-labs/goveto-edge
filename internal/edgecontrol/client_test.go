package edgecontrol

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"goveto-edge/internal/edgeprotocol"
)

func TestPushSiteConfigSignsAndParsesResponse(t *testing.T) {
	var sawSignature atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.URL.Path != "/v1/sites/site-1/config" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get(edgeprotocol.HeaderSignature) == "" || r.Header.Get(edgeprotocol.HeaderNonce) == "" {
			http.Error(w, "missing signature", http.StatusUnauthorized)
			return
		}
		if r.Host != "node-1" {
			http.Error(w, "bad host", http.StatusUnauthorized)
			return
		}
		sawSignature.Store(true)
		_ = json.NewEncoder(w).Encode(ApplySiteResult{SiteID: "site-1", Version: 2, ConfigVersion: 2, Applied: true})
	}))
	defer server.Close()

	client := New(server.URL, "node-1", "secret")
	result, err := client.PushSiteConfig(context.Background(), edgeprotocol.SiteConfig{
		SiteID:   "site-1",
		Version:  2,
		Domains:  []string{"example.com"},
		Listener: edgeprotocol.ListenerConfig{HTTPEnabled: true, HTTPPort: 80},
		Origins:  []edgeprotocol.OriginConfig{{Protocol: "http", Address: "origin:80"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !sawSignature.Load() || !result.Applied || result.ConfigVersion != 2 {
		t.Fatalf("unexpected result: %#v signed=%v", result, sawSignature.Load())
	}
}

func TestPushSiteConfigRejectsNonOK(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nope", http.StatusConflict)
	}))
	defer server.Close()
	client := New(server.URL, "node-1", "secret")
	_, err := client.PushSiteConfig(context.Background(), edgeprotocol.SiteConfig{SiteID: "site-1"})
	if err == nil || !strings.Contains(err.Error(), "rejected") {
		t.Fatalf("expected rejection, got %v", err)
	}
}

func TestPullLogsConsumesThenAcks(t *testing.T) {
	var pulls atomic.Int32
	var acked atomic.Uint64
	ackedCh := make(chan uint64, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/logs/pull"):
			pulls.Add(1)
			if pulls.Load() == 1 {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"records": []edgeprotocol.LogRecord{{ID: 7, Type: "access", Payload: json.RawMessage(`{"ok":true}`)}},
				})
				return
			}
			// After first batch is acked, return empty until cancelled.
			select {
			case <-r.Context().Done():
			case <-time.After(50 * time.Millisecond):
				_ = json.NewEncoder(w).Encode(map[string]any{"records": []edgeprotocol.LogRecord{}})
			}
		case r.Method == http.MethodPost && r.URL.Path == "/v1/logs/ack":
			body, _ := io.ReadAll(r.Body)
			var input struct {
				Through uint64 `json:"through"`
			}
			_ = json.Unmarshal(body, &input)
			acked.Store(input.Through)
			select {
			case ackedCh <- input.Through:
			default:
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := New(server.URL, "node-1", "secret")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() {
		errCh <- client.PullLogs(ctx, func(_ context.Context, records []edgeprotocol.LogRecord) error {
			if len(records) != 1 || records[0].ID != 7 {
				return fmt.Errorf("unexpected records: %#v", records)
			}
			return nil
		})
	}()
	select {
	case through := <-ackedCh:
		if through != 7 {
			t.Fatalf("expected ack through 7, got %d", through)
		}
	case err := <-errCh:
		t.Fatalf("pull ended early: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for ack")
	}
	cancel()
	select {
	case <-errCh:
	case <-time.After(2 * time.Second):
		t.Fatal("pull logs did not stop")
	}
	if acked.Load() != 7 {
		t.Fatalf("expected ack through 7, got %d", acked.Load())
	}
}

func TestPullLogsStopsOnUnauthorized(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "no", http.StatusUnauthorized)
	}))
	defer server.Close()
	client := New(server.URL, "node-1", "secret")
	err := client.PullLogs(context.Background(), func(context.Context, []edgeprotocol.LogRecord) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "unauthorized") {
		t.Fatalf("expected unauthorized, got %v", err)
	}
}

func TestRequestSetsSignedHeaders(t *testing.T) {
	client := New("http://127.0.0.1:9", "node-1", "secret")
	request, err := client.request(context.Background(), http.MethodPost, "/v1/logs/ack", []byte(`{"through":1}`))
	if err != nil {
		t.Fatal(err)
	}
	if request.Host != "node-1" {
		t.Fatalf("host: %s", request.Host)
	}
	for _, header := range []string{
		edgeprotocol.HeaderNodeID,
		edgeprotocol.HeaderTimestamp,
		edgeprotocol.HeaderNonce,
		edgeprotocol.HeaderContentHash,
		edgeprotocol.HeaderSignature,
	} {
		if request.Header.Get(header) == "" {
			t.Fatalf("missing header %s", header)
		}
	}
	hash := edgeprotocol.ContentHash([]byte(`{"through":1}`))
	if request.Header.Get(edgeprotocol.HeaderContentHash) != hash {
		t.Fatalf("content hash mismatch")
	}
	if !edgeprotocol.Verify(
		"secret",
		request.Header.Get(edgeprotocol.HeaderSignature),
		http.MethodPost,
		"node-1",
		request.URL.RequestURI(),
		request.Header.Get(edgeprotocol.HeaderTimestamp),
		request.Header.Get(edgeprotocol.HeaderNonce),
		hash,
	) {
		t.Fatal("signature does not verify")
	}
}
