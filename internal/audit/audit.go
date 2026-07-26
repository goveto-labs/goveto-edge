// Package audit records security-relevant control-plane activity.
package audit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"

	"goveto-edge/internal/auth"
	"goveto-edge/internal/storage/gen/client"
	"goveto-edge/internal/storage/gen/query"
)

const (
	ResultSuccess = "SUCCESS"
	ResultFailure = "FAILURE"

	requestIDHeader = "X-Request-ID"
	stateKey        = "audit.state"
	maxMetadata     = 2048
	maxFailure      = 8192
	maxRequestBody  = 1 << 20
	auditWriteLimit = 3 * time.Second
)

var safeRequestID = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

// Entry is the storage-neutral representation of one audit event.
type Entry struct {
	ActorID       string
	Actor         string
	SourceIP      string
	UserAgent     string
	Action        string
	ResourceType  string
	ResourceID    string
	Before        json.RawMessage
	After         json.RawMessage
	RequestID     string
	Result        string
	FailureReason string
}

// Recorder persists audit entries.
type Recorder interface {
	Record(context.Context, Entry) error
}

// DBRecorder writes audit entries through the generated ORM client.
type DBRecorder struct{ db *client.Client }

func New(db *client.Client) *DBRecorder { return &DBRecorder{db: db} }

func (recorder *DBRecorder) Record(ctx context.Context, entry Entry) error {
	sets := []query.AuditLogSetClause{
		query.AuditLog.Actor.Set(nonempty(entry.Actor, "anonymous")),
		query.AuditLog.SourceIp.Set(nonempty(entry.SourceIP, "unknown")),
		query.AuditLog.UserAgent.Set(limit(entry.UserAgent, maxMetadata)),
		query.AuditLog.Action.Set(entry.Action),
		query.AuditLog.ResourceType.Set(entry.ResourceType),
		query.AuditLog.ResourceId.Set(nonempty(entry.ResourceID, "unknown")),
		query.AuditLog.RequestId.Set(nonempty(entry.RequestID, uuid.NewString())),
		query.AuditLog.Result.Set(nonempty(entry.Result, ResultSuccess)),
	}
	if entry.ActorID != "" {
		sets = append(sets, query.AuditLog.ActorId.Set(entry.ActorID))
	}
	if len(entry.Before) > 0 {
		sets = append(sets, query.AuditLog.BeforeJson.Set(entry.Before))
	}
	if len(entry.After) > 0 {
		sets = append(sets, query.AuditLog.AfterJson.Set(entry.After))
	}
	if entry.FailureReason != "" {
		sets = append(sets, query.AuditLog.FailureReason.Set(limit(entry.FailureReason, maxFailure)))
	}
	_, err := recorder.db.AuditLog.Create().Set(sets...).Do(ctx)
	return err
}

// Route identifies one audited HTTP mutation.
type Route struct {
	Method        string
	Path          string
	Action        string
	ResourceType  string
	ResourceParam string
}

type requestState struct {
	entry Entry
}

type requestStateContextKey struct{}

// Middleware records every configured mutation, including handler failures.
func Middleware(recorder Recorder, routes []Route) echo.MiddlewareFunc {
	configured := make(map[string]Route, len(routes))
	for _, route := range routes {
		configured[route.Method+" "+route.Path] = route
	}
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			requestID := strings.TrimSpace(c.Request().Header.Get(requestIDHeader))
			if !safeRequestID.MatchString(requestID) {
				requestID = uuid.NewString()
			}
			c.Response().Header().Set(requestIDHeader, requestID)

			route, audited := configured[c.Request().Method+" "+c.Path()]
			if !audited {
				return next(c)
			}
			resourceID := ""
			if route.ResourceParam != "" {
				resourceID = c.Param(route.ResourceParam)
			}
			state := &requestState{entry: Entry{
				SourceIP: c.RealIP(), UserAgent: c.Request().UserAgent(), RequestID: requestID,
				Action: route.Action, ResourceType: route.ResourceType, ResourceID: resourceID,
			}}
			state.entry.After = requestSnapshot(c.Request())
			populatePrincipal(c, &state.entry)
			c.Set(stateKey, state)
			ctx := context.WithValue(c.Request().Context(), requestStateContextKey{}, state)
			c.SetRequest(c.Request().WithContext(ctx))

			err := next(c)
			populatePrincipal(c, &state.entry)
			status := 0
			if response, ok := c.Response().(*echo.Response); ok {
				status = response.Status
			}
			if err != nil || status >= http.StatusBadRequest {
				state.entry.Result = ResultFailure
				state.entry.FailureReason = failureReason(err, status)
			} else {
				state.entry.Result = ResultSuccess
			}
			recordCtx, cancel := context.WithTimeout(context.WithoutCancel(c.Request().Context()), auditWriteLimit)
			recordErr := recorder.Record(recordCtx, state.entry)
			cancel()
			if recordErr != nil {
				slog.Error("write audit log", "action", state.entry.Action, "request_id", requestID, "error", recordErr)
			}
			return err
		}
	}
}

// SetActor overrides the actor inferred from the authenticated request. It is
// used by login and registration before a session principal exists.
func SetActor(c *echo.Context, actorID, actor string) {
	if state := getState(c); state != nil {
		state.entry.ActorID = actorID
		state.entry.Actor = limit(actor, maxMetadata)
	}
}

// SetResourceID supplies an ID that was created or resolved by the handler.
func SetResourceID(c *echo.Context, resourceID string) {
	if state := getState(c); state != nil {
		state.entry.ResourceID = resourceID
	}
}

// SetChange stores sanitized before and after snapshots for the mutation.
func SetChange(c *echo.Context, before, after any) {
	if state := getState(c); state != nil {
		state.entry.Before = snapshot(before)
		state.entry.After = snapshot(after)
	}
}

// RecordSystem records a non-HTTP background operation.
func RecordSystem(ctx context.Context, recorder Recorder, action, resourceType, resourceID string, before, after any, operationErr error) {
	entry := Entry{
		Actor: "system", SourceIP: "internal", UserAgent: "goveto-edge",
		Action: action, ResourceType: resourceType, ResourceID: resourceID,
		Before: snapshot(before), After: snapshot(after), RequestID: uuid.NewString(), Result: ResultSuccess,
	}
	if operationErr != nil {
		entry.Result = ResultFailure
		entry.FailureReason = limit(operationErr.Error(), maxFailure)
	}
	recordCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), auditWriteLimit)
	err := recorder.Record(recordCtx, entry)
	cancel()
	if err != nil {
		slog.Error("write system audit log", "action", action, "resource_id", resourceID, "error", err)
	}
}

// RecordContext records a service-layer mutation while inheriting HTTP request
// metadata when available and falling back to a system principal otherwise.
func RecordContext(ctx context.Context, recorder Recorder, action, resourceType, resourceID string, before, after any, operationErr error) {
	entry := Entry{
		Actor: "system", SourceIP: "internal", UserAgent: "goveto-edge", RequestID: uuid.NewString(),
		Action: action, ResourceType: resourceType, ResourceID: resourceID,
		Before: snapshot(before), After: snapshot(after), Result: ResultSuccess,
	}
	if state, ok := ctx.Value(requestStateContextKey{}).(*requestState); ok && state != nil {
		entry.ActorID = state.entry.ActorID
		entry.Actor = nonempty(state.entry.Actor, entry.Actor)
		entry.SourceIP = nonempty(state.entry.SourceIP, entry.SourceIP)
		entry.UserAgent = state.entry.UserAgent
		entry.RequestID = nonempty(state.entry.RequestID, entry.RequestID)
	}
	if operationErr != nil {
		entry.Result = ResultFailure
		entry.FailureReason = limit(operationErr.Error(), maxFailure)
	}
	recordCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), auditWriteLimit)
	err := recorder.Record(recordCtx, entry)
	cancel()
	if err != nil {
		slog.Error("write contextual audit log", "action", action, "resource_id", resourceID, "error", err)
	}
}

func getState(c *echo.Context) *requestState {
	state, _ := c.Get(stateKey).(*requestState)
	return state
}

func populatePrincipal(c *echo.Context, entry *Entry) {
	if entry.Actor != "" {
		return
	}
	uid := auth.CurrentUID(c)
	if uid == "" {
		entry.Actor = "anonymous"
		return
	}
	entry.ActorID = uid
	entry.Actor = uid
	if user, ok := auth.CurrentUser(c.Request().Context(), uid); ok {
		entry.Actor = nonempty(user.Email, nonempty(user.Name, uid))
	}
}

func failureReason(err error, status int) string {
	if err == nil {
		return fmt.Sprintf("HTTP %d", status)
	}
	var httpError *echo.HTTPError
	if errors.As(err, &httpError) {
		return limit(fmt.Sprint(httpError.Message), maxFailure)
	}
	return limit(err.Error(), maxFailure)
}

func snapshot(value any) json.RawMessage {
	if value == nil {
		return nil
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return json.RawMessage(`{"snapshot_error":"unavailable"}`)
	}
	var document any
	if err = json.Unmarshal(encoded, &document); err != nil {
		return json.RawMessage(`{"snapshot_error":"unavailable"}`)
	}
	redact(document)
	encoded, err = json.Marshal(document)
	if err != nil {
		return json.RawMessage(`{"snapshot_error":"unavailable"}`)
	}
	return encoded
}

func requestSnapshot(request *http.Request) json.RawMessage {
	if request.Body == nil || !strings.Contains(strings.ToLower(request.Header.Get("Content-Type")), "json") {
		return nil
	}
	if request.ContentLength > maxRequestBody {
		return nil
	}
	data, err := io.ReadAll(io.LimitReader(request.Body, maxRequestBody+1))
	if err != nil || len(data) > maxRequestBody {
		request.Body = struct {
			io.Reader
			io.Closer
		}{Reader: io.MultiReader(bytes.NewReader(data), request.Body), Closer: request.Body}
		return nil
	}
	request.Body.Close()
	request.Body = io.NopCloser(bytes.NewReader(data))
	if len(data) == 0 {
		return nil
	}
	var value any
	if err = json.Unmarshal(data, &value); err != nil {
		return nil
	}
	return snapshot(value)
}

func redact(value any) {
	switch current := value.(type) {
	case map[string]any:
		for key, child := range current {
			if sensitiveKey(key) {
				current[key] = "[REDACTED]"
				continue
			}
			redact(child)
		}
	case []any:
		for _, child := range current {
			redact(child)
		}
	}
}

func sensitiveKey(key string) bool {
	normalized := strings.NewReplacer("_", "", "-", "").Replace(strings.ToLower(key))
	for _, marker := range []string{
		"password", "passphrase", "secret", "token", "privatekey", "credential",
		"authorization", "apikey", "cookie", "totp", "recoverycode",
	} {
		if strings.Contains(normalized, marker) {
			return true
		}
	}
	return normalized == "code"
}

func nonempty(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func limit(value string, maximum int) string {
	if len(value) <= maximum {
		return value
	}
	return value[:maximum]
}
