package nodes

import (
	"net/http"
	"strings"

	"github.com/labstack/echo/v5"

	"goveto-edge/internal/audit"
	"goveto-edge/internal/buildinfo"
	"goveto-edge/internal/httpapi/types"
	nodedomain "goveto-edge/internal/node"
	"goveto-edge/internal/storage/gen/client"
	"goveto-edge/internal/storage/gen/model"
	"goveto-edge/internal/storage/gen/query"
)

// @summary Retry edge agent upgrade
// @description Queue an upgrade to the release version embedded in this control plane using the node's stored SSH credential.
// @Tags nodes
func retryAgentUpgrade(db *client.Client, queue *nodedomain.AgentUpgradeQueue) echo.HandlerFunc {
	return func(c *echo.Context) error {
		if queue == nil || !buildinfo.IsRelease() {
			return echo.NewHTTPError(http.StatusConflict, "agent upgrades are unavailable in a development build")
		}
		ctx := c.Request().Context()
		node, err := db.Node.FindUnique(ctx, query.Node.Id.Equals(c.Param("node_id")))
		if err != nil {
			return err
		}
		if err = validateAgentUpgradeRetryNode(node, c.Param("cluster_id"), buildinfo.Current()); err != nil {
			return err
		}
		result, err := queue.Enqueue(ctx, node.Id, buildinfo.Current(), "manual")
		if err != nil {
			return err
		}
		response := types.NewAgentUpgrade(result.Job)
		audit.SetChange(c, nil, response)
		return types.JSON(c, http.StatusAccepted, response)
	}
}

func validateAgentUpgradeRetryNode(node *model.Node, clusterID, targetVersion string) error {
	if node == nil || node.ClusterId != clusterID {
		return echo.NewHTTPError(http.StatusNotFound, "node not found")
	}
	if node.Status != model.NodeStatusONLINE {
		return echo.NewHTTPError(http.StatusConflict, "only online nodes can be upgraded")
	}
	if node.SshCredentialId == nil || node.SshHost == nil || node.SshPort == nil ||
		strings.TrimSpace(*node.SshCredentialId) == "" || strings.TrimSpace(*node.SshHost) == "" ||
		*node.SshPort < 1 || *node.SshPort > 65535 {
		return echo.NewHTTPError(http.StatusConflict, "node SSH upgrade configuration is missing or invalid")
	}
	current := ""
	if node.Version != nil {
		current = *node.Version
	}
	if !buildinfo.NeedsUpgrade(current, targetVersion) {
		return echo.NewHTTPError(http.StatusConflict, "the node already reports this version or a newer version")
	}
	return nil
}
