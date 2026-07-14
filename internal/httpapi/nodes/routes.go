// Package nodes registers cluster node management endpoints.
package nodes

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"net/http"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"

	authn "goveto-edge/internal/auth"
	"goveto-edge/internal/clusteraccess"
	"goveto-edge/internal/dnssync"
	"goveto-edge/internal/httpapi/types"
	nodedomain "goveto-edge/internal/node"
	"goveto-edge/internal/storage/gen/client"
	"goveto-edge/internal/storage/gen/model"
	"goveto-edge/internal/storage/gen/query"
)

func Register(e *echo.Echo, db *client.Client, queue *nodedomain.InstallQueue, cipher *nodedomain.CredentialCipher, dnsService *dnssync.Service) {
	e.POST("/api/v1/clusters/:cluster_id/nodes", create(db, queue, cipher), authn.RequireAuth, clusteraccess.Require(db))
	e.POST("/api/v1/clusters/:cluster_id/nodes/test-connection", testConnection(), authn.RequireAuth, clusteraccess.Require(db))
	e.GET("/api/v1/clusters/:cluster_id/nodes", list(db), authn.RequireAuth, clusteraccess.Require(db))
	e.GET("/api/v1/clusters/:cluster_id/nodes/:node_id", get(db), authn.RequireAuth, clusteraccess.Require(db))
	e.GET("/api/v1/clusters/:cluster_id/nodes/:node_id/cache-config", getCacheConfig(db), authn.RequireAuth, clusteraccess.Require(db))
	e.PUT("/api/v1/clusters/:cluster_id/nodes/:node_id/cache-config", updateCacheConfig(db, cipher), authn.RequireAuth, clusteraccess.Require(db))
	e.POST("/api/v1/clusters/:cluster_id/nodes/:node_id/addresses", addAddress(db, dnsService), authn.RequireAuth, clusteraccess.Require(db))
	e.PUT("/api/v1/clusters/:cluster_id/nodes/:node_id/dns-lines", updateDNSLines(db, dnsService), authn.RequireAuth, clusteraccess.Require(db))
	e.POST("/api/v1/clusters/:cluster_id/nodes/:node_id/enable", enableNode(db, dnsService), authn.RequireAuth, clusteraccess.Require(db))
	e.POST("/api/v1/clusters/:cluster_id/nodes/:node_id/disable", disableNode(db, dnsService), authn.RequireAuth, clusteraccess.Require(db))
	e.DELETE("/api/v1/clusters/:cluster_id/nodes/:node_id", deleteNode(db, queue, dnsService), authn.RequireAuth, clusteraccess.Require(db))
}

type testConnectionRequest struct {
	SSH nodedomain.SSHInstallInput `json:"ssh"`
}

type testConnectionResponse struct {
	OK           bool   `json:"ok"`
	Architecture string `json:"architecture"`
}

// @summary Test node SSH connection
// @description Validate SSH credentials and detect the remote architecture without creating a node.
// @Tags nodes
func testConnection() echo.HandlerFunc {
	return func(c *echo.Context) error {
		var input testConnectionRequest
		if err := c.Bind(&input); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid request body")
		}
		architecture, err := nodedomain.TestSSHConnection(c.Request().Context(), input.SSH)
		if err != nil {
			return echo.NewHTTPError(http.StatusBadGateway, "SSH connection test failed: "+err.Error())
		}
		return types.JSON(c, http.StatusOK, testConnectionResponse{
			OK:           true,
			Architecture: architecture,
		})
	}
}

// @summary Create node
// @description Create a node and enqueue remote installation with SSH credentials.
// @Tags nodes
func create(db *client.Client, queue *nodedomain.InstallQueue, cipher *nodedomain.CredentialCipher) echo.HandlerFunc {
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

		nodeID := uuid.NewString()
		communicationKey, err := newCommunicationKey()
		if err != nil {
			return err
		}

		encryptedCommunicationKey, err := cipher.Encrypt(communicationKey)
		if err != nil {
			return err
		}

		if err := db.Tx(ctx, func(tx *client.Client) error {
			sets := []query.NodeSetClause{
				query.Node.Id.Set(nodeID),
				query.Node.ClusterId.Set(input.ClusterID),
				query.Node.Name.Set(input.Name),
				query.Node.Status.Set(model.NodeStatusPENDING),
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
					query.NodeCredential.CommunicationKeyEncrypted.Set(encryptedCommunicationKey),
				).
				Do(ctx); err != nil {
				return err
			}

			for index, address := range input.Addresses {
				if _, err := tx.NodeAddress.Create().
					Set(
						query.NodeAddress.NodeId.Set(nodeID),
						query.NodeAddress.Address.Set(address),
						query.NodeAddress.Primary.Set(index == 0),
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

		if err := queue.Enqueue(ctx, nodeID, nodedomain.InstallPayload{
			NodeID:           nodeID,
			CommunicationKey: communicationKey,
			SSH:              input.SSH,
		}); err != nil {
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

		created, err := db.Node.Query().
			Where(query.Node.Id.Equals(nodeID)).
			Include(
				query.Node.Addresses.Fetch(),
				query.Node.DnsLines.Fetch(),
				query.Node.GroupMemberships.Fetch(),
				query.Node.RegionMemberships.Fetch(),
			).
			First(ctx)
		if err != nil {
			return err
		}
		if created == nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "created node was not found")
		}
		return types.JSON(c, http.StatusAccepted, types.NewNode(created))
	}
}

func newCommunicationKey() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
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
