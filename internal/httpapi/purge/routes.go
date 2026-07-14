package purge

import (
	"net/http"
	"strings"

	"github.com/labstack/echo/v5"

	"goveto-edge/internal/auth"
	"goveto-edge/internal/clusteraccess"
	"goveto-edge/internal/httpapi/types"
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

// @summary Enqueue purge
// @description Enqueue a cache purge job for the site.
// @Tags purge
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
		return types.JSON(c, http.StatusAccepted, types.NewPurgeJob(job))
	}
}

// @summary List purge jobs
// @description List recent cache purge jobs for a site.
// @Tags purge
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

		result := make([]types.PurgeJob, 0, len(jobs))
		for i := range jobs {
			result = append(result, types.NewPurgeJob(&jobs[i]))
		}
		return types.JSON(c, http.StatusOK, result)
	}
}

// @summary Get purge job
// @description Get a single cache purge job by id.
// @Tags purge
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
		return types.JSON(c, http.StatusOK, types.NewPurgeJob(job))
	}
}
func siteInCluster(c *echo.Context, db *client.Client) (*model.Site, error) {
	site, err := db.Site.FindUnique(c.Request().Context(), query.Site.Id.Equals(c.Param("site_id")))
	if err != nil || site.ClusterId != c.Param("cluster_id") {
		return nil, echo.NewHTTPError(http.StatusNotFound, "site not found")
	}
	return site, nil
}
