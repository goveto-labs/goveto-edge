package auth

import (
	"errors"
	"net/http"
	"time"

	"github.com/labstack/echo/v5"

	authn "goveto-edge/internal/auth"
	"goveto-edge/internal/httpapi/types"
	"goveto-edge/internal/storage/gen/client"
	"goveto-edge/internal/storage/gen/query"
)

type sessionResponse struct {
	ID         string    `json:"id"`
	IPAddress  *string   `json:"ip_address,omitempty"`
	UserAgent  *string   `json:"user_agent,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	LastSeenAt time.Time `json:"last_seen_at"`
	ExpiresAt  time.Time `json:"expires_at"`
	Current    bool      `json:"current"`
}

func registerSessions(group *echo.Group, db *client.Client, sessions *authn.SessionStore) {
	group.GET("/sessions", listSessions(db), authn.RequireAuth)
	group.DELETE("/sessions/:session_id", revokeSession(sessions), authn.RequireAuth)
	group.POST("/sessions/revoke-others", revokeOtherSessions(sessions), authn.RequireAuth)
}

func listSessions(db *client.Client) echo.HandlerFunc {
	return func(c *echo.Context) error {
		items, err := db.UserSession.Query().Where(
			query.UserSession.UserId.Equals(authn.CurrentUID(c)),
			query.UserSession.RevokedAt.IsNull(),
			query.UserSession.ExpiresAt.Gt(time.Now().UTC()),
		).OrderBy(query.UserSession.LastSeenAt.Desc()).Do(c.Request().Context())
		if err != nil {
			return err
		}
		response := make([]sessionResponse, 0, len(items))
		currentID := authn.CurrentSessionID(c)
		for _, item := range items {
			response = append(response, sessionResponse{
				ID: item.Id, IPAddress: item.IpAddress, UserAgent: item.UserAgent,
				CreatedAt: item.CreatedAt, LastSeenAt: item.LastSeenAt, ExpiresAt: item.ExpiresAt,
				Current: item.Id == currentID,
			})
		}
		return types.JSON(c, http.StatusOK, response)
	}
}

func revokeSession(sessions *authn.SessionStore) echo.HandlerFunc {
	return func(c *echo.Context) error {
		id := c.Param("session_id")
		if id == authn.CurrentSessionID(c) {
			if err := sessions.Logout(c); err != nil {
				return err
			}
		} else if err := sessions.Revoke(c.Request().Context(), authn.CurrentUID(c), id); err != nil {
			if errors.Is(err, authn.ErrSessionNotFound) {
				return echo.NewHTTPError(http.StatusNotFound, "session not found")
			}
			return err
		}
		return c.NoContent(http.StatusNoContent)
	}
}

func revokeOtherSessions(sessions *authn.SessionStore) echo.HandlerFunc {
	return func(c *echo.Context) error {
		if err := sessions.RevokeAll(c.Request().Context(), authn.CurrentUID(c), authn.CurrentSessionID(c)); err != nil {
			return err
		}
		return c.NoContent(http.StatusNoContent)
	}
}
