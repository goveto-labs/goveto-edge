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
	authorized, err := clusteraccess.ListAuthorizedClusters(ctx, db, uid, rbac.PermissionClusterRead)
	if err != nil {
		return nil, err
	}
	items := make([]clusterChoice, 0, len(authorized))
	for _, item := range authorized {
		items = append(items, clusterChoice{
			ID: item.ID, Name: item.Name, Role: item.Role, CreatedAt: item.CreatedAt,
		})
	}
	return items, nil
}

// @summary List clusters
// @description List clusters the current user can access and the selected cluster id. API key principals only see the cluster their key is bound to.
// @Tags clusters
func listAvailable(db *client.Client, sessions *auth.SessionStore) echo.HandlerFunc {
	return listAvailableWithAPIKeyChoices(db, sessions, apiKeyClusterChoices)
}

type apiKeyClusterChoiceLoader func(context.Context, *client.Client, *auth.APIKeyPrincipal) ([]clusterChoice, error)

func listAvailableWithAPIKeyChoices(db *client.Client, sessions *auth.SessionStore, loadAPIKeyChoices apiKeyClusterChoiceLoader) echo.HandlerFunc {
	return func(c *echo.Context) error {
		ctx := c.Request().Context()
		if principal := auth.CurrentAPIKey(c); principal != nil {
			items, err := loadAPIKeyChoices(ctx, db, principal)
			if err != nil {
				return err
			}
			return types.JSON(c, http.StatusOK, apiKeyClusterListResponse(principal, items))
		}
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

func apiKeyClusterListResponse(principal *auth.APIKeyPrincipal, items []clusterChoice) clusterListResponse {
	selected := ""
	if len(items) > 0 {
		selected = principal.ClusterID
	}
	return clusterListResponse{Clusters: items, SelectedClusterID: selected, RequiresCluster: len(items) == 0}
}

// apiKeyClusterChoices exposes exactly the cluster the key is bound to, so
// automation clients can bootstrap discovery without broader visibility.
func apiKeyClusterChoices(ctx context.Context, db *client.Client, principal *auth.APIKeyPrincipal) ([]clusterChoice, error) {
	if principal == nil || principal.ClusterID == "" {
		return []clusterChoice{}, nil
	}
	cluster, err := db.Cluster.FindUnique(ctx, query.Cluster.Id.Equals(principal.ClusterID))
	if err != nil {
		return nil, err
	}
	if cluster == nil {
		return []clusterChoice{}, nil
	}
	return []clusterChoice{{
		ID: cluster.Id, Name: cluster.Name, Role: "API_KEY", CreatedAt: cluster.CreatedAt,
	}}, nil
}

// @summary Create cluster
// @description Create a new cluster owned by the current user and select it in session. Not available to API key principals.
// @Tags clusters
func create(db *client.Client, sessions *auth.SessionStore) echo.HandlerFunc {
	return func(c *echo.Context) error {
		if auth.CurrentAPIKey(c) != nil {
			return echo.NewHTTPError(http.StatusForbidden, "api keys cannot create clusters")
		}
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
// @description Set the current session cluster context. Not available to API key principals; keys address clusters through the URL path.
// @Tags session
func selectCurrent(db *client.Client, sessions *auth.SessionStore) echo.HandlerFunc {
	return func(c *echo.Context) error {
		if auth.CurrentAPIKey(c) != nil {
			return echo.NewHTTPError(http.StatusForbidden, "api keys address clusters through the resource path")
		}
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
