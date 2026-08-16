// Package apikeys registers cluster API key management endpoints.
package apikeys

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v5"

	"goveto-edge/internal/apikey"
	"goveto-edge/internal/audit"
	authn "goveto-edge/internal/auth"
	"goveto-edge/internal/clusteraccess"
	"goveto-edge/internal/httpapi/types"
	"goveto-edge/internal/httpsecurity"
	"goveto-edge/internal/rbac"
	"goveto-edge/internal/storage/gen/client"
	"goveto-edge/internal/storage/gen/model"
	"goveto-edge/internal/storage/gen/query"
)

// apiKeyResource is the safe projection of a key: no hashes, no secrets.
type apiKeyResource struct {
	ID          string             `json:"id"`
	Name        string             `json:"name"`
	Prefix      string             `json:"prefix"`
	Permissions []string           `json:"permissions"`
	Status      model.ApiKeyStatus `json:"status"`
	ExpiresAt   *time.Time         `json:"expires_at,omitempty"`
	RevokedAt   *time.Time         `json:"revoked_at,omitempty"`
	LastUsedAt  *time.Time         `json:"last_used_at,omitempty"`
	LastUsedIP  string             `json:"last_used_ip,omitempty"`
	CreatedBy   string             `json:"created_by"`
	CreatedAt   time.Time          `json:"created_at"`
	UpdatedAt   time.Time          `json:"updated_at"`
}

type apiKeyListResponse struct {
	Items    []apiKeyResource `json:"items"`
	Total    int64            `json:"total"`
	Page     int              `json:"page"`
	PageSize int              `json:"page_size"`
}

// createResponse carries the plaintext token exactly once. The audit
// middleware redacts the token field from snapshots automatically.
type createResponse struct {
	apiKeyResource
	Token string `json:"token"`
}

type createRequest struct {
	Name          string   `json:"name"`
	Permissions   []string `json:"permissions"`
	ExpiresAt     *string  `json:"expires_at,omitempty"`
	ExpiresInDays *int     `json:"expires_in_days,omitempty"`
}

type updateRequest struct {
	Name          *string   `json:"name,omitempty"`
	Permissions   *[]string `json:"permissions,omitempty"`
	Status        *string   `json:"status,omitempty"`
	ExpiresAt     *string   `json:"expires_at,omitempty"`
	ExpiresInDays *int      `json:"expires_in_days,omitempty"`
	ClearExpiry   *bool     `json:"clear_expiry,omitempty"`
}

type rotateRequest struct {
	GraceSeconds *int `json:"grace_seconds,omitempty"`
}

type apiKeyUpdater interface {
	Update(context.Context, string, string, ...query.ClusterApiKeySetClause) (*model.ClusterApiKey, *model.ClusterApiKey, error)
}

func Register(e *echo.Echo, db *client.Client, service *apikey.Service, limiter *httpsecurity.RateLimiter) {
	manage := clusteraccess.RequirePermission(db, rbac.PermissionAPIKeyManage)
	group := e.Group("/api/v1/clusters/:cluster_id/api-keys", authn.RequireUser, manage)
	// Mutations are throttled per session user; API keys cannot reach this
	// group at all because the manage permission is not grantable to them.
	mutationLimit := limiter.LimitKeyed("api-key-manage", 30, time.Minute, func(c *echo.Context) string {
		if uid := authn.CurrentUID(c); uid != "" {
			return uid
		}
		return c.RealIP()
	})
	group.GET("", list(db))
	group.POST("", create(service), mutationLimit)
	group.PATCH("/:key_id", update(service), mutationLimit)
	group.POST("/:key_id/rotate", rotate(service), mutationLimit)
	group.DELETE("/:key_id", revoke(service), mutationLimit)
}

// @summary List API keys
// @description Paginated list of cluster API keys with permissions, status and last usage; token material is never returned.
// @Tags clusters-api-keys
func list(db *client.Client) echo.HandlerFunc {
	return func(c *echo.Context) error {
		page := positiveInt(c.QueryParam("page"), 1, 0)
		pageSize := positiveInt(c.QueryParam("page_size"), 50, 200)
		wheres := []query.ClusterApiKeyWhereClause{
			query.ClusterApiKey.ClusterId.Equals(c.Param("cluster_id")),
		}
		if value := strings.ToUpper(strings.TrimSpace(c.QueryParam("status"))); value != "" {
			status := model.ApiKeyStatus(value)
			if status != model.ApiKeyStatusACTIVE && status != model.ApiKeyStatusDISABLED {
				return echo.NewHTTPError(http.StatusBadRequest, "invalid status")
			}
			wheres = append(wheres, query.ClusterApiKey.Status.Equals(status))
		}
		builder := db.ClusterApiKey.Query().Where(wheres...).OrderBy(query.ClusterApiKey.CreatedAt.Desc())
		total, err := builder.Count(c.Request().Context())
		if err != nil {
			return err
		}
		items, err := builder.Skip((page - 1) * pageSize).Take(pageSize).Do(c.Request().Context())
		if err != nil {
			return err
		}
		resources := make([]apiKeyResource, len(items))
		for index := range items {
			resources[index] = newResource(&items[index])
		}
		return types.JSON(c, http.StatusOK, apiKeyListResponse{
			Items: resources, Total: total, Page: page, PageSize: pageSize,
		})
	}
}

// @summary Create API key
// @description Create a cluster API key with a whitelisted permission subset; the plaintext token is returned exactly once.
// @Tags clusters-api-keys
func create(service *apikey.Service) echo.HandlerFunc {
	return func(c *echo.Context) error {
		var input createRequest
		if err := c.Bind(&input); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid request body")
		}
		permissions, err := parsePermissions(input.Permissions)
		if err != nil {
			return err
		}
		expiresAt, err := resolveExpiry(input.ExpiresAt, input.ExpiresInDays)
		if err != nil {
			return err
		}
		key, token, err := service.Create(c.Request().Context(), apikey.CreateInput{
			ClusterID:   c.Param("cluster_id"),
			Name:        input.Name,
			Permissions: permissions,
			ExpiresAt:   expiresAt,
			CreatedBy:   authn.CurrentUID(c),
		})
		if err != nil {
			return translateError(err)
		}
		response := createResponse{apiKeyResource: newResource(key), Token: token}
		audit.SetResourceID(c, key.Id)
		audit.SetChange(c, nil, response.apiKeyResource)
		return types.JSON(c, http.StatusCreated, response)
	}
}

// @summary Update API key
// @description Update name, permissions, status or expiry of a cluster API key; revoked keys are immutable.
// @Tags clusters-api-keys
func update(service apiKeyUpdater) echo.HandlerFunc {
	return func(c *echo.Context) error {
		var input updateRequest
		if err := c.Bind(&input); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid request body")
		}
		sets := []query.ClusterApiKeySetClause{}
		if input.Name != nil {
			name := strings.TrimSpace(*input.Name)
			if name == "" || len([]rune(name)) > 80 {
				return echo.NewHTTPError(http.StatusBadRequest, "name must contain between 1 and 80 characters")
			}
			sets = append(sets, query.ClusterApiKey.Name.Set(name))
		}
		if input.Permissions != nil {
			permissions, permErr := parsePermissions(*input.Permissions)
			if permErr != nil {
				return permErr
			}
			encoded, encErr := apikey.EncodePermissions(permissions)
			if encErr != nil {
				return translateError(encErr)
			}
			sets = append(sets, query.ClusterApiKey.PermissionsJson.Set(encoded))
		}
		if input.Status != nil {
			status := model.ApiKeyStatus(strings.ToUpper(strings.TrimSpace(*input.Status)))
			if status != model.ApiKeyStatusACTIVE && status != model.ApiKeyStatusDISABLED {
				return echo.NewHTTPError(http.StatusBadRequest, "status must be ACTIVE or DISABLED")
			}
			sets = append(sets, statusSets(status)...)
		}
		clearExpiry := input.ClearExpiry != nil && *input.ClearExpiry
		if clearExpiry && (input.ExpiresAt != nil || input.ExpiresInDays != nil) {
			return echo.NewHTTPError(http.StatusBadRequest,
				"clear_expiry cannot be combined with expires_at or expires_in_days")
		}
		if clearExpiry {
			sets = append(sets, query.ClusterApiKey.ExpiresAt.SetNull())
		} else {
			expiresAt, expErr := resolveExpiry(input.ExpiresAt, input.ExpiresInDays)
			if expErr != nil {
				return expErr
			}
			if expiresAt != nil {
				sets = append(sets, query.ClusterApiKey.ExpiresAt.Set(*expiresAt))
			}
		}
		if len(sets) == 0 {
			return echo.NewHTTPError(http.StatusBadRequest, "no fields to update")
		}
		before, key, err := service.Update(c.Request().Context(), c.Param("cluster_id"), c.Param("key_id"), sets...)
		if err != nil {
			return translateError(err)
		}
		response := newResource(key)
		audit.SetResourceID(c, key.Id)
		audit.SetChange(c, newResource(before), response)
		return types.JSON(c, http.StatusOK, response)
	}
}

func statusSets(status model.ApiKeyStatus) []query.ClusterApiKeySetClause {
	sets := []query.ClusterApiKeySetClause{query.ClusterApiKey.Status.Set(status)}
	if status == model.ApiKeyStatusDISABLED {
		sets = append(sets,
			query.ClusterApiKey.PreviousTokenHash.SetNull(),
			query.ClusterApiKey.PreviousExpiresAt.SetNull(),
		)
	}
	return sets
}

// @summary Rotate API key
// @description Replace the token of an API key; the previous token stays valid for a bounded grace window.
// @Tags clusters-api-keys
func rotate(service *apikey.Service) echo.HandlerFunc {
	return func(c *echo.Context) error {
		var input rotateRequest
		if err := c.Bind(&input); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid request body")
		}
		grace, err := resolveRotationGrace(input.GraceSeconds)
		if err != nil {
			return err
		}
		key, token, err := service.Rotate(c.Request().Context(), c.Param("cluster_id"), c.Param("key_id"), grace)
		if err != nil {
			return translateError(err)
		}
		response := createResponse{apiKeyResource: newResource(key), Token: token}
		audit.SetResourceID(c, key.Id)
		audit.SetChange(c, nil, response.apiKeyResource)
		return types.JSON(c, http.StatusOK, response)
	}
}

func resolveRotationGrace(seconds *int) (time.Duration, error) {
	if seconds == nil {
		return apikey.DefaultRotationGrace, nil
	}
	value := int64(*seconds)
	if value < 0 || value > int64(apikey.MaxRotationGrace/time.Second) {
		return 0, echo.NewHTTPError(http.StatusBadRequest, rotationGraceMessage())
	}
	return time.Duration(value) * time.Second, nil
}

func rotationGraceMessage() string {
	return fmt.Sprintf("grace_seconds must be between 0 and %d", apikey.MaxRotationGrace/time.Second)
}

// @summary Revoke API key
// @description Permanently revoke an API key; the row is retained for audit trails.
// @Tags clusters-api-keys
func revoke(service *apikey.Service) echo.HandlerFunc {
	return func(c *echo.Context) error {
		key, err := service.Revoke(c.Request().Context(), c.Param("cluster_id"), c.Param("key_id"))
		if err != nil {
			return translateError(err)
		}
		response := newResource(key)
		audit.SetResourceID(c, key.Id)
		audit.SetChange(c, nil, response)
		return types.JSON(c, http.StatusOK, response)
	}
}

func newResource(key *model.ClusterApiKey) apiKeyResource {
	permissions := apikey.PermissionsOf(key)
	values := make([]string, len(permissions))
	for index, permission := range permissions {
		values[index] = string(permission)
	}
	if values == nil {
		values = []string{}
	}
	var lastUsedIP string
	if key.LastUsedIp != nil {
		lastUsedIP = *key.LastUsedIp
	}
	return apiKeyResource{
		ID: key.Id, Name: key.Name, Prefix: key.Prefix, Permissions: values,
		Status: key.Status, ExpiresAt: key.ExpiresAt, RevokedAt: key.RevokedAt,
		LastUsedAt: key.LastUsedAt, LastUsedIP: lastUsedIP,
		CreatedBy: key.CreatedBy, CreatedAt: key.CreatedAt, UpdatedAt: key.UpdatedAt,
	}
}

// parsePermissions validates a requested permission list against the closed
// whitelist of key-grantable permissions.
func parsePermissions(values []string) ([]rbac.Permission, error) {
	if len(values) == 0 {
		return nil, echo.NewHTTPError(http.StatusBadRequest, "at least one permission is required")
	}
	permissions := make([]rbac.Permission, 0, len(values))
	seen := make(map[rbac.Permission]struct{}, len(values))
	for _, value := range values {
		permission := rbac.Permission(strings.TrimSpace(value))
		if !rbac.KeyPermissionAllowed(permission) {
			return nil, echo.NewHTTPError(http.StatusBadRequest,
				"permission cannot be granted to an api key: "+string(permission))
		}
		if _, duplicate := seen[permission]; duplicate {
			continue
		}
		seen[permission] = struct{}{}
		permissions = append(permissions, permission)
	}
	if len(permissions) == 0 {
		return nil, echo.NewHTTPError(http.StatusBadRequest, "at least one permission is required")
	}
	return permissions, nil
}

// resolveExpiry accepts either an absolute timestamp or a relative day count,
// rejecting requests that provide both.
func resolveExpiry(expiresAt *string, expiresInDays *int) (*time.Time, error) {
	if expiresAt != nil && expiresInDays != nil {
		return nil, echo.NewHTTPError(http.StatusBadRequest, "provide either expires_at or expires_in_days, not both")
	}
	if expiresAt != nil {
		parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(*expiresAt))
		if err != nil {
			return nil, echo.NewHTTPError(http.StatusBadRequest, "expires_at must be an RFC 3339 timestamp")
		}
		parsed = parsed.UTC()
		if !parsed.After(time.Now().UTC()) {
			return nil, echo.NewHTTPError(http.StatusBadRequest, "expires_at must be in the future")
		}
		return &parsed, nil
	}
	if expiresInDays != nil {
		if *expiresInDays < 1 || *expiresInDays > 3650 {
			return nil, echo.NewHTTPError(http.StatusBadRequest, "expires_in_days must be between 1 and 3650")
		}
		expires := time.Now().UTC().AddDate(0, 0, *expiresInDays)
		return &expires, nil
	}
	return nil, nil
}

func translateError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, apikey.ErrInvalidName):
		return echo.NewHTTPError(http.StatusBadRequest, "name must contain between 1 and 80 characters")
	case errors.Is(err, apikey.ErrPermissionsRequired):
		return echo.NewHTTPError(http.StatusBadRequest, "at least one permission is required")
	case errors.Is(err, apikey.ErrPermissionNotAllowed):
		return echo.NewHTTPError(http.StatusBadRequest, "one or more permissions cannot be granted to an api key")
	case errors.Is(err, apikey.ErrInvalidExpiry):
		return echo.NewHTTPError(http.StatusBadRequest, "expires_at must be in the future")
	case errors.Is(err, apikey.ErrKeyLimitReached):
		return echo.NewHTTPError(http.StatusConflict, "cluster already holds the maximum number of api keys")
	case errors.Is(err, apikey.ErrInvalidGrace):
		return echo.NewHTTPError(http.StatusBadRequest, rotationGraceMessage())
	case errors.Is(err, apikey.ErrNameTaken):
		return echo.NewHTTPError(http.StatusConflict, "api key name is already in use")
	case errors.Is(err, apikey.ErrClusterNotFound):
		return echo.NewHTTPError(http.StatusNotFound, "cluster not found")
	case errors.Is(err, apikey.ErrNotFound):
		return echo.NewHTTPError(http.StatusNotFound, "api key not found")
	case errors.Is(err, apikey.ErrRevoked):
		return echo.NewHTTPError(http.StatusConflict, "api key has been revoked")
	case errors.Is(err, apikey.ErrDisabled):
		return echo.NewHTTPError(http.StatusConflict, "api key is disabled")
	case errors.Is(err, apikey.ErrExpired):
		return echo.NewHTTPError(http.StatusConflict, "api key has expired")
	case errors.Is(err, apikey.ErrCreatorInactive):
		return echo.NewHTTPError(http.StatusConflict, "api key creator is inactive")
	default:
		return err
	}
}

func positiveInt(raw string, fallback, maximum int) int {
	value, err := strconv.Atoi(raw)
	if err != nil || value < 1 || (maximum > 0 && value > maximum) {
		return fallback
	}
	return value
}
