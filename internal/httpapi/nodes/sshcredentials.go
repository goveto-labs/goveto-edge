package nodes

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"

	"goveto-edge/internal/auth"
	"goveto-edge/internal/clusteraccess"
	"goveto-edge/internal/httpapi/types"
	nodedomain "goveto-edge/internal/node"
	"goveto-edge/internal/storage/gen/client"
	"goveto-edge/internal/storage/gen/model"
	"goveto-edge/internal/storage/gen/query"
)

type sshCredentialResponse struct {
	ID        string            `db:"id" json:"id"`
	Name      string            `db:"name" json:"name"`
	Username  string            `db:"username" json:"username"`
	AuthType  model.SSHAuthType `db:"auth_type" json:"auth_type"`
	NodeCount int64             `db:"node_count" json:"node_count"`
	CreatedAt time.Time         `db:"created_at" json:"created_at"`
	UpdatedAt time.Time         `db:"updated_at" json:"updated_at"`
}

type sshCredentialNodeResponse struct {
	ID     string           `db:"id" json:"id"`
	Name   string           `db:"name" json:"name"`
	Status model.NodeStatus `db:"status" json:"status"`
}

type sshCredentialWriteRequest struct {
	Name       string            `json:"name"`
	Username   string            `json:"username"`
	AuthType   model.SSHAuthType `json:"auth_type"`
	Password   string            `json:"password,omitempty"`
	PrivateKey string            `json:"private_key,omitempty"`
	Passphrase string            `json:"passphrase,omitempty"`
}

func listSSHCredentials(db *client.Client) echo.HandlerFunc {
	return func(c *echo.Context) error {
		items, err := client.Raw[sshCredentialResponse](c.Request().Context(), db, `
			SELECT c.id, c.name, c.username, c.auth_type,
				COUNT(n.id) AS node_count, c.created_at, c.updated_at
			FROM ssh_credentials c
			LEFT JOIN nodes n ON n.ssh_credential_id = c.id
			WHERE c.cluster_id = $1
			GROUP BY c.id, c.name, c.username, c.auth_type, c.created_at, c.updated_at
			ORDER BY c.updated_at DESC, c.name`, c.Param("cluster_id"))
		if err != nil {
			return err
		}
		if items == nil {
			items = make([]sshCredentialResponse, 0)
		}
		return types.JSON(c, http.StatusOK, items)
	}
}

func createSSHCredential(db *client.Client, cipher *nodedomain.CredentialCipher) echo.HandlerFunc {
	return func(c *echo.Context) error {
		if err := requireClusterOwner(c, db); err != nil {
			return err
		}
		var input sshCredentialWriteRequest
		if err := c.Bind(&input); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid request body")
		}
		if err := validateSSHCredentialWrite(&input); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, err.Error())
		}
		ctx := c.Request().Context()
		clusterID := c.Param("cluster_id")
		if err := ensureUniqueSSHCredentialName(ctx, db, clusterID, input.Name, ""); err != nil {
			return err
		}
		credentialID := uuid.NewString()
		encrypted, err := nodedomain.EncryptSSHCredentialSecret(
			cipher,
			clusterID,
			credentialID,
			requestSecret(input),
		)
		if err != nil {
			return err
		}
		item, err := db.SSHCredential.Create().Set(
			query.SSHCredential.Id.Set(credentialID),
			query.SSHCredential.ClusterId.Set(clusterID),
			query.SSHCredential.Name.Set(input.Name),
			query.SSHCredential.Username.Set(input.Username),
			query.SSHCredential.AuthType.Set(input.AuthType),
			query.SSHCredential.SecretEncrypted.Set(encrypted),
		).Do(ctx)
		if err != nil {
			return err
		}
		return types.JSON(c, http.StatusCreated, newSSHCredentialResponse(item, 0))
	}
}

func updateSSHCredential(db *client.Client, cipher *nodedomain.CredentialCipher) echo.HandlerFunc {
	return func(c *echo.Context) error {
		if err := requireClusterOwner(c, db); err != nil {
			return err
		}
		var input sshCredentialWriteRequest
		if err := c.Bind(&input); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid request body")
		}
		input.Name = strings.TrimSpace(input.Name)
		input.Username = strings.TrimSpace(input.Username)
		if input.Name == "" || input.Username == "" {
			return echo.NewHTTPError(http.StatusBadRequest, "name and username are required")
		}
		ctx := c.Request().Context()
		item, err := findClusterSSHCredential(ctx, db, c.Param("cluster_id"), c.Param("credential_id"))
		if err != nil {
			return err
		}
		if err := ensureUniqueSSHCredentialName(ctx, db, item.ClusterId, input.Name, item.Id); err != nil {
			return err
		}
		if input.AuthType == "" {
			input.AuthType = item.AuthType
		}
		secretProvided := input.Password != "" || input.PrivateKey != "" || input.Passphrase != ""
		if input.AuthType != item.AuthType && !secretProvided {
			return echo.NewHTTPError(http.StatusBadRequest, "changing auth_type requires a new secret")
		}
		sets := []query.SSHCredentialSetClause{
			query.SSHCredential.Name.Set(input.Name),
			query.SSHCredential.Username.Set(input.Username),
			query.SSHCredential.AuthType.Set(input.AuthType),
			query.SSHCredential.UpdatedAt.Set(time.Now()),
		}
		if secretProvided {
			secret := requestSecret(input)
			if err := secret.Validate(input.AuthType); err != nil {
				return echo.NewHTTPError(http.StatusBadRequest, err.Error())
			}
			encrypted, encryptErr := nodedomain.EncryptSSHCredentialSecret(
				cipher,
				item.ClusterId,
				item.Id,
				secret,
			)
			if encryptErr != nil {
				return encryptErr
			}
			sets = append(sets, query.SSHCredential.SecretEncrypted.Set(encrypted))
		}
		updated, err := db.SSHCredential.Update().
			Where(query.SSHCredential.Id.Equals(item.Id)).
			Set(sets...).
			Do(ctx)
		if err != nil {
			return err
		}
		count, err := db.Node.Query().Where(query.Node.SshCredentialId.Equals(&item.Id)).Count(ctx)
		if err != nil {
			return err
		}
		return types.JSON(c, http.StatusOK, newSSHCredentialResponse(updated, count))
	}
}

func deleteSSHCredential(db *client.Client) echo.HandlerFunc {
	return func(c *echo.Context) error {
		if err := requireClusterOwner(c, db); err != nil {
			return err
		}
		ctx := c.Request().Context()
		item, err := findClusterSSHCredential(ctx, db, c.Param("cluster_id"), c.Param("credential_id"))
		if err != nil {
			return err
		}
		count, err := db.Node.Query().Where(query.Node.SshCredentialId.Equals(&item.Id)).Count(ctx)
		if err != nil {
			return err
		}
		if count > 0 {
			nodes, nodesErr := querySSHCredentialNodes(ctx, db, item.ClusterId, item.Id)
			if nodesErr != nil {
				return nodesErr
			}
			return c.JSON(http.StatusConflict, types.H{
				Code: "conflict",
				Msg:  "SSH credential is still used by nodes; reinstall those nodes with another credential first",
				Data: map[string]any{"nodes": nodes},
			})
		}
		if _, err := db.SSHCredential.Delete().Where(query.SSHCredential.Id.Equals(item.Id)).Do(ctx); err != nil {
			return err
		}
		return types.JSON(c, http.StatusOK, nil)
	}
}

func listSSHCredentialNodes(db *client.Client) echo.HandlerFunc {
	return func(c *echo.Context) error {
		ctx := c.Request().Context()
		item, err := findClusterSSHCredential(ctx, db, c.Param("cluster_id"), c.Param("credential_id"))
		if err != nil {
			return err
		}
		nodes, err := querySSHCredentialNodes(ctx, db, item.ClusterId, item.Id)
		if err != nil {
			return err
		}
		return types.JSON(c, http.StatusOK, nodes)
	}
}

func querySSHCredentialNodes(
	ctx context.Context,
	db *client.Client,
	clusterID, credentialID string,
) ([]sshCredentialNodeResponse, error) {
	nodes, err := client.Raw[sshCredentialNodeResponse](ctx, db, `
		SELECT id, name, status
		FROM nodes
		WHERE cluster_id = $1 AND ssh_credential_id = $2
		ORDER BY name`, clusterID, credentialID)
	if err != nil {
		return nil, err
	}
	if nodes == nil {
		nodes = make([]sshCredentialNodeResponse, 0)
	}
	return nodes, nil
}

func requireClusterOwner(c *echo.Context, db *client.Client) error {
	_, owner, err := clusteraccess.Check(
		c.Request().Context(),
		db,
		c.Param("cluster_id"),
		auth.CurrentUID(c),
	)
	if err != nil {
		return err
	}
	if !owner {
		return echo.NewHTTPError(http.StatusForbidden, "only the cluster owner can manage SSH credentials")
	}
	return nil
}

func validateSSHCredentialWrite(input *sshCredentialWriteRequest) error {
	input.Name = strings.TrimSpace(input.Name)
	input.Username = strings.TrimSpace(input.Username)
	if input.Name == "" || input.Username == "" {
		return errors.New("name and username are required")
	}
	if len([]rune(input.Name)) > 80 || len([]rune(input.Username)) > 128 {
		return errors.New("name or username is too long")
	}
	return requestSecret(*input).Validate(input.AuthType)
}

func requestSecret(input sshCredentialWriteRequest) nodedomain.SSHCredentialSecret {
	return nodedomain.SSHCredentialSecret{
		Password:      input.Password,
		PrivateKeyPEM: input.PrivateKey,
		Passphrase:    input.Passphrase,
	}
}

func ensureUniqueSSHCredentialName(
	ctx context.Context,
	db *client.Client,
	clusterID, name, excludeID string,
) error {
	item, err := db.SSHCredential.Query().Where(
		query.SSHCredential.ClusterId.Equals(clusterID),
		query.SSHCredential.Name.Equals(name),
	).First(ctx)
	if err != nil {
		return err
	}
	if item != nil && item.Id != excludeID {
		return echo.NewHTTPError(http.StatusConflict, "an SSH credential with this name already exists")
	}
	return nil
}

func findClusterSSHCredential(
	ctx context.Context,
	db *client.Client,
	clusterID, credentialID string,
) (*model.SSHCredential, error) {
	item, err := db.SSHCredential.FindUnique(ctx, query.SSHCredential.Id.Equals(credentialID))
	if err != nil {
		return nil, err
	}
	if item == nil || item.ClusterId != clusterID {
		return nil, echo.NewHTTPError(http.StatusNotFound, "SSH credential not found")
	}
	return item, nil
}

func newSSHCredentialResponse(item *model.SSHCredential, nodeCount int64) sshCredentialResponse {
	return sshCredentialResponse{
		ID: item.Id, Name: item.Name, Username: item.Username, AuthType: item.AuthType,
		NodeCount: nodeCount, CreatedAt: item.CreatedAt, UpdatedAt: item.UpdatedAt,
	}
}
