// Package metrics registers the Prometheus scrape endpoint.
package metrics

import (
	"github.com/labstack/echo/v5"

	"goveto-edge/internal/telemetry"
)

// Register exposes GET /metrics without authentication, matching /health.
// Scrape targets are expected to be network-restricted by the operator.
func Register(e *echo.Echo) {
	e.GET("/metrics", echo.WrapHandler(telemetry.Handler()))
}
