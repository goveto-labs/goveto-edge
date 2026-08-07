package nodes

import (
	"net/http"

	"github.com/labstack/echo/v5"
	"golang.org/x/crypto/ssh"

	"goveto-edge/internal/audit"
	"goveto-edge/internal/httpapi/types"
	nodedomain "goveto-edge/internal/node"
	"goveto-edge/internal/storage/gen/client"
	"goveto-edge/internal/storage/gen/model"
	"goveto-edge/internal/storage/gen/query"
)

func mapHostKeyAPIError(err error) error {
	switch {
	case nodedomain.IsHostKeyMismatch(err):
		return types.Error(http.StatusConflict, types.CodeSSHHostKeyChanged, err.Error())
	case nodedomain.IsHostKeyNotPinned(err):
		return types.Error(http.StatusConflict, types.CodeSSHHostKeyRequired, err.Error())
	default:
		return nil
	}
}

func loadNodeSSHInstallInput(
	c *echo.Context,
	db *client.Client,
	cipher *nodedomain.CredentialCipher,
) (*model.Node, nodedomain.SSHInstallInput, error) {
	ctx := c.Request().Context()
	node, err := db.Node.FindUnique(ctx, query.Node.Id.Equals(c.Param("node_id")))
	if err != nil {
		return nil, nodedomain.SSHInstallInput{}, err
	}
	if node == nil || node.ClusterId != c.Param("cluster_id") {
		return nil, nodedomain.SSHInstallInput{}, echo.NewHTTPError(http.StatusNotFound, "node not found")
	}
	if node.SshCredentialId == nil || node.SshHost == nil || node.SshPort == nil {
		return nil, nodedomain.SSHInstallInput{}, echo.NewHTTPError(http.StatusConflict, "node SSH installation configuration is missing")
	}
	_, sshInput, err := nodedomain.ResolveSSHInstallInput(ctx, db, cipher, node.ClusterId, nodedomain.SSHInstallReference{
		EntryIP:      *node.SshHost,
		Port:         uint16(*node.SshPort),
		CredentialID: *node.SshCredentialId,
	})
	if err != nil {
		return nil, nodedomain.SSHInstallInput{}, echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	return node, sshInput, nil
}

func capturePresentedHostKey(c *echo.Context, sshInput nodedomain.SSHInstallInput) (ssh.PublicKey, error) {
	capture, captured := nodedomain.CaptureHostKey()
	if _, err := nodedomain.TestSSHConnection(c.Request().Context(), sshInput, capture); err != nil {
		return nil, echo.NewHTTPError(http.StatusBadGateway, "SSH connection failed: "+err.Error())
	}
	hostKey := captured()
	if hostKey == nil {
		return nil, echo.NewHTTPError(http.StatusBadGateway, "SSH connection failed: server presented no host key")
	}
	return hostKey, nil
}

// @summary Preview node SSH host key
// @description Connect to the node and return the currently presented SSH host key without pinning it.
// @Tags nodes
func previewSSHHostKey(db *client.Client, cipher *nodedomain.CredentialCipher) echo.HandlerFunc {
	return func(c *echo.Context) error {
		node, sshInput, err := loadNodeSSHInstallInput(c, db, cipher)
		if err != nil {
			return err
		}
		hostKey, err := capturePresentedHostKey(c, sshInput)
		if err != nil {
			return err
		}
		before, err := nodedomain.LoadPinnedHostKey(c.Request().Context(), db, node.Id)
		if err != nil {
			return err
		}
		keyType, publicKey, fingerprint := nodedomain.DescribeHostKey(hostKey)
		preview := types.NodeSSHHostKeyPreview{
			KeyType:           keyType,
			PublicKey:         publicKey,
			FingerprintSHA256: fingerprint,
		}
		if before != nil {
			preview.PinnedFingerprintSHA256 = before.FingerprintSha256
			preview.MatchesPin = before.PublicKey == publicKey
		}
		return types.JSON(c, http.StatusOK, preview)
	}
}

// @summary Re-trust node SSH host key
// @description Connect to the node, pin the currently presented SSH host key and return it for verification.
// @Tags nodes
func trustSSHHostKey(db *client.Client, cipher *nodedomain.CredentialCipher) echo.HandlerFunc {
	return func(c *echo.Context) error {
		ctx := c.Request().Context()
		node, sshInput, err := loadNodeSSHInstallInput(c, db, cipher)
		if err != nil {
			return err
		}
		before, err := nodedomain.LoadPinnedHostKey(ctx, db, node.Id)
		if err != nil {
			return err
		}
		hostKey, err := capturePresentedHostKey(c, sshInput)
		if err != nil {
			return err
		}
		pinned, err := nodedomain.PinHostKey(ctx, db, node.Id, hostKey)
		if err != nil {
			return err
		}
		after := types.NewNodeSSHHostKey(pinned)
		audit.SetResourceID(c, node.Id)
		if before != nil {
			beforeView := types.NewNodeSSHHostKey(before)
			audit.SetChange(c, beforeView, after)
		} else {
			audit.SetChange(c, nil, after)
		}
		return types.JSON(c, http.StatusOK, after)
	}
}
