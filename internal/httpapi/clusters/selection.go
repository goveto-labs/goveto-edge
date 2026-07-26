package clusters

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/labstack/echo/v5"

	"goveto-edge/internal/audit"
	"goveto-edge/internal/auth"
	"goveto-edge/internal/clusteraccess"
	"goveto-edge/internal/httpapi/types"
	"goveto-edge/internal/rbac"
	"goveto-edge/internal/storage/gen/client"
	"goveto-edge/internal/storage/gen/model"
	"goveto-edge/internal/storage/gen/query"
)

type clusterChoice struct {
	ID        string    `db:"id" json:"id"`
	Name      string    `db:"name" json:"name"`
	Role      string    `db:"role" json:"role"`
	CreatedAt time.Time `db:"created_at" json:"created_at"`
}

type clusterListResponse struct {
	Clusters          []clusterChoice `json:"clusters"`
	SelectedClusterID string          `json:"selected_cluster_id"`
	RequiresCluster   bool            `json:"requires_cluster"`
}

type clusterCreateResponse struct {
	Cluster           clusterChoice `json:"cluster"`
	SelectedClusterID string        `json:"selected_cluster_id"`
}

func registerSelection(e *echo.Echo, db *client.Client, sessions *auth.SessionStore) {
	e.GET("/api/v1/clusters", listAvailable(db, sessions), auth.RequireAuth)
	e.POST("/api/v1/clusters", create(db, sessions), auth.RequireAuth)
	e.PUT("/api/v1/session/cluster", selectCurrent(db, sessions), auth.RequireAuth)
}

func available(ctx context.Context, db *client.Client, uid string) ([]clusterChoice, error) {
	user, cached := auth.CurrentUser(ctx, uid)
	if !cached {
		var err error
		user, err = db.User.FindUnique(ctx, query.User.Id.Equals(uid))
		if err != nil {
			return nil, err
		}
	}
	if user == nil || user.Status != model.UserStatusACTIVE {
		return nil, nil
	}
	if user.Role == model.UserRoleADMIN {
		return client.Raw[clusterChoice](ctx, db, `SELECT c.id, c.name, 'ADMIN' AS role, c.created_at
			FROM clusters c ORDER BY c.created_at, c.name`)
	}
	items, err := client.Raw[clusterChoice](ctx, db, `SELECT c.id, c.name,
		CASE WHEN c.creator_id = $1 THEN 'OWNER' ELSE cm.permission::text END AS role,
		c.created_at
		FROM clusters c
		LEFT JOIN cluster_members cm ON cm.cluster_id = c.id AND cm.user_id = $1
		WHERE c.creator_id = $1 OR cm.user_id = $1
		ORDER BY c.created_at, c.name`, uid)
	if err != nil {
		return nil, err
	}
	for index := range items {
		items[index].Role = string(rbac.Highest(rbac.Role(items[index].Role), rbac.Role(user.Role)))
	}
	return items, nil
}

// @summary List clusters
// @description List clusters the current user can access and the selected cluster id.
// @Tags clusters
func listAvailable(db *client.Client, sessions *auth.SessionStore) echo.HandlerFunc {
	return func(c *echo.Context) error {
		ctx := c.Request().Context()
		items, err := available(ctx, db, auth.CurrentUID(c))
		if err != nil {
			return err
		}
		if items == nil {
			items = make([]clusterChoice, 0)
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
		return types.JSON(c, http.StatusOK, clusterListResponse{Clusters: items, SelectedClusterID: selected, RequiresCluster: len(items) == 0})
	}
}

// @summary Create cluster
// @description Create a new cluster owned by the current user and select it in session.
// @Tags clusters
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
		response := clusterCreateResponse{Cluster: clusterChoice{ID: item.Id, Name: item.Name, Role: "OWNER", CreatedAt: item.CreatedAt}, SelectedClusterID: item.Id}
		audit.SetResourceID(c, item.Id)
		audit.SetChange(c, nil, response.Cluster)
		return types.JSON(c, http.StatusCreated, response)
	}
}

type selectRequest struct {
	ClusterID string `json:"cluster_id"`
}
type selectResponse struct {
	SelectedClusterID string `json:"selected_cluster_id"`
}

// @summary Select cluster
// @description Set the current session cluster context.
// @Tags session
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
		audit.SetResourceID(c, input.ClusterID)
		audit.SetChange(c, nil, selectResponse{SelectedClusterID: input.ClusterID})
		return types.JSON(c, http.StatusOK, selectResponse{SelectedClusterID: input.ClusterID})
	}
}
