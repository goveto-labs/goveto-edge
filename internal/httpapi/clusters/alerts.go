package clusters

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v5"

	"goveto-edge/internal/alerting"
	"goveto-edge/internal/audit"
	"goveto-edge/internal/auth"
	"goveto-edge/internal/clusteraccess"
	"goveto-edge/internal/httpapi/types"
	"goveto-edge/internal/rbac"
	"goveto-edge/internal/storage/gen/client"
	"goveto-edge/internal/storage/gen/model"
	"goveto-edge/internal/storage/gen/query"
)

func registerAlerts(group *echo.Group, db *client.Client) {
	read := clusteraccess.RequirePermission(db, rbac.PermissionClusterRead)
	manage := clusteraccess.RequirePermission(db, rbac.PermissionAlertManage)

	alerts := group.Group("/alerts", read)
	alerts.GET("", listAlerts(db))
	alerts.GET("/:alert_id", alertDetail(db))
	alerts.GET("/:alert_id/events", alertEvents(db))
	alerts.GET("/:alert_id/deliveries", alertDeliveries(db))
	alerts.POST("/:alert_id/ack", ackAlert(db), manage)
	alerts.POST("/:alert_id/resolve", resolveAlert(db), manage)
	alerts.POST("/batch-ack", batchAckAlerts(db), manage)

	rules := group.Group("/alert-rules", read)
	rules.GET("", listAlertRules(db))
	rules.PUT("/:rule_id", updateAlertRule(db), manage)
}

type alertListPage struct {
	Items    []model.AlertInstance `json:"items"`
	Page     int                   `json:"page"`
	PageSize int                   `json:"page_size"`
	Total    int64                 `json:"total"`
}

type alertDetailResponse struct {
	model.AlertInstance
}

type alertEventResponse struct {
	ID          string          `json:"id"`
	Type        string          `json:"type"`
	FromStatus  *string         `json:"fromStatus"`
	ToStatus    *string         `json:"toStatus"`
	PayloadJSON json.RawMessage `json:"payloadJson"`
	CreatedAt   time.Time       `json:"createdAt"`
}

type alertDeliveryResponse struct {
	ID          string     `json:"id"`
	ChannelID   string     `json:"channelId"`
	ChannelName string     `json:"channelName"`
	Kind        string     `json:"kind"`
	Status      string     `json:"status"`
	Attempts    int        `json:"attempts"`
	LastError   *string    `json:"lastError"`
	NextRetryAt *time.Time `json:"nextRetryAt"`
	SentAt      *time.Time `json:"sentAt"`
	CreatedAt   time.Time  `json:"createdAt"`
}

type alertEventPage struct {
	Items    []alertEventResponse `json:"items"`
	Page     int                  `json:"page"`
	PageSize int                  `json:"page_size"`
	Total    int64                `json:"total"`
}

type alertDeliveryPage struct {
	Items    []alertDeliveryResponse `json:"items"`
	Page     int                     `json:"page"`
	PageSize int                     `json:"page_size"`
	Total    int64                   `json:"total"`
}

// @summary List alert instances
// @description List alert instances for the cluster with optional status, severity and kind filters.
// @Tags clusters-alerts
func listAlerts(db *client.Client) echo.HandlerFunc {
	return func(c *echo.Context) error {
		wheres := []query.AlertInstanceWhereClause{
			query.AlertInstance.ClusterId.Equals(c.Param("cluster_id")),
		}
		active := strings.TrimSpace(c.QueryParam("active"))
		switch strings.ToLower(active) {
		case "":
		case "true":
			wheres = append(wheres, query.AlertInstance.Status.Not(model.AlertStatusRESOLVED))
		case "false":
			wheres = append(wheres, query.AlertInstance.Status.Equals(model.AlertStatusRESOLVED))
		default:
			return echo.NewHTTPError(http.StatusBadRequest, "active must be true or false")
		}
		if status := strings.ToUpper(strings.TrimSpace(c.QueryParam("status"))); status != "" {
			switch model.AlertStatus(status) {
			case model.AlertStatusPENDING, model.AlertStatusFIRING, model.AlertStatusACKNOWLEDGED, model.AlertStatusRESOLVED:
				wheres = append(wheres, query.AlertInstance.Status.Equals(model.AlertStatus(status)))
			default:
				return echo.NewHTTPError(http.StatusBadRequest, "unknown alert status")
			}
		}
		if severity := strings.ToUpper(strings.TrimSpace(c.QueryParam("severity"))); severity != "" {
			switch model.AlertSeverity(severity) {
			case model.AlertSeverityINFO, model.AlertSeverityWARNING, model.AlertSeverityCRITICAL:
				wheres = append(wheres, query.AlertInstance.Severity.Equals(model.AlertSeverity(severity)))
			default:
				return echo.NewHTTPError(http.StatusBadRequest, "unknown alert severity")
			}
		}
		if kind := strings.TrimSpace(c.QueryParam("kind")); kind != "" {
			if alerting.Spec(kind) == nil {
				return echo.NewHTTPError(http.StatusBadRequest, "unknown alert kind")
			}
			wheres = append(wheres, query.AlertInstance.Kind.Equals(kind))
		}
		page, pageSize := parseAlertPagination(c)
		ctx := c.Request().Context()
		total, err := db.AlertInstance.Query().Where(wheres...).Count(ctx)
		if err != nil {
			return err
		}
		items, err := db.AlertInstance.Query().
			Where(wheres...).
			OrderBy(query.AlertInstance.LastSeenAt.Desc()).
			Skip((page - 1) * pageSize).
			Take(pageSize).
			Do(ctx)
		if err != nil {
			return err
		}
		if items == nil {
			items = []model.AlertInstance{}
		}
		return types.JSON(c, http.StatusOK, alertListPage{Items: items, Page: page, PageSize: pageSize, Total: total})
	}
}

// @summary Alert detail
// @description Fetch one alert instance. Event and delivery history are exposed by paginated subresources.
// @Tags clusters-alerts
func alertDetail(db *client.Client) echo.HandlerFunc {
	return func(c *echo.Context) error {
		ctx := c.Request().Context()
		instance, err := findAlertInstance(ctx, db, c.Param("cluster_id"), c.Param("alert_id"))
		if err != nil {
			return err
		}
		return types.JSON(c, http.StatusOK, alertDetailResponse{AlertInstance: *instance})
	}
}

// @summary Alert event history
// @description Fetch a paginated alert event timeline.
// @Tags clusters-alerts
func alertEvents(db *client.Client) echo.HandlerFunc {
	return func(c *echo.Context) error {
		ctx := c.Request().Context()
		instance, err := findAlertInstance(ctx, db, c.Param("cluster_id"), c.Param("alert_id"))
		if err != nil {
			return err
		}
		page, pageSize := parseAlertHistoryPagination(c)
		total, err := db.AlertEvent.Query().Where(query.AlertEvent.InstanceId.Equals(instance.Id)).Count(ctx)
		if err != nil {
			return err
		}
		events, err := db.AlertEvent.Query().
			Where(query.AlertEvent.InstanceId.Equals(instance.Id)).
			OrderBy(query.AlertEvent.CreatedAt.Desc()).
			Skip((page - 1) * pageSize).Take(pageSize).Do(ctx)
		if err != nil {
			return err
		}
		items := make([]alertEventResponse, 0, len(events))
		for _, event := range events {
			items = append(items, alertEventResponse{
				ID: event.Id, Type: string(event.Type), FromStatus: event.FromStatus, ToStatus: event.ToStatus,
				PayloadJSON: event.PayloadJson, CreatedAt: event.CreatedAt,
			})
		}
		return types.JSON(c, http.StatusOK, alertEventPage{Items: items, Page: page, PageSize: pageSize, Total: total})
	}
}

// @summary Alert delivery history
// @description Fetch paginated notification delivery records for an alert.
// @Tags clusters-alerts
func alertDeliveries(db *client.Client) echo.HandlerFunc {
	return func(c *echo.Context) error {
		ctx := c.Request().Context()
		instance, err := findAlertInstance(ctx, db, c.Param("cluster_id"), c.Param("alert_id"))
		if err != nil {
			return err
		}
		page, pageSize := parseAlertHistoryPagination(c)
		total, err := db.AlertDelivery.Query().Where(query.AlertDelivery.InstanceId.Equals(instance.Id)).Count(ctx)
		if err != nil {
			return err
		}
		deliveries, err := db.AlertDelivery.Query().
			Where(query.AlertDelivery.InstanceId.Equals(instance.Id)).
			OrderBy(query.AlertDelivery.CreatedAt.Desc()).
			Skip((page - 1) * pageSize).Take(pageSize).Do(ctx)
		if err != nil {
			return err
		}
		channelNames := alertChannelNames(ctx, db, deliveries)
		items := make([]alertDeliveryResponse, 0, len(deliveries))
		for _, delivery := range deliveries {
			items = append(items, alertDeliveryResponse{
				ID: delivery.Id, ChannelID: delivery.ChannelId, ChannelName: channelNames[delivery.ChannelId],
				Kind: delivery.Kind, Status: string(delivery.Status), Attempts: delivery.Attempts,
				LastError: delivery.LastError, NextRetryAt: delivery.NextRetryAt, SentAt: delivery.SentAt,
				CreatedAt: delivery.CreatedAt,
			})
		}
		return types.JSON(c, http.StatusOK, alertDeliveryPage{Items: items, Page: page, PageSize: pageSize, Total: total})
	}
}

type alertAckRequest struct {
	IDs []string `json:"ids"`
}

// @summary Acknowledge alert
// @description Acknowledge a firing alert. Re-acknowledging reassigns the acknowledgement.
// @Tags clusters-alerts
func ackAlert(db *client.Client) echo.HandlerFunc {
	return func(c *echo.Context) error {
		ctx := c.Request().Context()
		before, err := findAlertInstance(ctx, db, c.Param("cluster_id"), c.Param("alert_id"))
		if err != nil {
			return err
		}
		updated, err := alerting.Acknowledge(ctx, db, before.ClusterId, before.Id, auth.CurrentUID(c))
		if err != nil {
			return mapAlertMutationError(err)
		}
		audit.SetResourceID(c, updated.Id)
		audit.SetChange(c, alertSnapshot(before), alertSnapshot(updated))
		return types.JSON(c, http.StatusOK, updated)
	}
}

// @summary Batch acknowledge alerts
// @description Acknowledge multiple firing alerts at once. Unknown IDs are ignored.
// @Tags clusters-alerts
func batchAckAlerts(db *client.Client) echo.HandlerFunc {
	return func(c *echo.Context) error {
		var input alertAckRequest
		if err := c.Bind(&input); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid request body")
		}
		if len(input.IDs) == 0 {
			return echo.NewHTTPError(http.StatusBadRequest, "ids is required")
		}
		if len(input.IDs) > 200 {
			return echo.NewHTTPError(http.StatusBadRequest, "at most 200 alerts can be acknowledged at once")
		}
		clusterID := c.Param("cluster_id")
		uid := auth.CurrentUID(c)
		ctx := c.Request().Context()
		acknowledged := make([]string, 0, len(input.IDs))
		seen := make(map[string]struct{}, len(input.IDs))
		for _, id := range input.IDs {
			if _, duplicate := seen[id]; duplicate {
				continue
			}
			seen[id] = struct{}{}
			updated, err := alerting.Acknowledge(ctx, db, clusterID, id, uid)
			if err != nil {
				if errors.Is(err, alerting.ErrAlertNotFound) || errors.Is(err, alerting.ErrIllegalTransition) {
					continue
				}
				audit.SetChange(c, nil, map[string]any{"acknowledged": acknowledged})
				return err
			}
			acknowledged = append(acknowledged, updated.Id)
		}
		audit.SetChange(c, nil, map[string]any{"acknowledged": acknowledged})
		return types.JSON(c, http.StatusOK, map[string]any{"acknowledged": len(acknowledged)})
	}
}

type alertResolveRequest struct {
	Reason string `json:"reason"`
}

// @summary Resolve alert
// @description Manually resolve an alert and suppress the same condition until it clears. No recovery notification is sent.
// @Tags clusters-alerts
func resolveAlert(db *client.Client) echo.HandlerFunc {
	return func(c *echo.Context) error {
		var input alertResolveRequest
		if err := c.Bind(&input); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid request body")
		}
		reason := strings.TrimSpace(input.Reason)
		if len(reason) > 500 {
			return echo.NewHTTPError(http.StatusBadRequest, "reason must be 500 characters or fewer")
		}
		ctx := c.Request().Context()
		before, err := findAlertInstance(ctx, db, c.Param("cluster_id"), c.Param("alert_id"))
		if err != nil {
			return err
		}
		updated, err := alerting.Resolve(ctx, db, before.ClusterId, before.Id, reason)
		if err != nil {
			return mapAlertMutationError(err)
		}
		audit.SetResourceID(c, updated.Id)
		audit.SetChange(c, alertSnapshot(before), alertSnapshot(updated))
		return types.JSON(c, http.StatusOK, updated)
	}
}

type alertRuleResponse struct {
	ID              string                       `json:"id"`
	ClusterID       string                       `json:"clusterId"`
	Kind            string                       `json:"kind"`
	Label           string                       `json:"label"`
	Description     string                       `json:"description"`
	Enabled         bool                         `json:"enabled"`
	Severity        string                       `json:"severity"`
	ParamsJSON      json.RawMessage              `json:"paramsJson"`
	ParameterSpecs  []alertRuleParamSpecResponse `json:"parameterSpecs"`
	ForSeconds      int                          `json:"forSeconds"`
	CooldownSeconds int                          `json:"cooldownSeconds"`
	MutedUntil      *time.Time                   `json:"mutedUntil"`
	Channels        []string                     `json:"channels"`
	CreatedAt       time.Time                    `json:"createdAt"`
	UpdatedAt       time.Time                    `json:"updatedAt"`
}

type alertRuleParamSpecResponse struct {
	Key              string  `json:"key"`
	Label            string  `json:"label"`
	Type             string  `json:"type"`
	Default          float64 `json:"default"`
	Minimum          float64 `json:"minimum"`
	Maximum          float64 `json:"maximum"`
	ExclusiveMinimum bool    `json:"exclusiveMinimum"`
	Step             float64 `json:"step"`
}

// @summary List alert rules
// @description List the cluster's alert rules with built-in rule metadata.
// @Tags clusters-alerts
func listAlertRules(db *client.Client) echo.HandlerFunc {
	return func(c *echo.Context) error {
		items, err := db.AlertRule.Query().
			Where(query.AlertRule.ClusterId.Equals(c.Param("cluster_id"))).
			OrderBy(query.AlertRule.Kind.Asc()).
			Do(c.Request().Context())
		if err != nil {
			return err
		}
		result := make([]alertRuleResponse, 0, len(items))
		for index := range items {
			result = append(result, newAlertRuleResponse(&items[index]))
		}
		return types.JSON(c, http.StatusOK, result)
	}
}

type alertRuleUpdateRequest struct {
	Enabled         *bool          `json:"enabled"`
	Severity        *string        `json:"severity"`
	Params          map[string]any `json:"params"`
	ForSeconds      *int           `json:"forSeconds"`
	CooldownSeconds *int           `json:"cooldownSeconds"`
	MutedUntil      *time.Time     `json:"mutedUntil"`
	Unmute          bool           `json:"unmute"`
	Channels        *[]string      `json:"channels"`
}

// @summary Update alert rule
// @description Update an alert rule's enablement, severity, thresholds, hold/cooldown timers, mute window or channels.
// @Tags clusters-alerts
func updateAlertRule(db *client.Client) echo.HandlerFunc {
	return func(c *echo.Context) error {
		var input alertRuleUpdateRequest
		if err := c.Bind(&input); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid request body")
		}
		ctx := c.Request().Context()
		rule, err := db.AlertRule.FindFirst(ctx,
			query.AlertRule.ClusterId.Equals(c.Param("cluster_id")),
			query.AlertRule.Id.Equals(c.Param("rule_id")),
		)
		if err != nil {
			return err
		}
		if rule == nil {
			return echo.NewHTTPError(http.StatusNotFound, "alert rule not found")
		}
		spec := alerting.Spec(rule.Kind)
		if spec == nil {
			return echo.NewHTTPError(http.StatusConflict, "alert rule kind is no longer supported")
		}
		sets := []query.AlertRuleSetClause{}
		if input.Enabled != nil {
			sets = append(sets, query.AlertRule.Enabled.Set(*input.Enabled))
		}
		if input.Severity != nil {
			normalizedSeverity := strings.ToUpper(strings.TrimSpace(*input.Severity))
			switch model.AlertSeverity(normalizedSeverity) {
			case model.AlertSeverityINFO, model.AlertSeverityWARNING, model.AlertSeverityCRITICAL:
			default:
				return echo.NewHTTPError(http.StatusBadRequest, "unknown alert severity")
			}
			sets = append(sets, query.AlertRule.Severity.Set(model.AlertSeverity(normalizedSeverity)))
		}
		if input.Params != nil {
			merged, mergeErr := spec.MergeParams(rule.ParamsJson, input.Params)
			if mergeErr != nil {
				return echo.NewHTTPError(http.StatusBadRequest, mergeErr.Error())
			}
			sets = append(sets, query.AlertRule.ParamsJson.Set(merged))
		}
		nextForSeconds := rule.ForSeconds
		if input.ForSeconds != nil {
			nextForSeconds = *input.ForSeconds
			sets = append(sets, query.AlertRule.ForSeconds.Set(*input.ForSeconds))
		}
		nextCooldownSeconds := rule.CooldownSeconds
		if input.CooldownSeconds != nil {
			nextCooldownSeconds = *input.CooldownSeconds
			sets = append(sets, query.AlertRule.CooldownSeconds.Set(*input.CooldownSeconds))
		}
		if input.ForSeconds != nil || input.CooldownSeconds != nil {
			if timerErr := alerting.ValidateRuleTimers(nextForSeconds, nextCooldownSeconds); timerErr != nil {
				return echo.NewHTTPError(http.StatusBadRequest, timerErr.Error())
			}
		}
		if input.Unmute && input.MutedUntil != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "unmute and mutedUntil cannot be used together")
		}
		if input.Unmute {
			sets = append(sets, query.AlertRule.MutedUntil.SetNull())
		}
		if input.MutedUntil != nil {
			sets = append(sets, query.AlertRule.MutedUntil.Set(*input.MutedUntil))
		}
		if input.Channels != nil {
			if err := validateRuleChannels(ctx, db, rule.ClusterId, *input.Channels); err != nil {
				return err
			}
			encoded, encodeErr := json.Marshal(*input.Channels)
			if encodeErr != nil {
				return encodeErr
			}
			sets = append(sets, query.AlertRule.ChannelsJson.Set(encoded))
		}
		if len(sets) == 0 {
			return types.JSON(c, http.StatusOK, newAlertRuleResponse(rule))
		}
		updated, err := db.AlertRule.Update().
			Where(query.AlertRule.Id.Equals(rule.Id)).
			Set(sets...).Do(ctx)
		if err != nil {
			return err
		}
		before := newAlertRuleResponse(rule)
		response := newAlertRuleResponse(updated)
		audit.SetResourceID(c, updated.Id)
		audit.SetChange(c, before, response)
		return types.JSON(c, http.StatusOK, response)
	}
}

type alertOverviewCluster struct {
	ClusterID string `json:"clusterId"`
	Name      string `json:"name"`
	Firing    int64  `json:"firing"`
}

type alertOverviewItem struct {
	ID         string    `json:"id"`
	ClusterID  string    `json:"clusterId"`
	Kind       string    `json:"kind"`
	Title      string    `json:"title"`
	Severity   string    `json:"severity"`
	Status     string    `json:"status"`
	LastSeenAt time.Time `json:"lastSeenAt"`
}

type alertOverviewResponse struct {
	Clusters    []alertOverviewCluster `json:"clusters"`
	FiringTotal int64                  `json:"firingTotal"`
	Recent      []alertOverviewItem    `json:"recent"`
}

// registerAlertOverview exposes the cross-cluster alert summary used by the
// console bell. Access is limited to clusters the current user can read.
func registerAlertOverview(e *echo.Echo, db *client.Client) {
	group := e.Group("/api/v1/alerts", auth.RequireAuth)
	group.GET("/overview", alertOverview(db))
}

// @summary Alert overview
// @description Firing alert counts per accessible cluster plus the most recent unresolved alerts.
// @Tags alerts
func alertOverview(db *client.Client) echo.HandlerFunc {
	return func(c *echo.Context) error {
		ctx := c.Request().Context()
		uid := auth.CurrentUID(c)
		clusters, err := clusteraccess.ListAuthorizedClusters(ctx, db, uid, rbac.PermissionClusterRead)
		if err != nil {
			return err
		}
		result := alertOverviewResponse{
			Clusters: make([]alertOverviewCluster, 0, len(clusters)),
			Recent:   []alertOverviewItem{},
		}
		if len(clusters) == 0 {
			return types.JSON(c, http.StatusOK, result)
		}
		ids := make([]string, 0, len(clusters))
		for _, cluster := range clusters {
			ids = append(ids, cluster.ID)
		}
		type countRow struct {
			ClusterID string `db:"cluster_id"`
			Total     int64  `db:"total"`
		}
		counts, err := client.Raw[countRow](ctx, db, `SELECT cluster_id, COUNT(*) AS total FROM alert_instances
			WHERE cluster_id = ANY($1) AND status = 'FIRING' GROUP BY cluster_id`, ids)
		if err != nil {
			return err
		}
		countByCluster := make(map[string]int64, len(counts))
		for _, row := range counts {
			countByCluster[row.ClusterID] = row.Total
			result.FiringTotal += row.Total
		}
		for _, cluster := range clusters {
			result.Clusters = append(result.Clusters, alertOverviewCluster{
				ClusterID: cluster.ID, Name: cluster.Name, Firing: countByCluster[cluster.ID],
			})
		}
		type recentRow struct {
			ID         string    `db:"id"`
			ClusterID  string    `db:"cluster_id"`
			Kind       string    `db:"kind"`
			Title      string    `db:"title"`
			Severity   string    `db:"severity"`
			Status     string    `db:"status"`
			LastSeenAt time.Time `db:"last_seen_at"`
		}
		recent, err := client.Raw[recentRow](ctx, db, `SELECT id, cluster_id, kind, title, severity::text AS severity,
			status::text AS status, last_seen_at FROM alert_instances
			WHERE cluster_id = ANY($1) AND status IN ('PENDING','FIRING','ACKNOWLEDGED')
			ORDER BY CASE severity WHEN 'CRITICAL' THEN 0 WHEN 'WARNING' THEN 1 ELSE 2 END, last_seen_at DESC LIMIT 10`, ids)
		if err != nil {
			return err
		}
		for _, row := range recent {
			result.Recent = append(result.Recent, alertOverviewItem{
				ID: row.ID, ClusterID: row.ClusterID, Kind: row.Kind, Title: row.Title,
				Severity: row.Severity, Status: row.Status, LastSeenAt: row.LastSeenAt,
			})
		}
		return types.JSON(c, http.StatusOK, result)
	}
}

func findAlertInstance(ctx context.Context, db *client.Client, clusterID, alertID string) (*model.AlertInstance, error) {
	instance, err := db.AlertInstance.FindFirst(ctx,
		query.AlertInstance.ClusterId.Equals(clusterID),
		query.AlertInstance.Id.Equals(alertID),
	)
	if err != nil {
		return nil, err
	}
	if instance == nil {
		return nil, echo.NewHTTPError(http.StatusNotFound, "alert not found")
	}
	return instance, nil
}

func mapAlertMutationError(err error) error {
	switch {
	case errors.Is(err, alerting.ErrAlertNotFound):
		return echo.NewHTTPError(http.StatusNotFound, "alert not found")
	case errors.Is(err, alerting.ErrIllegalTransition):
		return echo.NewHTTPError(http.StatusConflict, "alert cannot transition from its current status")
	default:
		return err
	}
}

func alertSnapshot(instance *model.AlertInstance) map[string]any {
	return map[string]any{
		"id": instance.Id, "kind": instance.Kind, "status": string(instance.Status),
		"severity": string(instance.Severity), "title": instance.Title,
	}
}

func alertChannelNames(ctx context.Context, db *client.Client, deliveries []model.AlertDelivery) map[string]string {
	ids := make([]string, 0, len(deliveries))
	for _, delivery := range deliveries {
		ids = append(ids, delivery.ChannelId)
	}
	names := make(map[string]string, len(ids))
	if len(ids) == 0 {
		return names
	}
	rows, err := client.Raw[struct {
		ID   string `db:"id"`
		Name string `db:"name"`
	}](ctx, db, `SELECT id, name FROM notification_channels WHERE id = ANY($1)`, ids)
	if err != nil {
		return names
	}
	for _, row := range rows {
		names[row.ID] = row.Name
	}
	return names
}

func parseAlertPagination(c *echo.Context) (int, int) {
	page, pageSize := 1, 25
	if value, err := strconv.Atoi(c.QueryParam("page")); err == nil && value > 0 {
		page = value
	}
	if value, err := strconv.Atoi(c.QueryParam("page_size")); err == nil && value > 0 && value <= 100 {
		pageSize = value
	}
	return page, pageSize
}

func parseAlertHistoryPagination(c *echo.Context) (int, int) {
	page, pageSize := 1, 50
	if value, err := strconv.Atoi(c.QueryParam("page")); err == nil && value > 0 {
		page = value
	}
	if value, err := strconv.Atoi(c.QueryParam("page_size")); err == nil && value > 0 && value <= 100 {
		pageSize = value
	}
	return page, pageSize
}

func validateRuleChannels(ctx context.Context, db *client.Client, clusterID string, channels []string) error {
	if len(channels) > 20 {
		return echo.NewHTTPError(http.StatusBadRequest, "at most 20 channels can be bound to a rule")
	}
	if len(channels) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(channels))
	for _, id := range channels {
		if strings.TrimSpace(id) == "" {
			return echo.NewHTTPError(http.StatusBadRequest, "channel id cannot be empty")
		}
		if _, duplicate := seen[id]; duplicate {
			return echo.NewHTTPError(http.StatusBadRequest, "channel ids must not contain duplicates")
		}
		seen[id] = struct{}{}
	}
	count, err := db.NotificationChannel.Query().Where(
		query.NotificationChannel.ClusterId.Equals(clusterID),
		query.NotificationChannel.Id.In(channels...),
	).Count(ctx)
	if err != nil {
		return err
	}
	if count != int64(len(channels)) {
		return echo.NewHTTPError(http.StatusBadRequest, "one or more channel ids do not exist in this cluster")
	}
	return nil
}

func newAlertRuleResponse(rule *model.AlertRule) alertRuleResponse {
	response := alertRuleResponse{
		ID: rule.Id, ClusterID: rule.ClusterId, Kind: rule.Kind,
		Enabled: rule.Enabled, Severity: string(rule.Severity),
		ParamsJSON: rule.ParamsJson, ForSeconds: rule.ForSeconds, CooldownSeconds: rule.CooldownSeconds,
		MutedUntil: rule.MutedUntil, Channels: []string{}, ParameterSpecs: []alertRuleParamSpecResponse{},
		CreatedAt: rule.CreatedAt, UpdatedAt: rule.UpdatedAt,
	}
	if spec := alerting.Spec(rule.Kind); spec != nil {
		response.Label = spec.Label
		response.Description = spec.Description
		keys := make([]string, 0, len(spec.Params))
		for key := range spec.Params {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			param := spec.Params[key]
			response.ParameterSpecs = append(response.ParameterSpecs, alertRuleParamSpecResponse{
				Key: key, Label: param.Label, Type: param.Type, Default: param.Default,
				Minimum: param.Minimum, Maximum: param.Maximum,
				ExclusiveMinimum: param.ExclusiveMinimum, Step: param.Step,
			})
		}
	}
	if len(rule.ChannelsJson) > 0 {
		_ = json.Unmarshal(rule.ChannelsJson, &response.Channels)
	}
	return response
}
