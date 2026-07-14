package types

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/labstack/echo/v5"
)

const CodeOK = "ok"

type H struct {
	Code string `json:"code"`
	Msg  string `json:"msg,omitempty"`
	Data any    `json:"data,omitempty"`
}

func OK(data any) H {
	return H{Code: CodeOK, Data: data}
}

func Fail(code, msg string) H {
	return H{Code: code, Msg: msg}
}

func JSON(c *echo.Context, status int, data any) error {
	return c.JSON(status, OK(data))
}

func HTTPErrorHandler(c *echo.Context, err error) {
	if r, _ := echo.UnwrapResponse(c.Response()); r != nil && r.Committed {
		return
	}

	status := http.StatusInternalServerError
	msg := http.StatusText(status)
	code := "internal_error"

	var he *echo.HTTPError
	if errors.As(err, &he) {
		if he.Code != 0 {
			status = he.Code
		}
		if he.Message != "" {
			msg = he.Message
		} else {
			msg = http.StatusText(status)
		}
		code = httpStatusCode(status)
	} else {
		slog.Error("http request failed",
			"method", c.Request().Method,
			"path", c.Request().URL.Path,
			"error", err,
		)
	}

	if c.Request().Method == http.MethodHead {
		_ = c.NoContent(status)
		return
	}
	_ = c.JSON(status, Fail(code, msg))
}

func httpStatusCode(status int) string {
	switch status {
	case http.StatusBadRequest:
		return "bad_request"
	case http.StatusUnauthorized:
		return "unauthorized"
	case http.StatusForbidden:
		return "forbidden"
	case http.StatusNotFound:
		return "not_found"
	case http.StatusConflict:
		return "conflict"
	case http.StatusAccepted:
		return "accepted"
	case http.StatusCreated:
		return "created"
	case http.StatusServiceUnavailable:
		return "service_unavailable"
	case http.StatusBadGateway:
		return "bad_gateway"
	default:
		if status >= 500 {
			return "internal_error"
		}
		return "error"
	}
}
