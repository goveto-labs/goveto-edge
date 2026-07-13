// Package publish registers site publication endpoints.
package publish

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/labstack/echo/v5"

	"goveto-edge/internal/auth"
	"goveto-edge/internal/clusteraccess"
	"goveto-edge/internal/httpapi/types"
	"goveto-edge/internal/publisher"
	"goveto-edge/internal/storage/gen/client"
	"goveto-edge/internal/storage/gen/model"
	"goveto-edge/internal/storage/gen/query"
)

func Register(e *echo.Echo, db *client.Client, service *publisher.Service) {
	e.GET("/api/v1/clusters/:cluster_id/publish/status", clusterStatus(db), auth.RequireAuth, clusteraccess.Require(db))
	e.GET("/api/v1/clusters/:cluster_id/publish/events", clusterEvents(db), auth.RequireAuth, clusteraccess.Require(db))
	e.POST("/api/v1/clusters/:cluster_id/sites/:site_id/publish", enqueue(db, service), auth.RequireAuth, clusteraccess.Require(db))
	e.GET("/api/v1/clusters/:cluster_id/sites/:site_id/publish/:job_id", getJob(db), auth.RequireAuth, clusteraccess.Require(db))
	e.GET("/api/v1/clusters/:cluster_id/sites/:site_id/publish", listJobs(db), auth.RequireAuth, clusteraccess.Require(db))
}

type clusterPublishCounts struct {
	Pending int64 `db:"pending"`
	Running int64 `db:"running"`
	Failed  int64 `db:"failed"`
}

// @summary Cluster publish status
// @description Aggregate publish job counts and recent tasks for the cluster.
// @Tags publish
func clusterStatus(db *client.Client) echo.HandlerFunc {
	return func(c *echo.Context) error {
		snapshot, err := loadClusterStatus(c.Request().Context(), db, c.Param("cluster_id"))
		if err != nil {
			return err
		}
		return types.JSON(c, http.StatusOK, snapshot)
	}
}

func loadClusterStatus(ctx context.Context, db *client.Client, clusterID string) (map[string]any, error) {
	counts, err := client.Raw[clusterPublishCounts](ctx, db, `SELECT
			COUNT(*) FILTER (WHERE pj.status = 'PENDING') AS pending,
			COUNT(*) FILTER (WHERE pj.status = 'RUNNING') AS running,
			COUNT(*) FILTER (WHERE pj.status = 'FAILED') AS failed
			FROM publish_jobs pj JOIN sites s ON s.id = pj.site_id WHERE s.cluster_id = $1`, clusterID)
	if err != nil {
		return nil, err
	}

	count := clusterPublishCounts{}
	if len(counts) != 0 {
		count = counts[0]
	}

	jobs, err := client.Raw[model.PublishJob](ctx, db, `SELECT pj.id, pj.site_id, pj.version, pj.targets,
			pj.status, pj.result_json, pj.created_at, pj.updated_at
			FROM publish_jobs pj JOIN sites s ON s.id = pj.site_id
			WHERE s.cluster_id = $1 ORDER BY pj.created_at DESC LIMIT 50`, clusterID)
	if err != nil {
		return nil, err
	}

	recent := make([]map[string]any, 0, len(jobs))
	for i := range jobs {
		recent = append(recent, jobDetails(&jobs[i]))
	}

	active := count.Pending+count.Running > 0
	state := "idle"
	if count.Failed > 0 {
		state = "failed"
	}
	if active {
		state = "syncing"
	}

	return map[string]any{
		"state":            state,
		"has_active_tasks": active,
		"has_failed_tasks": count.Failed > 0,
		"pending_count":    count.Pending,
		"running_count":    count.Running,
		"failed_count":     count.Failed,
		"recent_tasks":     recent,
	}, nil
}

// @summary Cluster publish events
// @description SSE stream of cluster publish sync status snapshots.
// @Tags publish
func clusterEvents(db *client.Client) echo.HandlerFunc {
	return func(c *echo.Context) error {
		ctx := c.Request().Context()
		response := c.Response()
		response.Header().Set("Content-Type", "text/event-stream")
		response.Header().Set("Cache-Control", "no-cache, no-transform")
		response.Header().Set("Connection", "keep-alive")
		response.Header().Set("X-Accel-Buffering", "no")
		response.WriteHeader(http.StatusOK)

		poll := time.NewTicker(time.Second)
		heartbeat := time.NewTicker(15 * time.Second)
		defer poll.Stop()
		defer heartbeat.Stop()

		var previous []byte
		sendSnapshot := func() error {
			snapshot, err := loadClusterStatus(ctx, db, c.Param("cluster_id"))
			if err != nil {
				return writeSSE(response, "error", map[string]string{"error": err.Error()})
			}

			encoded, err := json.Marshal(snapshot)
			if err != nil {
				return err
			}
			if bytes.Equal(previous, encoded) {
				return nil
			}

			previous = append(previous[:0], encoded...)
			return writeSSEBytes(response, "sync_status", encoded)
		}

		if err := sendSnapshot(); err != nil {
			return nil
		}

		for {
			select {
			case <-ctx.Done():
				return nil
			case <-poll.C:
				if err := sendSnapshot(); err != nil {
					return nil
				}
			case <-heartbeat.C:
				if _, err := fmt.Fprint(response, ": heartbeat\n\n"); err != nil {
					return nil
				}
				_ = http.NewResponseController(response).Flush()
			}
		}
	}
}

func writeSSE(response http.ResponseWriter, event string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return writeSSEBytes(response, event, data)
}

func writeSSEBytes(response http.ResponseWriter, event string, data []byte) error {
	if _, err := fmt.Fprintf(response, "event: %s\ndata: %s\n\n", event, data); err != nil {
		return err
	}
	return http.NewResponseController(response).Flush()
}

// @summary Enqueue publish
// @description Enqueue a site configuration publish job.
// @Tags publish
func enqueue(db *client.Client, service *publisher.Service) echo.HandlerFunc {
	return func(c *echo.Context) error {
		site, err := db.Site.FindUnique(c.Request().Context(), query.Site.Id.Equals(c.Param("site_id")))
		if err != nil || site.ClusterId != c.Param("cluster_id") {
			return echo.NewHTTPError(http.StatusNotFound, "site not found")
		}

		job, err := service.Enqueue(c.Request().Context(), site.Id)
		if err != nil {
			return err
		}
		return types.JSON(c, http.StatusAccepted, job)
	}
}

// @summary Get publish job
// @description Get a single site publish job by id.
// @Tags publish
func getJob(db *client.Client) echo.HandlerFunc {
	return func(c *echo.Context) error {
		ctx := c.Request().Context()
		site, err := db.Site.FindUnique(ctx, query.Site.Id.Equals(c.Param("site_id")))
		if err != nil || site.ClusterId != c.Param("cluster_id") {
			return echo.NewHTTPError(http.StatusNotFound, "site not found")
		}

		job, err := db.PublishJob.FindUnique(ctx, query.PublishJob.Id.Equals(c.Param("job_id")))
		if err != nil || job.SiteId != site.Id {
			return echo.NewHTTPError(http.StatusNotFound, "publish job not found")
		}
		return types.JSON(c, http.StatusOK, jobDetails(job))
	}
}

// @summary List publish jobs
// @description List recent publish jobs for a site.
// @Tags publish
func listJobs(db *client.Client) echo.HandlerFunc {
	return func(c *echo.Context) error {
		ctx := c.Request().Context()
		site, err := db.Site.FindUnique(ctx, query.Site.Id.Equals(c.Param("site_id")))
		if err != nil || site.ClusterId != c.Param("cluster_id") {
			return echo.NewHTTPError(http.StatusNotFound, "site not found")
		}

		jobs, err := db.PublishJob.Query().
			Where(query.PublishJob.SiteId.Equals(site.Id)).
			OrderBy(query.PublishJob.CreatedAt.Desc()).
			Take(50).
			Do(ctx)
		if err != nil {
			return err
		}

		result := make([]map[string]any, 0, len(jobs))
		for i := range jobs {
			result = append(result, jobDetails(&jobs[i]))
		}
		return types.JSON(c, http.StatusOK, result)
	}
}

func jobDetails(job *model.PublishJob) map[string]any {
	result := map[string]any{
		"id":         job.Id,
		"site_id":    job.SiteId,
		"version":    job.Version,
		"status":     job.Status,
		"created_at": job.CreatedAt,
		"updated_at": job.UpdatedAt,
	}
	if job.ResultJson != nil && len(*job.ResultJson) != 0 {
		var details any
		if json.Unmarshal(*job.ResultJson, &details) == nil {
			result["details"] = details
		}
	}
	return result
}
