package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
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

func (store *fakeRedisStore) Set(context.Context, string, any, time.Duration) *redis.StatusCmd {
	return redis.NewStatusResult("OK", nil)
}

func (store *fakeRedisStore) SetNX(context.Context, string, any, time.Duration) *redis.BoolCmd {
	return redis.NewBoolResult(true, nil)
}

func (store *fakeRedisStore) Del(_ context.Context, keys ...string) *redis.IntCmd {
	store.deleted = append(store.deleted, keys...)
	return redis.NewIntResult(int64(len(keys)), nil)
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
