package nodes

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/labstack/echo/v5"

	"goveto-edge/internal/audit"
	"goveto-edge/internal/dnssync"
	"goveto-edge/internal/node"
	"goveto-edge/internal/storage/gen/client"
	"goveto-edge/internal/storage/gen/model"
	"goveto-edge/internal/storage/gen/query"
	staticassets "goveto-edge/static"
)

const agentServiceUnit = `[Unit]
Description=Goveto Edge Agent
After=network-online.target
[Service]
ExecStart=/usr/local/bin/goveto-edge-agent
Restart=always
RestartSec=3
[Install]
WantedBy=multi-user.target
`

type installationInfo struct {
	NodeID        string           `json:"node_id"`
	Status        model.NodeStatus `json:"status"`
	InstallError  *string          `json:"install_error,omitempty"`
	IdentityJSON  string           `json:"identity_json"`
	ServiceUnit   string           `json:"service_unit"`
	Architectures []string         `json:"architectures"`
}

func installationCredential(ctx *echo.Context, db *client.Client, cipher *node.CredentialCipher) (*model.Node, string, error) {
	item, err := nodeInCluster(ctx.Request().Context(), db, ctx.Param("cluster_id"), ctx.Param("node_id"))
	if err != nil {
		return nil, "", err
	}
	credential, err := db.NodeCredential.FindUnique(ctx.Request().Context(), query.NodeCredential.NodeId.Equals(item.Id))
	if err != nil {
		return nil, "", err
	}
	if credential == nil || credential.BootstrapIdentityEncrypted == nil {
		return nil, "", echo.NewHTTPError(http.StatusNotFound, "node bootstrap identity not found")
	}
	identityJSON, err := cipher.Decrypt(*credential.BootstrapIdentityEncrypted)
	if err != nil {
		return nil, "", err
	}
	return item, identityJSON, nil
}

func getInstallation(db *client.Client, cipher *node.CredentialCipher) echo.HandlerFunc {
	return func(c *echo.Context) error {
		item, identityJSON, err := installationCredential(c, db, cipher)
		if err != nil {
			return err
		}
		c.Response().Header().Set("Cache-Control", "no-store")
		var identityValue any
		_ = json.Unmarshal([]byte(identityJSON), &identityValue)
		identity, _ := json.MarshalIndent(identityValue, "", "  ")
		return c.JSON(http.StatusOK, map[string]any{"code": "ok", "data": installationInfo{
			NodeID: item.Id, Status: item.Status, InstallError: item.InstallError,
			IdentityJSON: string(identity), ServiceUnit: agentServiceUnit,
			Architectures: []string{"amd64", "arm64"},
		}})
	}
}

// @summary Initialize a manually installed node agent
// @description Verify the running agent, synchronize node configuration, and enter the normal health cycle.
// @Tags nodes
func initializeManualInstallation(
	db *client.Client,
	queue *node.InstallQueue,
	cipher *node.CredentialCipher,
	dnsService *dnssync.Service,
) echo.HandlerFunc {
	return func(c *echo.Context) error {
		ctx := c.Request().Context()
		item, err := nodeInCluster(ctx, db, c.Param("cluster_id"), c.Param("node_id"))
		if err != nil {
			return err
		}
		if item.Status == model.NodeStatusDISABLED {
			return echo.NewHTTPError(http.StatusConflict, "enable the node before initialization")
		}
		if item.Status == model.NodeStatusINSTALLING {
			return echo.NewHTTPError(http.StatusConflict, "automatic SSH installation is still in progress")
		}
		if err := queue.Delete(ctx, item.Id); err != nil {
			return err
		}

		deadline := time.NewTimer(15 * time.Second)
		defer deadline.Stop()
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		for item.Status != model.NodeStatusONLINE {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-deadline.C:
				message := "manual initialization failed: agent did not establish the mTLS management channel"
				if item.Status == model.NodeStatusPENDING || item.Status == model.NodeStatusINSTALL_FAILED {
					_, _ = db.Node.Update().Where(query.Node.Id.Equals(item.Id)).Set(
						query.Node.Status.Set(model.NodeStatusINSTALL_FAILED),
						query.Node.InstallError.Set(message),
						query.Node.HeartbeatAt.SetNull(),
					).DoMany(ctx)
				}
				return echo.NewHTTPError(http.StatusBadGateway, message)
			case <-ticker.C:
				item, err = nodeInCluster(ctx, db, c.Param("cluster_id"), c.Param("node_id"))
				if err != nil {
					return err
				}
			}
		}
		if err := enqueueDNSIfChanged(ctx, dnsService, item.ClusterId); err != nil {
			return err
		}
		response := map[string]any{
			"id":      item.Id,
			"status":  model.NodeStatusONLINE,
			"message": "node initialized through the mTLS management channel",
		}
		audit.SetChange(c, item, response)
		return c.JSON(http.StatusOK, map[string]any{"code": "ok", "data": response})
	}
}

func downloadAgentBinary(db *client.Client) echo.HandlerFunc {
	return func(c *echo.Context) error {
		if _, err := nodeInCluster(c.Request().Context(), db, c.Param("cluster_id"), c.Param("node_id")); err != nil {
			return err
		}
		arch := c.Param("arch")
		if arch != "amd64" && arch != "arm64" {
			return echo.NewHTTPError(http.StatusBadRequest, "architecture must be amd64 or arm64")
		}
		binary, err := staticassets.AgentBinary(arch)
		if err != nil {
			return err
		}
		c.Response().Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="goveto-edge-agent-linux-%s"`, arch))
		return c.Blob(http.StatusOK, "application/octet-stream", binary)
	}
}

func downloadIdentity(db *client.Client, cipher *node.CredentialCipher) echo.HandlerFunc {
	return func(c *echo.Context) error {
		_, identityJSON, err := installationCredential(c, db, cipher)
		if err != nil {
			return err
		}
		var identityValue any
		_ = json.Unmarshal([]byte(identityJSON), &identityValue)
		data, _ := json.MarshalIndent(identityValue, "", "  ")
		c.Response().Header().Set("Cache-Control", "no-store")
		c.Response().Header().Set("Content-Disposition", `attachment; filename="identity.json"`)
		return c.Blob(http.StatusOK, "application/json", data)
	}
}

func downloadServiceUnit(db *client.Client) echo.HandlerFunc {
	return func(c *echo.Context) error {
		if _, err := nodeInCluster(c.Request().Context(), db, c.Param("cluster_id"), c.Param("node_id")); err != nil {
			return err
		}
		c.Response().Header().Set("Content-Disposition", `attachment; filename="goveto-edge-agent.service"`)
		return c.String(http.StatusOK, agentServiceUnit)
	}
}
