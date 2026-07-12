// Package health registers service health endpoints.
package health

import (
	"context"
	"database/sql"
	"net/http"
	"time"

	"github.com/labstack/echo/v5"
)

func Register(e *echo.Echo, db *sql.DB) {
	group := e.Group("/health")
	group.GET("/live", live)
	group.GET("/ready", ready(db))
}

// @summary Liveness
// @description Process liveness probe; returns ok when the process is running.
// @Tags health
func live(c *echo.Context) error {
	return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
}

// @summary Readiness
// @description Readiness probe; checks database connectivity.
// @Tags health
func ready(db *sql.DB) echo.HandlerFunc {
	return func(c *echo.Context) error {
		ctx, cancel := context.WithTimeout(c.Request().Context(), time.Second)
		defer cancel()
		if err := db.PingContext(ctx); err != nil {
			return c.JSON(http.StatusServiceUnavailable, map[string]string{"status": "unavailable"})
		}
		return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
	}
}
