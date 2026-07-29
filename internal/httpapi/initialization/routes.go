// Package initialization registers first-run instance setup endpoints.
package initialization

import (
	"net/http"
	"strings"
	"time"

	"github.com/labstack/echo/v5"

	"goveto-edge/internal/audit"
	"goveto-edge/internal/httpapi/types"
	"goveto-edge/internal/httpsecurity"
	"goveto-edge/internal/password"
	"goveto-edge/internal/settings"
	"goveto-edge/internal/storage/gen/client"
	"goveto-edge/internal/storage/gen/model"
	"goveto-edge/internal/storage/gen/query"
)

const initializationLockID = 691782413

type request struct {
	Email                     string `json:"email"`
	Password                  string `json:"password"`
	Name                      string `json:"name"`
	AgentGatewayPublicAddress string `json:"agent_gateway_public_address"`
}

type statusResponse struct {
	Initialized bool `json:"initialized"`
}

type userResponse struct {
	ID              string           `json:"id"`
	Email           string           `json:"email"`
	Name            string           `json:"name"`
	Role            model.UserRole   `json:"role"`
	Status          model.UserStatus `json:"status"`
	IsInstanceOwner bool             `json:"is_instance_owner"`
}

func Register(e *echo.Echo, db *client.Client, settingStore *settings.Store, limiter *httpsecurity.RateLimiter) {
	group := e.Group("/api/v1/init")
	group.GET("/status", status(settingStore), limiter.Limit("initialization-status", 60, time.Minute))
	group.POST("", initialize(db), limiter.Limit("initialization", 5, time.Hour))
}

// @summary Instance initialization status
// @description Return whether initial instance setup has completed.
// @Tags initialization
func status(settingStore *settings.Store) echo.HandlerFunc {
	return func(c *echo.Context) error {
		initialized, err := settingStore.Initialized(c.Request().Context())
		if err != nil {
			return err
		}
		return types.JSON(c, http.StatusOK, statusResponse{Initialized: initialized})
	}
}

// @summary Initialize instance
// @description Create the first administrator account and mark the instance initialized.
// @Tags initialization
func initialize(db *client.Client) echo.HandlerFunc {
	return func(c *echo.Context) error {
		var input request
		if err := c.Bind(&input); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid request body")
		}
		input.Email = strings.ToLower(strings.TrimSpace(input.Email))
		input.Name = strings.TrimSpace(input.Name)
		audit.SetActor(c, "", input.Email)
		if input.Email == "" || input.Name == "" {
			return echo.NewHTTPError(http.StatusBadRequest, "email and name are required")
		}
		gatewayAddress, err := settings.ValidateAgentGatewayPublicAddress(input.AgentGatewayPublicAddress)
		if err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, err.Error())
		}
		if err := password.Validate(input.Password); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, err.Error())
		}

		hash, err := password.Hash(input.Password)
		if err != nil {
			return err
		}

		ctx := c.Request().Context()
		var administrator *model.User
		err = db.Tx(ctx, func(tx *client.Client) error {
			if _, lockErr := tx.RawExec(ctx, "SELECT pg_advisory_xact_lock($1)", initializationLockID); lockErr != nil {
				return lockErr
			}

			store := settings.New(tx, audit.New(tx))
			initialized, statusErr := store.Initialized(ctx)
			if statusErr != nil {
				return statusErr
			}
			if initialized {
				return echo.NewHTTPError(http.StatusConflict, "instance is already initialized")
			}

			administrator, err = tx.User.Create().Set(
				query.User.Email.Set(input.Email),
				query.User.PasswordHash.Set(hash),
				query.User.Name.Set(input.Name),
				query.User.Role.Set(model.UserRoleADMIN),
				query.User.Status.Set(model.UserStatusACTIVE),
				query.User.UpdatedAt.Set(time.Now().UTC()),
			).Do(ctx)
			if err != nil {
				return err
			}
			if err := store.Set(ctx, settings.InstanceOwnerUserIDKey, administrator.Id, "User allowed to manage instance-level settings"); err != nil {
				return err
			}
			if err := store.SetAgentGatewayPublicAddress(ctx, gatewayAddress); err != nil {
				return err
			}
			return store.Set(ctx, settings.InstanceInitializedKey, true, "Whether initial instance setup has completed")
		})
		if err != nil {
			return err
		}

		response := userResponse{
			ID: administrator.Id, Email: administrator.Email, Name: administrator.Name,
			Role: administrator.Role, Status: administrator.Status, IsInstanceOwner: true,
		}
		audit.SetActor(c, administrator.Id, administrator.Email)
		audit.SetResourceID(c, administrator.Id)
		audit.SetChange(c, nil, response)
		return types.JSON(c, http.StatusCreated, response)
	}
}
