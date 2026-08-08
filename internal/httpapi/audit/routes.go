// Package auditapi exposes read access to the audit log. Only principals who
// hold the platform-level audit-read permission (instance administrators) may
// query it, so the recorded security events remain visible to operators
// without leaking to ordinary cluster members.
package auditapi

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v5"

	authn "goveto-edge/internal/auth"
	"goveto-edge/internal/clusteraccess"
	"goveto-edge/internal/httpapi/types"
	"goveto-edge/internal/rbac"
	"goveto-edge/internal/storage/gen/client"
	"goveto-edge/internal/storage/gen/model"
	"goveto-edge/internal/storage/gen/query"
)

// Register mounts the audit query endpoints on the control-plane echo
// instance. The platform permission gate replaces the previously dangling
// platform.audit.read constant.
func Register(e *echo.Echo, db *client.Client) {
	guard := []echo.MiddlewareFunc{
		authn.RequireAuth,
		clusteraccess.RequirePlatform(db, rbac.PermissionPlatformAuditRead),
	}
	e.GET("/api/v1/audit/events", listEvents(db), guard...)
}

type userSummary struct {
	ID    string `json:"id"`
	Email string `json:"email"`
	Name  string `json:"name"`
}

type auditEvent struct {
	ID            string          `json:"id"`
	ActorID       string          `json:"actor_id,omitempty"`
	Actor         string          `json:"actor"`
	User          *userSummary    `json:"user,omitempty"`
	SourceIP      string          `json:"source_ip"`
	UserAgent     string          `json:"user_agent"`
	Action        string          `json:"action"`
	ResourceType  string          `json:"resource_type"`
	ResourceID    string          `json:"resource_id"`
	Before        json.RawMessage `json:"before,omitempty"`
	After         json.RawMessage `json:"after,omitempty"`
	RequestID     string          `json:"request_id"`
	Result        string          `json:"result"`
	FailureReason string          `json:"failure_reason,omitempty"`
	CreatedAt     time.Time       `json:"created_at"`
}

type auditListResponse struct {
	Items []auditEvent `json:"items"`
	Total int64        `json:"total"`
	Page  int          `json:"page"`
	Size  int          `json:"page_size"`
}

// listEvents returns a paginated, filterable view of the audit log. It is a
// read-only surface that complements the existing write recorder.
//
// @id listAuditEvents
// @summary List audit events
// @description Paginated audit log query with filters by actor, action and resource; requires the platform audit-read permission.
// @Tags audit
func listEvents(db *client.Client) echo.HandlerFunc {
	return func(c *echo.Context) error {
		page := 1
		if value, err := strconv.Atoi(c.QueryParam("page")); err == nil && value > 0 {
			page = value
		}
		size := 100
		if value, err := strconv.Atoi(c.QueryParam("page_size")); err == nil && value > 0 && value <= 500 {
			size = value
		}

		wheres := buildAuditFilters(c)
		builder := db.AuditLog.Query().Where(wheres...).OrderBy(query.AuditLog.CreatedAt.Desc())
		total, err := builder.Count(c.Request().Context())
		if err != nil {
			return err
		}
		items, err := builder.Skip((page - 1) * size).Take(size).Include(query.AuditLog.User.Fetch()).Do(c.Request().Context())
		if err != nil {
			return err
		}
		events := make([]auditEvent, 0, len(items))
		for index := range items {
			events = append(events, toAuditEvent(&items[index]))
		}
		return types.JSON(c, http.StatusOK, auditListResponse{
			Items: events, Total: total, Page: page, Size: size,
		})
	}
}

func buildAuditFilters(c *echo.Context) []query.AuditLogWhereClause {
	var wheres []query.AuditLogWhereClause
	if actor := strings.TrimSpace(c.QueryParam("actor")); actor != "" {
		wheres = append(wheres, query.AuditLog.Actor.Contains(actor))
	}
	if action := strings.TrimSpace(c.QueryParam("action")); action != "" {
		wheres = append(wheres, query.AuditLog.Action.Equals(action))
	}
	if resourceType := strings.TrimSpace(c.QueryParam("resource_type")); resourceType != "" {
		wheres = append(wheres, query.AuditLog.ResourceType.Equals(resourceType))
	}
	if resourceID := strings.TrimSpace(c.QueryParam("resource_id")); resourceID != "" {
		wheres = append(wheres, query.AuditLog.ResourceId.Equals(resourceID))
	}
	if requestID := strings.TrimSpace(c.QueryParam("request_id")); requestID != "" {
		wheres = append(wheres, query.AuditLog.RequestId.Equals(requestID))
	}
	if result := strings.TrimSpace(c.QueryParam("result")); result != "" {
		wheres = append(wheres, query.AuditLog.Result.Equals(result))
	}
	if from := parseTimeQuery(c, "from"); !from.IsZero() {
		wheres = append(wheres, query.AuditLog.CreatedAt.Gte(from))
	}
	if to := parseTimeQuery(c, "to"); !to.IsZero() {
		wheres = append(wheres, query.AuditLog.CreatedAt.Lt(to))
	}
	return wheres
}

func parseTimeQuery(c *echo.Context, name string) time.Time {
	value := strings.TrimSpace(c.QueryParam(name))
	if value == "" {
		return time.Time{}
	}
	// Accept RFC3339 first, then the date-only form (2006-01-02).
	if parsed, err := time.Parse(time.RFC3339, value); err == nil {
		return parsed
	}
	if parsed, err := time.Parse("2006-01-02", value); err == nil {
		if name == "to" {
			return parsed.AddDate(0, 0, 1)
		}
		return parsed
	}
	return time.Time{}
}

func toAuditEvent(entry *model.AuditLog) auditEvent {
	event := auditEvent{
		ID:           entry.Id,
		Actor:        entry.Actor,
		SourceIP:     entry.SourceIp,
		UserAgent:    entry.UserAgent,
		Action:       entry.Action,
		ResourceType: entry.ResourceType,
		ResourceID:   entry.ResourceId,
		RequestID:    entry.RequestId,
		Result:       entry.Result,
		CreatedAt:    entry.CreatedAt,
	}
	if entry.ActorId != nil {
		event.ActorID = *entry.ActorId
	}
	if entry.BeforeJson != nil {
		event.Before = json.RawMessage(*entry.BeforeJson)
	}
	if entry.AfterJson != nil {
		event.After = json.RawMessage(*entry.AfterJson)
	}
	if entry.FailureReason != nil {
		event.FailureReason = *entry.FailureReason
	}
	if entry.User != nil {
		event.User = &userSummary{ID: entry.User.Id, Email: entry.User.Email, Name: entry.User.Name}
	}
	return event
}

// authn keeps the import stable even if future helpers move; RequireAuth is
// provided by the auth package in tests.
var _ = authn.RequireAuth
