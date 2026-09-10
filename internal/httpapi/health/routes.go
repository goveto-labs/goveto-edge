// Package health registers service health endpoints.
package health

import (
	"context"
	"database/sql"
	"net/http"
	"time"

	"github.com/labstack/echo/v5"

	"goveto-edge/internal/analytics"
	"goveto-edge/internal/buildinfo"
	"goveto-edge/internal/httpapi/types"
)

func Register(e *echo.Echo, db *sql.DB, analyticsStore ...*analytics.Store) {
	group := e.Group("/health")
	group.GET("/live", live)
	group.GET("/ready", ready(db, analyticsStore...))
}

// startedAt records process boot time so dev builds without a release
// version can still be identified by their startup stamp.
var startedAt = time.Now()

type statusResponse struct {
	Status    string `json:"status"`
	Version   string `json:"version"`
	StartedAt string `json:"startedAt"`
}

// @summary Liveness
// @description Process liveness probe; returns ok when the process is running.
// @Tags health
func live(c *echo.Context) error {
	return types.JSON(c, http.StatusOK, statusResponse{
		Status:    "ok",
		Version:   buildinfo.Current(),
		StartedAt: startedAt.Format(time.RFC3339),
	})
}

// @summary Readiness
// @description Readiness probe; checks database connectivity.
// @Tags health
func ready(db *sql.DB, stores ...*analytics.Store) echo.HandlerFunc {
	return func(c *echo.Context) error {
		ctx, cancel := context.WithTimeout(c.Request().Context(), time.Second)
		defer cancel()
		if err := db.PingContext(ctx); err != nil {
			return c.JSON(http.StatusServiceUnavailable, types.Fail("service_unavailable", "unavailable"))
		}
		if len(stores) > 0 && stores[0] != nil {
			if err := stores[0].Ready(ctx); err != nil {
				return c.JSON(http.StatusServiceUnavailable, types.Fail("service_unavailable", "unavailable"))
			}
		}
		return types.JSON(c, http.StatusOK, statusResponse{
			Status:    "ok",
			Version:   buildinfo.Current(),
			StartedAt: startedAt.Format(time.RFC3339),
		})
	}
}
