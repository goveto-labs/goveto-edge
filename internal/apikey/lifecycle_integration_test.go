package apikey

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"

	"goveto-edge/internal/auth"
	"goveto-edge/internal/httpapi/types"
	"goveto-edge/internal/rbac"
	"goveto-edge/internal/storage"
	"goveto-edge/internal/storage/gen/model"
	"goveto-edge/internal/storage/gen/query"
	"goveto-edge/schema"
)

// testDatabaseURL gates the lifecycle integration test. It is empty in normal
// `go test ./...` runs so CI never needs PostgreSQL for this package.
func testDatabaseURL() string {
	return strings.TrimSpace(os.Getenv("GOVETO_TEST_DATABASE_URL"))
}

// TestKeyLifecycleIntegration walks create -> verify -> rotate grace ->
// revoke against a real PostgreSQL instance.
func TestKeyLifecycleIntegration(t *testing.T) {
	databaseURL := testDatabaseURL()
	if databaseURL == "" {
		t.Skip("GOVETO_TEST_DATABASE_URL not set; skipping database lifecycle test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	db, orm, err := storage.OpenPostgreSQL(ctx, databaseURL)
	if err != nil {
		t.Skipf("database unreachable: %v", err)
	}
	defer db.Close()
	defer orm.Close()
	if _, err = storage.InitSchema(ctx, db, schema.FS, databaseURL); err != nil {
		t.Fatalf("init schema: %v", err)
	}

	suffix := time.Now().UTC().Format("20060102150405.000000000")
	user, err := orm.User.Create().Set(
		query.User.Email.Set("apikey-test-"+suffix+"@example.invalid"),
		query.User.PasswordHash.Set("x"),
		query.User.Name.Set("Api Key Test"),
	).Do(ctx)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	cluster, err := orm.Cluster.Create().Set(
		query.Cluster.CreatorId.Set(user.Id),
		query.Cluster.Name.Set("apikey-test-"+suffix),
	).Do(ctx)
	if err != nil {
		t.Fatalf("create cluster: %v", err)
	}
	t.Cleanup(func() {
		cleanup, done := context.WithTimeout(context.Background(), 30*time.Second)
		defer done()
		_, _ = orm.Cluster.DeleteMany(cleanup, query.Cluster.Id.Equals(cluster.Id))
		_, _ = orm.User.DeleteMany(cleanup, query.User.Id.Equals(user.Id))
	})

	service := New(orm)
	if _, _, err = service.Create(ctx, CreateInput{
		ClusterID: "missing-cluster", Name: "missing-cluster",
		Permissions: []rbac.Permission{rbac.PermissionClusterRead},
		CreatedBy:   user.Id,
	}); !errors.Is(err, ErrClusterNotFound) {
		t.Fatalf("create for missing cluster error=%v, want %v", err, ErrClusterNotFound)
	}

	quotaCluster, err := orm.Cluster.Create().Set(
		query.Cluster.CreatorId.Set(user.Id),
		query.Cluster.Name.Set("apikey-quota-test-"+suffix),
	).Do(ctx)
	if err != nil {
		t.Fatalf("create quota cluster: %v", err)
	}
	t.Cleanup(func() {
		cleanup, done := context.WithTimeout(context.Background(), 30*time.Second)
		defer done()
		_, _ = orm.Cluster.DeleteMany(cleanup, query.Cluster.Id.Equals(quotaCluster.Id))
	})
	expiredAt := time.Now().UTC().Add(-time.Hour)
	now := time.Now().UTC()
	expiredRows := make([]query.ClusterApiKeyCreateInput, MaxKeysPerCluster)
	for index := range expiredRows {
		expiredRows[index] = query.ClusterApiKeyCreateInput{
			Id: uuid.NewString(), ClusterId: quotaCluster.Id, Name: fmt.Sprintf("expired-%03d", index),
			Prefix: "gve1_expired", TokenHash: fmt.Sprintf("quota-expired-hash-%03d-%s", index, suffix),
			PermissionsJson: []byte(`["cluster.read"]`), Status: model.ApiKeyStatusACTIVE,
			ExpiresAt: &expiredAt, CreatedBy: user.Id, CreatedAt: now, UpdatedAt: now,
		}
	}
	if _, err = orm.ClusterApiKey.BulkCreate(expiredRows).BatchSize(MaxKeysPerCluster).Do(ctx); err != nil {
		t.Fatalf("create expired quota fixtures: %v", err)
	}
	if _, _, err = service.Create(ctx, CreateInput{
		ClusterID: quotaCluster.Id, Name: "live-key",
		Permissions: []rbac.Permission{rbac.PermissionClusterRead}, CreatedBy: user.Id,
	}); err != nil {
		t.Fatalf("expired keys consumed the active key quota: %v", err)
	}

	key, token, err := service.Create(ctx, CreateInput{
		ClusterID: cluster.Id, Name: "ci",
		Permissions: []rbac.Permission{rbac.PermissionClusterRead, rbac.PermissionPublish},
		CreatedBy:   user.Id,
	})
	if err != nil {
		t.Fatalf("create key: %v", err)
	}
	if !strings.HasPrefix(token, TokenPrefix) || token == key.TokenHash {
		t.Fatal("create must return the plaintext token, never the hash")
	}
	if _, _, err = service.Create(ctx, CreateInput{
		ClusterID: cluster.Id, Name: " ci ",
		Permissions: []rbac.Permission{rbac.PermissionClusterRead},
		CreatedBy:   user.Id,
	}); !errors.Is(err, ErrNameTaken) {
		t.Fatalf("duplicate key name error=%v, want %v (constraint %q)", err, ErrNameTaken, apiKeyNameConstraint)
	}

	verified, err := service.Verify(ctx, token)
	if err != nil || verified == nil || verified.Id != key.Id {
		t.Fatalf("verify fresh token: verified=%v err=%v", verified, err)
	}

	expiredKey, _, err := service.Create(ctx, CreateInput{
		ClusterID: cluster.Id, Name: "expired",
		Permissions: []rbac.Permission{rbac.PermissionClusterRead},
		CreatedBy:   user.Id,
	})
	if err != nil {
		t.Fatalf("create expiry fixture: %v", err)
	}
	if _, err = orm.ClusterApiKey.UpdateOne(ctx, query.ClusterApiKey.Id.Equals(expiredKey.Id),
		query.ClusterApiKey.ExpiresAt.Set(time.Now().UTC().Add(-time.Minute)),
	); err != nil {
		t.Fatalf("expire key fixture: %v", err)
	}
	if _, _, err = service.Rotate(ctx, cluster.Id, expiredKey.Id, time.Minute); !errors.Is(err, ErrExpired) {
		t.Fatalf("rotate expired key error=%v, want %v", err, ErrExpired)
	}

	// Rotation keeps the superseded token valid inside the grace window.
	_, newToken, err := service.Rotate(ctx, cluster.Id, key.Id, time.Minute)
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if newToken == token {
		t.Fatal("rotation must issue a distinct token")
	}
	if _, err = service.Verify(ctx, token); err != nil {
		t.Fatalf("previous token must stay valid during grace: %v", err)
	}
	if _, err = service.Verify(ctx, newToken); err != nil {
		t.Fatalf("new token must verify: %v", err)
	}

	graceExpiryKey, graceExpiryToken, err := service.Create(ctx, CreateInput{
		ClusterID: cluster.Id, Name: "grace-expiry",
		Permissions: []rbac.Permission{rbac.PermissionClusterRead},
		CreatedBy:   user.Id,
	})
	if err != nil {
		t.Fatalf("create grace-expiry fixture: %v", err)
	}
	_, graceExpiryNewToken, err := service.Rotate(ctx, cluster.Id, graceExpiryKey.Id, time.Minute)
	if err != nil {
		t.Fatalf("rotate grace-expiry fixture: %v", err)
	}
	if _, err = service.Verify(ctx, graceExpiryToken); err != nil {
		t.Fatalf("old token rejected before grace expiry: %v", err)
	}
	if _, err = orm.ClusterApiKey.UpdateOne(ctx, query.ClusterApiKey.Id.Equals(graceExpiryKey.Id),
		query.ClusterApiKey.PreviousExpiresAt.Set(time.Now().UTC().Add(-time.Second)),
	); err != nil {
		t.Fatalf("expire previous token fixture: %v", err)
	}
	if _, err = service.Verify(ctx, graceExpiryToken); !errors.Is(err, ErrInvalid) {
		t.Fatalf("old token after grace expiry error=%v, want %v", err, ErrInvalid)
	}
	if _, err = service.Verify(ctx, graceExpiryNewToken); err != nil {
		t.Fatalf("current token failed after grace expiry: %v", err)
	}

	zeroGraceKey, zeroGraceToken, err := service.Create(ctx, CreateInput{
		ClusterID: cluster.Id, Name: "zero-grace",
		Permissions: []rbac.Permission{rbac.PermissionClusterRead},
		CreatedBy:   user.Id,
	})
	if err != nil {
		t.Fatalf("create zero-grace fixture: %v", err)
	}
	withGraceRow, withGraceToken, err := service.Rotate(ctx, cluster.Id, zeroGraceKey.Id, time.Minute)
	if err != nil {
		t.Fatalf("prepare zero-grace fixture: %v", err)
	}
	if withGraceRow.PreviousTokenHash == nil || withGraceRow.PreviousExpiresAt == nil {
		t.Fatalf("zero-grace fixture has no previous token state: %#v", withGraceRow)
	}
	if _, err = service.Verify(ctx, zeroGraceToken); err != nil {
		t.Fatalf("fixture token must verify during grace: %v", err)
	}
	zeroGraceRow, zeroGraceNewToken, err := service.Rotate(ctx, cluster.Id, zeroGraceKey.Id, 0)
	if err != nil {
		t.Fatalf("zero-grace rotate: %v", err)
	}
	if zeroGraceRow.PreviousTokenHash != nil || zeroGraceRow.PreviousExpiresAt != nil {
		t.Fatalf("zero-grace rotation retained old token state: %#v", zeroGraceRow)
	}
	if _, err = service.Verify(ctx, zeroGraceToken); !errors.Is(err, ErrInvalid) {
		t.Fatalf("grace token after zero-grace rotation error=%v, want %v", err, ErrInvalid)
	}
	if _, err = service.Verify(ctx, withGraceToken); !errors.Is(err, ErrInvalid) {
		t.Fatalf("superseded current token after zero-grace rotation error=%v, want %v", err, ErrInvalid)
	}
	if _, err = service.Verify(ctx, zeroGraceNewToken); err != nil {
		t.Fatalf("zero-grace current token failed: %v", err)
	}

	// Concurrent rotations are serialized. Each successful caller receives a
	// token that remains either current or in the grace slot after both commit.
	var rotatedTokens [2]string
	var rotateErrors [2]error
	start := make(chan struct{})
	var rotations sync.WaitGroup
	for index := range rotatedTokens {
		rotations.Add(1)
		go func() {
			defer rotations.Done()
			<-start
			_, rotatedTokens[index], rotateErrors[index] = service.Rotate(ctx, cluster.Id, key.Id, time.Minute)
		}()
	}
	close(start)
	rotations.Wait()
	for index := range rotatedTokens {
		if rotateErrors[index] != nil {
			t.Fatalf("concurrent rotate %d: %v", index, rotateErrors[index])
		}
		if _, err = service.Verify(ctx, rotatedTokens[index]); err != nil {
			t.Fatalf("token returned by concurrent rotate %d is invalid: %v", index, err)
		}
	}

	if _, err = orm.ClusterApiKey.UpdateOne(ctx, query.ClusterApiKey.Id.Equals(key.Id),
		query.ClusterApiKey.Status.Set(model.ApiKeyStatusDISABLED),
		query.ClusterApiKey.PreviousTokenHash.SetNull(),
		query.ClusterApiKey.PreviousExpiresAt.SetNull(),
	); err != nil {
		t.Fatalf("disable key: %v", err)
	}
	if _, _, err = service.Rotate(ctx, cluster.Id, key.Id, time.Minute); !errors.Is(err, ErrDisabled) {
		t.Fatalf("rotate disabled key error=%v, want %v", err, ErrDisabled)
	}
	if _, err = orm.ClusterApiKey.UpdateOne(ctx, query.ClusterApiKey.Id.Equals(key.Id),
		query.ClusterApiKey.Status.Set(model.ApiKeyStatusACTIVE),
	); err != nil {
		t.Fatalf("re-enable key: %v", err)
	}

	// Update, rotate and revoke share one row-lock protocol. Regardless of
	// which mutation wins, revocation must leave a disabled row with no grace.
	raceKey, _, err := service.Create(ctx, CreateInput{
		ClusterID: cluster.Id, Name: "mutation-race",
		Permissions: []rbac.Permission{rbac.PermissionClusterRead},
		CreatedBy:   user.Id,
	})
	if err != nil {
		t.Fatalf("create mutation fixture: %v", err)
	}
	mutationStart := make(chan struct{})
	var updateErr, revokeErr error
	var mutations sync.WaitGroup
	mutations.Add(2)
	go func() {
		defer mutations.Done()
		<-mutationStart
		_, _, updateErr = service.Update(ctx, cluster.Id, raceKey.Id,
			query.ClusterApiKey.Status.Set(model.ApiKeyStatusACTIVE))
	}()
	go func() {
		defer mutations.Done()
		<-mutationStart
		_, revokeErr = service.Revoke(ctx, cluster.Id, raceKey.Id)
	}()
	close(mutationStart)
	mutations.Wait()
	if updateErr != nil && !errors.Is(updateErr, ErrRevoked) {
		t.Fatalf("concurrent update: %v", updateErr)
	}
	if revokeErr != nil {
		t.Fatalf("concurrent revoke: %v", revokeErr)
	}
	raceRow, err := orm.ClusterApiKey.FindUnique(ctx, query.ClusterApiKey.Id.Equals(raceKey.Id))
	if err != nil || raceRow == nil || raceRow.RevokedAt == nil || raceRow.Status != model.ApiKeyStatusDISABLED ||
		raceRow.PreviousTokenHash != nil || raceRow.PreviousExpiresAt != nil {
		t.Fatalf("concurrent lifecycle left inconsistent row: row=%#v err=%v", raceRow, err)
	}

	// Revocation kills both tokens immediately and retains the row.
	if _, err = service.Revoke(ctx, cluster.Id, key.Id); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if _, err = service.Verify(ctx, newToken); err == nil {
		t.Fatal("revoked key must not verify")
	}
	if _, err = service.Verify(ctx, token); err == nil {
		t.Fatal("grace token must die with the key")
	}
	retained, err := orm.ClusterApiKey.FindUnique(ctx, query.ClusterApiKey.Id.Equals(key.Id))
	if err != nil || retained == nil || retained.RevokedAt == nil {
		t.Fatalf("revoked row must be retained for audit: row=%v err=%v", retained, err)
	}
}

// TestMiddlewareAuthenticatesRequest runs the HTTP middleware chain against a
// real database: bearer token -> principal -> RequireAuth acceptance, and the
// machine-readable failure code for an unknown token.
func TestMiddlewareAuthenticatesRequest(t *testing.T) {
	databaseURL := testDatabaseURL()
	if databaseURL == "" {
		t.Skip("GOVETO_TEST_DATABASE_URL not set; skipping database middleware test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	db, orm, err := storage.OpenPostgreSQL(ctx, databaseURL)
	if err != nil {
		t.Skipf("database unreachable: %v", err)
	}
	defer db.Close()
	defer orm.Close()
	if _, err = storage.InitSchema(ctx, db, schema.FS, databaseURL); err != nil {
		t.Fatalf("init schema: %v", err)
	}

	suffix := time.Now().UTC().Format("20060102150405.000000000")
	user, err := orm.User.Create().Set(
		query.User.Email.Set("apikey-mw-test-"+suffix+"@example.invalid"),
		query.User.PasswordHash.Set("x"),
		query.User.Name.Set("Middleware Test"),
	).Do(ctx)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	cluster, err := orm.Cluster.Create().Set(
		query.Cluster.CreatorId.Set(user.Id),
		query.Cluster.Name.Set("apikey-mw-test-"+suffix),
	).Do(ctx)
	if err != nil {
		t.Fatalf("create cluster: %v", err)
	}
	t.Cleanup(func() {
		cleanup, done := context.WithTimeout(context.Background(), 30*time.Second)
		defer done()
		_, _ = orm.Cluster.DeleteMany(cleanup, query.Cluster.Id.Equals(cluster.Id))
		_, _ = orm.User.DeleteMany(cleanup, query.User.Id.Equals(user.Id))
	})

	service := New(orm)
	_, token, err := service.Create(ctx, CreateInput{
		ClusterID: cluster.Id, Name: "mw",
		Permissions: []rbac.Permission{rbac.PermissionClusterRead},
		CreatedBy:   user.Id,
	})
	if err != nil {
		t.Fatalf("create key: %v", err)
	}

	e := echo.New()
	// The assembled server installs this handler; it serializes the
	// machine-readable APIError codes produced by the middleware.
	e.HTTPErrorHandler = types.HTTPErrorHandler
	principalSeen := ""
	loadSession := func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			c.Set("auth.current_uid", "stale-user")
			return next(c)
		}
	}
	e.GET("/api/v1/probe", func(c *echo.Context) error {
		if principal := auth.CurrentAPIKey(c); principal != nil {
			principalSeen = principal.ClusterID
		}
		if auth.CurrentUID(c) != "" {
			t.Fatal("explicit api key did not clear the loaded session principal")
		}
		return c.String(http.StatusOK, "ok")
	}, loadSession, service.Middleware(nil), auth.RequireAuth)

	do := func(tokenValue string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodGet, "/api/v1/probe", nil)
		if tokenValue != "" {
			request.Header.Set("Authorization", "Bearer "+tokenValue)
		}
		response := httptest.NewRecorder()
		e.ServeHTTP(response, request)
		return response
	}

	if response := do(token); response.Code != http.StatusOK {
		t.Fatalf("valid token: status=%d body=%s", response.Code, response.Body.String())
	}
	if principalSeen != cluster.Id {
		t.Fatalf("principal cluster=%q, want %q", principalSeen, cluster.Id)
	}
	if response := do(""); response.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous: status=%d, want 401", response.Code)
	}
	tamperedToken := tamperValidToken(token)
	if !ValidTokenFormat(tamperedToken) {
		t.Fatalf("tampered token must retain a valid format: %q", tamperedToken)
	}
	response := do(tamperedToken)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("tampered token: status=%d, want 401", response.Code)
	}
	if !strings.Contains(response.Body.String(), "api_key_invalid") {
		t.Fatalf("failure must carry the machine-readable code: %s", response.Body.String())
	}

	if _, err = orm.User.UpdateOne(ctx, query.User.Id.Equals(user.Id),
		query.User.Status.Set(model.UserStatusDISABLED),
	); err != nil {
		t.Fatalf("disable key creator: %v", err)
	}
	response = do(token)
	if response.Code != http.StatusUnauthorized || !strings.Contains(response.Body.String(), "api_key_creator_inactive") {
		t.Fatalf("disabled creator key: status=%d body=%s", response.Code, response.Body.String())
	}
}

func tamperValidToken(token string) string {
	last := len(token) - 1
	replacement := byte('A')
	if token[last] == replacement {
		replacement = 'B'
	}
	return token[:last] + string(replacement)
}
