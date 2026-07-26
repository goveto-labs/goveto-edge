// Package nodes registers cluster node management endpoints.
package nodes

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"

	"goveto-edge/internal/audit"
	authn "goveto-edge/internal/auth"
	"goveto-edge/internal/clusteraccess"
	"goveto-edge/internal/dnssync"
	"goveto-edge/internal/edgecontrol"
	"goveto-edge/internal/httpapi/types"
	nodedomain "goveto-edge/internal/node"
	"goveto-edge/internal/rbac"
	"goveto-edge/internal/storage/gen/client"
	"goveto-edge/internal/storage/gen/model"
	"goveto-edge/internal/storage/gen/query"
)

func Register(e *echo.Echo, db *client.Client, queue *nodedomain.InstallQueue, cipher *nodedomain.CredentialCipher, authority *edgecontrol.Authority, gateway *edgecontrol.Gateway, dnsService *dnssync.Service) {
	read := clusteraccess.RequirePermission(db, rbac.PermissionClusterRead)
	nodeManage := clusteraccess.RequirePermission(db, rbac.PermissionNodeManage)
	credentialManage := clusteraccess.RequirePermission(db, rbac.PermissionCredentialManage)
	e.POST("/api/v1/clusters/:cluster_id/nodes", create(db, queue, cipher, authority), authn.RequireAuth, nodeManage)
	e.POST("/api/v1/clusters/:cluster_id/nodes/test-connection", testConnection(db, cipher), authn.RequireAuth, credentialManage)
	e.GET("/api/v1/clusters/:cluster_id/ssh-credentials", listSSHCredentials(db), authn.RequireAuth, credentialManage)
	e.POST("/api/v1/clusters/:cluster_id/ssh-credentials", createSSHCredential(db, cipher), authn.RequireAuth, credentialManage)
	e.PUT("/api/v1/clusters/:cluster_id/ssh-credentials/:credential_id", updateSSHCredential(db, cipher), authn.RequireAuth, credentialManage)
	e.DELETE("/api/v1/clusters/:cluster_id/ssh-credentials/:credential_id", deleteSSHCredential(db), authn.RequireAuth, credentialManage)
	e.GET("/api/v1/clusters/:cluster_id/ssh-credentials/:credential_id/nodes", listSSHCredentialNodes(db), authn.RequireAuth, credentialManage)
	e.GET("/api/v1/clusters/:cluster_id/nodes", list(db), authn.RequireAuth, read)
	e.GET("/api/v1/clusters/:cluster_id/nodes/:node_id", get(db), authn.RequireAuth, read)
	e.GET("/api/v1/clusters/:cluster_id/nodes/:node_id/cache-config", getCacheConfig(db), authn.RequireAuth, read)
	e.PUT("/api/v1/clusters/:cluster_id/nodes/:node_id/cache-config", updateCacheConfig(db, gateway), authn.RequireAuth, nodeManage)
	e.POST("/api/v1/clusters/:cluster_id/nodes/:node_id/addresses", addAddress(db, dnsService), authn.RequireAuth, nodeManage)
	e.PUT("/api/v1/clusters/:cluster_id/nodes/:node_id/addresses/:address_id", updateAddress(db, dnsService), authn.RequireAuth, nodeManage)
	e.DELETE("/api/v1/clusters/:cluster_id/nodes/:node_id/addresses/:address_id", deleteAddress(db, dnsService), authn.RequireAuth, nodeManage)
	e.PUT("/api/v1/clusters/:cluster_id/nodes/:node_id/dns-lines", updateDNSLines(db, dnsService), authn.RequireAuth, nodeManage)
	e.POST("/api/v1/clusters/:cluster_id/nodes/:node_id/enable", enableNode(db, dnsService), authn.RequireAuth, nodeManage)
	e.POST("/api/v1/clusters/:cluster_id/nodes/:node_id/disable", disableNode(db, gateway, dnsService), authn.RequireAuth, nodeManage)
	e.POST("/api/v1/clusters/:cluster_id/nodes/:node_id/credentials/revoke", revokeNodeCredential(db, gateway, dnsService), authn.RequireAuth, credentialManage)
	e.POST("/api/v1/clusters/:cluster_id/nodes/:node_id/reinstall", reinstall(db, queue, cipher, authority, gateway), authn.RequireAuth, credentialManage)
	e.GET("/api/v1/clusters/:cluster_id/nodes/:node_id/installation", getInstallation(db, cipher), authn.RequireAuth, credentialManage)
	e.POST("/api/v1/clusters/:cluster_id/nodes/:node_id/installation/initialize", initializeManualInstallation(db, queue, cipher, dnsService), authn.RequireAuth, nodeManage)
	e.GET("/api/v1/clusters/:cluster_id/nodes/:node_id/installation/binary/:arch", downloadAgentBinary(db), authn.RequireAuth, nodeManage)
	e.GET("/api/v1/clusters/:cluster_id/nodes/:node_id/installation/identity", downloadIdentity(db, cipher), authn.RequireAuth, credentialManage)
	e.GET("/api/v1/clusters/:cluster_id/nodes/:node_id/installation/service", downloadServiceUnit(db), authn.RequireAuth, nodeManage)
	e.DELETE("/api/v1/clusters/:cluster_id/nodes/:node_id", deleteNode(db, queue, gateway, dnsService), authn.RequireAuth, clusteraccess.RequirePermission(db, rbac.PermissionNodeDelete))
}

type testConnectionRequest struct {
	SSH nodedomain.SSHInstallReference `json:"ssh"`
}

type testConnectionResponse struct {
	OK           bool   `json:"ok"`
	Architecture string `json:"architecture"`
}

type reinstallRequest struct {
	SSH   nodedomain.SSHInstallReference `json:"ssh"`
	Force bool                           `json:"force"`
}

// @summary Test node SSH connection
// @description Resolve a stored SSH credential and detect the remote architecture without creating a node.
// @Tags nodes
func testConnection(db *client.Client, cipher *nodedomain.CredentialCipher) echo.HandlerFunc {
	return func(c *echo.Context) error {
		var input testConnectionRequest
		if err := c.Bind(&input); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid request body")
		}
		_, sshInput, err := nodedomain.ResolveSSHInstallInput(
			c.Request().Context(), db, cipher, c.Param("cluster_id"), input.SSH,
		)
		if err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, err.Error())
		}
		architecture, err := nodedomain.TestSSHConnection(c.Request().Context(), sshInput)
		if err != nil {
			return echo.NewHTTPError(http.StatusBadGateway, "SSH connection test failed: "+err.Error())
		}
		return types.JSON(c, http.StatusOK, testConnectionResponse{
			OK:           true,
			Architecture: architecture,
		})
	}
}

// @summary Reinstall node agent
// @description Test a stored SSH credential and enqueue agent reinstallation for an existing node.
// @Tags nodes
func reinstall(
	db *client.Client,
	queue *nodedomain.InstallQueue,
	cipher *nodedomain.CredentialCipher,
	authority *edgecontrol.Authority,
	gateway *edgecontrol.Gateway,
) echo.HandlerFunc {
	return func(c *echo.Context) error {
		var input reinstallRequest
		if err := c.Bind(&input); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid request body")
		}
		ctx := c.Request().Context()
		node, err := db.Node.FindUnique(ctx, query.Node.Id.Equals(c.Param("node_id")))
		if err != nil {
			return err
		}
		if node == nil || node.ClusterId != c.Param("cluster_id") {
			return echo.NewHTTPError(http.StatusNotFound, "node not found")
		}
		before := types.NewNode(node)
		if (node.Status == model.NodeStatusPENDING || node.Status == model.NodeStatusINSTALLING) && !input.Force {
			return echo.NewHTTPError(http.StatusConflict, "node installation is already in progress")
		}
		sshCredential, sshInput, err := nodedomain.ResolveSSHInstallInput(
			ctx, db, cipher, node.ClusterId, input.SSH,
		)
		if err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, err.Error())
		}
		if _, err := nodedomain.TestSSHConnection(ctx, sshInput); err != nil {
			return echo.NewHTTPError(http.StatusBadGateway, "SSH connection test failed: "+err.Error())
		}
		credential, err := db.NodeCredential.FindUnique(
			ctx,
			query.NodeCredential.NodeId.Equals(node.Id),
		)
		if err != nil {
			return err
		}
		if credential == nil {
			return echo.NewHTTPError(http.StatusConflict, "node credential is missing")
		}
		bundle, err := authority.IssueNode(node.Id)
		if err != nil {
			return err
		}
		identityJSON, err := json.Marshal(bundle)
		if err != nil {
			return err
		}
		encryptedIdentity, err := cipher.Encrypt(string(identityJSON))
		if err != nil {
			return err
		}
		if err := db.Tx(ctx, func(tx *client.Client) error {
			if _, err := tx.NodeCredential.Update().Where(query.NodeCredential.NodeId.Equals(node.Id)).Set(
				query.NodeCredential.CertificateSerial.Set(bundle.Serial),
				query.NodeCredential.CertificateNotAfter.Set(bundle.NotAfter),
				query.NodeCredential.BootstrapIdentityEncrypted.Set(encryptedIdentity),
				query.NodeCredential.PreviousCertificateSerial.SetNull(),
				query.NodeCredential.PreviousCertificateValidUntil.SetNull(),
				query.NodeCredential.RotationCsrSha256.SetNull(),
				query.NodeCredential.RotationCertificatePem.SetNull(),
				query.NodeCredential.RevokedAt.SetNull(),
			).Do(ctx); err != nil {
				return err
			}
			_, err := tx.Node.Update().Where(query.Node.Id.Equals(node.Id)).Set(
				query.Node.Status.Set(model.NodeStatusPENDING),
				query.Node.InstallError.SetNull(),
				query.Node.HeartbeatAt.SetNull(),
				query.Node.SshCredentialId.Set(sshCredential.Id),
				query.Node.SshHost.Set(input.SSH.EntryIP),
				query.Node.SshPort.Set(int(input.SSH.Port)),
			).Do(ctx)
			if err != nil {
				return err
			}
			_, err = tx.RawExec(ctx, `UPDATE agent_tasks SET status = 'CANCELLED',
				error = 'node reinstallation replaced the active credential',
				lease_owner = NULL, lease_until = NULL, updated_at = NOW()
				WHERE node_id = $1 AND status IN ('PENDING', 'RUNNING')`, node.Id)
			return err
		}); err != nil {
			return err
		}
		if gateway != nil {
			gateway.Disconnect(ctx, node.Id)
		}
		if input.Force {
			_ = queue.Delete(ctx, node.Id)
		}
		if err := queue.Enqueue(ctx, node.Id, nodedomain.InstallPayload{NodeID: node.Id}); err != nil {
			message := "unable to queue node reinstallation: " + err.Error()
			_, _ = db.Node.Update().
				Where(query.Node.Id.Equals(node.Id)).
				Set(
					query.Node.Status.Set(model.NodeStatusINSTALL_FAILED),
					query.Node.InstallError.Set(message),
				).
				DoMany(ctx)
			return echo.NewHTTPError(http.StatusServiceUnavailable, message)
		}
		node.Status = model.NodeStatusPENDING
		node.InstallError = nil
		node.HeartbeatAt = nil
		node.SshCredentialId = &sshCredential.Id
		node.SshHost = &input.SSH.EntryIP
		sshPort := int(input.SSH.Port)
		node.SshPort = &sshPort
		if err := loadNodeRelations(ctx, db, node, true); err != nil {
			return err
		}
		audit.SetChange(c, before, types.NewNode(node))
		return types.JSON(c, http.StatusAccepted, types.NewNode(node))
	}
}

// @summary Create node
// @description Create a node and enqueue remote installation with SSH credentials.
// @Tags nodes
func create(db *client.Client, queue *nodedomain.InstallQueue, cipher *nodedomain.CredentialCipher, authority *edgecontrol.Authority) echo.HandlerFunc {
	return func(c *echo.Context) error {
		var input nodedomain.CreateInput
		if err := c.Bind(&input); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid request body")
		}

		input.ClusterID = c.Param("cluster_id")
		if err := input.Validate(); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, err.Error())
		}

		ctx := c.Request().Context()
		if err := validateReferences(ctx, db, input); err != nil {
			return err
		}
		sshCredential, sshInput, err := nodedomain.ResolveSSHInstallInput(
			ctx, db, cipher, input.ClusterID, input.SSH,
		)
		if err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, err.Error())
		}
		if _, err := nodedomain.TestSSHConnection(ctx, sshInput); err != nil {
			return echo.NewHTTPError(http.StatusBadGateway, "SSH connection test failed: "+err.Error())
		}

		nodeID := uuid.NewString()
		bundle, err := authority.IssueNode(nodeID)
		if err != nil {
			return err
		}
		identityJSON, err := json.Marshal(bundle)
		if err != nil {
			return err
		}
		encryptedIdentity, err := cipher.Encrypt(string(identityJSON))
		if err != nil {
			return err
		}

		if err := db.Tx(ctx, func(tx *client.Client) error {
			sets := []query.NodeSetClause{
				query.Node.Id.Set(nodeID),
				query.Node.ClusterId.Set(input.ClusterID),
				query.Node.Name.Set(input.Name),
				query.Node.Status.Set(model.NodeStatusPENDING),
				query.Node.SshCredentialId.Set(sshCredential.Id),
				query.Node.SshHost.Set(input.SSH.EntryIP),
				query.Node.SshPort.Set(int(input.SSH.Port)),
			}

			if _, err := tx.Node.Create().Set(sets...).Do(ctx); err != nil {
				return err
			}
			if _, err := tx.NodeCacheConfig.Create().
				Set(query.NodeCacheConfig.NodeId.Set(nodeID)).
				Do(ctx); err != nil {
				return err
			}
			if _, err := tx.NodeCredential.Create().
				Set(
					query.NodeCredential.NodeId.Set(nodeID),
					query.NodeCredential.CertificateSerial.Set(bundle.Serial),
					query.NodeCredential.CertificateNotAfter.Set(bundle.NotAfter),
					query.NodeCredential.BootstrapIdentityEncrypted.Set(encryptedIdentity),
				).
				Do(ctx); err != nil {
				return err
			}

			for _, address := range input.Addresses {
				if _, err := tx.NodeAddress.Create().
					Set(
						query.NodeAddress.NodeId.Set(nodeID),
						query.NodeAddress.Address.Set(address),
					).
					Do(ctx); err != nil {
					return err
				}
			}
			for _, lineID := range input.DNSLineIDs {
				if _, err := tx.NodeDNSLine.Create().
					Set(
						query.NodeDNSLine.NodeId.Set(nodeID),
						query.NodeDNSLine.DnsLineId.Set(lineID),
					).
					Do(ctx); err != nil {
					return err
				}
			}
			for _, groupID := range input.GroupIDs {
				if _, err := tx.NodeGroupMembership.Create().
					Set(
						query.NodeGroupMembership.NodeId.Set(nodeID),
						query.NodeGroupMembership.GroupId.Set(groupID),
					).
					Do(ctx); err != nil {
					return err
				}
			}
			for _, regionID := range input.RegionIDs {
				if _, err := tx.NodeRegionMembership.Create().
					Set(
						query.NodeRegionMembership.NodeId.Set(nodeID),
						query.NodeRegionMembership.RegionId.Set(regionID),
					).
					Do(ctx); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return err
		}

		if err := queue.Enqueue(ctx, nodeID, nodedomain.InstallPayload{NodeID: nodeID}); err != nil {
			message := "unable to queue node installation: " + err.Error()
			_, _ = db.Node.Update().
				Where(query.Node.Id.Equals(nodeID)).
				Set(
					query.Node.Status.Set(model.NodeStatusINSTALL_FAILED),
					query.Node.InstallError.Set(message),
				).
				DoMany(ctx)
			return echo.NewHTTPError(http.StatusServiceUnavailable, "unable to queue node installation")
		}

		created, err := db.Node.FindUnique(ctx, query.Node.Id.Equals(nodeID))
		if err != nil {
			return err
		}
		if created == nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "created node was not found")
		}
		if err := loadNodeRelations(ctx, db, created, true); err != nil {
			return err
		}
		slog.Info(
			"node created with relations",
			"node_id", created.Id,
			"address_count", len(created.Addresses),
			"dns_line_count", len(created.DnsLines),
			"group_count", len(created.GroupMemberships),
			"region_count", len(created.RegionMemberships),
		)
		audit.SetResourceID(c, created.Id)
		audit.SetChange(c, nil, types.NewNode(created))
		return types.JSON(c, http.StatusAccepted, types.NewNode(created))
	}
}

func validateReferences(ctx context.Context, db *client.Client, input nodedomain.CreateInput) error {
	cluster, err := db.Cluster.FindUnique(ctx, query.Cluster.Id.Equals(input.ClusterID))
	if err != nil {
		return err
	}
	if cluster == nil {
		return echo.NewHTTPError(http.StatusNotFound, "cluster not found")
	}

	seenGroups := make(map[string]struct{}, len(input.GroupIDs))
	for _, groupID := range input.GroupIDs {
		if _, exists := seenGroups[groupID]; exists {
			return echo.NewHTTPError(http.StatusBadRequest, "duplicate group")
		}
		seenGroups[groupID] = struct{}{}
		group, err := db.ClusterGroup.FindUnique(ctx, query.ClusterGroup.Id.Equals(groupID))
		if err != nil {
			return err
		}
		if group == nil || group.ClusterId != input.ClusterID {
			return echo.NewHTTPError(http.StatusBadRequest, "group does not belong to cluster")
		}
	}

	seenRegions := make(map[string]struct{}, len(input.RegionIDs))
	for _, regionID := range input.RegionIDs {
		if _, exists := seenRegions[regionID]; exists {
			return echo.NewHTTPError(http.StatusBadRequest, "duplicate region")
		}
		seenRegions[regionID] = struct{}{}
		region, err := db.ClusterRegion.FindUnique(ctx, query.ClusterRegion.Id.Equals(regionID))
		if err != nil {
			return err
		}
		if region == nil || region.ClusterId != input.ClusterID {
			return echo.NewHTTPError(http.StatusBadRequest, "region does not belong to cluster")
		}
	}

	seen := make(map[string]struct{}, len(input.DNSLineIDs))
	for _, lineID := range input.DNSLineIDs {
		if _, exists := seen[lineID]; exists {
			return echo.NewHTTPError(http.StatusBadRequest, "duplicate DNS line")
		}
		seen[lineID] = struct{}{}
		line, err := db.DNSLine.FindUnique(ctx, query.DNSLine.Id.Equals(lineID))
		if err != nil {
			return err
		}
		if line == nil || line.ClusterId != input.ClusterID {
			return echo.NewHTTPError(http.StatusBadRequest, "DNS line does not belong to cluster")
		}
	}
	return nil
}
