package clusters

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/labstack/echo/v5"
	"goveto-edge/internal/auth"
	"goveto-edge/internal/clusteraccess"
	"goveto-edge/internal/storage/gen/client"
	"goveto-edge/internal/storage/gen/query"
)

type clusterChoice struct {
	ID        string    `db:"id" json:"id"`
	Name      string    `db:"name" json:"name"`
	Role      string    `db:"role" json:"role"`
	CreatedAt time.Time `db:"created_at" json:"created_at"`
}

func registerSelection(e *echo.Echo, db *client.Client, sessions *auth.SessionStore) {
	e.GET("/api/v1/clusters", listAvailable(db, sessions), auth.RequireAuth)
	e.POST("/api/v1/clusters", create(db, sessions), auth.RequireAuth)
	e.PUT("/api/v1/session/cluster", selectCurrent(db, sessions), auth.RequireAuth)
}

func available(ctx context.Context, db *client.Client, uid string) ([]clusterChoice, error) {
	return client.Raw[clusterChoice](ctx, db, `SELECT c.id, c.name,
		CASE WHEN c.creator_id = $1 THEN 'OWNER' ELSE cm.permission::text END AS role,
		c.created_at
		FROM clusters c
		LEFT JOIN cluster_members cm ON cm.cluster_id = c.id AND cm.user_id = $1
		WHERE c.creator_id = $1 OR cm.user_id = $1
		ORDER BY c.created_at, c.name`, uid)
}

func listAvailable(db *client.Client, sessions *auth.SessionStore) echo.HandlerFunc {
	return func(c *echo.Context) error {
		ctx := c.Request().Context()
		items, err := available(ctx, db, auth.CurrentUID(c))
		if err != nil {
			return err
		}
		selected, err := sessions.SelectedCluster(ctx, c)
		if err != nil {
			return err
		}
		valid := false
		for _, item := range items {
			if item.ID == selected {
				valid = true
				break
			}
		}
		if !valid {
			selected = ""
			if len(items) > 0 {
				selected = items[0].ID
				_ = sessions.SetSelectedCluster(ctx, c, selected)
			}
		}
		return c.JSON(http.StatusOK, map[string]any{"clusters": items, "selected_cluster_id": selected, "requires_cluster": len(items) == 0})
	}
}

func create(db *client.Client, sessions *auth.SessionStore) echo.HandlerFunc {
	return func(c *echo.Context) error {
		var input nameRequest
		if err := c.Bind(&input); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid request body")
		}
		input.Name = strings.TrimSpace(input.Name)
		if input.Name == "" || len([]rune(input.Name)) > 80 {
			return echo.NewHTTPError(http.StatusBadRequest, "name must contain between 1 and 80 characters")
		}
		item, err := db.Cluster.Create().Set(query.Cluster.CreatorId.Set(auth.CurrentUID(c)), query.Cluster.Name.Set(input.Name)).Do(c.Request().Context())
		if err != nil {
			return err
		}
		if err = sessions.SetSelectedCluster(c.Request().Context(), c, item.Id); err != nil {
			return err
		}
		return c.JSON(http.StatusCreated, map[string]any{"cluster": item, "selected_cluster_id": item.Id})
	}
}

type selectRequest struct {
	ClusterID string `json:"cluster_id"`
}

func selectCurrent(db *client.Client, sessions *auth.SessionStore) echo.HandlerFunc {
	return func(c *echo.Context) error {
		var input selectRequest
		if err := c.Bind(&input); err != nil || strings.TrimSpace(input.ClusterID) == "" {
			return echo.NewHTTPError(http.StatusBadRequest, "cluster_id is required")
		}
		allowed, _, err := clusteraccess.Check(c.Request().Context(), db, input.ClusterID, auth.CurrentUID(c))
		if err != nil {
			return err
		}
		if !allowed {
			return echo.NewHTTPError(http.StatusForbidden, "cluster access denied")
		}
		if err = sessions.SetSelectedCluster(c.Request().Context(), c, input.ClusterID); err != nil {
			return err
		}
		return c.JSON(http.StatusOK, map[string]string{"selected_cluster_id": input.ClusterID})
	}
}
