package apikey

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"goveto-edge/internal/rbac"
	"goveto-edge/internal/storage/gen/model"
)

func TestGenerateTokenShape(t *testing.T) {
	seen := make(map[string]struct{})
	for i := 0; i < 100; i++ {
		token, prefix, hash, err := generateToken()
		if err != nil {
			t.Fatal(err)
		}
		if !ValidTokenFormat(token) {
			t.Fatalf("generated token rejected by format check: %q", token)
		}
		if prefix != token[:prefixLength] {
			t.Fatalf("prefix %q is not the token head", prefix)
		}
		if len(hash) != 64 {
			t.Fatalf("hash length=%d, want 64 hex characters", len(hash))
		}
		if hash == TokenHash(token+"x") {
			t.Fatal("hash collision across distinct tokens")
		}
		seen[token] = struct{}{}
	}
	if len(seen) != 100 {
		t.Fatalf("token uniqueness: generated %d distinct tokens out of 100", len(seen))
	}
}

func TestValidTokenFormat(t *testing.T) {
	mixed := "a-b_c" + strings.Repeat("x", 38)
	valid := []string{
		"gve1_" + strings.Repeat("A", 43),
		"gve1_" + mixed,
	}
	for _, token := range valid {
		if !ValidTokenFormat(token) {
			t.Errorf("expected %q to be valid", token)
		}
	}
	invalid := []string{
		"",
		"gve1_short",
		"gve1_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", // one char too long
		"gve1_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA!",  // illegal character
		"bearer_gve1_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		"gve2_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", // missing prefix
	}
	for _, token := range invalid {
		if ValidTokenFormat(token) {
			t.Errorf("expected %q to be invalid", token)
		}
	}
}

func TestTokenHashIsStableAndOpaque(t *testing.T) {
	first := TokenHash("gve1_example")
	second := TokenHash("gve1_example")
	if first != second {
		t.Fatal("hash is not deterministic")
	}
	if first == "gve1_example" {
		t.Fatal("hash must not leak the token")
	}
}

func keyFor(status model.ApiKeyStatus, expiresAt, revokedAt *time.Time, permissions string) *model.ClusterApiKey {
	return &model.ClusterApiKey{
		Id: "key", ClusterId: "cluster", Status: status,
		ExpiresAt: expiresAt, RevokedAt: revokedAt, PermissionsJson: json.RawMessage(permissions),
	}
}

func TestVerifyStateMatrix(t *testing.T) {
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	future := now.Add(time.Hour)
	past := now.Add(-time.Hour)
	revoked := now.Add(-time.Minute)
	cases := []struct {
		name string
		key  *model.ClusterApiKey
		want error
	}{
		{"active key", keyFor(model.ApiKeyStatusACTIVE, nil, nil, `["cluster.read"]`), nil},
		{"active with future expiry", keyFor(model.ApiKeyStatusACTIVE, &future, nil, `["cluster.read"]`), nil},
		{"expired", keyFor(model.ApiKeyStatusACTIVE, &past, nil, `["cluster.read"]`), ErrExpired},
		{"expiry exactly now", keyFor(model.ApiKeyStatusACTIVE, &now, nil, `["cluster.read"]`), ErrExpired},
		{"disabled", keyFor(model.ApiKeyStatusDISABLED, nil, nil, `["cluster.read"]`), ErrDisabled},
		{"revoked", keyFor(model.ApiKeyStatusACTIVE, nil, &revoked, `["cluster.read"]`), ErrRevoked},
		{"revoked and disabled", keyFor(model.ApiKeyStatusDISABLED, nil, &revoked, `["cluster.read"]`), ErrRevoked},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			err := verifyState(testCase.key, now)
			if !errors.Is(err, testCase.want) {
				t.Fatalf("verifyState=%v, want %v", err, testCase.want)
			}
		})
	}
}

func TestEncodePermissionsWhitelist(t *testing.T) {
	encoded, err := EncodePermissions([]rbac.Permission{
		rbac.PermissionClusterRead, rbac.PermissionPublish, rbac.PermissionCacheOperate,
	})
	if err != nil {
		t.Fatal(err)
	}
	var values []string
	if err = json.Unmarshal(encoded, &values); err != nil {
		t.Fatal(err)
	}
	if len(values) != 3 {
		t.Fatalf("encoded %d permissions, want 3", len(values))
	}

	denied := []rbac.Permission{
		rbac.PermissionNodeManage,
		rbac.PermissionCredentialManage,
		rbac.PermissionMemberManage,
		rbac.PermissionNotificationManage,
		rbac.PermissionAPIKeyManage,
		rbac.PermissionPlatformUserManage,
		rbac.PermissionClusterTransfer,
		rbac.PermissionPlatformAuditRead,
	}
	for _, permission := range denied {
		if _, err = EncodePermissions([]rbac.Permission{permission}); !errors.Is(err, ErrPermissionNotAllowed) {
			t.Errorf("permission %q must not be grantable to an api key", permission)
		}
	}
}

func TestNameConflictRequiresExactPostgresConstraint(t *testing.T) {
	nameConflict := &pgconn.PgError{Code: "23505", ConstraintName: apiKeyNameConstraint}
	if !isNameConflict(nameConflict) || !isNameConflict(fmt.Errorf("insert api key: %w", nameConflict)) {
		t.Fatal("name constraint violation was not recognized through wrapping")
	}
	for _, databaseError := range []error{
		&pgconn.PgError{Code: "23505", ConstraintName: "cluster_api_keys_token_hash_key"},
		&pgconn.PgError{Code: "23505", ConstraintName: "cluster_api_keys_prefix_key"},
		&pgconn.PgError{Code: "42501", Message: "permission denied for relation cluster_api_keys"},
		errors.New("duplicate key in an unrelated subsystem"),
	} {
		if isNameConflict(databaseError) {
			t.Fatalf("database error was misclassified as a name conflict: %v", databaseError)
		}
	}
}

func TestPermissionsOfFiltersUnknownValues(t *testing.T) {
	key := &model.ClusterApiKey{PermissionsJson: json.RawMessage(`["cluster.read","platform.user.manage","site.write","made.up.permission"]`)}
	permissions := PermissionsOf(key)
	want := []rbac.Permission{rbac.PermissionClusterRead, rbac.PermissionSiteWrite}
	if len(permissions) != len(want) {
		t.Fatalf("permissions=%v, want %v", permissions, want)
	}
	for index := range want {
		if permissions[index] != want[index] {
			t.Fatalf("permissions=%v, want %v", permissions, want)
		}
	}
}

func TestPermissionsOfHandlesCorruptJson(t *testing.T) {
	key := &model.ClusterApiKey{PermissionsJson: json.RawMessage(`{not json`)}
	if permissions := PermissionsOf(key); len(permissions) != 0 {
		t.Fatalf("corrupt permissions json must yield no permissions, got %v", permissions)
	}
}

func TestTouchReservationThrottlesAndExpires(t *testing.T) {
	service := New(nil)
	now := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
	reserved, ok := service.reserveTouch("key-1", now)
	if !ok || reserved != now.Add(lastUsedUpdateInterval) {
		t.Fatalf("first reservation = %v, %v", reserved, ok)
	}
	if _, ok = service.reserveTouch("key-1", now.Add(time.Minute)); ok {
		t.Fatal("touch was not throttled inside the update interval")
	}
	if _, ok = service.reserveTouch("key-1", now.Add(lastUsedUpdateInterval)); !ok {
		t.Fatal("touch reservation did not expire")
	}
}
