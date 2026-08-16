// Package apikey implements cluster-scoped API key authentication for
// automation clients. Keys are bound to a single cluster, hold a subset of
// the RBAC permissions whitelisted in internal/rbac, and only ever persist a
// SHA-256 hash of the token; the plaintext is returned exactly once at
// creation or rotation.
package apikey

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"goveto-edge/internal/rbac"
	"goveto-edge/internal/storage/gen/client"
	"goveto-edge/internal/storage/gen/model"
	"goveto-edge/internal/storage/gen/query"
)

// TokenPrefix makes generated secrets recognizable by scanners and humans.
// v1 tokens are "gve1_" followed by 43 base64url characters (32 random bytes).
const TokenPrefix = "gve1_"

const (
	tokenRandomBytes = 32
	prefixLength     = 12
)

// Operational limits for API keys (per cluster / per key).
const (
	MaxKeysPerCluster      = 100
	DefaultRotationGrace   = 10 * time.Minute
	MaxRotationGrace       = 24 * time.Hour
	lastUsedUpdateInterval = 5 * time.Minute
	touchWriteTimeout      = 3 * time.Second
)

// Failure reasons surfaced as stable machine-readable API error codes.
var (
	ErrInvalid         = errors.New("api key is invalid")
	ErrExpired         = errors.New("api key has expired")
	ErrRevoked         = errors.New("api key has been revoked")
	ErrDisabled        = errors.New("api key is disabled")
	ErrCreatorInactive = errors.New("api key creator is inactive")
)

// ErrNotFound reports a missing key row for management operations.
var ErrNotFound = errors.New("api key not found")

// ErrClusterNotFound reports a missing target cluster during key creation.
var ErrClusterNotFound = errors.New("cluster not found")

// ErrNameTaken reports a duplicate (cluster, name) pair.
var ErrNameTaken = errors.New("api key name is already in use")

// Validation failures returned by lifecycle operations. HTTP callers map
// these sentinels to fixed public messages without inspecting error text.
var (
	ErrInvalidName          = errors.New("api key name must contain between 1 and 80 characters")
	ErrPermissionsRequired  = errors.New("at least one api key permission is required")
	ErrPermissionNotAllowed = errors.New("permission cannot be granted to an api key")
	ErrInvalidExpiry        = errors.New("api key expiry must be in the future")
	ErrKeyLimitReached      = errors.New("cluster api key limit reached")
	ErrInvalidGrace         = errors.New("api key rotation grace is out of range")
)

const apiKeyNameConstraint = "cluster_api_keys_cluster_id_name_key"

// Service verifies tokens and manages the key lifecycle.
type Service struct {
	db *client.Client

	touchMu        sync.Mutex
	nextTouch      map[string]time.Time
	lastTouchSweep time.Time
}

func New(db *client.Client) *Service {
	return &Service{db: db, nextTouch: make(map[string]time.Time)}
}

type lockRow struct {
	ID string `db:"id"`
}

type verificationRow struct {
	ID                string             `db:"id"`
	ClusterID         string             `db:"cluster_id"`
	Name              string             `db:"name"`
	Prefix            string             `db:"prefix"`
	TokenHash         string             `db:"token_hash"`
	PreviousTokenHash *string            `db:"previous_token_hash"`
	PreviousExpiresAt *time.Time         `db:"previous_expires_at"`
	PermissionsJSON   json.RawMessage    `db:"permissions_json"`
	Status            model.ApiKeyStatus `db:"status"`
	ExpiresAt         *time.Time         `db:"expires_at"`
	RevokedAt         *time.Time         `db:"revoked_at"`
	LastUsedAt        *time.Time         `db:"last_used_at"`
	LastUsedIP        *string            `db:"last_used_ip"`
	CreatedBy         string             `db:"created_by"`
	CreatedAt         time.Time          `db:"created_at"`
	UpdatedAt         time.Time          `db:"updated_at"`
	CreatorStatus     model.UserStatus   `db:"creator_status"`
	MatchedPrevious   bool               `db:"matched_previous"`
}

func (row verificationRow) key() *model.ClusterApiKey {
	return &model.ClusterApiKey{
		Id: row.ID, ClusterId: row.ClusterID, Name: row.Name, Prefix: row.Prefix,
		TokenHash: row.TokenHash, PreviousTokenHash: row.PreviousTokenHash,
		PreviousExpiresAt: row.PreviousExpiresAt, PermissionsJson: row.PermissionsJSON,
		Status: row.Status, ExpiresAt: row.ExpiresAt, RevokedAt: row.RevokedAt,
		LastUsedAt: row.LastUsedAt, LastUsedIp: row.LastUsedIP, CreatedBy: row.CreatedBy,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
}

// generateToken produces a new opaque token together with its display prefix
// and storage hash.
func generateToken() (token, prefix, hash string, err error) {
	raw := make([]byte, tokenRandomBytes)
	if _, err = rand.Read(raw); err != nil {
		return "", "", "", err
	}
	token = TokenPrefix + base64.RawURLEncoding.EncodeToString(raw)
	return token, token[:prefixLength], TokenHash(token), nil
}

// TokenHash derives the persisted SHA-256 fingerprint of a token.
func TokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// ValidTokenFormat reports whether a presented credential has the shape of a
// generated token, avoiding database lookups for foreign bearer tokens.
func ValidTokenFormat(token string) bool {
	if !strings.HasPrefix(token, TokenPrefix) {
		return false
	}
	rest := token[len(TokenPrefix):]
	if len(rest) != 43 {
		return false
	}
	for _, r := range rest {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
		default:
			return false
		}
	}
	return true
}

// EncodePermissions validates and serializes permissions for storage. It is
// the single gate for what may land in cluster_api_keys.permissions_json.
func EncodePermissions(permissions []rbac.Permission) (json.RawMessage, error) {
	values := make([]string, len(permissions))
	for index, permission := range permissions {
		if !rbac.KeyPermissionAllowed(permission) {
			return nil, fmt.Errorf("%w: %q", ErrPermissionNotAllowed, permission)
		}
		values[index] = string(permission)
	}
	encoded, err := json.Marshal(values)
	if err != nil {
		return nil, err
	}
	return encoded, nil
}

// PermissionsOf decodes the stored permission list of a key.
func PermissionsOf(key *model.ClusterApiKey) []rbac.Permission {
	var values []string
	if len(key.PermissionsJson) > 0 {
		if err := json.Unmarshal(key.PermissionsJson, &values); err != nil {
			return nil
		}
	}
	permissions := make([]rbac.Permission, 0, len(values))
	for _, value := range values {
		permission := rbac.Permission(value)
		if !rbac.KeyPermissionAllowed(permission) {
			continue
		}
		permissions = append(permissions, permission)
	}
	return permissions
}

// CreateInput describes a new API key.
type CreateInput struct {
	ClusterID   string
	Name        string
	Permissions []rbac.Permission
	ExpiresAt   *time.Time
	CreatedBy   string
}

// Create generates a token, validates the grant and persists the key. The
// returned token is the only time the plaintext ever leaves storage.
func (s *Service) Create(ctx context.Context, input CreateInput) (*model.ClusterApiKey, string, error) {
	name := strings.TrimSpace(input.Name)
	if name == "" || len([]rune(name)) > 80 {
		return nil, "", ErrInvalidName
	}
	if len(input.Permissions) == 0 {
		return nil, "", ErrPermissionsRequired
	}
	if strings.TrimSpace(input.CreatedBy) == "" {
		return nil, "", fmt.Errorf("created_by is required")
	}
	permissions, err := EncodePermissions(input.Permissions)
	if err != nil {
		return nil, "", err
	}
	if input.ExpiresAt != nil && !input.ExpiresAt.After(time.Now().UTC()) {
		return nil, "", ErrInvalidExpiry
	}
	token, prefix, hash, err := generateToken()
	if err != nil {
		return nil, "", err
	}
	sets := []query.ClusterApiKeySetClause{
		query.ClusterApiKey.ClusterId.Set(input.ClusterID),
		query.ClusterApiKey.Name.Set(name),
		query.ClusterApiKey.Prefix.Set(prefix),
		query.ClusterApiKey.TokenHash.Set(hash),
		query.ClusterApiKey.PermissionsJson.Set(permissions),
		query.ClusterApiKey.CreatedBy.Set(input.CreatedBy),
	}
	if input.ExpiresAt != nil {
		sets = append(sets, query.ClusterApiKey.ExpiresAt.Set(*input.ExpiresAt))
	}
	var key *model.ClusterApiKey
	err = s.db.Tx(ctx, func(tx *client.Client) error {
		locked, lockErr := client.Raw[lockRow](ctx, tx,
			"SELECT id FROM clusters WHERE id=$1 FOR UPDATE", input.ClusterID)
		if lockErr != nil {
			return lockErr
		}
		if len(locked) == 0 {
			return ErrClusterNotFound
		}
		if creatorErr := requireActiveCreator(ctx, tx, input.CreatedBy); creatorErr != nil {
			return creatorErr
		}
		now := time.Now().UTC()
		count, countErr := tx.ClusterApiKey.Count(ctx,
			query.ClusterApiKey.ClusterId.Equals(input.ClusterID),
			query.ClusterApiKey.RevokedAt.IsNull(),
			query.ClusterApiKey.OR(
				query.ClusterApiKey.ExpiresAt.IsNull(),
				query.ClusterApiKey.ExpiresAt.Gt(&now),
			),
		)
		if countErr != nil {
			return countErr
		}
		if count >= MaxKeysPerCluster {
			return ErrKeyLimitReached
		}
		key, countErr = tx.ClusterApiKey.Create().Set(sets...).Do(ctx)
		return countErr
	})
	if err != nil {
		if isNameConflict(err) {
			return nil, "", ErrNameTaken
		}
		return nil, "", err
	}
	return key, token, nil
}

// Rotate replaces the token of an active key. The previous token stays valid
// until the grace window closes, so automation can cut over without downtime.
func (s *Service) Rotate(ctx context.Context, clusterID, keyID string, grace time.Duration) (*model.ClusterApiKey, string, error) {
	if grace < 0 || grace > MaxRotationGrace {
		return nil, "", ErrInvalidGrace
	}
	token, _, hash, err := generateToken()
	if err != nil {
		return nil, "", err
	}
	var key *model.ClusterApiKey
	err = s.db.Tx(ctx, func(tx *client.Client) error {
		current, lockErr := lockKey(ctx, tx, clusterID, keyID)
		if lockErr != nil {
			return lockErr
		}
		now := time.Now().UTC()
		if stateErr := verifyState(current, now); stateErr != nil {
			return stateErr
		}
		if creatorErr := requireActiveCreator(ctx, tx, current.CreatedBy); creatorErr != nil {
			return creatorErr
		}
		sets := []query.ClusterApiKeySetClause{
			query.ClusterApiKey.TokenHash.Set(hash),
			query.ClusterApiKey.Prefix.Set(token[:prefixLength]),
		}
		if grace > 0 {
			sets = append(sets,
				query.ClusterApiKey.PreviousTokenHash.Set(current.TokenHash),
				query.ClusterApiKey.PreviousExpiresAt.Set(now.Add(grace)),
			)
		} else {
			sets = append(sets,
				query.ClusterApiKey.PreviousTokenHash.SetNull(),
				query.ClusterApiKey.PreviousExpiresAt.SetNull(),
			)
		}
		var updateErr error
		key, updateErr = tx.ClusterApiKey.UpdateOne(ctx, query.ClusterApiKey.Id.Equals(keyID), sets...)
		return updateErr
	})
	if err != nil {
		return nil, "", err
	}
	return key, token, nil
}

// Update applies management changes while holding the same row lock used by
// rotate and revoke. The returned before value is suitable for audit diffs.
func (s *Service) Update(ctx context.Context, clusterID, keyID string, sets ...query.ClusterApiKeySetClause) (*model.ClusterApiKey, *model.ClusterApiKey, error) {
	var before, updated *model.ClusterApiKey
	err := s.db.Tx(ctx, func(tx *client.Client) error {
		current, lockErr := lockKey(ctx, tx, clusterID, keyID)
		if lockErr != nil {
			return lockErr
		}
		if current.RevokedAt != nil {
			return ErrRevoked
		}
		before = current
		updated, lockErr = tx.ClusterApiKey.UpdateOne(ctx, query.ClusterApiKey.Id.Equals(keyID), sets...)
		return lockErr
	})
	if err != nil {
		if isNameConflict(err) {
			return nil, nil, ErrNameTaken
		}
		return nil, nil, err
	}
	return before, updated, nil
}

// Revoke permanently invalidates a key. The row is retained for audit trails.
func (s *Service) Revoke(ctx context.Context, clusterID, keyID string) (*model.ClusterApiKey, error) {
	var key *model.ClusterApiKey
	err := s.db.Tx(ctx, func(tx *client.Client) error {
		current, lockErr := lockKey(ctx, tx, clusterID, keyID)
		if lockErr != nil {
			return lockErr
		}
		if current.RevokedAt != nil {
			key = current
			return nil
		}
		key, lockErr = tx.ClusterApiKey.UpdateOne(ctx, query.ClusterApiKey.Id.Equals(keyID),
			query.ClusterApiKey.RevokedAt.Set(time.Now().UTC()),
			query.ClusterApiKey.Status.Set(model.ApiKeyStatusDISABLED),
			query.ClusterApiKey.PreviousTokenHash.SetNull(),
			query.ClusterApiKey.PreviousExpiresAt.SetNull(),
		)
		return lockErr
	})
	if err != nil {
		return nil, err
	}
	return key, nil
}

// verifyState evaluates a loaded key row against the current time and returns
// the failure reason when the key must not authenticate.
func verifyState(key *model.ClusterApiKey, now time.Time) error {
	if key.RevokedAt != nil {
		return ErrRevoked
	}
	if key.Status != model.ApiKeyStatusACTIVE {
		return ErrDisabled
	}
	if key.ExpiresAt != nil && !now.Before(*key.ExpiresAt) {
		return ErrExpired
	}
	return nil
}

// Verify resolves a presented token to its key row. Both the current and the
// rotation-grace token hashes are accepted. The caller is responsible for
// throttled lastUsed bookkeeping via Touch.
func (s *Service) Verify(ctx context.Context, token string) (*model.ClusterApiKey, error) {
	if !ValidTokenFormat(token) {
		return nil, ErrInvalid
	}
	hash := TokenHash(token)
	now := time.Now().UTC()

	rows, err := client.Raw[verificationRow](ctx, s.db, `SELECT
		k.id, k.cluster_id, k.name, k.prefix, k.token_hash,
		k.previous_token_hash, k.previous_expires_at, k.permissions_json,
		k.status, k.expires_at, k.revoked_at, k.last_used_at, k.last_used_ip,
		k.created_by, k.created_at, k.updated_at,
		COALESCE(u.status, '') AS creator_status,
		COALESCE(k.previous_token_hash = $1, FALSE) AS matched_previous
		FROM cluster_api_keys AS k
		LEFT JOIN users AS u ON u.id = k.created_by
		WHERE k.token_hash = $1 OR k.previous_token_hash = $1
		ORDER BY CASE WHEN k.token_hash = $1 THEN 0 ELSE 1 END
		LIMIT 1`, hash)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, ErrInvalid
	}
	row := rows[0]
	key := row.key()
	if stateErr := verifyState(key, now); stateErr != nil {
		return nil, stateErr
	}
	if row.CreatorStatus != model.UserStatusACTIVE {
		return nil, ErrCreatorInactive
	}
	if row.MatchedPrevious && (key.PreviousExpiresAt == nil || !now.Before(*key.PreviousExpiresAt)) {
		return nil, ErrInvalid
	}
	return key, nil
}

func lockKey(ctx context.Context, tx *client.Client, clusterID, keyID string) (*model.ClusterApiKey, error) {
	locked, err := client.Raw[lockRow](ctx, tx,
		"SELECT id FROM cluster_api_keys WHERE id=$1 AND cluster_id=$2 FOR UPDATE", keyID, clusterID)
	if err != nil {
		return nil, err
	}
	if len(locked) == 0 {
		return nil, ErrNotFound
	}
	key, err := tx.ClusterApiKey.FindUnique(ctx, query.ClusterApiKey.Id.Equals(keyID))
	if err != nil {
		return nil, err
	}
	if key == nil {
		return nil, ErrNotFound
	}
	return key, nil
}

func requireActiveCreator(ctx context.Context, db *client.Client, userID string) error {
	user, err := db.User.FindUnique(ctx, query.User.Id.Equals(userID))
	if err != nil {
		return err
	}
	if user == nil || user.Status != model.UserStatusACTIVE {
		return ErrCreatorInactive
	}
	return nil
}

func isNameConflict(err error) bool {
	var postgresError *pgconn.PgError
	return errors.As(err, &postgresError) && postgresError.Code == "23505" &&
		postgresError.ConstraintName == apiKeyNameConstraint
}

// Touch records usage asynchronously and reserves one write per key per
// interval. Authentication never waits for this best-effort bookkeeping.
func (s *Service) Touch(keyID, ip string) {
	now := time.Now().UTC()
	reservedUntil, ok := s.reserveTouch(keyID, now)
	if !ok {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), touchWriteTimeout)
		defer cancel()
		sets := []query.ClusterApiKeySetClause{query.ClusterApiKey.LastUsedAt.Set(now)}
		if ip != "" {
			sets = append(sets, query.ClusterApiKey.LastUsedIp.Set(ip))
		}
		if _, err := s.db.ClusterApiKey.UpdateOne(ctx, query.ClusterApiKey.Id.Equals(keyID), sets...); err != nil {
			s.releaseTouch(keyID, reservedUntil)
		}
	}()
}

func (s *Service) reserveTouch(keyID string, now time.Time) (time.Time, bool) {
	s.touchMu.Lock()
	defer s.touchMu.Unlock()
	if s.nextTouch == nil {
		s.nextTouch = make(map[string]time.Time)
	}
	if s.lastTouchSweep.IsZero() || now.Sub(s.lastTouchSweep) >= lastUsedUpdateInterval {
		for id, next := range s.nextTouch {
			if !now.Before(next) {
				delete(s.nextTouch, id)
			}
		}
		s.lastTouchSweep = now
	}
	if next, found := s.nextTouch[keyID]; found && now.Before(next) {
		return time.Time{}, false
	}
	next := now.Add(lastUsedUpdateInterval)
	s.nextTouch[keyID] = next
	return next, true
}

func (s *Service) releaseTouch(keyID string, reservedUntil time.Time) {
	s.touchMu.Lock()
	defer s.touchMu.Unlock()
	if s.nextTouch[keyID] == reservedUntil {
		delete(s.nextTouch, keyID)
	}
}
