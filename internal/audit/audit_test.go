package audit

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"
)

type memoryRecorder struct{ entries []Entry }

func (recorder *memoryRecorder) Record(_ context.Context, entry Entry) error {
	recorder.entries = append(recorder.entries, entry)
	return nil
}

type contextRecorder struct{ contextErr error }

func (recorder *contextRecorder) Record(ctx context.Context, _ Entry) error {
	recorder.contextErr = ctx.Err()
	return nil
}

func TestMiddlewareRecordsFailedMutationAndRestoresBody(t *testing.T) {
	recorder := &memoryRecorder{}
	e := echo.New()
	e.Use(Middleware(recorder, []Route{{
		Method: http.MethodPost, Path: "/login", Action: "auth.login", ResourceType: "session",
	}}))
	e.POST("/login", func(c *echo.Context) error {
		var input map[string]any
		if err := c.Bind(&input); err != nil {
			t.Fatalf("body was not restored: %v", err)
		}
		SetActor(c, "", input["email"].(string))
		SetResourceID(c, input["email"].(string))
		return echo.NewHTTPError(http.StatusUnauthorized, "invalid credentials")
	})

	request := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(`{
		"email":"user@example.com","password":"do-not-store","nested":{"api_token":"secret"}
	}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "audit-test")
	request.Header.Set("X-Forwarded-For", "203.0.113.8")
	request.Header.Set(requestIDHeader, "request-123")
	recorderHTTP := httptest.NewRecorder()
	e.ServeHTTP(recorderHTTP, request)

	if len(recorder.entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(recorder.entries))
	}
	entry := recorder.entries[0]
	if entry.Result != ResultFailure || entry.FailureReason != "invalid credentials" {
		t.Fatalf("unexpected failure result: %#v", entry)
	}
	if entry.Actor != "user@example.com" || entry.ResourceID != "user@example.com" {
		t.Fatalf("unexpected actor/resource: %#v", entry)
	}
	if entry.SourceIP != "192.0.2.1" || entry.UserAgent != "audit-test" || entry.RequestID != "request-123" {
		t.Fatalf("request metadata missing: %#v", entry)
	}
	if recorderHTTP.Header().Get(requestIDHeader) != "request-123" {
		t.Fatal("request id was not returned to the client")
	}
	assertRedacted(t, entry.After, "password")
	assertRedacted(t, entry.After, "nested.api_token")
}

func TestSetChangeOverridesRequestSnapshot(t *testing.T) {
	recorder := &memoryRecorder{}
	e := echo.New()
	e.Use(Middleware(recorder, []Route{{
		Method: http.MethodPost, Path: "/resources", Action: "resource.create", ResourceType: "resource",
	}}))
	e.POST("/resources", func(c *echo.Context) error {
		SetResourceID(c, "resource-1")
		SetChange(c, nil, map[string]any{"id": "resource-1", "private_key": "hidden"})
		return c.NoContent(http.StatusCreated)
	})

	request := httptest.NewRequest(http.MethodPost, "/resources", strings.NewReader(`{
		"name":"input","headers":{"Authorization":"Bearer hidden","X-API-Key":"hidden"}
	}`))
	request.Header.Set("Content-Type", "application/json")
	recorderHTTP := httptest.NewRecorder()
	e.ServeHTTP(recorderHTTP, request)

	entry := recorder.entries[0]
	if entry.Result != ResultSuccess || entry.ResourceID != "resource-1" {
		t.Fatalf("unexpected entry: %#v", entry)
	}
	assertRedacted(t, entry.After, "private_key")
	var after map[string]any
	if err := json.Unmarshal(entry.After, &after); err != nil || after["id"] != "resource-1" {
		t.Fatalf("unexpected after snapshot: %s", entry.After)
	}
}

func TestSnapshotRedactsSensitiveHeaders(t *testing.T) {
	raw := snapshot(map[string]any{
		"headers": map[string]any{
			"Authorization": "Bearer hidden",
			"X-API-Key":     "hidden",
			"Cookie":        "session=hidden",
		},
	})
	assertRedacted(t, raw, "headers.Authorization")
	assertRedacted(t, raw, "headers.X-API-Key")
	assertRedacted(t, raw, "headers.Cookie")
}

func TestInvalidInboundRequestIDIsReplaced(t *testing.T) {
	recorder := &memoryRecorder{}
	e := echo.New()
	e.Use(Middleware(recorder, []Route{{http.MethodPost, "/action", "action", "resource", ""}}))
	e.POST("/action", func(c *echo.Context) error { return c.NoContent(http.StatusNoContent) })
	request := httptest.NewRequest(http.MethodPost, "/action", nil)
	request.Header.Set(requestIDHeader, strings.Repeat("x", 129))
	recorderHTTP := httptest.NewRecorder()
	e.ServeHTTP(recorderHTTP, request)
	if recorder.entries[0].RequestID == request.Header.Get(requestIDHeader) || recorder.entries[0].RequestID == "" {
		t.Fatalf("invalid request id was not replaced: %q", recorder.entries[0].RequestID)
	}
}

func TestMiddlewareDoesNotTruncateLargeUnknownLengthBody(t *testing.T) {
	recorder := &memoryRecorder{}
	e := echo.New()
	e.Use(Middleware(recorder, []Route{{http.MethodPost, "/upload", "resource.upload", "resource", ""}}))
	want := append([]byte(`{"value":"`), bytes.Repeat([]byte("x"), maxRequestBody)...)
	want = append(want, []byte(`"}`)...)
	e.POST("/upload", func(c *echo.Context) error {
		got, err := io.ReadAll(c.Request().Body)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("handler body length = %d, want %d", len(got), len(want))
		}
		return c.NoContent(http.StatusNoContent)
	})

	request := httptest.NewRequest(http.MethodPost, "/upload", io.NopCloser(bytes.NewReader(want)))
	request.ContentLength = -1
	request.Header.Set("Content-Type", "application/json")
	e.ServeHTTP(httptest.NewRecorder(), request)

	if len(recorder.entries) != 1 || len(recorder.entries[0].After) != 0 {
		t.Fatalf("large body should be forwarded without a snapshot: %#v", recorder.entries)
	}
}

func TestRecordContextInheritsRequestMetadata(t *testing.T) {
	recorder := &memoryRecorder{}
	state := &requestState{entry: Entry{
		ActorID: "user-1", Actor: "user@example.com", SourceIP: "203.0.113.1",
		UserAgent: "audit-test", RequestID: "request-1",
	}}
	ctx := context.WithValue(context.Background(), requestStateContextKey{}, state)

	RecordContext(ctx, recorder, "setting.update", "setting", "retention", "old", "new", nil)

	if len(recorder.entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(recorder.entries))
	}
	entry := recorder.entries[0]
	if entry.ActorID != "user-1" || entry.Actor != "user@example.com" || entry.SourceIP != "203.0.113.1" ||
		entry.UserAgent != "audit-test" || entry.RequestID != "request-1" || entry.Result != ResultSuccess {
		t.Fatalf("request metadata was not inherited: %#v", entry)
	}
}

func TestRecordContextWritesAfterRequestCancellation(t *testing.T) {
	recorder := &contextRecorder{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	RecordContext(ctx, recorder, "setting.update", "setting", "retention", "old", "new", nil)

	if recorder.contextErr != nil {
		t.Fatalf("recorder received cancelled context: %v", recorder.contextErr)
	}
}

func assertRedacted(t *testing.T, raw json.RawMessage, path string) {
	t.Helper()
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatalf("invalid snapshot: %v", err)
	}
	current := value
	for _, part := range strings.Split(path, ".") {
		object, ok := current.(map[string]any)
		if !ok {
			t.Fatalf("snapshot path %s is not an object", path)
		}
		current = object[part]
	}
	if current != "[REDACTED]" {
		t.Fatalf("%s = %#v, want redacted", path, current)
	}
}
