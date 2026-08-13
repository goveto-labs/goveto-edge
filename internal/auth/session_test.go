package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/redis/go-redis/v9"

	"goveto-edge/internal/storage/gen/model"
)

type fakeRedisStore struct {
	values  map[string]string
	deleted []string
}

func (store *fakeRedisStore) Get(_ context.Context, key string) *redis.StringCmd {
	if value, ok := store.values[key]; ok {
		return redis.NewStringResult(value, nil)
	}
	return redis.NewStringResult("", redis.Nil)
}

func (store *fakeRedisStore) GetDel(_ context.Context, key string) *redis.StringCmd {
	value, ok := store.values[key]
	if !ok {
		return redis.NewStringResult("", redis.Nil)
	}
	delete(store.values, key)
	return redis.NewStringResult(value, nil)
}

func (store *fakeRedisStore) Set(_ context.Context, key string, value any, _ time.Duration) *redis.StatusCmd {
	if store.values == nil {
		store.values = map[string]string{}
	}
	switch typed := value.(type) {
	case []byte:
		store.values[key] = string(typed)
	case string:
		store.values[key] = typed
	default:
		encoded, _ := json.Marshal(typed)
		store.values[key] = string(encoded)
	}
	return redis.NewStatusResult("OK", nil)
}

func (store *fakeRedisStore) SetNX(context.Context, string, any, time.Duration) *redis.BoolCmd {
	return redis.NewBoolResult(true, nil)
}

func (store *fakeRedisStore) Del(_ context.Context, keys ...string) *redis.IntCmd {
	store.deleted = append(store.deleted, keys...)
	for _, key := range keys {
		delete(store.values, key)
	}
	return redis.NewIntResult(int64(len(keys)), nil)
}

func TestExternalAuthStateIsConsumedOnce(t *testing.T) {
	store := &fakeRedisStore{}
	sessions := &SessionStore{redis: store, cookieName: "session", secure: true}
	want := ExternalAuthState{CodeVerifier: "verifier", ReturnPath: "/jobs", ProviderID: "provider-1"}
	if err := sessions.StoreExternalAuthState(context.Background(), "state", want, "browser-binding"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(store.values["external-auth:state:state"], "browser-binding") {
		t.Fatal("raw browser binding was stored in Redis")
	}
	got, err := sessions.ConsumeExternalAuthState(context.Background(), "state", "browser-binding")
	if err != nil {
		t.Fatal(err)
	}
	if got.CodeVerifier != want.CodeVerifier || got.ReturnPath != want.ReturnPath || got.ProviderID != want.ProviderID || got.BrowserBindingHash == "" {
		t.Fatalf("external authentication state = %#v, want %#v", got, want)
	}
	if _, err = sessions.ConsumeExternalAuthState(context.Background(), "state", "browser-binding"); err != ErrExternalAuthStateNotFound {
		t.Fatalf("second consumption error = %v, want %v", err, ErrExternalAuthStateNotFound)
	}
}

func TestExternalAuthStateRejectsAnotherBrowser(t *testing.T) {
	store := &fakeRedisStore{}
	sessions := &SessionStore{redis: store, cookieName: "session"}
	if err := sessions.StoreExternalAuthState(context.Background(), "state", ExternalAuthState{}, "first-browser"); err != nil {
		t.Fatal(err)
	}
	if _, err := sessions.ConsumeExternalAuthState(context.Background(), "state", "another-browser"); err != ErrExternalAuthBindingMismatch {
		t.Fatalf("binding mismatch error = %v, want %v", err, ErrExternalAuthBindingMismatch)
	}
	if _, err := sessions.ConsumeExternalAuthState(context.Background(), "state", "first-browser"); err != ErrExternalAuthStateNotFound {
		t.Fatalf("mismatched state was not consumed: %v", err)
	}
}

func TestExternalAuthBindingCookieLifecycle(t *testing.T) {
	sessions := &SessionStore{cookieName: "session", secure: true}
	recorder := httptest.NewRecorder()
	c := echo.New().NewContext(httptest.NewRequest(http.MethodGet, "/api/v1/auth/providers/start", nil), recorder)
	sessions.SetExternalAuthBindingCookie(c, "state-1", "binding")
	cookie := recorder.Result().Cookies()[0]
	if cookie.Name != sessions.externalAuthBindingCookieName("state-1") || cookie.Value != "binding" || !cookie.HttpOnly || !cookie.Secure || cookie.SameSite != http.SameSiteLaxMode {
		t.Fatalf("external authentication binding cookie = %#v", cookie)
	}
	if cookie.Name == sessions.externalAuthBindingCookieName("state-2") {
		t.Fatal("different OAuth states use the same browser binding cookie")
	}

	request := httptest.NewRequest(http.MethodGet, "/api/v1/auth/providers/callback", nil)
	request.AddCookie(cookie)
	recorder = httptest.NewRecorder()
	c = echo.New().NewContext(request, recorder)
	if got := sessions.ExternalAuthBinding(c, "state-1"); got != "binding" {
		t.Fatalf("external authentication binding = %q", got)
	}
	sessions.ClearExternalAuthBindingCookie(c, "state-1")
	cleared := recorder.Result().Cookies()[0]
	if cleared.Name != cookie.Name || cleared.MaxAge >= 0 || !cleared.HttpOnly || !cleared.Secure || cleared.Path != cookie.Path {
		t.Fatalf("cleared external authentication binding cookie = %#v", cleared)
	}
}

func TestRequireActiveUserInvalidatesDisabledSession(t *testing.T) {
	redisStore := &fakeRedisStore{}
	sessions := &SessionStore{redis: redisStore, cookieName: "session", ttl: time.Hour, secure: true}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/protected", nil)
	ctx := echo.New().NewContext(request, recorder)
	ctx.Set(currentUIDKey, "user-1")
	ctx.Set(currentSessionTokenKey, "token-1")
	nextCalled := false

	handler := sessions.requireActiveUser(func(context.Context, string) (*model.User, error) {
		return &model.User{Id: "user-1", Status: model.UserStatusDISABLED}, nil
	})(func(c *echo.Context) error {
		nextCalled = true
		if CurrentUID(c) != "" {
			t.Fatal("disabled user remained authenticated")
		}
		return nil
	})

	if err := handler(ctx); err != nil {
		t.Fatalf("middleware returned error: %v", err)
	}
	if !nextCalled {
		t.Fatal("middleware did not continue after clearing the principal")
	}
	wantDeleted := []string{sessionKey("token-1"), selectedClusterKey("token-1")}
	if len(redisStore.deleted) != len(wantDeleted) {
		t.Fatalf("deleted keys = %v, want %v", redisStore.deleted, wantDeleted)
	}
	for index := range wantDeleted {
		if redisStore.deleted[index] != wantDeleted[index] {
			t.Fatalf("deleted keys = %v, want %v", redisStore.deleted, wantDeleted)
		}
	}
	cookies := recorder.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != "session" || cookies[0].MaxAge >= 0 || !cookies[0].Secure {
		t.Fatalf("session cookie was not expired securely: %#v", cookies)
	}
}

func TestRequireActiveUserKeepsActiveSession(t *testing.T) {
	redisStore := &fakeRedisStore{}
	sessions := &SessionStore{redis: redisStore, cookieName: "session", ttl: time.Hour}
	ctx := echo.New().NewContext(httptest.NewRequest(http.MethodGet, "/protected", nil), httptest.NewRecorder())
	ctx.Set(currentUIDKey, "user-1")
	ctx.Set(currentSessionTokenKey, "token-1")

	handler := sessions.requireActiveUser(func(context.Context, string) (*model.User, error) {
		return &model.User{Id: "user-1", Status: model.UserStatusACTIVE}, nil
	})(func(c *echo.Context) error {
		if CurrentUID(c) != "user-1" {
			t.Fatal("active user lost authentication")
		}
		user, ok := CurrentUser(c.Request().Context(), "user-1")
		if !ok || user.Status != model.UserStatusACTIVE {
			t.Fatal("active user was not cached in the request context")
		}
		return nil
	})

	if err := handler(ctx); err != nil {
		t.Fatalf("middleware returned error: %v", err)
	}
	if len(redisStore.deleted) != 0 {
		t.Fatalf("active session keys were deleted: %v", redisStore.deleted)
	}
}

func TestSetCookieCreatesSecureSessionAndCSRFCookies(t *testing.T) {
	sessions := &SessionStore{cookieName: "session", csrfCookie: "session_csrf", ttl: time.Hour, secure: true}
	recorder := httptest.NewRecorder()
	c := echo.New().NewContext(httptest.NewRequest(http.MethodPost, "/login", nil), recorder)
	if err := sessions.SetCookie(c, "session-token"); err != nil {
		t.Fatal(err)
	}
	cookies := recorder.Result().Cookies()
	if len(cookies) != 2 {
		t.Fatalf("cookies = %#v", cookies)
	}
	if cookies[0].Name != "session" || !cookies[0].HttpOnly || !cookies[0].Secure {
		t.Fatalf("session cookie = %#v", cookies[0])
	}
	if cookies[1].Name != "session_csrf" || cookies[1].HttpOnly || !cookies[1].Secure || cookies[1].Value == "" {
		t.Fatalf("CSRF cookie = %#v", cookies[1])
	}
}

func TestSessionReissuesMissingCSRFCookie(t *testing.T) {
	redisStore := &fakeRedisStore{values: map[string]string{sessionKey("token-1"): "user-1"}}
	sessions := &SessionStore{redis: redisStore, cookieName: "session", csrfCookie: "session_csrf", ttl: time.Hour}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/protected", nil)
	request.AddCookie(&http.Cookie{Name: "session", Value: "token-1"})
	ctx := echo.New().NewContext(request, recorder)

	handler := sessions.Session(func(c *echo.Context) error {
		if CurrentUID(c) != "user-1" {
			t.Fatal("session was not loaded")
		}
		return nil
	})
	if err := handler(ctx); err != nil {
		t.Fatal(err)
	}
	cookies := recorder.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != "session_csrf" || cookies[0].Value == "" {
		t.Fatalf("CSRF cookie was not reissued: %#v", cookies)
	}

	// A request that still carries the CSRF cookie must not receive a new one.
	recorder = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodGet, "/protected", nil)
	request.AddCookie(&http.Cookie{Name: "session", Value: "token-1"})
	request.AddCookie(&http.Cookie{Name: "session_csrf", Value: "existing"})
	if err := handler(echo.New().NewContext(request, recorder)); err != nil {
		t.Fatal(err)
	}
	if cookies := recorder.Result().Cookies(); len(cookies) != 0 {
		t.Fatalf("unexpected cookies on repeat request: %#v", cookies)
	}
}
