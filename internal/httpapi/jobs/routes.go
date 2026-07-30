// Package jobs exposes the unified background-job operations surface.
package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v5"

	"goveto-edge/internal/audit"
	"goveto-edge/internal/auth"
	"goveto-edge/internal/clusteraccess"
	"goveto-edge/internal/httpapi/types"
	"goveto-edge/internal/jobqueue"
	"goveto-edge/internal/publisher"
	"goveto-edge/internal/rbac"
	"goveto-edge/internal/storage/gen/client"
	"goveto-edge/internal/storage/gen/model"
	"goveto-edge/internal/storage/gen/query"
)

type Job struct {
	ID                string           `db:"id" json:"id"`
	Kind              string           `db:"kind" json:"kind"`
	ResourceID        string           `db:"resource_id" json:"resource_id"`
	ResourceType      string           `db:"resource_type" json:"resource_type"`
	ResourceName      string           `db:"resource_name" json:"resource_name"`
	ResourceHint      string           `db:"resource_hint" json:"resource_hint,omitempty"`
	Operation         string           `db:"operation" json:"operation"`
	Status            string           `db:"status" json:"status"`
	Attempts          int              `db:"attempts" json:"attempts"`
	MaxAttempts       int              `db:"max_attempts" json:"max_attempts"`
	NextAttemptAt     time.Time        `db:"next_attempt_at" json:"next_attempt_at"`
	LeaseOwner        *string          `db:"lease_owner" json:"lease_owner,omitempty"`
	LeaseUntil        *time.Time       `db:"lease_until" json:"lease_until,omitempty"`
	HeartbeatAt       *time.Time       `db:"heartbeat_at" json:"heartbeat_at,omitempty"`
	CancelRequestedAt *time.Time       `db:"cancel_requested_at" json:"cancel_requested_at,omitempty"`
	TimeoutAt         *time.Time       `db:"timeout_at" json:"timeout_at,omitempty"`
	ResultJSON        *json.RawMessage `db:"result_json" json:"result_json,omitempty"`
	CompensationJSON  *json.RawMessage `db:"compensation_json" json:"compensation_json,omitempty"`
	InputJSON         *json.RawMessage `db:"input_json" json:"input_json,omitempty"`
	Error             *string          `db:"error" json:"error,omitempty"`
	CreatedAt         time.Time        `db:"created_at" json:"created_at"`
	UpdatedAt         time.Time        `db:"updated_at" json:"updated_at"`
}

type JobPage struct {
	Items    []Job `json:"items"`
	Page     int   `json:"page"`
	PageSize int   `json:"page_size"`
	Total    int64 `json:"total"`
}

type listParams struct {
	Status   string
	Kind     string
	Query    string
	Page     int
	PageSize int
}

type Execution struct {
	ID          string           `db:"id" json:"id"`
	Attempt     int              `db:"attempt" json:"attempt"`
	WorkerID    string           `db:"worker_id" json:"worker_id"`
	Status      string           `db:"status" json:"status"`
	StartedAt   time.Time        `db:"started_at" json:"started_at"`
	HeartbeatAt time.Time        `db:"heartbeat_at" json:"heartbeat_at"`
	FinishedAt  *time.Time       `db:"finished_at" json:"finished_at,omitempty"`
	ResultJSON  *json.RawMessage `db:"result_json" json:"result_json,omitempty"`
	Error       *string          `db:"error" json:"error,omitempty"`
}

type mutationSnapshot struct {
	ID               string           `db:"id" json:"id"`
	Kind             string           `db:"kind" json:"kind"`
	Status           string           `db:"status" json:"status"`
	Attempts         int              `db:"attempts" json:"attempts"`
	ResultJSON       *json.RawMessage `db:"result_json" json:"result_json,omitempty"`
	CompensationJSON *json.RawMessage `db:"compensation_json" json:"compensation_json,omitempty"`
	Error            *string          `db:"error" json:"error,omitempty"`
}

func Register(e *echo.Echo, db *client.Client, publishService *publisher.Service) {
	read := clusteraccess.RequirePermission(db, rbac.PermissionClusterRead)
	group := e.Group("/api/v1/clusters/:cluster_id/jobs", auth.RequireAuth, read)
	group.GET("", list(db))
	group.GET("/:kind/:job_id", detail(db))
	group.GET("/:kind/:job_id/executions", executions(db))
	group.POST("/:kind/:job_id/cancel", mutate(db, publishService, true))
	group.POST("/:kind/:job_id/replay", mutate(db, publishService, false))
}

func list(db *client.Client) echo.HandlerFunc {
	return func(c *echo.Context) error {
		params, err := parseListParams(c)
		if err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, err.Error())
		}
		jobs, err := loadJobs(c.Request().Context(), db, c.Param("cluster_id"), params)
		if err != nil {
			return err
		}
		return types.JSON(c, http.StatusOK, jobs)
	}
}

func detail(db *client.Client) echo.HandlerFunc {
	return func(c *echo.Context) error {
		kind, err := parseKind(c.Param("kind"))
		if err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, err.Error())
		}
		job, err := loadJobDetail(
			c.Request().Context(), db, c.Param("cluster_id"), kind, c.Param("job_id"),
		)
		if err != nil {
			return err
		}
		return types.JSON(c, http.StatusOK, job)
	}
}

func executions(db *client.Client) echo.HandlerFunc {
	return func(c *echo.Context) error {
		kind, err := parseKind(c.Param("kind"))
		if err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, err.Error())
		}
		if err = requireJobInCluster(c.Request().Context(), db, c.Param("cluster_id"), kind, c.Param("job_id")); err != nil {
			return err
		}
		rows, err := client.Raw[Execution](c.Request().Context(), db, `SELECT id, attempt, worker_id,
			status, started_at, heartbeat_at, finished_at, result_json, error FROM job_executions
			WHERE job_type=$1 AND job_id=$2 ORDER BY attempt DESC, started_at DESC`, string(kind), c.Param("job_id"))
		if err != nil {
			return err
		}
		if rows == nil {
			rows = []Execution{}
		}
		return types.JSON(c, http.StatusOK, rows)
	}
}

func mutate(db *client.Client, publishService *publisher.Service, cancel bool) echo.HandlerFunc {
	return func(c *echo.Context) error {
		kind, err := parseKind(c.Param("kind"))
		if err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, err.Error())
		}
		permission := permissionFor(kind)
		allowed, _, err := clusteraccess.Authorize(c.Request().Context(), db, c.Param("cluster_id"), auth.CurrentUID(c), permission)
		if err != nil {
			return err
		}
		if !allowed {
			return echo.NewHTTPError(http.StatusForbidden, "permission denied: "+string(permission))
		}
		if err = requireJobInCluster(c.Request().Context(), db, c.Param("cluster_id"), kind, c.Param("job_id")); err != nil {
			return err
		}
		before, err := loadMutationSnapshot(c.Request().Context(), db, kind, c.Param("job_id"))
		if err != nil {
			return err
		}
		manager := jobqueue.New(db)
		responseID := c.Param("job_id")
		if cancel {
			err = manager.Cancel(c.Request().Context(), kind, c.Param("job_id"))
		} else if kind == jobqueue.Publish {
			oldJob, loadErr := db.PublishJob.FindUnique(c.Request().Context(), query.PublishJob.Id.Equals(c.Param("job_id")))
			if loadErr != nil || oldJob == nil {
				return echo.NewHTTPError(http.StatusNotFound, "publish job not found")
			}
			switch oldJob.Status {
			case model.JobStatusFAILED, model.JobStatusDEAD_LETTER, model.JobStatusCANCELLED:
			default:
				return echo.NewHTTPError(http.StatusConflict, "job is not replayable")
			}
			newJob, enqueueErr := publishService.Enqueue(c.Request().Context(), oldJob.SiteId)
			if enqueueErr != nil {
				err = enqueueErr
			} else {
				responseID = newJob.Id
			}
		} else {
			err = manager.Replay(c.Request().Context(), kind, c.Param("job_id"))
		}
		if err != nil {
			if errors.Is(err, jobqueue.ErrNotCancellable) || errors.Is(err, jobqueue.ErrNotReplayable) ||
				errors.Is(err, jobqueue.ErrIdempotencyConflict) {
				return echo.NewHTTPError(http.StatusConflict, err.Error())
			}
			return err
		}
		mode := "cancel"
		if !cancel && kind == jobqueue.Publish {
			mode = "create"
		} else if !cancel {
			mode = "reset"
		}
		response := map[string]string{
			"id": responseID, "source_id": c.Param("job_id"), "status": "accepted", "mode": mode,
		}
		audit.SetChange(c, before, response)
		return types.JSON(c, http.StatusAccepted, response)
	}
}

func loadMutationSnapshot(ctx context.Context, db *client.Client, kind jobqueue.Kind, id string) (*mutationSnapshot, error) {
	tables := map[jobqueue.Kind]string{
		jobqueue.Publish: "publish_jobs", jobqueue.Purge: "purge_jobs", jobqueue.Install: "install_jobs",
		jobqueue.DNS: "dns_sync_jobs", jobqueue.Certificate: "certificate_jobs",
	}
	rows, err := client.Raw[mutationSnapshot](ctx, db, fmt.Sprintf(`SELECT id, $2::text AS kind,
		status::text, attempts, result_json, compensation_json, error FROM %s WHERE id=$1`, tables[kind]),
		id, string(kind))
	if err != nil {
		return nil, err
	}
	if len(rows) != 1 {
		return nil, echo.NewHTTPError(http.StatusNotFound, "job not found")
	}
	return &rows[0], nil
}

const jobListSourceSQL = `
		SELECT j.id, 'PUBLISH' AS kind, j.site_id AS resource_id, 'SITE' AS resource_type,
		s.name AS resource_name, COALESCE((SELECT MIN(d.hostname) FROM site_domains d WHERE d.site_id=j.site_id), '') AS resource_hint,
		'VERSION ' || j.version::text AS operation, j.status::text, j.attempts,
		j.max_attempts, j.next_attempt_at, j.lease_owner, j.lease_until, j.heartbeat_at,
		j.cancel_requested_at, j.timeout_at, j.result_json, j.compensation_json, j.error, j.created_at, j.updated_at
		FROM publish_jobs j JOIN sites s ON s.id=j.site_id WHERE s.cluster_id=$1
		UNION ALL SELECT j.id, 'PURGE', j.site_id, 'SITE', s.name,
		COALESCE((SELECT MIN(d.hostname) FROM site_domains d WHERE d.site_id=j.site_id), ''),
		j.type::text || COALESCE(' ' || NULLIF(j.value, ''), ''), j.status::text, j.attempts, j.max_attempts,
		j.next_attempt_at, j.lease_owner, j.lease_until, j.heartbeat_at, j.cancel_requested_at,
		j.timeout_at, j.result_json, j.compensation_json, j.error, j.created_at, j.updated_at
		FROM purge_jobs j JOIN sites s ON s.id=j.site_id WHERE s.cluster_id=$1
		UNION ALL SELECT j.id, 'INSTALL', j.node_id::text, 'NODE', n.name,
		COALESCE((SELECT MIN(a.address) FROM node_addresses a WHERE a.node_id=j.node_id), ''),
		'INSTALL', j.status::text, j.attempts, j.max_attempts,
		j.next_attempt_at, j.lease_owner, j.lease_until, j.heartbeat_at, j.cancel_requested_at,
		j.timeout_at, j.result_json, j.compensation_json, j.error, j.created_at, j.updated_at
		FROM install_jobs j JOIN nodes n ON n.id=j.node_id WHERE n.cluster_id=$1
		UNION ALL SELECT j.id, 'DNS', COALESCE(j.site_id, j.cluster_id),
		CASE WHEN j.site_id IS NULL THEN 'CLUSTER' ELSE 'SITE' END,
		COALESCE(s.name, c.name), COALESCE((SELECT MIN(d.hostname) FROM site_domains d WHERE d.site_id=j.site_id), c.primary_hostname, ''),
		j.action::text, j.status::text, j.attempts, j.max_attempts,
		j.next_attempt_at, j.lease_owner, j.lease_until, j.heartbeat_at, j.cancel_requested_at,
		j.timeout_at, j.result_json, j.compensation_json, j.error, j.created_at, j.updated_at
		FROM dns_sync_jobs j JOIN clusters c ON c.id=j.cluster_id LEFT JOIN sites s ON s.id=j.site_id WHERE j.cluster_id=$1
		UNION ALL SELECT j.id, 'CERTIFICATE', j.certificate_id, 'CERTIFICATE', c.name,
		COALESCE(c.domains_json->>0, ''), j.operation::text, j.status::text, j.attempts, j.max_attempts,
		j.next_attempt_at, j.lease_owner, j.lease_until, j.heartbeat_at, j.cancel_requested_at,
		j.timeout_at, j.result_json, j.compensation_json, j.error, j.created_at, j.updated_at
		FROM certificate_jobs j JOIN certificates c ON c.id=j.certificate_id WHERE c.cluster_id=$1`

const jobListFilterSQL = `($2='' OR status=$2) AND ($3='' OR kind=$3) AND ($4='' OR concat_ws(' ',
		id, resource_id, resource_type, resource_name, resource_hint, operation
	) ILIKE '%' || $4 || '%')`

func loadJobs(ctx context.Context, db *client.Client, clusterID string, params listParams) (JobPage, error) {
	result := JobPage{Items: make([]Job, 0, params.PageSize), Page: params.Page, PageSize: params.PageSize}
	type countRow struct {
		Total int64 `db:"total"`
	}
	counts, err := client.Raw[countRow](ctx, db, `SELECT COUNT(*) AS total FROM (`+jobListSourceSQL+
		`) jobs WHERE `+jobListFilterSQL, clusterID, params.Status, params.Kind, params.Query)
	if err != nil {
		return JobPage{}, err
	}
	if len(counts) != 0 {
		result.Total = counts[0].Total
	}
	rows, err := client.Raw[Job](ctx, db, `SELECT * FROM (`+jobListSourceSQL+
		`) jobs WHERE `+jobListFilterSQL+` ORDER BY created_at DESC, kind, id LIMIT $5 OFFSET $6`,
		clusterID, params.Status, params.Kind, params.Query, params.PageSize, (params.Page-1)*params.PageSize)
	if err != nil {
		return JobPage{}, err
	}
	result.Items = append(result.Items, rows...)
	return result, nil
}

func loadJobDetail(ctx context.Context, db *client.Client, clusterID string, kind jobqueue.Kind, id string) (*Job, error) {
	rows, err := client.Raw[Job](ctx, db, `SELECT * FROM (
		SELECT jobs.*, jsonb_build_object('version', j.version, 'targets', j.targets,
			'target_resources', (SELECT COALESCE(jsonb_agg(jsonb_build_object(
				'id', target.value->>'node_id', 'name', n.name,
				'address', (SELECT MIN(a.address) FROM node_addresses a WHERE a.node_id=n.id)
			) ORDER BY n.name), '[]'::jsonb) FROM jsonb_array_elements(j.targets::jsonb) target
			LEFT JOIN nodes n ON n.id::text=target.value->>'node_id'),
			'config', cv.config_json) AS input_json
		FROM (`+jobListSourceSQL+`) jobs
		JOIN publish_jobs j ON jobs.kind='PUBLISH' AND j.id=jobs.id
		LEFT JOIN config_versions cv ON cv.site_id=j.site_id AND cv.version=j.version
		UNION ALL SELECT jobs.*, jsonb_build_object('type', j.type, 'value', j.value)
		FROM (`+jobListSourceSQL+`) jobs JOIN purge_jobs j ON jobs.kind='PURGE' AND j.id=jobs.id
		UNION ALL SELECT jobs.*, j.payload::jsonb
		FROM (`+jobListSourceSQL+`) jobs JOIN install_jobs j ON jobs.kind='INSTALL' AND j.id=jobs.id
		UNION ALL SELECT jobs.*, jsonb_build_object('action', j.action, 'site_id', j.site_id)
		FROM (`+jobListSourceSQL+`) jobs JOIN dns_sync_jobs j ON jobs.kind='DNS' AND j.id=jobs.id
		UNION ALL SELECT jobs.*, jsonb_build_object('operation', j.operation)
		FROM (`+jobListSourceSQL+`) jobs JOIN certificate_jobs j ON jobs.kind='CERTIFICATE' AND j.id=jobs.id
	) details WHERE kind=$2 AND id=$3`, clusterID, string(kind), id)
	if err != nil {
		return nil, err
	}
	if len(rows) != 1 {
		return nil, echo.NewHTTPError(http.StatusNotFound, "job not found")
	}
	if err = redactSensitiveInput(rows[0].InputJSON); err != nil {
		return nil, fmt.Errorf("redact job input: %w", err)
	}
	return &rows[0], nil
}

func redactSensitiveInput(raw *json.RawMessage) error {
	if raw == nil {
		return nil
	}
	var value any
	if err := json.Unmarshal(*raw, &value); err != nil {
		return err
	}
	redactSensitiveValue(value)
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	*raw = encoded
	return nil
}

func redactSensitiveValue(value any) {
	switch current := value.(type) {
	case map[string]any:
		for key, item := range current {
			normalized := strings.ToLower(strings.ReplaceAll(key, "-", "_"))
			if isSensitiveKey(normalized) {
				current[key] = "[REDACTED]"
				continue
			}
			redactSensitiveValue(item)
		}
	case []any:
		for _, item := range current {
			redactSensitiveValue(item)
		}
	}
}

func isSensitiveKey(key string) bool {
	for _, fragment := range []string{
		"private_key", "password", "passphrase", "secret", "token", "key_auth",
		"api_key", "authorization", "cookie",
	} {
		if strings.Contains(key, fragment) {
			return true
		}
	}
	return false
}

func parseListParams(c *echo.Context) (listParams, error) {
	params := listParams{
		Status: strings.ToUpper(strings.TrimSpace(c.QueryParam("status"))),
		Kind:   strings.ToUpper(strings.TrimSpace(c.QueryParam("kind"))),
		Query:  strings.TrimSpace(c.QueryParam("query")),
		Page:   1, PageSize: 25,
	}
	if params.Kind != "" {
		if _, err := parseKind(params.Kind); err != nil {
			return listParams{}, err
		}
	}
	if params.Status != "" {
		switch params.Status {
		case "PENDING", "RUNNING", "SUCCEEDED", "FAILED", "DEAD_LETTER", "CANCELLED":
		default:
			return listParams{}, errors.New("unknown job status")
		}
	}
	if len(params.Query) > 256 {
		return listParams{}, errors.New("query must be at most 256 characters")
	}
	if value, err := strconv.Atoi(c.QueryParam("page")); err == nil && value > 0 {
		params.Page = value
	}
	if value, err := strconv.Atoi(c.QueryParam("page_size")); err == nil && value > 0 && value <= 100 {
		params.PageSize = value
	}
	return params, nil
}

func requireJobInCluster(ctx context.Context, db *client.Client, clusterID string, kind jobqueue.Kind, id string) error {
	queries := map[jobqueue.Kind]string{
		jobqueue.Publish:     `SELECT j.id FROM publish_jobs j JOIN sites s ON s.id=j.site_id WHERE j.id=$1 AND s.cluster_id=$2`,
		jobqueue.Purge:       `SELECT j.id FROM purge_jobs j JOIN sites s ON s.id=j.site_id WHERE j.id=$1 AND s.cluster_id=$2`,
		jobqueue.Install:     `SELECT j.id FROM install_jobs j JOIN nodes n ON n.id=j.node_id WHERE j.id=$1 AND n.cluster_id=$2`,
		jobqueue.DNS:         `SELECT j.id FROM dns_sync_jobs j WHERE j.id=$1 AND j.cluster_id=$2`,
		jobqueue.Certificate: `SELECT j.id FROM certificate_jobs j JOIN certificates c ON c.id=j.certificate_id WHERE j.id=$1 AND c.cluster_id=$2`,
	}
	type idRow struct {
		ID string `db:"id"`
	}
	rows, err := client.Raw[idRow](ctx, db, queries[kind], id, clusterID)
	if err != nil {
		return err
	}
	if len(rows) == 1 {
		return nil
	}
	return echo.NewHTTPError(http.StatusNotFound, "job not found")
}

func parseKind(value string) (jobqueue.Kind, error) {
	kind := jobqueue.Kind(strings.ToUpper(value))
	switch kind {
	case jobqueue.Publish, jobqueue.Purge, jobqueue.Install, jobqueue.DNS, jobqueue.Certificate:
		return kind, nil
	default:
		return "", errors.New("unknown job kind")
	}
}

func permissionFor(kind jobqueue.Kind) rbac.Permission {
	switch kind {
	case jobqueue.Publish:
		return rbac.PermissionPublish
	case jobqueue.Purge:
		return rbac.PermissionCacheOperate
	case jobqueue.Install:
		return rbac.PermissionNodeManage
	case jobqueue.DNS:
		return rbac.PermissionCredentialManage
	default:
		return rbac.PermissionCertificateManage
	}
}
