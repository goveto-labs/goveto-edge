// Package jobs exposes the unified background-job operations surface.
package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
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
	Error             *string          `db:"error" json:"error,omitempty"`
	CreatedAt         time.Time        `db:"created_at" json:"created_at"`
	UpdatedAt         time.Time        `db:"updated_at" json:"updated_at"`
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
	group.GET("/:kind/:job_id/executions", executions(db))
	group.POST("/:kind/:job_id/cancel", mutate(db, publishService, true))
	group.POST("/:kind/:job_id/replay", mutate(db, publishService, false))
}

func list(db *client.Client) echo.HandlerFunc {
	return func(c *echo.Context) error {
		jobs, err := loadJobs(c.Request().Context(), db, c.Param("cluster_id"), c.QueryParam("status"), c.QueryParam("kind"))
		if err != nil {
			return err
		}
		return types.JSON(c, http.StatusOK, jobs)
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

func loadJobs(ctx context.Context, db *client.Client, clusterID, status, kind string) ([]Job, error) {
	rows, err := client.Raw[Job](ctx, db, `SELECT * FROM (
		SELECT j.id, 'PUBLISH' AS kind, j.site_id AS resource_id, j.status::text, j.attempts,
		j.max_attempts, j.next_attempt_at, j.lease_owner, j.lease_until, j.heartbeat_at,
		j.cancel_requested_at, j.timeout_at, j.result_json, j.compensation_json, j.error, j.created_at, j.updated_at
		FROM publish_jobs j JOIN sites s ON s.id=j.site_id WHERE s.cluster_id=$1
		UNION ALL SELECT j.id, 'PURGE', j.site_id, j.status::text, j.attempts, j.max_attempts,
		j.next_attempt_at, j.lease_owner, j.lease_until, j.heartbeat_at, j.cancel_requested_at,
		j.timeout_at, j.result_json, j.compensation_json, j.error, j.created_at, j.updated_at
		FROM purge_jobs j JOIN sites s ON s.id=j.site_id WHERE s.cluster_id=$1
		UNION ALL SELECT j.id, 'INSTALL', j.node_id::text, j.status::text, j.attempts, j.max_attempts,
		j.next_attempt_at, j.lease_owner, j.lease_until, j.heartbeat_at, j.cancel_requested_at,
		j.timeout_at, j.result_json, j.compensation_json, j.error, j.created_at, j.updated_at
		FROM install_jobs j JOIN nodes n ON n.id=j.node_id WHERE n.cluster_id=$1
		UNION ALL SELECT j.id, 'DNS', j.cluster_id, j.status::text, j.attempts, j.max_attempts,
		j.next_attempt_at, j.lease_owner, j.lease_until, j.heartbeat_at, j.cancel_requested_at,
		j.timeout_at, j.result_json, j.compensation_json, j.error, j.created_at, j.updated_at
		FROM dns_sync_jobs j WHERE j.cluster_id=$1
		UNION ALL SELECT j.id, 'CERTIFICATE', j.certificate_id, j.status::text, j.attempts, j.max_attempts,
		j.next_attempt_at, j.lease_owner, j.lease_until, j.heartbeat_at, j.cancel_requested_at,
		j.timeout_at, j.result_json, j.compensation_json, j.error, j.created_at, j.updated_at
		FROM certificate_jobs j JOIN certificates c ON c.id=j.certificate_id WHERE c.cluster_id=$1
	) jobs WHERE ($2='' OR status=$2) AND ($3='' OR kind=$3) ORDER BY created_at DESC LIMIT 200`,
		clusterID, strings.ToUpper(status), strings.ToUpper(kind))
	if err != nil {
		return nil, err
	}
	if rows == nil {
		rows = []Job{}
	}
	return rows, nil
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
