package analytics

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/labstack/echo/v5"

	"goveto-edge/internal/analytics"
	"goveto-edge/internal/auth"
	"goveto-edge/internal/clusteraccess"
	"goveto-edge/internal/httpapi/types"
	"goveto-edge/internal/storage/gen/client"
)

func Register(e *echo.Echo, db *client.Client, store *analytics.Store) {
	if store == nil {
		return
	}

	require := []echo.MiddlewareFunc{auth.RequireAuth, clusteraccess.Require(db)}

	e.GET("/api/v1/clusters/:cluster_id/analytics/summary", summary(store), require...)
	e.GET("/api/v1/clusters/:cluster_id/analytics/overview", monitoringOverview(store), require...)
	e.GET("/api/v1/clusters/:cluster_id/analytics/top-urls", top(store, "url"), require...)
	e.GET("/api/v1/clusters/:cluster_id/analytics/top-ips", top(store, "ip"), require...)
	e.GET("/api/v1/clusters/:cluster_id/analytics/traffic", traffic(store), require...)
	e.GET("/api/v1/clusters/:cluster_id/analytics/rankings/:dimension", ranking(store), require...)
	e.GET("/api/v1/clusters/:cluster_id/analytics/distributions/:dimension", ranking(store), require...)
	e.GET("/api/v1/clusters/:cluster_id/analytics/nodes/runtime", nodeRuntime(store), require...)
	e.GET("/api/v1/clusters/:cluster_id/analytics/nodes/runtime/latest", latestNodeRuntime(store), require...)
	e.GET("/api/v1/clusters/:cluster_id/analytics/nodes/logs", nodeLogs(store), require...)
	e.GET("/api/v1/clusters/:cluster_id/analytics/sites/logs", siteLogs(store), require...)
	e.GET("/api/v1/clusters/:cluster_id/analytics/logs/stream", liveLogs(store), require...)
	e.GET("/api/v1/clusters/:cluster_id/analytics/waf", wafStats(store), require...)
}

func liveLogs(s *analytics.Store) echo.HandlerFunc {
	return func(c *echo.Context) error {
		ctx := c.Request().Context()
		response := c.Response()
		response.Header().Set("Content-Type", "text/event-stream")
		response.Header().Set("Cache-Control", "no-cache, no-transform")
		response.Header().Set("Connection", "keep-alive")
		response.Header().Set("X-Accel-Buffering", "no")
		response.WriteHeader(http.StatusOK)

		events := s.Subscribe(ctx, analytics.LiveFilter{
			ClusterID: c.Param("cluster_id"), SiteID: c.QueryParam("site_id"),
			NodeID: c.QueryParam("node_id"),
		}, 256)
		heartbeat := time.NewTicker(15 * time.Second)
		defer heartbeat.Stop()
		if _, err := fmt.Fprint(response, "event: ready\ndata: {}\n\n"); err != nil {
			return nil
		}
		_ = http.NewResponseController(response).Flush()
		for {
			select {
			case <-ctx.Done():
				return nil
			case event := <-events:
				encoded, err := json.Marshal(event)
				if err != nil {
					continue
				}
				if _, err := fmt.Fprintf(response, "event: access_log\ndata: %s\n\n", encoded); err != nil {
					return nil
				}
				_ = http.NewResponseController(response).Flush()
			case <-heartbeat.C:
				if _, err := fmt.Fprint(response, ": keepalive\n\n"); err != nil {
					return nil
				}
				_ = http.NewResponseController(response).Flush()
			}
		}
	}
}

func wafStats(s *analytics.Store) echo.HandlerFunc {
	return func(c *echo.Context) error {
		siteID := c.QueryParam("site_id")
		if siteID == "" {
			return echo.NewHTTPError(http.StatusBadRequest, "site_id is required")
		}
		from, to, err := period(c)
		if err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid time range")
		}
		limit := 100
		if value, parseErr := strconv.Atoi(c.QueryParam("limit")); parseErr == nil && value > 0 && value <= 500 {
			limit = value
		}
		items, err := s.WAFRuleStats(c.Request().Context(), c.Param("cluster_id"), siteID, from, to, limit)
		if err != nil {
			return err
		}
		return types.JSON(c, http.StatusOK, items)
	}
}

func siteLogs(s *analytics.Store) echo.HandlerFunc {
	return func(c *echo.Context) error {
		siteID := c.QueryParam("site_id")
		if siteID == "" {
			return echo.NewHTTPError(http.StatusBadRequest, "site_id is required")
		}
		limit := 100
		if value, err := strconv.Atoi(c.QueryParam("limit")); err == nil && value > 0 && value <= 500 {
			limit = value
		}
		items, err := s.SiteRequestLogs(c.Request().Context(), c.Param("cluster_id"), siteID, limit)
		if err != nil {
			return err
		}
		return types.JSON(c, http.StatusOK, items)
	}
}

func nodeLogs(s *analytics.Store) echo.HandlerFunc {
	return func(c *echo.Context) error {
		nodeID := c.QueryParam("node_id")
		if nodeID == "" {
			return echo.NewHTTPError(http.StatusBadRequest, "node_id is required")
		}
		limit := 100
		if value, err := strconv.Atoi(c.QueryParam("limit")); err == nil && value > 0 && value <= 500 {
			limit = value
		}
		items, err := s.NodeRequestLogs(c.Request().Context(), c.Param("cluster_id"), nodeID, limit)
		if err != nil {
			return err
		}
		return types.JSON(c, http.StatusOK, items)
	}
}

// @summary Monitoring traffic overview
// @description Today, yesterday and current-month traffic totals.
// @Tags analytics
func monitoringOverview(s *analytics.Store) echo.HandlerFunc {
	return func(c *echo.Context) error {
		result, err := s.MonitoringOverview(
			c.Request().Context(),
			c.Param("cluster_id"),
			c.QueryParam("site_id"),
		)
		if err != nil {
			return err
		}
		return types.JSON(c, http.StatusOK, result)
	}
}

// @summary Latest node runtime metrics
// @description Latest recorded runtime metric for each node.
// @Tags analytics
func latestNodeRuntime(s *analytics.Store) echo.HandlerFunc {
	return func(c *echo.Context) error {
		items, err := s.LatestNodeRuntime(
			c.Request().Context(),
			c.Param("cluster_id"),
			c.QueryParam("node_id"),
		)
		if err != nil {
			return err
		}

		return types.JSON(c, http.StatusOK, items)
	}
}

type trafficResponse struct {
	Period      string                   `json:"period"`
	Granularity string                   `json:"granularity"`
	Series      []analytics.TrafficPoint `json:"series"`
}
type nodeRuntimeResponse struct {
	Period string                       `json:"period"`
	Series []analytics.NodeRuntimePoint `json:"series"`
}

func chartPeriod(c *echo.Context) (string, error) {
	value := c.QueryParam("period")
	if value == "" {
		value = "24h"
	}

	if value != "24h" && value != "30d" {
		return "", echo.NewHTTPError(http.StatusBadRequest, "period must be 24h or 30d")
	}

	return value, nil
}

func nodeRuntimePeriod(c *echo.Context) (string, error) {
	value := c.QueryParam("period")
	if value == "" {
		value = "12h"
	}
	if value != "12h" && value != "24h" && value != "30d" {
		return "", echo.NewHTTPError(http.StatusBadRequest, "period must be 12h, 24h or 30d")
	}
	return value, nil
}

// @summary Traffic series
// @description Time-series traffic metrics for a cluster or site (period: 24h or 30d).
// @Tags analytics
func traffic(s *analytics.Store) echo.HandlerFunc {
	return func(c *echo.Context) error {
		p, err := chartPeriod(c)
		if err != nil {
			return err
		}

		items, err := s.TrafficSeries(
			c.Request().Context(),
			c.Param("cluster_id"),
			c.QueryParam("site_id"),
			c.QueryParam("node_id"),
			p,
		)
		if err != nil {
			return err
		}

		granularity := "hour"
		if p == "30d" {
			granularity = "day"
		}

		return types.JSON(c, http.StatusOK, trafficResponse{Period: p, Granularity: granularity, Series: items})
	}
}

// @summary Ranking / distribution
// @description Rank or distribute traffic by dimension (period, sort, limit query params; node_id filter supported for 24h).
// @Tags analytics
func ranking(s *analytics.Store) echo.HandlerFunc {
	return func(c *echo.Context) error {
		p, err := chartPeriod(c)
		if err != nil {
			return err
		}

		nodeID := c.QueryParam("node_id")
		if nodeID != "" && p == "30d" {
			return echo.NewHTTPError(http.StatusBadRequest, "node_id filter is only supported for the 24h period")
		}

		limit := 20
		if v, e := strconv.Atoi(c.QueryParam("limit")); e == nil && v > 0 && v <= 100 {
			limit = v
		}

		sortBy := c.QueryParam("sort")
		if sortBy == "" {
			sortBy = "requests"
		}
		if sortBy != "requests" && sortBy != "traffic" {
			return echo.NewHTTPError(http.StatusBadRequest, "sort must be requests or traffic")
		}

		items, err := s.Ranking(
			c.Request().Context(),
			c.Param("cluster_id"),
			c.QueryParam("site_id"),
			nodeID,
			p,
			c.Param("dimension"),
			sortBy,
			limit,
		)
		if err != nil {
			if errors.Is(err, analytics.ErrInvalidDimension) {
				return echo.NewHTTPError(http.StatusBadRequest, "invalid dimension")
			}
			return err
		}

		return types.JSON(c, http.StatusOK, items)
	}
}

// @summary Node runtime series
// @description Time-series node runtime metrics (optional node_id filter).
// @Tags analytics
func nodeRuntime(s *analytics.Store) echo.HandlerFunc {
	return func(c *echo.Context) error {
		p, err := nodeRuntimePeriod(c)
		if err != nil {
			return err
		}

		items, err := s.NodeRuntime(
			c.Request().Context(),
			c.Param("cluster_id"),
			c.QueryParam("node_id"),
			p,
		)
		if err != nil {
			return err
		}

		return types.JSON(c, http.StatusOK, nodeRuntimeResponse{Period: p, Series: items})
	}
}

func period(c *echo.Context) (time.Time, time.Time, error) {
	to := time.Now().UTC()
	from := to.Add(-24 * time.Hour)
	var err error

	if v := c.QueryParam("from"); v != "" {
		from, err = time.Parse(time.RFC3339, v)
		if err != nil {
			return from, to, err
		}
	}

	if v := c.QueryParam("to"); v != "" {
		to, err = time.Parse(time.RFC3339, v)
	}

	return from, to, err
}

// @summary Analytics summary
// @description Aggregate request/traffic summary for a time range (from/to).
// @Tags analytics
func summary(s *analytics.Store) echo.HandlerFunc {
	return func(c *echo.Context) error {
		from, to, err := period(c)
		if err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid time range")
		}

		v, err := s.Summary(
			c.Request().Context(),
			c.Param("cluster_id"),
			c.QueryParam("site_id"),
			from,
			to,
		)
		if err != nil {
			return err
		}

		return types.JSON(c, http.StatusOK, v)
	}
}

// @summary Top URLs or IPs
// @description Top ranked URLs or client IPs for a time range.
// @Tags analytics
func top(s *analytics.Store, dim string) echo.HandlerFunc {
	return func(c *echo.Context) error {
		from, to, err := period(c)
		if err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid time range")
		}

		limit := 20
		if v, e := strconv.Atoi(c.QueryParam("limit")); e == nil && v > 0 && v <= 100 {
			limit = v
		}

		items, err := s.Top(
			c.Request().Context(),
			dim,
			c.Param("cluster_id"),
			c.QueryParam("site_id"),
			from,
			to,
			limit,
		)
		if err != nil {
			return err
		}

		return types.JSON(c, http.StatusOK, items)
	}
}
