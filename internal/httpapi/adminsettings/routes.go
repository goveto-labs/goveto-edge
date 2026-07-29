// Package adminsettings exposes instance-level settings to the instance owner.
package adminsettings

import (
	"net/http"
	"time"

	"github.com/labstack/echo/v5"

	authn "goveto-edge/internal/auth"
	"goveto-edge/internal/httpapi/types"
	"goveto-edge/internal/settings"
)

type response struct {
	AgentGatewayPublicAddress string `json:"agent_gateway_public_address"`
	RestartRequired           bool   `json:"restart_required"`
	Restarting                bool   `json:"restarting"`
}

type updateRequest struct {
	AgentGatewayPublicAddress string `json:"agent_gateway_public_address"`
	Restart                   bool   `json:"restart"`
}

func Register(e *echo.Echo, settingStore *settings.Store, restartControlPlane func()) {
	group := e.Group(
		"/api/v1/admin/settings",
		authn.RequireAuth,
		requireInstanceOwner(settingStore),
	)
	group.GET("", get(settingStore))
	group.PUT("", update(settingStore, restartControlPlane))
}

func requireInstanceOwner(settingStore *settings.Store) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			allowed, err := settingStore.IsInstanceOwner(c.Request().Context(), authn.CurrentUID(c))
			if err != nil {
				return err
			}
			if !allowed {
				return echo.NewHTTPError(http.StatusForbidden, "instance owner access required")
			}
			return next(c)
		}
	}
}

// @summary Get instance settings
// @description Return system-level settings visible only to the instance owner.
// @Tags admin settings
func get(settingStore *settings.Store) echo.HandlerFunc {
	return func(c *echo.Context) error {
		result, err := readResponse(c, settingStore, false, false)
		if err != nil {
			return err
		}
		return types.JSON(c, http.StatusOK, result)
	}
}

// @summary Update instance settings
// @description Update system-level settings as the instance owner.
// @Tags admin settings
func update(settingStore *settings.Store, restartControlPlane func()) echo.HandlerFunc {
	return func(c *echo.Context) error {
		var input updateRequest
		if err := c.Bind(&input); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid request body")
		}
		address, err := settings.ValidateAgentGatewayPublicAddress(input.AgentGatewayPublicAddress)
		if err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, err.Error())
		}
		current, _, err := settingStore.AgentGatewayPublicAddress(c.Request().Context())
		if err != nil {
			return err
		}
		restartRequired := current != address
		if restartRequired {
			if err := settingStore.SetAgentGatewayPublicAddress(c.Request().Context(), address); err != nil {
				return err
			}
		}
		restarting := restartRequired && input.Restart && restartControlPlane != nil

		result, err := readResponse(c, settingStore, restartRequired, restarting)
		if err != nil {
			return err
		}
		if restarting {
			go func() {
				time.Sleep(500 * time.Millisecond)
				restartControlPlane()
			}()
		}
		return types.JSON(c, http.StatusOK, result)
	}
}

func readResponse(c *echo.Context, settingStore *settings.Store, restartRequired, restarting bool) (response, error) {
	address, found, err := settingStore.AgentGatewayPublicAddress(c.Request().Context())
	if err != nil {
		return response{}, err
	}
	if !found {
		return response{}, echo.NewHTTPError(http.StatusInternalServerError, "agent gateway public address is not configured")
	}
	return response{
		AgentGatewayPublicAddress: address,
		RestartRequired:           restartRequired,
		Restarting:                restarting,
	}, nil
}
