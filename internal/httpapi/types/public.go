package types

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/labstack/echo/v5"
)

const CodeOK = "ok"

// Stable API error codes for client branching.
const (
	CodeSSHHostKeyChanged  = "ssh_host_key_changed"
	CodeSSHHostKeyRequired = "ssh_host_key_required"
)

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

// APIError is an HTTP error with a stable machine-readable code.
type APIError struct {
	Status  int
	Code    string
	Message string
}

func (e *APIError) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

// Error returns an APIError that HTTPErrorHandler serializes with Code.
func Error(status int, code, message string) *APIError {
	return &APIError{Status: status, Code: code, Message: message}
}

func HTTPErrorHandler(c *echo.Context, err error) {
	if r, _ := echo.UnwrapResponse(c.Response()); r != nil && r.Committed {
		return
	}

	status := http.StatusInternalServerError
	msg := http.StatusText(status)
	code := "internal_error"

	var apiErr *APIError
	var he *echo.HTTPError
	switch {
	case errors.As(err, &apiErr):
		if apiErr.Status != 0 {
			status = apiErr.Status
		}
		if apiErr.Message != "" {
			msg = apiErr.Message
		} else {
			msg = http.StatusText(status)
		}
		if apiErr.Code != "" {
			code = apiErr.Code
		} else {
			code = httpStatusCode(status)
		}
	case errors.As(err, &he):
		if he.Code != 0 {
			status = he.Code
		}
		if he.Message != "" {
			msg = he.Message
		} else {
			msg = http.StatusText(status)
		}
		code = httpStatusCode(status)
	default:
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
