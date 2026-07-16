package nodes

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/labstack/echo/v5"

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
	NodeID           string           `json:"node_id"`
	Status           model.NodeStatus `json:"status"`
	InstallError     *string          `json:"install_error,omitempty"`
	CommunicationKey string           `json:"communication_key"`
	IdentityJSON     string           `json:"identity_json"`
	ServiceUnit      string           `json:"service_unit"`
	Architectures    []string         `json:"architectures"`
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
	if credential == nil {
		return nil, "", echo.NewHTTPError(http.StatusNotFound, "node communication credential not found")
	}
	key, err := cipher.Decrypt(credential.CommunicationKeyEncrypted)
	if err != nil {
		return nil, "", err
	}
	return item, key, nil
}

func getInstallation(db *client.Client, cipher *node.CredentialCipher) echo.HandlerFunc {
	return func(c *echo.Context) error {
		item, key, err := installationCredential(c, db, cipher)
		if err != nil {
			return err
		}
		c.Response().Header().Set("Cache-Control", "no-store")
		identity, _ := json.MarshalIndent(map[string]string{"node_id": item.Id, "communication_key": key}, "", "  ")
		return c.JSON(http.StatusOK, map[string]any{"code": "ok", "data": installationInfo{
			NodeID: item.Id, Status: item.Status, InstallError: item.InstallError,
			CommunicationKey: key, IdentityJSON: string(identity), ServiceUnit: agentServiceUnit,
			Architectures: []string{"amd64", "arm64"},
		}})
	}
}

type installationStatusInput struct {
	Status model.NodeStatus `json:"status"`
}

func setInstallationStatus(db *client.Client, queue *node.InstallQueue, dnsService *dnssync.Service) echo.HandlerFunc {
	return func(c *echo.Context) error {
		ctx := c.Request().Context()
		item, err := nodeInCluster(ctx, db, c.Param("cluster_id"), c.Param("node_id"))
		if err != nil {
			return err
		}
		var input installationStatusInput
		if err := c.Bind(&input); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid request body")
		}
		switch input.Status {
		case model.NodeStatusPENDING, model.NodeStatusINSTALLING, model.NodeStatusOFFLINE, model.NodeStatusINSTALL_FAILED, model.NodeStatusDISABLED:
		default:
			return echo.NewHTTPError(http.StatusBadRequest, "unsupported manual installation status")
		}
		if input.Status != model.NodeStatusPENDING {
			if err := queue.Delete(ctx, item.Id); err != nil {
				return err
			}
		}
		sets := []query.NodeSetClause{query.Node.Status.Set(input.Status)}
		if input.Status != model.NodeStatusINSTALL_FAILED {
			sets = append(sets, query.Node.InstallError.SetNull())
		}
		updated, err := db.Node.Update().Where(query.Node.Id.Equals(item.Id)).Set(sets...).Do(ctx)
		if err != nil {
			return err
		}
		if err := enqueueDNSIfChanged(ctx, dnsService, item.ClusterId); err != nil {
			return err
		}
		return c.JSON(http.StatusOK, map[string]any{"code": "ok", "data": map[string]any{"id": updated.Id, "status": updated.Status}})
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
		item, key, err := installationCredential(c, db, cipher)
		if err != nil {
			return err
		}
		data, _ := json.MarshalIndent(map[string]string{"node_id": item.Id, "communication_key": key}, "", "  ")
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
