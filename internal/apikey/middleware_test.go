package apikey

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v5"

	"goveto-edge/internal/auth"
	"goveto-edge/internal/httpapi/types"
	"goveto-edge/internal/httpsecurity"
	"goveto-edge/internal/storage/gen/model"
)

func TestExtractToken(t *testing.T) {
	request := func(headers map[string]string) *http.Request {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/clusters", nil)
		for name, value := range headers {
			req.Header.Set(name, value)
		}
		return req
	}
	token := "gve1_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

	if got := ExtractToken(request(nil)); got != "" {
		t.Fatalf("no headers must yield empty token, got %q", got)
	}
	if got := ExtractToken(request(map[string]string{"Authorization": "Bearer " + token})); got != token {
		t.Fatalf("bearer header: got %q", got)
	}
	if got := ExtractToken(request(map[string]string{"Authorization": "bearer " + token})); got != token {
		t.Fatalf("bearer scheme must be case-insensitive: got %q", got)
	}
	if got := ExtractToken(request(map[string]string{"X-API-Key": token})); got != token {
		t.Fatalf("x-api-key header: got %q", got)
	}
	// Foreign bearer tokens are ignored, not adopted.
	if got := ExtractToken(request(map[string]string{"Authorization": "Bearer oauth-token"})); got != "" {
		t.Fatalf("foreign bearer token must be ignored, got %q", got)
	}
	if got := ExtractToken(request(map[string]string{"Authorization": "Basic dXNlcjpwYXNz"})); got != "" {
		t.Fatalf("basic auth must be ignored, got %q", got)
	}
}

// TestMiddlewarePassesThroughWithoutCredential covers the anonymous and
// foreign-token paths: without a gve1_ credential the middleware must not
// touch the database (nil client here would panic otherwise).
func TestMiddlewarePassesThroughWithoutCredential(t *testing.T) {
	service := New(nil)
	handler := service.Middleware(nil)
	called := false
	next := func(c *echo.Context) error {
		called = true
		return nil
	}

	requests := []*http.Request{
		httptest.NewRequest(http.MethodGet, "/api/v1/clusters", nil),
		func() *http.Request {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/clusters", nil)
			req.Header.Set("Authorization", "Bearer some-oauth-token")
			return req
		}(),
	}
	for _, request := range requests {
		called = false
		response := httptest.NewRecorder()
		c := &echo.Context{}
		c.SetRequest(request)
		c.SetResponse(response)
		if err := handler(next)(c); err != nil {
			t.Fatal(err)
		}
		if !called {
			t.Fatal("handler must be called when no api key credential is presented")
		}
		if auth.CurrentAPIKey(c) != nil {
			t.Fatal("no principal may be loaded without a credential")
		}
	}
}

func TestMiddlewareLeavesSessionAloneWithoutAPIKey(t *testing.T) {
	service := New(nil)
	handler := service.Middleware(nil)
	request := httptest.NewRequest(http.MethodGet, "/api/v1/clusters", nil)
	response := httptest.NewRecorder()
	c := &echo.Context{}
	c.SetRequest(request)
	c.SetResponse(response)
	c.Set("auth.current_uid", "user-1")
	called := false
	next := func(inner *echo.Context) error {
		called = true
		if uid := auth.CurrentUID(inner); uid != "user-1" {
			t.Fatalf("session uid=%q, want user-1", uid)
		}
		return nil
	}
	if err := handler(next)(c); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("session request without api key must pass through")
	}
	if auth.CurrentAPIKey(c) != nil {
		t.Fatal("api key must not override a session principal")
	}
	if uid := auth.CurrentUID(c); uid != "user-1" {
		t.Fatalf("session uid after middleware=%q, want user-1", uid)
	}
}

func TestMiddlewareNeverFallsBackToSessionForExplicitInvalidKey(t *testing.T) {
	service := New(nil)
	request := httptest.NewRequest(http.MethodGet, "/api/v1/clusters", nil)
	request.Header.Set("Authorization", "Bearer gve1_short")
	response := httptest.NewRecorder()
	c := echo.New().NewContext(request, response)
	c.Set("auth.current_uid", "user-1")
	called := false
	err := service.Middleware(nil)(func(*echo.Context) error {
		called = true
		return nil
	})(c)
	if err == nil || called {
		t.Fatalf("invalid explicit key fell back to session: called=%v err=%v", called, err)
	}
}

func TestFailureCodeDoesNotTurnDatabaseErrorsIntoUnauthorized(t *testing.T) {
	for _, testCase := range []struct {
		err     error
		code    string
		message string
	}{
		{ErrInvalid, "api_key_invalid", "api key is invalid"},
		{ErrExpired, "api_key_expired", "api key has expired"},
		{ErrRevoked, "api_key_revoked", "api key has been revoked"},
		{ErrDisabled, "api_key_disabled", "api key is disabled"},
		{ErrCreatorInactive, "api_key_creator_inactive", "api key creator is inactive"},
	} {
		code, message, ok := failureCode(testCase.err)
		if !ok || code != testCase.code || message != testCase.message {
			t.Fatalf("failureCode(%v) = %q, %q, %v", testCase.err, code, message, ok)
		}
	}

	databaseError := errors.New("permission denied for relation cluster_api_keys")
	if code, message, ok := failureCode(databaseError); ok || code != "" || message != "" {
		t.Fatalf("database failure was exposed as a credential error: code=%q message=%q ok=%v", code, message, ok)
	}
}

func TestTokenDisplayPrefixNeverReturnsMalformedCredential(t *testing.T) {
	valid := "gve1_" + strings.Repeat("A", 43)
	if got := tokenDisplayPrefix(valid); got != valid[:prefixLength] {
		t.Fatalf("valid token prefix=%q", got)
	}
	if got := tokenDisplayPrefix("gve1_short"); got != TokenPrefix {
		t.Fatalf("malformed credential was retained in audit prefix: %q", got)
	}
}

func TestMiddlewareRateLimitsAuthenticatedKey(t *testing.T) {
	key := &model.ClusterApiKey{Id: "key-1", ClusterId: "cluster-1", Prefix: "gve1_example", CreatedBy: "user-1"}
	verify := func(context.Context, string) (*model.ClusterApiKey, error) { return key, nil }
	limiter := httpsecurity.NewRateLimiter(nil)
	called := 0
	handler := authenticationMiddleware(verify, func(string, string) {}, limiter)(func(*echo.Context) error {
		called++
		return nil
	})
	token := "gve1_" + strings.Repeat("A", 43)
	for attempt := 1; attempt <= RequestsPerMinute+1; attempt++ {
		request := httptest.NewRequest(http.MethodGet, "/api/v1/clusters", nil)
		request.Header.Set("Authorization", "Bearer "+token)
		response := httptest.NewRecorder()
		err := handler(echo.New().NewContext(request, response))
		if attempt <= RequestsPerMinute {
			if err != nil {
				t.Fatalf("attempt %d: %v", attempt, err)
			}
			continue
		}
		var apiError *types.APIError
		if !errors.As(err, &apiError) || apiError.Status != http.StatusTooManyRequests || apiError.Code != "too_many_requests" {
			t.Fatalf("attempt %d: error=%v, want API 429", attempt, err)
		}
		if response.Header().Get("Retry-After") == "" {
			t.Fatal("rate-limited response is missing Retry-After")
		}
	}
	if called != RequestsPerMinute {
		t.Fatalf("next called %d times, want %d", called, RequestsPerMinute)
	}
}

func TestMiddlewareRateLimitsInvalidAttemptsPerIPBeforeAudit(t *testing.T) {
	verify := func(context.Context, string) (*model.ClusterApiKey, error) { return nil, ErrInvalid }
	limiter := httpsecurity.NewRateLimiter(nil)
	token := "gve1_" + strings.Repeat("C", 43)
	request := func(ip string, recorder AuthFailureRecorder) error {
		handler := authenticationMiddleware(verify, func(string, string) {}, limiter, recorder)(func(*echo.Context) error {
			t.Fatal("invalid credential reached next handler")
			return nil
		})
		httpRequest := httptest.NewRequest(http.MethodGet, "/api/v1/clusters", nil)
		httpRequest.RemoteAddr = ip + ":1234"
		httpRequest.Header.Set("Authorization", "Bearer "+token)
		return handler(echo.New().NewContext(httpRequest, httptest.NewRecorder()))
	}

	for attempt := 1; attempt <= invalidAttemptsPerMinute; attempt++ {
		var apiError *types.APIError
		if err := request("192.0.2.20", nil); !errors.As(err, &apiError) || apiError.Status != http.StatusUnauthorized {
			t.Fatalf("attempt %d: error=%v, want 401", attempt, err)
		}
	}
	audited := make(chan struct{}, 1)
	recorder := func(context.Context, AuthFailureEvent) error {
		audited <- struct{}{}
		return nil
	}
	var apiError *types.APIError
	if err := request("192.0.2.20", recorder); !errors.As(err, &apiError) || apiError.Status != http.StatusTooManyRequests {
		t.Fatalf("over-limit attempt: error=%v, want 429", err)
	}
	select {
	case <-audited:
		t.Fatal("rate-limited invalid attempt was written to the audit log")
	case <-time.After(50 * time.Millisecond):
	}

	apiError = nil
	if err := request("192.0.2.21", nil); !errors.As(err, &apiError) || apiError.Status != http.StatusUnauthorized {
		t.Fatalf("different IP shared the exhausted bucket: error=%v, want 401", err)
	}
}

func TestMiddlewareAuditsCredentialFailureWithoutTokenMaterial(t *testing.T) {
	events := make(chan AuthFailureEvent, 1)
	recorder := func(_ context.Context, event AuthFailureEvent) error {
		events <- event
		return nil
	}
	verify := func(context.Context, string) (*model.ClusterApiKey, error) { return nil, ErrExpired }
	handler := authenticationMiddleware(verify, func(string, string) {}, nil, recorder)(func(*echo.Context) error {
		t.Fatal("failed credential reached next handler")
		return nil
	})
	token := "gve1_" + strings.Repeat("B", 43)
	request := httptest.NewRequest(http.MethodGet, "/api/v1/clusters", nil)
	request.RemoteAddr = "192.0.2.10:1234"
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("User-Agent", "integration-client/1.0")
	request.Header.Set(httpsecurity.RequestIDHeader, "request-123")
	err := handler(echo.New().NewContext(request, httptest.NewRecorder()))
	var apiError *types.APIError
	if !errors.As(err, &apiError) || apiError.Status != http.StatusUnauthorized {
		t.Fatalf("error=%v, want 401", err)
	}
	select {
	case event := <-events:
		if event.Prefix != token[:prefixLength] || event.FailureCode != "api_key_expired" ||
			event.SourceIP != "192.0.2.10" || event.UserAgent != "integration-client/1.0" || event.RequestID != "request-123" {
			t.Fatalf("unexpected audit event: %#v", event)
		}
		if strings.Contains(fmt.Sprintf("%#v", event), token) {
			t.Fatal("audit event contains the complete token")
		}
	case <-time.After(time.Second):
		t.Fatal("credential failure was not audited")
	}
}
