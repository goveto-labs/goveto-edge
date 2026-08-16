package metrics

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/labstack/echo/v5"

	"goveto-edge/internal/httpapi/types"
	"goveto-edge/internal/telemetry"
)

// Middleware records per-route request counts and latency. The route label
// uses the registered route template (c.Path()), never the raw request path,
// so label cardinality stays bounded.
func Middleware() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			start := time.Now()
			err := next(c)
			route := c.Path()
			if route == "" {
				route = "unknown"
			}
			status := responseStatus(c, err)
			telemetry.HTTPRequestsTotal.WithLabelValues(route, metricMethod(c.Request().Method), strconv.Itoa(status)).Inc()
			telemetry.HTTPRequestDuration.WithLabelValues(route).Observe(time.Since(start).Seconds())
			return err
		}
	}
}

func metricMethod(method string) string {
	switch method {
	case http.MethodConnect, http.MethodDelete, http.MethodGet, http.MethodHead,
		http.MethodOptions, http.MethodPatch, http.MethodPost, http.MethodPut,
		http.MethodTrace:
		return method
	default:
		return "OTHER"
	}
}

// responseStatus mirrors types.HTTPErrorHandler: when the handler returned an
// error the status is only decided once echo invokes the error handler after
// the middleware chain, so derive it from the error value instead.
func responseStatus(c *echo.Context, err error) int {
	if err != nil {
		var apiErr *types.APIError
		var he *echo.HTTPError
		switch {
		case errors.As(err, &apiErr) && apiErr.Status != 0:
			return apiErr.Status
		case errors.As(err, &he) && he.Code != 0:
			return he.Code
		default:
			return http.StatusInternalServerError
		}
	}
	if r, unwrapErr := echo.UnwrapResponse(c.Response()); unwrapErr == nil && r.Status != 0 {
		return r.Status
	}
	return http.StatusOK
}
