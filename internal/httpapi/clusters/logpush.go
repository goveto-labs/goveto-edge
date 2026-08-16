package clusters

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"

	"goveto-edge/internal/audit"
	"goveto-edge/internal/clusteraccess"
	"goveto-edge/internal/httpapi/types"
	"goveto-edge/internal/logpush"
	"goveto-edge/internal/node"
	"goveto-edge/internal/rbac"
	"goveto-edge/internal/storage/gen/client"
	"goveto-edge/internal/storage/gen/model"
	"goveto-edge/internal/storage/gen/query"
)

// logpushTestSlots bounds concurrent connectivity tests against Kafka brokers.
var logpushTestSlots = make(chan struct{}, 4)

// logpushInvalidator is implemented by the logpush dispatcher so CRUD writes
// refresh cached destinations without waiting for the cache TTL.
type logpushInvalidator interface {
	Invalidate(clusterID string)
}

type logpushDestinationRequest struct {
	Name          string   `json:"name"`
	Brokers       string   `json:"brokers,omitempty"`
	Topic         string   `json:"topic,omitempty"`
	LogTypes      []string `json:"log_types,omitempty"`
	TLSEnabled    *bool    `json:"tls_enabled,omitempty"`
	SASLMechanism *string  `json:"sasl_mechanism,omitempty"`
	Username      string   `json:"username,omitempty"`
	Password      string   `json:"password,omitempty"`
	Enabled       *bool    `json:"enabled,omitempty"`
}

type logpushDestinationResponse struct {
	ID                    string    `json:"id"`
	Name                  string    `json:"name"`
	Type                  string    `json:"type"`
	Brokers               []string  `json:"brokers"`
	Topic                 string    `json:"topic"`
	LogTypes              []string  `json:"log_types"`
	TLSEnabled            bool      `json:"tls_enabled"`
	SASLMechanism         string    `json:"sasl_mechanism"`
	CredentialsConfigured bool      `json:"credentials_configured"`
	Enabled               bool      `json:"enabled"`
	CreatedAt             time.Time `json:"created_at"`
	UpdatedAt             time.Time `json:"updated_at"`
}

func registerLogpushDestinations(group *echo.Group, db *client.Client, cipher *node.CredentialCipher, invalidator logpushInvalidator) {
	group.GET("/logpush-destinations", listLogpushDestinations(db))
	manage := clusteraccess.RequirePermission(db, rbac.PermissionLogpushManage)
	group.POST("/logpush-destinations", createLogpushDestination(db, cipher, invalidator), manage)
	group.POST("/logpush-destinations/test", testDraftLogpushDestination(), manage)
	group.PUT("/logpush-destinations/:destination_id", updateLogpushDestination(db, cipher, invalidator), manage)
	group.DELETE("/logpush-destinations/:destination_id", deleteLogpushDestination(db, invalidator), manage)
	group.POST("/logpush-destinations/:destination_id/test", testLogpushDestination(db, cipher), manage)
}

// RewrapLogpushSecrets migrates stored destination credentials onto the
// current notification keyring after a master-key rotation.
func RewrapLogpushSecrets(ctx context.Context, db *client.Client, cipher *node.CredentialCipher) error {
	items, err := db.LogpushDestination.Query().Do(ctx)
	if err != nil {
		return err
	}
	for index := range items {
		item := &items[index]
		if item.CredentialsEncrypted == nil || *item.CredentialsEncrypted == "" {
			continue
		}
		wrapped, changed, rewrapErr := cipher.RewrapScoped(logpush.CredentialScope(item.ClusterId, item.Id), *item.CredentialsEncrypted)
		if rewrapErr != nil {
			return fmt.Errorf("rewrap logpush destination %s: %w", item.Id, rewrapErr)
		}
		if changed {
			if _, err = db.LogpushDestination.Update().Where(query.LogpushDestination.Id.Equals(item.Id)).Set(query.LogpushDestination.CredentialsEncrypted.Set(wrapped)).Do(ctx); err != nil {
				return err
			}
		}
	}
	return nil
}

// @summary List logpush destinations
// @description List the cluster's logpush destinations without exposing credentials.
// @Tags clusters-logpush
func listLogpushDestinations(db *client.Client) echo.HandlerFunc {
	return func(c *echo.Context) error {
		items, err := db.LogpushDestination.Query().
			Where(query.LogpushDestination.ClusterId.Equals(c.Param("cluster_id"))).
			OrderBy(query.LogpushDestination.Name.Asc()).
			Do(c.Request().Context())
		if err != nil {
			return err
		}
		result := make([]logpushDestinationResponse, len(items))
		for index := range items {
			result[index] = newLogpushDestinationResponse(&items[index])
		}
		return types.JSON(c, http.StatusOK, result)
	}
}

// @summary Create logpush destination
// @description Create a Kafka logpush destination scoped to the cluster; credentials are stored encrypted.
// @Tags clusters-logpush
func createLogpushDestination(db *client.Client, cipher *node.CredentialCipher, invalidator logpushInvalidator) echo.HandlerFunc {
	return func(c *echo.Context) error {
		if cipher == nil {
			return errors.New("logpush credential cipher is unavailable")
		}
		var input logpushDestinationRequest
		if err := c.Bind(&input); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid request body")
		}
		normalized, err := validateLogpushDestinationInput(input, true)
		if err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, err.Error())
		}
		clusterID := c.Param("cluster_id")
		ctx := c.Request().Context()
		id := uuid.NewString()
		var credentialsEncrypted *string
		if normalized.hasCredentials {
			encoded, marshalErr := logpush.MarshalCredentials(input.Username, input.Password)
			if marshalErr != nil {
				return marshalErr
			}
			encrypted, encryptErr := cipher.EncryptScoped(logpush.CredentialScope(clusterID, id), encoded)
			if encryptErr != nil {
				return encryptErr
			}
			credentialsEncrypted = &encrypted
		}
		enabled := true
		if input.Enabled != nil {
			enabled = *input.Enabled
		}
		tlsEnabled := true
		if input.TLSEnabled != nil {
			tlsEnabled = *input.TLSEnabled
		}
		sets := []query.LogpushDestinationSetClause{
			query.LogpushDestination.Id.Set(id),
			query.LogpushDestination.ClusterId.Set(clusterID),
			query.LogpushDestination.Name.Set(normalized.name),
			query.LogpushDestination.Brokers.Set(strings.Join(normalized.brokers, ",")),
			query.LogpushDestination.Topic.Set(normalized.topic),
			query.LogpushDestination.LogTypes.Set(normalized.logTypesJSON),
			query.LogpushDestination.TlsEnabled.Set(tlsEnabled),
			query.LogpushDestination.Enabled.Set(enabled),
		}
		if normalized.mechanism != logpush.SASLNone {
			sets = append(sets, query.LogpushDestination.SaslMechanism.Set(normalized.mechanism))
		}
		if credentialsEncrypted != nil {
			sets = append(sets, query.LogpushDestination.CredentialsEncrypted.Set(*credentialsEncrypted))
		}
		var item *model.LogpushDestination
		err = db.Tx(ctx, func(tx *client.Client) error {
			if lockErr := lockLogpushCluster(ctx, tx, clusterID); lockErr != nil {
				return lockErr
			}
			count, countErr := tx.LogpushDestination.Query().
				Where(query.LogpushDestination.ClusterId.Equals(clusterID)).Count(ctx)
			if countErr != nil {
				return countErr
			}
			if count >= logpush.MaxDestinationsPerCluster {
				return echo.NewHTTPError(http.StatusConflict, "logpush destination limit reached")
			}
			if uniqueErr := ensureUniqueLogpushDestinationName(ctx, tx, clusterID, normalized.name, ""); uniqueErr != nil {
				return uniqueErr
			}
			item, countErr = tx.LogpushDestination.Create().Set(sets...).Do(ctx)
			return countErr
		})
		if err != nil {
			return err
		}
		invalidateLogpushCluster(invalidator, clusterID)
		response := newLogpushDestinationResponse(item)
		audit.SetResourceID(c, item.Id)
		audit.SetChange(c, nil, response)
		return types.JSON(c, http.StatusCreated, response)
	}
}

// @summary Update logpush destination
// @description Update destination metadata; omit brokers/topic/credentials to retain stored values. Set sasl_mechanism to "none" to clear authentication.
// @Tags clusters-logpush
func updateLogpushDestination(db *client.Client, cipher *node.CredentialCipher, invalidator logpushInvalidator) echo.HandlerFunc {
	return func(c *echo.Context) error {
		var input logpushDestinationRequest
		if err := c.Bind(&input); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid request body")
		}
		normalized, err := validateLogpushDestinationInput(input, false)
		if err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, err.Error())
		}
		ctx := c.Request().Context()
		clusterID := c.Param("cluster_id")
		var item, updated *model.LogpushDestination
		err = db.Tx(ctx, func(tx *client.Client) error {
			if lockErr := lockLogpushCluster(ctx, tx, clusterID); lockErr != nil {
				return lockErr
			}
			item, err = findLogpushDestination(ctx, tx, clusterID, c.Param("destination_id"))
			if err != nil {
				return err
			}
			if err = ensureUniqueLogpushDestinationName(ctx, tx, item.ClusterId, normalized.name, item.Id); err != nil {
				return err
			}
			sets := []query.LogpushDestinationSetClause{
				query.LogpushDestination.Name.Set(normalized.name),
				query.LogpushDestination.UpdatedAt.Set(time.Now()),
			}
			if len(normalized.brokers) > 0 {
				sets = append(sets, query.LogpushDestination.Brokers.Set(strings.Join(normalized.brokers, ",")))
			}
			if normalized.topic != "" {
				sets = append(sets, query.LogpushDestination.Topic.Set(normalized.topic))
			}
			if input.LogTypes != nil {
				sets = append(sets, query.LogpushDestination.LogTypes.Set(normalized.logTypesJSON))
			}
			if input.TLSEnabled != nil {
				sets = append(sets, query.LogpushDestination.TlsEnabled.Set(*input.TLSEnabled))
			}
			if input.Enabled != nil {
				sets = append(sets, query.LogpushDestination.Enabled.Set(*input.Enabled))
			}

			effectiveMechanism := logpush.SASLNone
			if item.SaslMechanism != nil {
				effectiveMechanism = *item.SaslMechanism
			}
			if input.SASLMechanism != nil {
				effectiveMechanism = normalized.mechanism
			}
			if normalized.hasCredentials && effectiveMechanism == logpush.SASLNone {
				return echo.NewHTTPError(http.StatusBadRequest, "credentials require a SASL mechanism")
			}

			if input.SASLMechanism != nil && normalized.mechanism == logpush.SASLNone {
				sets = append(sets,
					query.LogpushDestination.SaslMechanism.SetNull(),
					query.LogpushDestination.CredentialsEncrypted.SetNull(),
				)
			} else {
				if effectiveMechanism != logpush.SASLNone && !normalized.hasCredentials &&
					(item.CredentialsEncrypted == nil || *item.CredentialsEncrypted == "") {
					return echo.NewHTTPError(http.StatusBadRequest, "username and password are required for SASL authentication")
				}
				if normalized.hasCredentials {
					encrypted, encryptErr := encryptLogpushCredentials(cipher, item.ClusterId, item.Id, input.Username, input.Password)
					if encryptErr != nil {
						return encryptErr
					}
					sets = append(sets, query.LogpushDestination.CredentialsEncrypted.Set(encrypted))
				}
				if input.SASLMechanism != nil {
					sets = append(sets, query.LogpushDestination.SaslMechanism.Set(normalized.mechanism))
				}
			}
			updated, err = tx.LogpushDestination.Update().
				Where(query.LogpushDestination.Id.Equals(item.Id)).
				Set(sets...).Do(ctx)
			return err
		})
		if err != nil {
			return err
		}
		invalidateLogpushCluster(invalidator, item.ClusterId)
		before := newLogpushDestinationResponse(item)
		response := newLogpushDestinationResponse(updated)
		audit.SetChange(c, before, response)
		return types.JSON(c, http.StatusOK, response)
	}
}

// @summary Delete logpush destination
// @description Delete a logpush destination from the cluster.
// @Tags clusters-logpush
func deleteLogpushDestination(db *client.Client, invalidator logpushInvalidator) echo.HandlerFunc {
	return func(c *echo.Context) error {
		ctx := c.Request().Context()
		clusterID := c.Param("cluster_id")
		var item *model.LogpushDestination
		err := db.Tx(ctx, func(tx *client.Client) error {
			if lockErr := lockLogpushCluster(ctx, tx, clusterID); lockErr != nil {
				return lockErr
			}
			var findErr error
			item, findErr = findLogpushDestination(ctx, tx, clusterID, c.Param("destination_id"))
			if findErr != nil {
				return findErr
			}
			_, findErr = tx.LogpushDestination.Delete().Where(query.LogpushDestination.Id.Equals(item.Id)).Do(ctx)
			return findErr
		})
		if err != nil {
			return err
		}
		invalidateLogpushCluster(invalidator, item.ClusterId)
		audit.SetChange(c, newLogpushDestinationResponse(item), nil)
		return c.NoContent(http.StatusNoContent)
	}
}

// @summary Test logpush destination
// @description Verify the stored destination's brokers are reachable and its credentials authenticate.
// @Tags clusters-logpush
func testLogpushDestination(db *client.Client, cipher *node.CredentialCipher) echo.HandlerFunc {
	return func(c *echo.Context) error {
		if cipher == nil {
			return errors.New("logpush credential cipher is unavailable")
		}
		ctx := c.Request().Context()
		item, err := findLogpushDestination(ctx, db, c.Param("cluster_id"), c.Param("destination_id"))
		if err != nil {
			return err
		}
		credentialsJSON := ""
		if item.CredentialsEncrypted != nil && *item.CredentialsEncrypted != "" {
			credentialsJSON, err = cipher.DecryptScoped(logpush.CredentialScope(item.ClusterId, item.Id), *item.CredentialsEncrypted)
			if err != nil {
				return errors.New("decrypt logpush destination credentials")
			}
		}
		dest, err := logpush.DestinationFromModel(item, credentialsJSON)
		if err != nil {
			return echo.NewHTTPError(http.StatusUnprocessableEntity, "logpush destination configuration is invalid")
		}
		if err = runLogpushConnectionTest(ctx, dest); err != nil {
			return err
		}
		audit.SetResourceID(c, item.Id)
		return types.JSON(c, http.StatusOK, map[string]bool{"delivered": true})
	}
}

// @summary Test a draft logpush destination
// @description Verify an unsaved Kafka destination configuration connects before saving it.
// @Tags clusters-logpush
func testDraftLogpushDestination() echo.HandlerFunc {
	return func(c *echo.Context) error {
		var input logpushDestinationRequest
		if err := c.Bind(&input); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid request body")
		}
		normalized, err := validateLogpushDestinationInput(input, true)
		if err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, err.Error())
		}
		dest := logpush.Destination{
			Name:          normalized.name,
			Brokers:       normalized.brokers,
			Topic:         normalized.topic,
			TLSEnabled:    true,
			SASLMechanism: normalized.mechanism,
			Username:      strings.TrimSpace(input.Username),
			Password:      input.Password,
		}
		if input.TLSEnabled != nil {
			dest.TLSEnabled = *input.TLSEnabled
		}
		if err = runLogpushConnectionTest(c.Request().Context(), dest); err != nil {
			return err
		}
		return types.JSON(c, http.StatusOK, map[string]bool{"delivered": true})
	}
}

func runLogpushConnectionTest(ctx context.Context, dest logpush.Destination) error {
	select {
	case logpushTestSlots <- struct{}{}:
		defer func() { <-logpushTestSlots }()
	case <-ctx.Done():
		return ctx.Err()
	}
	testCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := logpush.TestConnection(testCtx, dest); err != nil {
		return echo.NewHTTPError(http.StatusBadGateway, "kafka connection test failed")
	}
	return nil
}

type normalizedLogpushInput struct {
	name           string
	brokers        []string
	topic          string
	logTypesJSON   json.RawMessage
	mechanism      string
	hasCredentials bool
}

func validateLogpushDestinationInput(input logpushDestinationRequest, requireTarget bool) (normalizedLogpushInput, error) {
	normalized := normalizedLogpushInput{name: strings.TrimSpace(input.Name)}
	if normalized.name == "" {
		return normalized, errors.New("name is required")
	}
	if len(normalized.name) > 120 {
		return normalized, errors.New("name must be 120 characters or fewer")
	}
	brokersRaw := strings.TrimSpace(input.Brokers)
	if brokersRaw == "" {
		if requireTarget {
			return normalized, errors.New("brokers are required")
		}
	} else {
		brokers, err := logpush.ParseBrokers(brokersRaw)
		if err != nil {
			return normalized, err
		}
		normalized.brokers = brokers
	}
	normalized.topic = strings.TrimSpace(input.Topic)
	if normalized.topic == "" {
		if requireTarget {
			return normalized, errors.New("topic is required")
		}
	} else if err := logpush.ValidateTopic(normalized.topic); err != nil {
		return normalized, err
	}
	logTypes := input.LogTypes
	if logTypes == nil && requireTarget {
		logTypes = []string{logpush.LogTypeAccess}
	}
	if logTypes != nil {
		if len(logTypes) == 0 {
			return normalized, errors.New("log_types must not be empty")
		}
		for _, logType := range logTypes {
			if !logpush.ValidLogType(logType) {
				return normalized, errors.New("log_types contains an unsupported type")
			}
		}
		encoded, err := json.Marshal(logTypes)
		if err != nil {
			return normalized, err
		}
		normalized.logTypesJSON = encoded
	}
	normalized.mechanism = logpush.SASLNone
	if input.SASLMechanism != nil {
		mechanism := strings.ToLower(strings.TrimSpace(*input.SASLMechanism))
		if mechanism == "none" {
			mechanism = logpush.SASLNone
		}
		if !logpush.ValidSASLMechanism(mechanism) {
			return normalized, errors.New("sasl_mechanism must be one of none, plain, scram-sha-256, scram-sha-512")
		}
		normalized.mechanism = mechanism
	}
	username := strings.TrimSpace(input.Username)
	if username != "" || input.Password != "" {
		if username == "" || input.Password == "" {
			return normalized, errors.New("username and password must be provided together")
		}
		normalized.hasCredentials = true
	}
	if requireTarget && normalized.mechanism != logpush.SASLNone && !normalized.hasCredentials {
		return normalized, errors.New("username and password are required for SASL authentication")
	}
	if normalized.hasCredentials && normalized.mechanism == logpush.SASLNone &&
		(requireTarget || input.SASLMechanism != nil) {
		return normalized, errors.New("credentials require a SASL mechanism")
	}
	return normalized, nil
}

func lockLogpushCluster(ctx context.Context, db *client.Client, clusterID string) error {
	_, err := db.RawExec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1, 0))", "logpush:"+clusterID)
	return err
}

func encryptLogpushCredentials(cipher *node.CredentialCipher, clusterID, destinationID, username, password string) (string, error) {
	if cipher == nil {
		return "", errors.New("logpush credential cipher is unavailable")
	}
	encoded, err := logpush.MarshalCredentials(username, password)
	if err != nil {
		return "", err
	}
	return cipher.EncryptScoped(logpush.CredentialScope(clusterID, destinationID), encoded)
}

func ensureUniqueLogpushDestinationName(ctx context.Context, db *client.Client, clusterID, name, excludeID string) error {
	clauses := []query.LogpushDestinationWhereClause{
		query.LogpushDestination.ClusterId.Equals(clusterID),
		query.LogpushDestination.Name.Equals(name),
	}
	if excludeID != "" {
		clauses = append(clauses, query.LogpushDestination.Id.Not(excludeID))
	}
	count, err := db.LogpushDestination.Query().Where(clauses...).Count(ctx)
	if err != nil {
		return err
	}
	if count > 0 {
		return echo.NewHTTPError(http.StatusConflict, "logpush destination name already exists")
	}
	return nil
}

func findLogpushDestination(ctx context.Context, db *client.Client, clusterID, destinationID string) (*model.LogpushDestination, error) {
	item, err := db.LogpushDestination.FindFirst(ctx,
		query.LogpushDestination.ClusterId.Equals(clusterID),
		query.LogpushDestination.Id.Equals(destinationID),
	)
	if err != nil {
		return nil, err
	}
	if item == nil {
		return nil, echo.NewHTTPError(http.StatusNotFound, "logpush destination not found")
	}
	return item, nil
}

func newLogpushDestinationResponse(item *model.LogpushDestination) logpushDestinationResponse {
	brokers, _ := logpush.ParseBrokers(item.Brokers)
	var logTypes []string
	if len(item.LogTypes) > 0 {
		_ = json.Unmarshal(item.LogTypes, &logTypes)
	}
	mechanism := logpush.SASLNone
	if item.SaslMechanism != nil {
		mechanism = *item.SaslMechanism
	}
	return logpushDestinationResponse{
		ID: item.Id, Name: item.Name, Type: string(item.Type),
		Brokers: brokers, Topic: item.Topic, LogTypes: logTypes,
		TLSEnabled: item.TlsEnabled, SASLMechanism: mechanism,
		CredentialsConfigured: item.CredentialsEncrypted != nil && *item.CredentialsEncrypted != "",
		Enabled:               item.Enabled,
		CreatedAt:             item.CreatedAt, UpdatedAt: item.UpdatedAt,
	}
}

func invalidateLogpushCluster(invalidator logpushInvalidator, clusterID string) {
	if invalidator != nil {
		invalidator.Invalidate(clusterID)
	}
}
