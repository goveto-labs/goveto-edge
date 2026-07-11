package nodes

import (
	"net"
	"net/http"
	"strings"

	"github.com/labstack/echo/v5"
	"goveto-edge/internal/edgecontrol"
	"goveto-edge/internal/edgeprotocol"
	nodedomain "goveto-edge/internal/node"
	"goveto-edge/internal/storage/gen/client"
	"goveto-edge/internal/storage/gen/query"
)

func getCacheConfig(db *client.Client) echo.HandlerFunc {
	return func(c *echo.Context) error {
		if err := ensureNodeInCluster(c, db); err != nil {
			return err
		}

		config, err := db.NodeCacheConfig.FindUnique(
			c.Request().Context(),
			query.NodeCacheConfig.NodeId.Equals(c.Param("node_id")),
		)
		if err != nil {
			return err
		}
		return c.JSON(http.StatusOK, config)
	}
}

func updateCacheConfig(db *client.Client, cipher *nodedomain.CredentialCipher) echo.HandlerFunc {
	return func(c *echo.Context) error {
		if err := ensureNodeInCluster(c, db); err != nil {
			return err
		}

		var input edgeprotocol.NodeCacheConfig
		if err := c.Bind(&input); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid request body")
		}

		input.CacheDirectory = strings.TrimSpace(input.CacheDirectory)
		if input.CacheDirectory == "" || !strings.HasPrefix(input.CacheDirectory, "/") {
			return echo.NewHTTPError(http.StatusBadRequest, "cache_directory must be an absolute path")
		}
		if input.MaxDiskUsagePercent < 1 || input.MaxDiskUsagePercent > 95 {
			return echo.NewHTTPError(http.StatusBadRequest, "max_disk_usage_percent must be between 1 and 95")
		}
		if !input.AutoMaxSize && input.MaxSizeBytes == 0 {
			return echo.NewHTTPError(http.StatusBadRequest, "max_size_bytes is required when auto_max_size is false")
		}

		ctx, nodeID := c.Request().Context(), c.Param("node_id")
		sets := []query.NodeCacheConfigSetClause{
			query.NodeCacheConfig.CacheDir.Set(input.CacheDirectory),
			query.NodeCacheConfig.AutoMaxSize.Set(input.AutoMaxSize),
			query.NodeCacheConfig.MaxDiskUsagePercent.Set(input.MaxDiskUsagePercent),
		}
		if input.AutoMaxSize {
			sets = append(sets, query.NodeCacheConfig.MaxSizeBytes.SetNull())
		} else {
			sets = append(sets, query.NodeCacheConfig.MaxSizeBytes.Set(int64(input.MaxSizeBytes)))
		}

		updated, err := db.NodeCacheConfig.Update().
			Where(query.NodeCacheConfig.NodeId.Equals(nodeID)).
			Set(sets...).
			Do(ctx)
		if err != nil {
			return err
		}

		response := map[string]any{"cache_config": updated, "synced": false}
		address, addressErr := db.NodeAddress.Query().
			Where(
				query.NodeAddress.NodeId.Equals(nodeID),
				query.NodeAddress.Primary.Equals(true),
			).
			First(ctx)
		credential, credentialErr := db.NodeCredential.FindUnique(
			ctx, query.NodeCredential.NodeId.Equals(nodeID),
		)
		if addressErr == nil && credentialErr == nil {
			key, decryptErr := cipher.Decrypt(credential.CommunicationKeyEncrypted)
			if decryptErr == nil {
				syncErr := edgecontrol.New(
					"http://"+net.JoinHostPort(address.Address, "80"),
					nodeID,
					key,
				).PushNodeCacheConfig(ctx, input)
				if syncErr == nil {
					response["synced"] = true
				} else {
					response["sync_error"] = syncErr.Error()
				}
			}
		}
		return c.JSON(http.StatusOK, response)
	}
}

func ensureNodeInCluster(c *echo.Context, db *client.Client) error {
	node, err := db.Node.FindUnique(c.Request().Context(), query.Node.Id.Equals(c.Param("node_id")))
	if err != nil || node.ClusterId != c.Param("cluster_id") {
		return echo.NewHTTPError(http.StatusNotFound, "node not found")
	}
	return nil
}
