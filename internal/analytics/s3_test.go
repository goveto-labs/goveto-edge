package analytics

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestS3ObjectStoreSignsPathStylePut(t *testing.T) {
	var received *http.Request
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		received = request.Clone(request.Context())
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	store, err := NewS3ObjectStore(S3Options{
		Endpoint: server.URL, Bucket: "edge-logs", Region: "us-east-1",
		AccessKey: "access", SecretKey: "secret", SessionToken: "session-token",
		HTTPClient: server.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	store.now = func() time.Time { return time.Date(2026, 7, 28, 1, 2, 3, 0, time.UTC) }
	if err := store.Put(context.Background(), "archive/node batch.ndjson.gz", ArchiveObject{
		ContentType: "application/x-ndjson", ContentEncoding: "gzip", Data: []byte("payload"),
	}); err != nil {
		t.Fatal(err)
	}
	if received == nil || received.URL.EscapedPath() != "/edge-logs/archive/node%20batch.ndjson.gz" {
		t.Fatalf("unexpected S3 request: %#v", received)
	}
	if received.Header.Get("Content-Encoding") != "gzip" ||
		!strings.HasPrefix(received.Header.Get("Authorization"), "AWS4-HMAC-SHA256 Credential=access/20260728/us-east-1/s3/aws4_request") {
		t.Fatalf("S3 request was not signed correctly: %#v", received.Header)
	}
	authorization := received.Header.Get("Authorization")
	for _, header := range []string{
		"content-encoding", "content-length", "content-type", "host",
		"x-amz-content-sha256", "x-amz-date", "x-amz-security-token",
	} {
		if !strings.Contains(authorization, header) {
			t.Fatalf("S3 request did not sign %s: %s", header, authorization)
		}
	}
	if received.Header.Get("X-Amz-Security-Token") != "session-token" ||
		!strings.Contains(authorization, "x-amz-security-token") {
		t.Fatalf("S3 session token was not signed: %#v", received.Header)
	}
}

func TestS3ObjectStoreRejectsIncompleteConfig(t *testing.T) {
	if _, err := NewS3ObjectStore(S3Options{Endpoint: "https://s3.example.com"}); err == nil {
		t.Fatal("incomplete S3 configuration was accepted")
	}
}
