// Package publish registers site publication endpoints.
package publish

import (
	"errors"
	"net/http"

	"github.com/labstack/echo/v5"

	"goveto-edge/internal/auth"
	"goveto-edge/internal/clusteraccess"
	"goveto-edge/internal/publisher"
	"goveto-edge/internal/storage/gen/client"
	"goveto-edge/internal/storage/gen/query"
)

func Register(e *echo.Echo, db *client.Client, service *publisher.Service) {
	e.POST("/api/v1/clusters/:cluster_id/sites/:site_id/publish", enqueue(db, service), auth.RequireAuth, clusteraccess.Require(db))
	e.GET("/api/v1/clusters/:cluster_id/sites/:site_id/publish/:job_id", getJob(db), auth.RequireAuth, clusteraccess.Require(db))
}

func enqueue(db *client.Client, service *publisher.Service) echo.HandlerFunc {
	return func(c *echo.Context) error {
		site, err := db.Site.FindUnique(c.Request().Context(), query.Site.Id.Equals(c.Param("site_id")))
		if err != nil || site.ClusterId != c.Param("cluster_id") {
			return echo.NewHTTPError(http.StatusNotFound, "site not found")
		}
		job, err := service.Enqueue(c.Request().Context(), site.Id)
		if err != nil {
			if errors.Is(err, publisher.ErrPublishInProgress) {
				return echo.NewHTTPError(http.StatusConflict, err.Error())
			}
			return err
		}
		return c.JSON(http.StatusAccepted, job)
	}
}

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
		return c.JSON(http.StatusOK, job)
	}
}
