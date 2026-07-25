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
	deleted []string
}

func (store *fakeRedisStore) Get(context.Context, string) *redis.StringCmd {
	return redis.NewStringResult("", redis.Nil)
}

func (store *fakeRedisStore) Set(context.Context, string, any, time.Duration) *redis.StatusCmd {
	return redis.NewStatusResult("OK", nil)
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
