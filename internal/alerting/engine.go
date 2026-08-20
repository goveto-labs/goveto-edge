package alerting

import (
	"bytes"
	"context"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"time"

	"github.com/google/uuid"

	"goveto-edge/internal/node"
	"goveto-edge/internal/notify"
	"goveto-edge/internal/storage/gen/client"
	"goveto-edge/internal/storage/gen/model"
	"goveto-edge/internal/storage/gen/query"
	"goveto-edge/internal/telemetry"
)

const (
	// DefaultInterval is the alert evaluation cadence.
	DefaultInterval = 30 * time.Second
	// defaultSeedInterval discovers clusters created after startup without
	// repeating the full rule-seeding fan-out on every evaluation tick.
	defaultSeedInterval = 5 * time.Minute
	// deliveryMaxAttempts bounds retries per notification delivery.
	deliveryMaxAttempts = 5
	// deliveryClaimWindow hides a claimed delivery from other replicas while
	// the attempt is in flight.
	deliveryClaimWindow = 3 * time.Minute
	// deliveryBatchSize bounds deliveries attempted per tick.
	deliveryBatchSize = 32
	// resolvedRetention and deliveryRetention bound history growth.
	resolvedRetention = 30 * 24 * time.Hour
	deliveryRetention = 7 * 24 * time.Hour
)

// SendFunc delivers one notification; replaced in tests.
type SendFunc func(ctx context.Context, rawURL string, message notify.Message) error

// Engine periodically evaluates the built-in alert rules, drives the alert
// state machine and flushes notification deliveries. Multiple control-plane
// replicas are safe: evaluation is serialized by an advisory lock and
// deliveries are claimed with FOR UPDATE SKIP LOCKED.
type Engine struct {
	db           *client.Client
	analytics    AnalyticsQuerier
	cipher       *node.CredentialCipher
	interval     time.Duration
	send         SendFunc
	now          func() time.Time
	lastSweep    time.Time
	analyticsOff bool
}

func New(db *client.Client, analytics AnalyticsQuerier, cipher *node.CredentialCipher, interval time.Duration) *Engine {
	if interval <= 0 {
		interval = DefaultInterval
	}
	return &Engine{
		db: db, analytics: analytics, cipher: cipher, interval: interval,
		send: notify.Send,
		now:  time.Now,
	}
}

// Run blocks until ctx is done, evaluating on every tick.
func (e *Engine) Run(ctx context.Context) {
	if err := e.SeedDefaultRules(ctx); err != nil && !errors.Is(err, context.Canceled) {
		slog.Warn("seed alert rules", "error", err)
	}
	evaluationTicker := time.NewTicker(e.interval)
	seedTicker := time.NewTicker(defaultSeedInterval)
	defer evaluationTicker.Stop()
	defer seedTicker.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		e.Tick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-evaluationTicker.C:
		case <-seedTicker.C:
			if err := e.SeedDefaultRules(ctx); err != nil && !errors.Is(err, context.Canceled) {
				slog.Warn("seed alert rules", "error", err)
			}
		}
	}
}

// Tick runs one evaluation and delivery cycle.
func (e *Engine) Tick(ctx context.Context) {
	if err := e.evaluate(ctx); err != nil && !errors.Is(err, context.Canceled) {
		slog.Warn("evaluate alert rules", "error", err)
	}
	if err := e.flushDeliveries(ctx); err != nil && !errors.Is(err, context.Canceled) {
		slog.Warn("flush alert deliveries", "error", err)
	}
	e.updateMetrics(ctx)
	e.sweepHistory(ctx)
}

// SeedDefaultRules creates the default rule set for every cluster. Existing
// rules (including operator-customized ones) are never overwritten.
func (e *Engine) SeedDefaultRules(ctx context.Context) error {
	clusters, err := e.db.Cluster.Query().Do(ctx)
	if err != nil {
		return err
	}
	now := e.now()
	for index := range clusters {
		cluster := &clusters[index]
		for _, kind := range Kinds() {
			spec := Spec(kind)
			if _, err = e.db.RawExec(ctx, `INSERT INTO alert_rules
				(id, cluster_id, kind, enabled, severity, params_json, for_seconds, cooldown_seconds, channels_json, created_at, updated_at)
				VALUES ($1, $2, $3, true, $4, $5, $6, $7, '[]'::jsonb, $8, $8)
				ON CONFLICT (cluster_id, kind) DO NOTHING`,
				uuid.NewString(), cluster.Id, kind, string(spec.Severity), `{}`,
				spec.ForSeconds, spec.CooldownSeconds, now); err != nil {
				return err
			}
		}
	}
	return nil
}

func (e *Engine) evaluate(ctx context.Context) error {
	conn, err := e.db.DB().Conn(ctx)
	if err != nil {
		return err
	}
	locked := false
	if err = conn.QueryRowContext(ctx,
		`SELECT pg_try_advisory_lock(hashtextextended($1, 0))`, "alerting:evaluate",
	).Scan(&locked); err != nil {
		_ = conn.Close()
		return err
	}
	if !locked {
		_ = conn.Close()
		return nil
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if _, unlockErr := conn.ExecContext(cleanupCtx,
			`SELECT pg_advisory_unlock(hashtextextended($1, 0))`, "alerting:evaluate"); unlockErr != nil {
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		}
		_ = conn.Close()
	}()

	rules, err := e.db.AlertRule.Query().
		Where(query.AlertRule.Enabled.Equals(true)).
		OrderBy(query.AlertRule.ClusterId.Asc()).
		Do(ctx)
	if err != nil {
		return err
	}
	channels, err := e.db.NotificationChannel.Query().
		Where(query.NotificationChannel.Enabled.Equals(true)).
		Do(ctx)
	if err != nil {
		return err
	}
	channelsByCluster := make(map[string][]model.NotificationChannel)
	for index := range channels {
		channelsByCluster[channels[index].ClusterId] = append(channelsByCluster[channels[index].ClusterId], channels[index])
	}
	var evalErrors []error
	for index := range rules {
		if ctx.Err() != nil {
			return errors.Join(append(evalErrors, ctx.Err())...)
		}
		ruleID := rules[index].Id
		outcome := "ok"
		err = e.db.Tx(ctx, func(tx *client.Client) error {
			rule, findErr := tx.AlertRule.FindFirst(ctx,
				query.AlertRule.Id.Equals(ruleID),
				query.AlertRule.Enabled.Equals(true),
			)
			if findErr != nil || rule == nil {
				return findErr
			}
			spec := Spec(rule.Kind)
			if spec == nil {
				return nil
			}
			params, paramErr := spec.DecodeParams(rule.ParamsJson)
			if paramErr != nil {
				outcome = "param_config_error"
				slog.Warn("invalid stored alert rule parameters; using defaults for invalid values",
					"rule_id", rule.Id, "cluster_id", rule.ClusterId, "error", paramErr)
			}
			compacted, _ := spec.MergeParams(rule.ParamsJson, nil)
			if !bytes.Equal(bytes.TrimSpace(rule.ParamsJson), compacted) {
				if _, compactErr := tx.AlertRule.Update().
					Where(query.AlertRule.Id.Equals(rule.Id)).
					Set(query.AlertRule.ParamsJson.Set(compacted)).Do(ctx); compactErr != nil {
					return compactErr
				}
			}
			selectedChannels, channelErr := channelsFor(rule, channelsByCluster)
			notificationsSuppressed := rule.MutedUntil != nil && rule.MutedUntil.After(e.now())
			if channelErr != nil {
				slog.Warn("invalid alert rule channel configuration; notifications disabled for this evaluation",
					"rule_id", rule.Id, "cluster_id", rule.ClusterId, "error", channelErr)
				if outcome == "ok" {
					outcome = "channel_config_error"
				}
				selectedChannels = nil
				notificationsSuppressed = true
			}
			return e.evaluateRule(ctx, tx, rule, spec, params, selectedChannels, notificationsSuppressed)
		})
		if err != nil {
			evalErrors = append(evalErrors, fmt.Errorf("rule %s/%s: %w", rules[index].ClusterId, rules[index].Kind, err))
			telemetry.AlertEvaluationsTotal.WithLabelValues(rules[index].Kind, "error").Inc()
			continue
		}
		telemetry.AlertEvaluationsTotal.WithLabelValues(rules[index].Kind, outcome).Inc()
	}
	return errors.Join(evalErrors...)
}

func channelsFor(rule *model.AlertRule, byCluster map[string][]model.NotificationChannel) ([]model.NotificationChannel, error) {
	cluster := byCluster[rule.ClusterId]
	var selected []string
	if len(rule.ChannelsJson) > 0 {
		if err := json.Unmarshal(rule.ChannelsJson, &selected); err != nil {
			return nil, fmt.Errorf("decode channels: %w", err)
		}
	}
	if len(selected) == 0 {
		return cluster, nil
	}
	wanted := make(map[string]struct{}, len(selected))
	for _, id := range selected {
		wanted[id] = struct{}{}
	}
	filtered := make([]model.NotificationChannel, 0, len(cluster))
	for _, channel := range cluster {
		if _, ok := wanted[channel.Id]; ok {
			filtered = append(filtered, channel)
		}
	}
	return filtered, nil
}

func (e *Engine) evaluateRule(
	ctx context.Context,
	tx *client.Client,
	rule *model.AlertRule,
	spec *RuleSpec,
	params Params,
	channels []model.NotificationChannel,
	notificationsSuppressed bool,
) error {
	if err := ValidateRuleTimers(rule.ForSeconds, rule.CooldownSeconds); err != nil {
		return err
	}
	analytics := e.analytics
	if analytics == nil && requiresAnalytics(spec) {
		if !e.analyticsOff {
			e.analyticsOff = true
			slog.Info("analytics database is not configured; metric-backed alert rules are disabled",
				"kinds", KindOriginErrorRate+","+KindNodeResourceUsage)
		}
		return nil
	}
	observations, err := spec.Evaluate(ctx, tx, rule.ClusterId, params, analytics)
	if err != nil {
		return err
	}
	now := e.now()
	candidates, err := client.Raw[model.AlertInstance](ctx, tx, `SELECT * FROM alert_instances
		WHERE rule_id=$1 AND (status <> 'RESOLVED' OR suppressed_until_clear=true)
		ORDER BY created_at FOR UPDATE`, rule.Id)
	if err != nil {
		return err
	}
	active := make([]model.AlertInstance, 0, len(candidates))
	suppressed := make([]model.AlertInstance, 0, len(candidates))
	for index := range candidates {
		if candidates[index].Status == model.AlertStatusRESOLVED && candidates[index].SuppressedUntilClear {
			suppressed = append(suppressed, candidates[index])
			continue
		}
		if candidates[index].Status != model.AlertStatusRESOLVED {
			active = append(active, candidates[index])
		}
	}
	byFingerprint := make(map[string]*model.AlertInstance, len(active))
	for index := range active {
		byFingerprint[active[index].Fingerprint] = &active[index]
	}
	suppressedByFingerprint := make(map[string]*model.AlertInstance, len(suppressed))
	for index := range suppressed {
		suppressedByFingerprint[suppressed[index].Fingerprint] = &suppressed[index]
	}
	seen := make(map[string]struct{}, len(observations))
	for _, observation := range observations {
		fingerprint := Fingerprint(rule.Kind, observation.Key)
		seen[fingerprint] = struct{}{}
		instance := byFingerprint[fingerprint]
		if instance == nil {
			if suppressedInstance := suppressedByFingerprint[fingerprint]; suppressedInstance != nil {
				if _, err = tx.AlertInstance.Update().
					Where(query.AlertInstance.Id.Equals(suppressedInstance.Id)).
					Set(
						query.AlertInstance.LastSeenAt.Set(now),
						query.AlertInstance.Title.Set(observation.Title),
						query.AlertInstance.DetailJson.Set(DetailJSON(observation.Detail)),
						query.AlertInstance.Severity.Set(rule.Severity),
					).Do(ctx); err != nil {
					return err
				}
				continue
			}
		}
		if instance == nil {
			instance, err = tx.AlertInstance.Create().Set(
				query.AlertInstance.Id.Set(uuid.NewString()),
				query.AlertInstance.RuleId.Set(rule.Id),
				query.AlertInstance.ClusterId.Set(rule.ClusterId),
				query.AlertInstance.Kind.Set(rule.Kind),
				query.AlertInstance.Fingerprint.Set(fingerprint),
				query.AlertInstance.Status.Set(model.AlertStatusPENDING),
				query.AlertInstance.Severity.Set(rule.Severity),
				query.AlertInstance.Title.Set(observation.Title),
				query.AlertInstance.DetailJson.Set(DetailJSON(observation.Detail)),
				query.AlertInstance.FirstSeenAt.Set(now),
				query.AlertInstance.LastSeenAt.Set(now),
			).Do(ctx)
			if err != nil {
				return err
			}
		} else if _, err = tx.AlertInstance.Update().
			Where(query.AlertInstance.Id.Equals(instance.Id)).
			Set(
				query.AlertInstance.LastSeenAt.Set(now),
				query.AlertInstance.Title.Set(observation.Title),
				query.AlertInstance.DetailJson.Set(DetailJSON(observation.Detail)),
				query.AlertInstance.Severity.Set(rule.Severity),
			).Do(ctx); err != nil {
			return err
		}
		decision := decideObserved(
			instance.Status, instance.FirstSeenAt, instance.LastNotifiedAt,
			time.Duration(rule.ForSeconds)*time.Second, time.Duration(rule.CooldownSeconds)*time.Second, now,
		)
		if decision.Changed {
			if _, err = tx.AlertInstance.Update().
				Where(query.AlertInstance.Id.Equals(instance.Id)).
				Set(
					query.AlertInstance.Status.Set(decision.Next),
					query.AlertInstance.FiredAt.Set(now),
				).Do(ctx); err != nil {
				return err
			}
			previous := instance.Status
			instance.Status = decision.Next
			recordTransition(string(previous), string(decision.Next))
			if _, err = recordEvent(ctx, tx, instance.Id, model.AlertEventTypeSTATE_CHANGE,
				string(previous), string(decision.Next), nil); err != nil {
				return err
			}
		}
		if decision.Notify && !notificationsSuppressed {
			if err := e.enqueueNotification(ctx, tx, instance, channels, "firing"); err != nil {
				return err
			}
		}
	}
	for index := range active {
		instance := &active[index]
		if _, ok := seen[instance.Fingerprint]; ok {
			continue
		}
		next, recovered := decideMissing(instance.Status)
		if next == instance.Status {
			continue
		}
		reason := "condition cleared"
		if _, err = tx.AlertInstance.Update().
			Where(query.AlertInstance.Id.Equals(instance.Id)).
			Set(
				query.AlertInstance.Status.Set(next),
				query.AlertInstance.ResolvedAt.Set(now),
				query.AlertInstance.ResolvedReason.Set(reason),
			).Do(ctx); err != nil {
			return err
		}
		recordTransition(string(instance.Status), string(next))
		if _, err = recordEvent(ctx, tx, instance.Id, model.AlertEventTypeSTATE_CHANGE,
			string(instance.Status), string(next), map[string]any{"reason": reason}); err != nil {
			return err
		}
		if recovered && !notificationsSuppressed {
			if err := e.enqueueNotification(ctx, tx, instance, channels, "recovery"); err != nil {
				return err
			}
		}
	}
	for index := range suppressed {
		instance := &suppressed[index]
		if _, ok := seen[instance.Fingerprint]; ok {
			continue
		}
		if _, err = tx.AlertInstance.Update().
			Where(query.AlertInstance.Id.Equals(instance.Id)).
			Set(query.AlertInstance.SuppressedUntilClear.Set(false)).Do(ctx); err != nil {
			return err
		}
		if _, err = recordEvent(ctx, tx, instance.Id, model.AlertEventTypeREARM,
			string(model.AlertStatusRESOLVED), string(model.AlertStatusRESOLVED),
			map[string]any{"reason": "condition cleared"}); err != nil {
			return err
		}
	}
	return nil
}

// enqueueNotification records a notify event, one delivery row per channel and
// the instance's notification bookkeeping. When the rule has no channels the
// wave is still marked notified so reminders respect the cooldown; the alert
// remains visible in the console.
func (e *Engine) enqueueNotification(
	ctx context.Context,
	tx *client.Client,
	instance *model.AlertInstance,
	channels []model.NotificationChannel,
	phase string,
) error {
	now := e.now()
	var eventID *string
	if len(channels) > 0 {
		payload := map[string]any{"phase": phase, "channels": len(channels)}
		if event, err := recordEvent(ctx, tx, instance.Id, model.AlertEventTypeNOTIFY, string(instance.Status), string(instance.Status), payload); err != nil {
			return err
		} else if event != nil {
			eventID = &event.Id
		}
		for index := range channels {
			sets := []query.AlertDeliverySetClause{
				query.AlertDelivery.InstanceId.Set(instance.Id),
				query.AlertDelivery.ChannelId.Set(channels[index].Id),
				query.AlertDelivery.Kind.Set(phase),
				query.AlertDelivery.Status.Set(model.AlertDeliveryStatusPENDING),
				query.AlertDelivery.Attempts.Set(0),
				query.AlertDelivery.NextRetryAt.SetNull(),
			}
			if eventID != nil {
				sets = append(sets, query.AlertDelivery.EventId.Set(*eventID))
			}
			if _, err := tx.AlertDelivery.Create().Set(sets...).Do(ctx); err != nil {
				return err
			}
		}
	}
	_, err := tx.AlertInstance.Update().
		Where(query.AlertInstance.Id.Equals(instance.Id)).
		Set(
			query.AlertInstance.LastNotifiedAt.Set(now),
			query.AlertInstance.NotifyCount.Set(instance.NotifyCount+len(channels)),
		).Do(ctx)
	return err
}

func recordEvent(
	ctx context.Context,
	tx *client.Client,
	instanceID string,
	eventType model.AlertEventType,
	from, to string,
	payload map[string]any,
) (*model.AlertEvent, error) {
	sets := []query.AlertEventSetClause{
		query.AlertEvent.InstanceId.Set(instanceID),
		query.AlertEvent.Type.Set(eventType),
	}
	if from != "" {
		sets = append(sets, query.AlertEvent.FromStatus.Set(from))
	}
	if to != "" {
		sets = append(sets, query.AlertEvent.ToStatus.Set(to))
	}
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		sets = append(sets, query.AlertEvent.PayloadJson.Set(encoded))
	}
	return tx.AlertEvent.Create().Set(sets...).Do(ctx)
}

func recordTransition(from, to string) {
	telemetry.AlertStateTransitionsTotal.WithLabelValues(statusLabel(from), statusLabel(to)).Inc()
}

func statusLabel(value string) string {
	if value == "" {
		return "new"
	}
	return strings.ToLower(value)
}

func requiresAnalytics(spec *RuleSpec) bool {
	return spec.Kind == KindOriginErrorRate || spec.Kind == KindNodeResourceUsage
}

func (e *Engine) flushDeliveries(ctx context.Context) error {
	type claimed struct {
		ID       string `db:"id"`
		Attempts int    `db:"attempts"`
	}
	rows, err := client.Raw[claimed](ctx, e.db, `WITH claimable AS (
		SELECT id FROM alert_deliveries
		WHERE status='PENDING' AND (next_retry_at IS NULL OR next_retry_at <= NOW())
		ORDER BY created_at LIMIT $1 FOR UPDATE SKIP LOCKED)
	UPDATE alert_deliveries d SET
		next_retry_at = NOW() + ($2 * INTERVAL '1 minute'), updated_at = NOW()
	FROM claimable c WHERE d.id = c.id RETURNING d.id, d.attempts`, deliveryBatchSize, int(deliveryClaimWindow.Minutes()))
	if err != nil {
		return err
	}
	for _, item := range rows {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		e.deliverOne(ctx, item.ID, item.Attempts+1)
	}
	return nil
}

type deliveryRow struct {
	DeliveryID   string          `db:"delivery_id"`
	NotifyKind   string          `db:"notify_kind"`
	Attempts     int             `db:"attempts"`
	ClusterID    string          `db:"cluster_id"`
	ClusterName  string          `db:"cluster_name"`
	ChannelID    string          `db:"channel_id"`
	ChannelName  string          `db:"channel_name"`
	Service      string          `db:"service"`
	URLEncrypted string          `db:"url_encrypted"`
	InstanceID   string          `db:"instance_id"`
	AlertKind    string          `db:"alert_kind"`
	Title        string          `db:"title"`
	Severity     string          `db:"severity"`
	Status       string          `db:"status"`
	DetailJSON   json.RawMessage `db:"detail_json"`
	FirstSeenAt  time.Time       `db:"first_seen_at"`
}

func (e *Engine) deliverOne(ctx context.Context, deliveryID string, attempts int) {
	rows, err := client.Raw[deliveryRow](ctx, e.db, `SELECT d.id AS delivery_id, d.kind AS notify_kind, d.attempts,
		c.id AS cluster_id, c.name AS cluster_name, ch.id AS channel_id, ch.name AS channel_name, ch.service,
		ch.url_encrypted, i.id AS instance_id, i.kind AS alert_kind, i.title, i.severity::text AS severity,
		i.status::text AS status, i.detail_json, i.first_seen_at
		FROM alert_deliveries d
			JOIN notification_channels ch ON ch.id = d.channel_id AND ch.enabled=true
		JOIN alert_instances i ON i.id = d.instance_id
		JOIN clusters c ON c.id = i.cluster_id
		WHERE d.id = $1`, deliveryID)
	if err != nil {
		if ctx.Err() == nil {
			e.finishDelivery(ctx, deliveryID, attempts, false, err, false)
		}
		return
	}
	if len(rows) != 1 {
		e.finishDelivery(ctx, deliveryID, attempts, false, errors.New("notification channel is unavailable"), true)
		return
	}
	row := rows[0]
	if e.cipher == nil {
		e.finishDelivery(ctx, deliveryID, attempts, false, errors.New("notification cipher is unavailable"), false)
		return
	}
	rawURL, err := e.cipher.DecryptScoped(notify.ChannelScope(row.ClusterID, row.ChannelID), row.URLEncrypted)
	if err != nil {
		e.finishDelivery(ctx, deliveryID, attempts, false, fmt.Errorf("decrypt channel URL: %w", err), false)
		return
	}
	sendCtx, cancel := context.WithTimeout(ctx, notify.SendTimeout+5*time.Second)
	defer cancel()
	if err = e.send(sendCtx, rawURL, renderMessage(row)); err != nil {
		telemetry.AlertNotificationsTotal.WithLabelValues(row.AlertKind, row.Service, "failed").Inc()
		if errors.Is(err, notify.ErrSendCongestion) {
			return
		}
		if ctx.Err() == nil {
			e.finishDelivery(ctx, deliveryID, attempts, false, err, false)
		}
		return
	}
	telemetry.AlertNotificationsTotal.WithLabelValues(row.AlertKind, row.Service, "sent").Inc()
	e.finishDelivery(ctx, deliveryID, attempts, true, nil, false)
}

func (e *Engine) finishDelivery(ctx context.Context, deliveryID string, attempts int, sent bool, cause error, permanent bool) {
	if sent {
		_, err := e.db.RawExec(ctx, `UPDATE alert_deliveries SET status='SENT', sent_at=NOW(),
			attempts=$2, next_retry_at=NULL, last_error=NULL, updated_at=NOW() WHERE id=$1`, deliveryID, attempts)
		if err != nil {
			slog.Warn("mark alert delivery sent", "delivery_id", deliveryID, "error", err)
		}
		return
	}
	status := "PENDING"
	nextRetry := time.Now().Add(backoff(attempts))
	if permanent || attempts >= deliveryMaxAttempts {
		status = "FAILED"
		nextRetry = time.Time{}
	}
	var retryArg any
	if !nextRetry.IsZero() {
		retryArg = nextRetry
	}
	var message string
	if cause != nil {
		message = notify.SafeErrorMessage(cause)
	}
	_, err := e.db.RawExec(ctx, `UPDATE alert_deliveries SET status=$2, next_retry_at=$3, last_error=$4,
		attempts=$5, updated_at=NOW() WHERE id=$1`, deliveryID, status, retryArg, message, attempts)
	if err != nil {
		slog.Warn("record alert delivery failure", "delivery_id", deliveryID, "error", err)
	}
}

// backoff returns the retry delay after a failed attempt. Attempts one through
// four retry after 2, 4, 8 and 16 minutes; attempt five becomes terminal.
func backoff(attempts int) time.Duration {
	if attempts < 1 {
		attempts = 1
	}
	delay := time.Duration(math.Pow(2, float64(attempts))) * time.Minute
	return delay
}

func renderMessage(row deliveryRow) notify.Message {
	phase := "firing"
	resolved := false
	if row.NotifyKind == "recovery" || row.Status == "RESOLVED" {
		phase = "resolved"
		resolved = true
	}
	var body strings.Builder
	fmt.Fprintf(&body, "Goveto alert %s\n", phase)
	fmt.Fprintf(&body, "Cluster: %s\n", row.ClusterName)
	fmt.Fprintf(&body, "Alert: %s\n", row.Title)
	fmt.Fprintf(&body, "Rule: %s\n", row.AlertKind)
	fmt.Fprintf(&body, "Severity: %s\n", strings.ToLower(row.Severity))
	fmt.Fprintf(&body, "Status: %s\n", row.Status)
	fmt.Fprintf(&body, "First seen: %s\n", row.FirstSeenAt.UTC().Format(time.RFC3339))
	if detail := formatDetail(row.DetailJSON); detail != "" {
		body.WriteString("Detail:\n")
		body.WriteString(detail)
	}
	titleVerb := "firing"
	if resolved {
		titleVerb = "resolved"
	}
	return notify.Message{
		Title: fmt.Sprintf("Goveto [%s] %s: %s", strings.ToLower(row.Severity), titleVerb, row.Title),
		Body:  body.String(),
	}
}

func formatDetail(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var detail map[string]any
	if err := json.Unmarshal(raw, &detail); err != nil || len(detail) == 0 {
		return ""
	}
	keys := make([]string, 0, len(detail))
	for key := range detail {
		keys = append(keys, key)
	}
	sortStrings(keys)
	var builder strings.Builder
	for _, key := range keys {
		if value := detail[key]; value != nil && fmt.Sprint(value) != "" {
			fmt.Fprintf(&builder, "  %s: %v\n", key, value)
		}
	}
	return builder.String()
}

func sortStrings(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}

func (e *Engine) updateMetrics(ctx context.Context) {
	rows, err := client.Raw[struct {
		Status string `db:"status"`
		Total  int64  `db:"total"`
	}](ctx, e.db, `SELECT status::text AS status, COUNT(*) AS total FROM alert_instances GROUP BY status`)
	if err != nil {
		return
	}
	telemetry.AlertInstances.Reset()
	for _, row := range rows {
		telemetry.AlertInstances.WithLabelValues(strings.ToLower(row.Status)).Set(float64(row.Total))
	}
}

func (e *Engine) sweepHistory(ctx context.Context) {
	now := e.now()
	if !e.lastSweep.IsZero() && now.Sub(e.lastSweep) < time.Hour {
		return
	}
	e.lastSweep = now
	sweepCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if _, err := e.db.RawExec(sweepCtx,
		`DELETE FROM alert_instances WHERE status='RESOLVED' AND suppressed_until_clear=false
		AND resolved_at < NOW() - ($1 * INTERVAL '1 day')`,
		int(resolvedRetention.Hours()/24)); err != nil {
		slog.Warn("sweep resolved alert instances", "error", err)
	}
	if _, err := e.db.RawExec(sweepCtx,
		`DELETE FROM alert_deliveries WHERE status IN ('SENT','FAILED') AND updated_at < NOW() - ($1 * INTERVAL '1 day')`,
		int(deliveryRetention.Hours()/24)); err != nil {
		slog.Warn("sweep alert deliveries", "error", err)
	}
}
