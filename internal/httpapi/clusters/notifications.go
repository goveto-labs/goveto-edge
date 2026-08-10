package clusters

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/containrrr/shoutrrr"
	shoutrrrtypes "github.com/containrrr/shoutrrr/pkg/types"
	"github.com/google/uuid"
	"github.com/labstack/echo/v5"

	"goveto-edge/internal/audit"
	"goveto-edge/internal/clusteraccess"
	"goveto-edge/internal/httpapi/types"
	"goveto-edge/internal/node"
	"goveto-edge/internal/rbac"
	"goveto-edge/internal/storage/gen/client"
	"goveto-edge/internal/storage/gen/model"
	"goveto-edge/internal/storage/gen/query"
)

const notificationURLLimit = 8192

type notificationChannelRequest struct {
	Name    string `json:"name"`
	URL     string `json:"url,omitempty"`
	Enabled *bool  `json:"enabled,omitempty"`
}

type notificationChannelTestRequest struct {
	URL  string `json:"url"`
	Name string `json:"name,omitempty"`
}

type notificationChannelResponse struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	Service       string    `json:"service"`
	MaskedURL     string    `json:"masked_url"`
	Enabled       bool      `json:"enabled"`
	URLConfigured bool      `json:"url_configured"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

func registerNotificationChannels(group *echo.Group, db *client.Client, cipher *node.CredentialCipher) {
	group.GET("/notification-channels", listNotificationChannels(db))
	manage := clusteraccess.RequirePermission(db, rbac.PermissionNotificationManage)
	group.POST("/notification-channels", createNotificationChannel(db, cipher), manage)
	group.POST("/notification-channels/test", testDraftNotificationChannel(), manage)
	group.PUT("/notification-channels/:channel_id", updateNotificationChannel(db, cipher), manage)
	group.DELETE("/notification-channels/:channel_id", deleteNotificationChannel(db), manage)
	group.POST("/notification-channels/:channel_id/test", testNotificationChannel(db, cipher), manage)
}

func RewrapNotificationSecrets(ctx context.Context, db *client.Client, cipher *node.CredentialCipher) error {
	items, err := db.NotificationChannel.Query().Do(ctx)
	if err != nil {
		return err
	}
	for index := range items {
		item := &items[index]
		wrapped, changed, rewrapErr := cipher.RewrapScoped(notificationChannelScope(item.ClusterId, item.Id), item.UrlEncrypted)
		if rewrapErr != nil {
			return fmt.Errorf("rewrap notification channel %s: %w", item.Id, rewrapErr)
		}
		if changed {
			if _, err = db.NotificationChannel.Update().Where(query.NotificationChannel.Id.Equals(item.Id)).Set(query.NotificationChannel.UrlEncrypted.Set(wrapped)).Do(ctx); err != nil {
				return err
			}
		}
	}
	return nil
}

// @summary List notification channels
// @description List the cluster's notification channels without exposing their Shoutrrr URLs.
// @Tags clusters-notifications
func listNotificationChannels(db *client.Client) echo.HandlerFunc {
	return func(c *echo.Context) error {
		items, err := db.NotificationChannel.Query().
			Where(query.NotificationChannel.ClusterId.Equals(c.Param("cluster_id"))).
			OrderBy(query.NotificationChannel.Name.Asc()).
			Do(c.Request().Context())
		if err != nil {
			return err
		}
		result := make([]notificationChannelResponse, len(items))
		for index := range items {
			result[index] = newNotificationChannelResponse(&items[index])
		}
		return types.JSON(c, http.StatusOK, result)
	}
}

// @summary Create notification channel
// @description Create an encrypted Shoutrrr notification destination scoped to the cluster.
// @Tags clusters-notifications
func createNotificationChannel(db *client.Client, cipher *node.CredentialCipher) echo.HandlerFunc {
	return func(c *echo.Context) error {
		if cipher == nil {
			return errors.New("notification credential cipher is unavailable")
		}
		var input notificationChannelRequest
		if err := c.Bind(&input); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid request body")
		}
		name, rawURL, service, err := validateNotificationChannelInput(input, true)
		if err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, err.Error())
		}
		clusterID := c.Param("cluster_id")
		ctx := c.Request().Context()
		if err = ensureUniqueNotificationChannelName(ctx, db, clusterID, name, ""); err != nil {
			return err
		}
		id := uuid.NewString()
		encrypted, err := cipher.EncryptScoped(notificationChannelScope(clusterID, id), rawURL)
		if err != nil {
			return err
		}
		enabled := true
		if input.Enabled != nil {
			enabled = *input.Enabled
		}
		item, err := db.NotificationChannel.Create().Set(
			query.NotificationChannel.Id.Set(id),
			query.NotificationChannel.ClusterId.Set(clusterID),
			query.NotificationChannel.Name.Set(name),
			query.NotificationChannel.Service.Set(service),
			query.NotificationChannel.UrlEncrypted.Set(encrypted),
			query.NotificationChannel.Enabled.Set(enabled),
		).Do(ctx)
		if err != nil {
			return err
		}
		response := newNotificationChannelResponse(item)
		audit.SetResourceID(c, item.Id)
		audit.SetChange(c, nil, response)
		return types.JSON(c, http.StatusCreated, response)
	}
}

// @summary Update notification channel
// @description Update channel metadata and optionally replace its encrypted Shoutrrr URL; omit URL to retain it.
// @Tags clusters-notifications
func updateNotificationChannel(db *client.Client, cipher *node.CredentialCipher) echo.HandlerFunc {
	return func(c *echo.Context) error {
		var input notificationChannelRequest
		if err := c.Bind(&input); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid request body")
		}
		name, rawURL, service, err := validateNotificationChannelInput(input, false)
		if err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, err.Error())
		}
		ctx := c.Request().Context()
		item, err := findNotificationChannel(ctx, db, c.Param("cluster_id"), c.Param("channel_id"))
		if err != nil {
			return err
		}
		if err = ensureUniqueNotificationChannelName(ctx, db, item.ClusterId, name, item.Id); err != nil {
			return err
		}
		sets := []query.NotificationChannelSetClause{
			query.NotificationChannel.Name.Set(name),
			query.NotificationChannel.UpdatedAt.Set(time.Now()),
		}
		if input.Enabled != nil {
			sets = append(sets, query.NotificationChannel.Enabled.Set(*input.Enabled))
		}
		if rawURL != "" {
			if cipher == nil {
				return errors.New("notification credential cipher is unavailable")
			}
			encrypted, encryptErr := cipher.EncryptScoped(notificationChannelScope(item.ClusterId, item.Id), rawURL)
			if encryptErr != nil {
				return encryptErr
			}
			sets = append(sets,
				query.NotificationChannel.Service.Set(service),
				query.NotificationChannel.UrlEncrypted.Set(encrypted),
			)
		}
		updated, err := db.NotificationChannel.Update().
			Where(query.NotificationChannel.Id.Equals(item.Id)).
			Set(sets...).Do(ctx)
		if err != nil {
			return err
		}
		before := newNotificationChannelResponse(item)
		response := newNotificationChannelResponse(updated)
		audit.SetChange(c, before, response)
		return types.JSON(c, http.StatusOK, response)
	}
}

// @summary Delete notification channel
// @description Delete a notification channel from the cluster.
// @Tags clusters-notifications
func deleteNotificationChannel(db *client.Client) echo.HandlerFunc {
	return func(c *echo.Context) error {
		ctx := c.Request().Context()
		item, err := findNotificationChannel(ctx, db, c.Param("cluster_id"), c.Param("channel_id"))
		if err != nil {
			return err
		}
		if _, err = db.NotificationChannel.Delete().Where(query.NotificationChannel.Id.Equals(item.Id)).Do(ctx); err != nil {
			return err
		}
		audit.SetChange(c, newNotificationChannelResponse(item), nil)
		return c.NoContent(http.StatusNoContent)
	}
}

// @summary Test notification channel
// @description Send a test message through the channel's stored Shoutrrr destination.
// @Tags clusters-notifications
func testNotificationChannel(db *client.Client, cipher *node.CredentialCipher) echo.HandlerFunc {
	return func(c *echo.Context) error {
		if cipher == nil {
			return errors.New("notification credential cipher is unavailable")
		}
		ctx := c.Request().Context()
		item, err := findNotificationChannel(ctx, db, c.Param("cluster_id"), c.Param("channel_id"))
		if err != nil {
			return err
		}
		rawURL, err := cipher.DecryptScoped(notificationChannelScope(item.ClusterId, item.Id), item.UrlEncrypted)
		if err != nil {
			return errors.New("decrypt notification channel URL")
		}
		if err = deliverTestNotification(rawURL, item.ClusterId, item.Name); err != nil {
			return err
		}
		audit.SetResourceID(c, item.Id)
		return types.JSON(c, http.StatusOK, map[string]bool{"delivered": true})
	}
}

// @summary Test a draft notification channel
// @description Validate an unsaved Shoutrrr URL and deliver a test message through it.
// @Tags clusters-notifications
func testDraftNotificationChannel() echo.HandlerFunc {
	return func(c *echo.Context) error {
		var input notificationChannelTestRequest
		if err := c.Bind(&input); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid request body")
		}
		rawURL := strings.TrimSpace(input.URL)
		if rawURL == "" {
			return echo.NewHTTPError(http.StatusBadRequest, "url is required")
		}
		if _, err := validateNotificationURL(rawURL); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, err.Error())
		}
		name := strings.TrimSpace(input.Name)
		if name == "" {
			name = "draft"
		}
		if err := deliverTestNotification(rawURL, c.Param("cluster_id"), name); err != nil {
			return err
		}
		return types.JSON(c, http.StatusOK, map[string]bool{"delivered": true})
	}
}

func deliverTestNotification(rawURL, clusterID, channelName string) error {
	sender, err := shoutrrr.CreateSender(rawURL)
	if err != nil {
		return echo.NewHTTPError(http.StatusUnprocessableEntity, "notification URL is invalid")
	}
	sender.Timeout = 10 * time.Second
	message := fmt.Sprintf("Goveto notification test\nCluster: %s\nChannel: %s", clusterID, channelName)
	for _, sendErr := range sender.Send(message, &shoutrrrtypes.Params{"title": "Goveto notification test"}) {
		if sendErr != nil {
			return echo.NewHTTPError(http.StatusBadGateway, "notification delivery failed")
		}
	}
	return nil
}

func validateNotificationChannelInput(input notificationChannelRequest, requireURL bool) (name, rawURL, service string, err error) {
	name = strings.TrimSpace(input.Name)
	if name == "" {
		return "", "", "", errors.New("name is required")
	}
	if len(name) > 120 {
		return "", "", "", errors.New("name must be 120 characters or fewer")
	}
	rawURL = strings.TrimSpace(input.URL)
	if rawURL == "" {
		if requireURL {
			return "", "", "", errors.New("url is required")
		}
		return name, "", "", nil
	}
	if len(rawURL) > notificationURLLimit {
		return "", "", "", errors.New("url is too long")
	}
	service, err = validateNotificationURL(rawURL)
	if err != nil {
		return "", "", "", err
	}
	return name, rawURL, service, nil
}

func validateNotificationURL(rawURL string) (string, error) {
	if len(rawURL) > notificationURLLimit {
		return "", errors.New("url is too long")
	}
	parsed, parseErr := url.Parse(rawURL)
	if parseErr != nil || parsed.Scheme == "" {
		return "", errors.New("url must be a valid Shoutrrr URL")
	}
	service := strings.ToLower(strings.SplitN(parsed.Scheme, "+", 2)[0])
	if _, createErr := shoutrrr.CreateSender(rawURL); createErr != nil {
		return "", errors.New("url must be a valid Shoutrrr URL")
	}
	return service, nil
}

func ensureUniqueNotificationChannelName(ctx context.Context, db *client.Client, clusterID, name, excludeID string) error {
	clauses := []query.NotificationChannelWhereClause{
		query.NotificationChannel.ClusterId.Equals(clusterID),
		query.NotificationChannel.Name.Equals(name),
	}
	if excludeID != "" {
		clauses = append(clauses, query.NotificationChannel.Id.Not(excludeID))
	}
	count, err := db.NotificationChannel.Query().Where(clauses...).Count(ctx)
	if err != nil {
		return err
	}
	if count > 0 {
		return echo.NewHTTPError(http.StatusConflict, "notification channel name already exists")
	}
	return nil
}

func findNotificationChannel(ctx context.Context, db *client.Client, clusterID, channelID string) (*model.NotificationChannel, error) {
	item, err := db.NotificationChannel.FindFirst(ctx,
		query.NotificationChannel.ClusterId.Equals(clusterID),
		query.NotificationChannel.Id.Equals(channelID),
	)
	if err != nil {
		return nil, err
	}
	if item == nil {
		return nil, echo.NewHTTPError(http.StatusNotFound, "notification channel not found")
	}
	return item, nil
}

func newNotificationChannelResponse(item *model.NotificationChannel) notificationChannelResponse {
	return notificationChannelResponse{
		ID: item.Id, Name: item.Name, Service: item.Service,
		MaskedURL: item.Service + "://***", Enabled: item.Enabled, URLConfigured: item.UrlEncrypted != "",
		CreatedAt: item.CreatedAt, UpdatedAt: item.UpdatedAt,
	}
}

func notificationChannelScope(clusterID, channelID string) string {
	return "notification-channel:" + clusterID + ":" + channelID
}
