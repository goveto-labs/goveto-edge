package purge

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/labstack/echo/v5"
	"goveto-edge/internal/auth"
	"goveto-edge/internal/clusteraccess"
	"goveto-edge/internal/purge"
	"goveto-edge/internal/storage/gen/client"
	"goveto-edge/internal/storage/gen/model"
	"goveto-edge/internal/storage/gen/query"
)

func Register(e *echo.Echo, db *client.Client, service *purge.Service) {
	e.POST("/api/v1/clusters/:cluster_id/sites/:site_id/purge", enqueue(db, service), auth.RequireAuth, clusteraccess.Require(db))
	e.GET("/api/v1/clusters/:cluster_id/sites/:site_id/purge", list(db), auth.RequireAuth, clusteraccess.Require(db))
	e.GET("/api/v1/clusters/:cluster_id/sites/:site_id/purge/:job_id", get(db), auth.RequireAuth, clusteraccess.Require(db))
}

type request struct {
	Type  model.PurgeType `json:"type"`
	Value *string         `json:"value"`
}

func enqueue(db *client.Client, service *purge.Service) echo.HandlerFunc {
	return func(c *echo.Context) error {
		site, err := siteInCluster(c, db)
		if err != nil {
			return err
		}

		var input request
		if err = c.Bind(&input); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid request body")
		}
		if input.Value != nil {
			value := strings.TrimSpace(*input.Value)
			input.Value = &value
		}

		job, err := service.Enqueue(c.Request().Context(), site.Id, input.Type, input.Value)
		if err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, err.Error())
		}
		return c.JSON(http.StatusAccepted, details(job))
	}
}

func list(db *client.Client) echo.HandlerFunc {
	return func(c *echo.Context) error {
		site, err := siteInCluster(c, db)
		if err != nil {
			return err
		}

		jobs, err := db.PurgeJob.Query().
			Where(query.PurgeJob.SiteId.Equals(site.Id)).
			OrderBy(query.PurgeJob.CreatedAt.Desc()).
			Take(50).
			Do(c.Request().Context())
		if err != nil {
			return err
		}

		result := make([]map[string]any, 0, len(jobs))
		for i := range jobs {
			result = append(result, details(&jobs[i]))
		}
		return c.JSON(http.StatusOK, result)
	}
}
func get(db *client.Client) echo.HandlerFunc {
	return func(c *echo.Context) error {
		site, err := siteInCluster(c, db)
		if err != nil {
			return err
		}

		job, err := db.PurgeJob.FindUnique(c.Request().Context(), query.PurgeJob.Id.Equals(c.Param("job_id")))
		if err != nil || job.SiteId != site.Id {
			return echo.NewHTTPError(http.StatusNotFound, "purge job not found")
		}
		return c.JSON(http.StatusOK, details(job))
	}
}
func siteInCluster(c *echo.Context, db *client.Client) (*model.Site, error) {
	site, err := db.Site.FindUnique(c.Request().Context(), query.Site.Id.Equals(c.Param("site_id")))
	if err != nil || site.ClusterId != c.Param("cluster_id") {
		return nil, echo.NewHTTPError(http.StatusNotFound, "site not found")
	}
	return site, nil
}
func details(job *model.PurgeJob) map[string]any {
	result := map[string]any{
		"id":         job.Id,
		"site_id":    job.SiteId,
		"type":       job.Type,
		"value":      job.Value,
		"status":     job.Status,
		"created_at": job.CreatedAt,
		"updated_at": job.UpdatedAt,
	}
	if job.ResultJson != nil {
		var value any
		if json.Unmarshal(*job.ResultJson, &value) == nil {
			result["details"] = value
		}
	}
	return result
}
