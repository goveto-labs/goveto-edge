// Package users exposes platform-level user administration.
package users

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v5"

	"goveto-edge/internal/audit"
	authn "goveto-edge/internal/auth"
	"goveto-edge/internal/clusteraccess"
	"goveto-edge/internal/httpapi/types"
	"goveto-edge/internal/rbac"
	"goveto-edge/internal/storage/gen/client"
	"goveto-edge/internal/storage/gen/model"
	"goveto-edge/internal/storage/gen/query"
)

type userResponse struct {
	ID          string           `json:"id"`
	Email       string           `json:"email"`
	Name        string           `json:"name"`
	Role        model.UserRole   `json:"role"`
	Status      model.UserStatus `json:"status"`
	TOTPEnabled bool             `json:"totp_enabled"`
	LastLoginAt *time.Time       `json:"last_login_at,omitempty"`
	CreatedAt   time.Time        `json:"created_at"`
	UpdatedAt   time.Time        `json:"updated_at"`
}

type userListResponse struct {
	Items    []userResponse `json:"items"`
	Total    int64          `json:"total"`
	Page     int            `json:"page"`
	PageSize int            `json:"page_size"`
}

type updateStatusRequest struct {
	Status model.UserStatus `json:"status"`
}

func Register(e *echo.Echo, db *client.Client, sessions *authn.SessionStore) {
	guard := []echo.MiddlewareFunc{
		authn.RequireAuth,
		clusteraccess.RequirePlatform(db, rbac.PermissionPlatformUserManage),
	}
	e.GET("/api/v1/users", listUsers(db), guard...)
	e.PATCH("/api/v1/users/:user_id/status", updateStatus(db, sessions), guard...)
}

// @summary List platform users
// @description Paginated user directory with search, role and status filters; requires platform user management permission.
// @Tags users
func listUsers(db *client.Client) echo.HandlerFunc {
	return func(c *echo.Context) error {
		page := positiveInt(c.QueryParam("page"), 1, 0)
		pageSize := positiveInt(c.QueryParam("page_size"), 50, 200)
		wheres, err := userFilters(c)
		if err != nil {
			return err
		}
		builder := db.User.Query().Where(wheres...).OrderBy(query.User.CreatedAt.Desc())
		total, err := builder.Count(c.Request().Context())
		if err != nil {
			return err
		}
		items, err := builder.Skip((page - 1) * pageSize).Take(pageSize).Do(c.Request().Context())
		if err != nil {
			return err
		}
		responses := make([]userResponse, len(items))
		for index := range items {
			responses[index] = newUserResponse(&items[index])
		}
		return types.JSON(c, http.StatusOK, userListResponse{
			Items: responses, Total: total, Page: page, PageSize: pageSize,
		})
	}
}

// @summary Update user status
// @description Enable or disable a platform user; disabling revokes all sessions and cannot target the caller or final active administrator.
// @Tags users
func updateStatus(db *client.Client, sessions *authn.SessionStore) echo.HandlerFunc {
	return func(c *echo.Context) error {
		var input updateStatusRequest
		if err := c.Bind(&input); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid request body")
		}
		if input.Status != model.UserStatusACTIVE && input.Status != model.UserStatusDISABLED {
			return echo.NewHTTPError(http.StatusBadRequest, "status must be ACTIVE or DISABLED")
		}

		ctx := c.Request().Context()
		targetID := c.Param("user_id")
		var before, updated *model.User
		err := db.Tx(ctx, func(tx *client.Client) error {
			var err error
			before, err = tx.User.FindUnique(ctx, query.User.Id.Equals(targetID))
			if err != nil {
				return err
			}
			if before == nil {
				return echo.NewHTTPError(http.StatusNotFound, "user not found")
			}
			if before.Status == input.Status {
				updated = before
				return nil
			}
			if input.Status == model.UserStatusDISABLED {
				activeAdmins := 0
				if before.Role == model.UserRoleADMIN {
					count, countErr := lockAndCountActiveAdmins(ctx, tx)
					if countErr != nil {
						return countErr
					}
					activeAdmins = count
				}
				if conflict := validateStatusChange(authn.CurrentUID(c), before, input.Status, activeAdmins); conflict != nil {
					return conflict
				}
			}
			updated, err = tx.User.Update().Where(query.User.Id.Equals(targetID)).Set(
				query.User.Status.Set(input.Status), query.User.UpdatedAt.Set(time.Now()),
			).Do(ctx)
			return err
		})
		if err != nil {
			return err
		}
		if input.Status == model.UserStatusDISABLED && before.Status != model.UserStatusDISABLED {
			if err = sessions.RevokeAll(ctx, targetID, ""); err != nil {
				return err
			}
		}
		response := newUserResponse(updated)
		audit.SetResourceID(c, targetID)
		audit.SetChange(c, newUserResponse(before), response)
		return types.JSON(c, http.StatusOK, response)
	}
}

func validateStatusChange(actorID string, target *model.User, status model.UserStatus, activeAdmins int) error {
	if status != model.UserStatusDISABLED {
		return nil
	}
	if target.Id == actorID {
		return echo.NewHTTPError(http.StatusConflict, "you cannot disable your own account")
	}
	if target.Role == model.UserRoleADMIN && activeAdmins <= 1 {
		return echo.NewHTTPError(http.StatusConflict, "the final active administrator cannot be disabled")
	}
	return nil
}

func userFilters(c *echo.Context) ([]query.UserWhereClause, error) {
	var wheres []query.UserWhereClause
	if search := strings.TrimSpace(c.QueryParam("search")); search != "" {
		wheres = append(wheres, query.User.OR(
			query.User.Email.Contains(search), query.User.Name.Contains(search),
		))
	}
	if value := strings.ToUpper(strings.TrimSpace(c.QueryParam("role"))); value != "" {
		role := model.UserRole(value)
		if role != model.UserRoleADMIN && role != model.UserRoleOPERATOR && role != model.UserRoleVIEWER {
			return nil, echo.NewHTTPError(http.StatusBadRequest, "invalid role")
		}
		wheres = append(wheres, query.User.Role.Equals(role))
	}
	if value := strings.ToUpper(strings.TrimSpace(c.QueryParam("status"))); value != "" {
		status := model.UserStatus(value)
		if status != model.UserStatusACTIVE && status != model.UserStatusDISABLED {
			return nil, echo.NewHTTPError(http.StatusBadRequest, "invalid status")
		}
		wheres = append(wheres, query.User.Status.Equals(status))
	}
	return wheres, nil
}

func lockAndCountActiveAdmins(ctx context.Context, db *client.Client) (int, error) {
	rows, err := client.Raw[struct {
		ID string `db:"id"`
	}](ctx, db, `SELECT id FROM users WHERE role='ADMIN' AND status='ACTIVE' FOR UPDATE`)
	return len(rows), err
}

func newUserResponse(user *model.User) userResponse {
	if user == nil {
		return userResponse{}
	}
	return userResponse{
		ID: user.Id, Email: user.Email, Name: user.Name, Role: user.Role, Status: user.Status,
		TOTPEnabled: user.TotpSecret != nil && strings.TrimSpace(*user.TotpSecret) != "",
		LastLoginAt: user.LastLoginAt, CreatedAt: user.CreatedAt, UpdatedAt: user.UpdatedAt,
	}
}

func positiveInt(raw string, fallback, maximum int) int {
	value, err := strconv.Atoi(raw)
	if err != nil || value < 1 || (maximum > 0 && value > maximum) {
		return fallback
	}
	return value
}
