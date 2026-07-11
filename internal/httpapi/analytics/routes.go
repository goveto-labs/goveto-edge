package analytics

import (
	"net/http"
	"strconv"
	"time"

	"github.com/labstack/echo/v5"

	"goveto-edge/internal/analytics"
	"goveto-edge/internal/auth"
	"goveto-edge/internal/clusteraccess"
	"goveto-edge/internal/storage/gen/client"
)

func Register(e *echo.Echo, db *client.Client, store *analytics.Store) {
	if store == nil {
		return
	}

	require := []echo.MiddlewareFunc{auth.RequireAuth, clusteraccess.Require(db)}

	e.GET("/api/v1/clusters/:cluster_id/analytics/summary", summary(store), require...)
	e.GET("/api/v1/clusters/:cluster_id/analytics/top-urls", top(store, "url"), require...)
	e.GET("/api/v1/clusters/:cluster_id/analytics/top-ips", top(store, "ip"), require...)
	e.GET("/api/v1/clusters/:cluster_id/analytics/traffic", traffic(store), require...)
	e.GET("/api/v1/clusters/:cluster_id/analytics/rankings/:dimension", ranking(store), require...)
	e.GET("/api/v1/clusters/:cluster_id/analytics/distributions/:dimension", ranking(store), require...)
	e.GET("/api/v1/clusters/:cluster_id/analytics/nodes/runtime", nodeRuntime(store), require...)
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
			p,
		)
		if err != nil {
			return err
		}

		granularity := "hour"
		if p == "30d" {
			granularity = "day"
		}

		return c.JSON(http.StatusOK, map[string]any{
			"period":      p,
			"granularity": granularity,
			"series":      items,
		})
	}
}

func ranking(s *analytics.Store) echo.HandlerFunc {
	return func(c *echo.Context) error {
		p, err := chartPeriod(c)
		if err != nil {
			return err
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
			p,
			c.Param("dimension"),
			sortBy,
			limit,
		)
		if err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, err.Error())
		}

		return c.JSON(http.StatusOK, items)
	}
}

func nodeRuntime(s *analytics.Store) echo.HandlerFunc {
	return func(c *echo.Context) error {
		p, err := chartPeriod(c)
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

		return c.JSON(http.StatusOK, map[string]any{
			"period": p,
			"series": items,
		})
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

		return c.JSON(http.StatusOK, v)
	}
}

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

		return c.JSON(http.StatusOK, items)
	}
}
