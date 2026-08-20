package alerting

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"

	"goveto-edge/internal/storage/gen/client"
	"goveto-edge/internal/storage/gen/model"
)

// Observation is one resource currently violating a rule.
type Observation struct {
	// Key uniquely identifies the resource within the rule and cluster, e.g.
	// the node ID. Combined with the kind it forms the dedup fingerprint.
	Key   string
	Title string
	// Detail carries low-cardinality display values for the console and the
	// notification body (names, counters, thresholds). Never include unbounded
	// user input such as raw request paths.
	Detail map[string]any
}

// Params are the effective tunable thresholds of one rule. Persisted rules
// store only overrides; DecodeParams merges them over the current spec defaults.
type Params map[string]any

func (p Params) Int(key string, fallback int) int {
	switch value := p[key].(type) {
	case float64:
		return int(value)
	case int:
		return value
	case json.Number:
		if parsed, err := value.Int64(); err == nil {
			return int(parsed)
		}
	}
	return fallback
}

func (p Params) Float(key string, fallback float64) float64 {
	switch value := p[key].(type) {
	case float64:
		return value
	case int:
		return float64(value)
	case json.Number:
		if parsed, err := value.Float64(); err == nil {
			return parsed
		}
	}
	return fallback
}

// Evaluator queries the state that backs one rule kind. It receives the
// transaction used by the evaluation tick so reads are consistent with the
// advisory lock, plus the optional analytics querier for metric-backed rules.
type Evaluator func(ctx context.Context, db *client.Client, clusterID string, params Params, analytics AnalyticsQuerier) ([]Observation, error)

// AnalyticsQuerier runs read-only queries against the analytics database.
// Query must return rows compatible with pgx.RowScanner iteration via
// ScanRows.
type AnalyticsQuerier interface {
	// QueryAnalytics executes sql and decodes each row with scan.
	QueryAnalytics(ctx context.Context, sql string, scan func(row AnalyticsRow) error, args ...any) error
}

// AnalyticsRow decodes one analytics row (values are positionally bound).
type AnalyticsRow interface {
	Scan(dest ...any) error
}

// RuleSpec describes one built-in rule kind.
type RuleSpec struct {
	Kind            string
	Label           string
	Description     string
	Severity        model.AlertSeverity
	ForSeconds      int
	CooldownSeconds int
	Params          map[string]ParamSpec
	Evaluate        Evaluator
}

// ParamSpec is the validation and presentation contract for a numeric rule
// parameter. Built-in evaluators currently expose only integer and number
// parameters, so the HTTP API and engine can share one source of truth.
type ParamSpec struct {
	Label            string
	Type             string
	Default          float64
	Minimum          float64
	Maximum          float64
	ExclusiveMinimum bool
	Step             float64
}

func integerParam(label string, defaultValue, minimum, maximum int) ParamSpec {
	return ParamSpec{
		Label: label, Type: "integer", Default: float64(defaultValue),
		Minimum: float64(minimum), Maximum: float64(maximum), Step: 1,
	}
}

func numberParam(label string, defaultValue, minimum, maximum, step float64, exclusiveMinimum bool) ParamSpec {
	return ParamSpec{
		Label: label, Type: "number", Default: defaultValue,
		Minimum: minimum, Maximum: maximum, ExclusiveMinimum: exclusiveMinimum, Step: step,
	}
}

// DefaultParams returns a fresh map containing the rule's normalized defaults.
func (s *RuleSpec) DefaultParams() Params {
	result := make(Params, len(s.Params))
	for key, spec := range s.Params {
		result[key] = spec.normalizedDefault()
	}
	return result
}

// DecodeParams merges valid persisted overrides over current defaults. Invalid
// legacy entries are ignored and returned as an error alongside usable params
// so evaluation can continue while surfacing the broken configuration.
func (s *RuleSpec) DecodeParams(raw json.RawMessage) (Params, error) {
	result := s.DefaultParams()
	overrides, err := s.decodeOverrides(raw)
	for key, value := range overrides {
		result[key] = value
	}
	return result, err
}

func (s *RuleSpec) decodeOverrides(raw json.RawMessage) (Params, error) {
	result := Params{}
	if len(raw) == 0 {
		return result, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var stored map[string]any
	if err := decoder.Decode(&stored); err != nil {
		return result, fmt.Errorf("decode params: %w", err)
	}
	var validationErrors []error
	for key, value := range stored {
		param, ok := s.Params[key]
		if !ok {
			validationErrors = append(validationErrors, fmt.Errorf("unknown parameter %s for rule %s", key, s.Kind))
			continue
		}
		// Historical JSON null has the same reset-to-default meaning as an API
		// update and therefore is not retained as an override.
		if value == nil {
			continue
		}
		normalized, err := param.Normalize(key, value)
		if err != nil {
			validationErrors = append(validationErrors, err)
			continue
		}
		if normalized == param.normalizedDefault() {
			continue
		}
		result[key] = normalized
	}
	return result, errors.Join(validationErrors...)
}

// MergeParams applies a strict partial API update over recoverable legacy
// overrides. Invalid existing entries are discarded, JSON null removes an
// override, and defaults are not persisted so future spec defaults propagate.
func (s *RuleSpec) MergeParams(current json.RawMessage, update map[string]any) (json.RawMessage, error) {
	merged, _ := s.decodeOverrides(current)
	for key, value := range update {
		param, ok := s.Params[key]
		if !ok {
			return nil, fmt.Errorf("unknown parameter %s for rule %s", key, s.Kind)
		}
		if value == nil {
			delete(merged, key)
			continue
		}
		normalized, normalizeErr := param.Normalize(key, value)
		if normalizeErr != nil {
			return nil, normalizeErr
		}
		if normalized == param.normalizedDefault() {
			delete(merged, key)
		} else {
			merged[key] = normalized
		}
	}
	return json.Marshal(merged)
}

func (p ParamSpec) normalizedDefault() any {
	if p.Type == "integer" {
		return int(p.Default)
	}
	return p.Default
}

// Normalize validates one decoded JSON value and returns a stable Go number.
func (p ParamSpec) Normalize(key string, value any) (any, error) {
	var number float64
	switch typed := value.(type) {
	case json.Number:
		parsed, err := typed.Float64()
		if err != nil {
			return nil, fmt.Errorf("parameter %s must be a %s", key, p.Type)
		}
		number = parsed
	case float64:
		number = typed
	case int:
		number = float64(typed)
	case int64:
		number = float64(typed)
	default:
		return nil, fmt.Errorf("parameter %s must be a %s", key, p.Type)
	}
	if math.IsNaN(number) || math.IsInf(number, 0) || (p.Type == "integer" && math.Trunc(number) != number) {
		return nil, fmt.Errorf("parameter %s must be a %s", key, p.Type)
	}
	belowMinimum := number < p.Minimum || (p.ExclusiveMinimum && number == p.Minimum)
	if belowMinimum || number > p.Maximum {
		operator := "between"
		if p.ExclusiveMinimum {
			operator = "greater than"
		}
		if p.ExclusiveMinimum {
			return nil, fmt.Errorf("parameter %s must be %s %g and at most %g", key, operator, p.Minimum, p.Maximum)
		}
		return nil, fmt.Errorf("parameter %s must be between %g and %g", key, p.Minimum, p.Maximum)
	}
	if p.Type == "integer" {
		return int(number), nil
	}
	return number, nil
}

// Built-in rule kinds. The registry is the closed set of evaluables; rules in
// the database reference kinds by string so new kinds can be added without a
// migration.
const (
	MinForSeconds      = 0
	MaxForSeconds      = 86400
	MinCooldownSeconds = 60
	MaxCooldownSeconds = 86400

	KindNodeOffline       = "node_offline"
	KindNodeRedisDown     = "node_redis_unavailable"
	KindAgentLogBacklog   = "agent_log_backlog"
	KindAgentLogsDropped  = "agent_logs_dropped"
	KindCertExpiring      = "cert_expiring"
	KindCertRenewFailed   = "cert_renewal_failed"
	KindPublishFailed     = "publish_failed"
	KindJobDeadLetter     = "job_dead_letter"
	KindOriginErrorRate   = "origin_error_rate"
	KindNodeResourceUsage = "node_resource_usage"
)

// ValidateRuleTimers enforces the public timer contract used by both the API
// and evaluator, including a non-zero reminder cooldown.
func ValidateRuleTimers(forSeconds, cooldownSeconds int) error {
	if forSeconds < MinForSeconds || forSeconds > MaxForSeconds {
		return fmt.Errorf("forSeconds must be between %d and %d", MinForSeconds, MaxForSeconds)
	}
	if cooldownSeconds < MinCooldownSeconds || cooldownSeconds > MaxCooldownSeconds {
		return fmt.Errorf("cooldownSeconds must be between %d and %d", MinCooldownSeconds, MaxCooldownSeconds)
	}
	return nil
}

var registry = map[string]*RuleSpec{
	KindNodeOffline: {
		Kind: KindNodeOffline, Label: "Node offline",
		Description:     "A node's management stream stopped producing heartbeats and the node was marked offline.",
		Severity:        model.AlertSeverityCRITICAL,
		ForSeconds:      0,
		CooldownSeconds: 1800,
		Params:          map[string]ParamSpec{},
		Evaluate:        evaluateNodeOffline,
	},
	KindNodeRedisDown: {
		Kind: KindNodeRedisDown, Label: "Node Redis unavailable",
		Description:     "An online node reports that its distributed state backend (Redis) is unreachable.",
		Severity:        model.AlertSeverityWARNING,
		ForSeconds:      60,
		CooldownSeconds: 1800,
		Params:          map[string]ParamSpec{},
		Evaluate:        evaluateNodeRedisDown,
	},
	KindAgentLogBacklog: {
		Kind: KindAgentLogBacklog, Label: "Agent log backlog",
		Description:     "An agent's queued access log records exceed the threshold, delaying analytics ingestion.",
		Severity:        model.AlertSeverityWARNING,
		ForSeconds:      300,
		CooldownSeconds: 1800,
		Params: map[string]ParamSpec{
			"records_threshold": integerParam("Records threshold", 100000, 1, math.MaxInt32),
		},
		Evaluate: evaluateAgentLogBacklog,
	},
	KindAgentLogsDropped: {
		Kind: KindAgentLogsDropped, Label: "Agent logs dropped",
		Description:     "An agent dropped queued access log records; analytics data for the node is incomplete.",
		Severity:        model.AlertSeverityWARNING,
		ForSeconds:      0,
		CooldownSeconds: 1800,
		Params: map[string]ParamSpec{
			"window_seconds": integerParam("Window seconds", 300, 1, 86400),
		},
		Evaluate: evaluateAgentLogsDropped,
	},
	KindCertExpiring: {
		Kind: KindCertExpiring, Label: "Certificate expiring",
		Description:     "A certificate entered its expiry or renewal window.",
		Severity:        model.AlertSeverityWARNING,
		ForSeconds:      0,
		CooldownSeconds: 21600,
		Params:          map[string]ParamSpec{},
		Evaluate:        evaluateCertExpiring,
	},
	KindCertRenewFailed: {
		Kind: KindCertRenewFailed, Label: "Certificate renewal failed",
		Description:     "A certificate renewal or revocation job failed terminally.",
		Severity:        model.AlertSeverityCRITICAL,
		ForSeconds:      0,
		CooldownSeconds: 3600,
		Params:          map[string]ParamSpec{},
		Evaluate:        evaluateCertRenewFailed,
	},
	KindPublishFailed: {
		Kind: KindPublishFailed, Label: "Publish failed",
		Description:     "A site publish job failed or entered the dead letter queue within the lookback window.",
		Severity:        model.AlertSeverityCRITICAL,
		ForSeconds:      0,
		CooldownSeconds: 3600,
		Params: map[string]ParamSpec{
			"hours": integerParam("Hours", 24, 1, 720),
		},
		Evaluate: evaluatePublishFailed,
	},
	KindJobDeadLetter: {
		Kind: KindJobDeadLetter, Label: "Job dead letter",
		Description:     "A background job exhausted its retries and entered the dead letter queue within the lookback window.",
		Severity:        model.AlertSeverityWARNING,
		ForSeconds:      0,
		CooldownSeconds: 3600,
		Params: map[string]ParamSpec{
			"hours": integerParam("Hours", 24, 1, 720),
		},
		Evaluate: evaluateJobDeadLetter,
	},
	KindOriginErrorRate: {
		Kind: KindOriginErrorRate, Label: "Origin error rate",
		Description:     "Origin error rate exceeded the threshold over the analytics window.",
		Severity:        model.AlertSeverityCRITICAL,
		ForSeconds:      120,
		CooldownSeconds: 1800,
		Params: map[string]ParamSpec{
			"window_minutes": integerParam("Window minutes", 10, 1, 1440),
			"min_requests":   integerParam("Minimum requests", 100, 1, math.MaxInt32),
			"threshold":      numberParam("Error rate threshold", 0.1, 0, 1, 0.01, true),
		},
		Evaluate: evaluateOriginErrorRate,
	},
	KindNodeResourceUsage: {
		Kind: KindNodeResourceUsage, Label: "Node resource usage",
		Description:     "Node CPU or memory usage averaged above the threshold over the analytics window.",
		Severity:        model.AlertSeverityWARNING,
		ForSeconds:      600,
		CooldownSeconds: 1800,
		Params: map[string]ParamSpec{
			"window_minutes":   integerParam("Window minutes", 10, 1, 1440),
			"cpu_threshold":    numberParam("CPU threshold", 90, 0, 100, 1, true),
			"memory_threshold": numberParam("Memory threshold", 92, 0, 100, 1, true),
		},
		Evaluate: evaluateNodeResourceUsage,
	},
}

// Spec returns the registry entry for a kind.
func Spec(kind string) *RuleSpec {
	return registry[kind]
}

// Kinds returns the sorted list of built-in rule kinds.
func Kinds() []string {
	kinds := make([]string, 0, len(registry))
	for kind := range registry {
		kinds = append(kinds, kind)
	}
	sort.Strings(kinds)
	return kinds
}

// Fingerprint builds the dedup key for one observation. Only instances that
// are not RESOLVED are matched against it.
func Fingerprint(kind, key string) string {
	return kind + "/" + key
}

func evaluateNodeOffline(ctx context.Context, db *client.Client, clusterID string, _ Params, _ AnalyticsQuerier) ([]Observation, error) {
	rows, err := client.Raw[struct {
		ID     string `db:"id"`
		Name   string `db:"name"`
		LastHB string `db:"last_heartbeat"`
	}](ctx, db, `SELECT id, name,
		CASE WHEN heartbeat_at IS NULL THEN '' ELSE FLOOR(EXTRACT(EPOCH FROM (NOW() - heartbeat_at)))::text || 's' END AS last_heartbeat
		FROM nodes WHERE cluster_id=$1 AND status='OFFLINE' ORDER BY name`, clusterID)
	if err != nil {
		return nil, err
	}
	observations := make([]Observation, 0, len(rows))
	for _, row := range rows {
		observations = append(observations, Observation{
			Key:   row.ID,
			Title: fmt.Sprintf("Node %s is offline", row.Name),
			Detail: map[string]any{
				"node_id": row.ID, "node_name": row.Name,
				"last_heartbeat_age": row.LastHB,
			},
		})
	}
	return observations, nil
}

func evaluateNodeRedisDown(ctx context.Context, db *client.Client, clusterID string, _ Params, _ AnalyticsQuerier) ([]Observation, error) {
	rows, err := client.Raw[struct {
		ID    string  `db:"id"`
		Name  string  `db:"name"`
		Error *string `db:"error_detail"`
	}](ctx, db, `SELECT id, name, redis_status_error AS error_detail FROM nodes
		WHERE cluster_id=$1 AND status='ONLINE' AND redis_available = false ORDER BY name`, clusterID)
	if err != nil {
		return nil, err
	}
	observations := make([]Observation, 0, len(rows))
	for _, row := range rows {
		observations = append(observations, Observation{
			Key:   row.ID,
			Title: fmt.Sprintf("Node %s cannot reach Redis", row.Name),
			Detail: map[string]any{
				"node_id": row.ID, "node_name": row.Name, "error": row.Error,
			},
		})
	}
	return observations, nil
}

func evaluateAgentLogBacklog(ctx context.Context, db *client.Client, clusterID string, params Params, _ AnalyticsQuerier) ([]Observation, error) {
	threshold := int64(params.Int("records_threshold", 100000))
	rows, err := client.Raw[struct {
		ID      string `db:"id"`
		Name    string `db:"name"`
		Records int64  `db:"queue_records"`
		Bytes   int64  `db:"queue_bytes"`
	}](ctx, db, `SELECT id, name, queue_records, queue_bytes FROM nodes
		WHERE cluster_id=$1 AND status='ONLINE' AND queue_records >= $2 ORDER BY queue_records DESC`, clusterID, threshold)
	if err != nil {
		return nil, err
	}
	observations := make([]Observation, 0, len(rows))
	for _, row := range rows {
		observations = append(observations, Observation{
			Key:   row.ID,
			Title: fmt.Sprintf("Node %s has a %d-record log backlog", row.Name, row.Records),
			Detail: map[string]any{
				"node_id": row.ID, "node_name": row.Name,
				"queue_records": row.Records, "queue_bytes": row.Bytes, "threshold": threshold,
			},
		})
	}
	return observations, nil
}

func evaluateAgentLogsDropped(ctx context.Context, db *client.Client, clusterID string, params Params, _ AnalyticsQuerier) ([]Observation, error) {
	window := params.Int("window_seconds", 300)
	rows, err := client.Raw[struct {
		ID      string `db:"id"`
		Name    string `db:"name"`
		Dropped int64  `db:"dropped_logs"`
		At      string `db:"dropped_at"`
	}](ctx, db, `SELECT id, name, dropped_logs,
		FLOOR(EXTRACT(EPOCH FROM (NOW() - dropped_logs_at)))::text || 's' AS dropped_at FROM nodes
		WHERE cluster_id=$1 AND dropped_logs > 0 AND dropped_logs_at IS NOT NULL
		AND dropped_logs_at > NOW() - ($2 * INTERVAL '1 second') ORDER BY name`, clusterID, window)
	if err != nil {
		return nil, err
	}
	observations := make([]Observation, 0, len(rows))
	for _, row := range rows {
		observations = append(observations, Observation{
			Key:   row.ID,
			Title: fmt.Sprintf("Node %s dropped %d queued log records", row.Name, row.Dropped),
			Detail: map[string]any{
				"node_id": row.ID, "node_name": row.Name,
				"dropped_logs": row.Dropped, "dropped_age": row.At, "window_seconds": window,
			},
		})
	}
	return observations, nil
}

func evaluateCertExpiring(ctx context.Context, db *client.Client, clusterID string, _ Params, _ AnalyticsQuerier) ([]Observation, error) {
	rows, err := client.Raw[struct {
		ID       string  `db:"id"`
		Name     string  `db:"name"`
		Status   string  `db:"status"`
		Expires  *string `db:"expires_at"`
		DaysLeft *string `db:"days_left"`
	}](ctx, db, `SELECT id, name, status::text AS status,
		COALESCE(to_char(expires_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'), '') AS expires_at,
		CASE WHEN expires_at IS NULL THEN NULL
		ELSE FLOOR(EXTRACT(EPOCH FROM (expires_at - NOW())) / 86400)::text END AS days_left
		FROM certificates WHERE cluster_id=$1 AND status IN ('EXPIRING','EXPIRED') ORDER BY expires_at NULLS LAST`, clusterID)
	if err != nil {
		return nil, err
	}
	observations := make([]Observation, 0, len(rows))
	for _, row := range rows {
		observations = append(observations, Observation{
			Key:   row.ID,
			Title: fmt.Sprintf("Certificate %s is %s", row.Name, strings.ToLower(row.Status)),
			Detail: map[string]any{
				"certificate_id": row.ID, "name": row.Name,
				"status": row.Status, "expires_at": row.Expires, "days_left": row.DaysLeft,
			},
		})
	}
	return observations, nil
}

func evaluateCertRenewFailed(ctx context.Context, db *client.Client, clusterID string, _ Params, _ AnalyticsQuerier) ([]Observation, error) {
	rows, err := client.Raw[struct {
		ID     string `db:"id"`
		Name   string `db:"name"`
		Status string `db:"status"`
		Error  string `db:"error_detail"`
	}](ctx, db, `SELECT id, name, status::text AS status,
		COALESCE(last_renewal_error, last_revocation_error, '') AS error_detail
		FROM certificates WHERE cluster_id=$1 AND status IN ('RENEWAL_FAILED','REVOCATION_FAILED') ORDER BY name`, clusterID)
	if err != nil {
		return nil, err
	}
	observations := make([]Observation, 0, len(rows))
	for _, row := range rows {
		failure := "renewal failed"
		if row.Status == string(model.CertificateStatusREVOCATION_FAILED) {
			failure = "revocation failed"
		}
		observations = append(observations, Observation{
			Key:   row.ID,
			Title: fmt.Sprintf("Certificate %s %s", row.Name, failure),
			Detail: map[string]any{
				"certificate_id": row.ID, "name": row.Name,
				"status": row.Status, "error": row.Error,
			},
		})
	}
	return observations, nil
}

func evaluatePublishFailed(ctx context.Context, db *client.Client, clusterID string, params Params, _ AnalyticsQuerier) ([]Observation, error) {
	hours := params.Int("hours", 24)
	rows, err := client.Raw[struct {
		SiteID string `db:"site_id"`
		Name   string `db:"name"`
		Count  int64  `db:"failures"`
		Status string `db:"latest_status"`
	}](ctx, db, `SELECT j.site_id, s.name, COUNT(*) AS failures,
			(SELECT p.status::text FROM publish_jobs p WHERE p.site_id = j.site_id
			AND p.status IN ('FAILED','DEAD_LETTER')
			AND p.updated_at > NOW() - ($2 * INTERVAL '1 hour')
			ORDER BY p.updated_at DESC LIMIT 1) AS latest_status
		FROM publish_jobs j JOIN sites s ON s.id = j.site_id
		WHERE s.cluster_id=$1 AND j.status IN ('FAILED','DEAD_LETTER')
		AND j.updated_at > NOW() - ($2 * INTERVAL '1 hour')
		GROUP BY j.site_id, s.name ORDER BY failures DESC`, clusterID, hours)
	if err != nil {
		return nil, err
	}
	observations := make([]Observation, 0, len(rows))
	for _, row := range rows {
		observations = append(observations, Observation{
			Key:   row.SiteID,
			Title: fmt.Sprintf("Publish for site %s failed (%s)", row.Name, row.Status),
			Detail: map[string]any{
				"site_id": row.SiteID, "site_name": row.Name,
				"failures": row.Count, "latest_status": row.Status, "hours": hours,
			},
		})
	}
	return observations, nil
}

const deadLetterSourceSQL = `SELECT 'PUBLISH' AS kind, j.id, j.site_id::text AS resource_id, s.name AS resource_name,
	j.updated_at FROM publish_jobs j JOIN sites s ON s.id=j.site_id WHERE s.cluster_id=$1 AND j.status='DEAD_LETTER'
	UNION ALL SELECT 'PURGE', j.id, j.site_id::text, s.name, j.updated_at
	FROM purge_jobs j JOIN sites s ON s.id=j.site_id WHERE s.cluster_id=$1 AND j.status='DEAD_LETTER'
	UNION ALL SELECT 'INSTALL', j.id, j.node_id::text, n.name, j.updated_at
	FROM install_jobs j JOIN nodes n ON n.id=j.node_id WHERE n.cluster_id=$1 AND j.status='DEAD_LETTER'
	UNION ALL SELECT 'DNS', j.id, COALESCE(j.site_id, j.cluster_id), COALESCE(s.name, c.name), j.updated_at
	FROM dns_sync_jobs j JOIN clusters c ON c.id=j.cluster_id LEFT JOIN sites s ON s.id=j.site_id
	WHERE j.cluster_id=$1 AND j.status='DEAD_LETTER'
	UNION ALL SELECT 'CERTIFICATE', j.id, j.certificate_id, c.name, j.updated_at
	FROM certificate_jobs j JOIN certificates c ON c.id=j.certificate_id WHERE c.cluster_id=$1 AND j.status='DEAD_LETTER'`

func evaluateJobDeadLetter(ctx context.Context, db *client.Client, clusterID string, params Params, _ AnalyticsQuerier) ([]Observation, error) {
	hours := params.Int("hours", 24)
	rows, err := client.Raw[struct {
		Kind         string `db:"kind"`
		ID           string `db:"id"`
		ResourceID   string `db:"resource_id"`
		ResourceName string `db:"resource_name"`
	}](ctx, db, `SELECT kind, id, resource_id, resource_name FROM (`+deadLetterSourceSQL+`) dead
		WHERE updated_at > NOW() - ($2 * INTERVAL '1 hour') ORDER BY kind, id`, clusterID, hours)
	if err != nil {
		return nil, err
	}
	observations := make([]Observation, 0, len(rows))
	for _, row := range rows {
		observations = append(observations, Observation{
			Key:   row.Kind + ":" + row.ID,
			Title: fmt.Sprintf("%s job for %s entered the dead letter queue", strings.ToLower(row.Kind), row.ResourceName),
			Detail: map[string]any{
				"job_kind": row.Kind, "job_id": row.ID,
				"resource_id": row.ResourceID, "resource_name": row.ResourceName, "hours": hours,
			},
		})
	}
	return observations, nil
}

func evaluateOriginErrorRate(ctx context.Context, db *client.Client, clusterID string, params Params, analytics AnalyticsQuerier) ([]Observation, error) {
	if analytics == nil {
		return nil, nil
	}
	window := params.Int("window_minutes", 10)
	minRequests := params.Int("min_requests", 100)
	threshold := params.Float("threshold", 0.1)
	type row struct {
		SiteID   string  `db:"site_id"`
		Origin   string  `db:"origin_address"`
		Errors   int64   `db:"errors"`
		Requests int64   `db:"requests"`
		Rate     float64 `db:"rate"`
	}
	var rows []row
	err := analytics.QueryAnalytics(ctx, `SELECT site_id, origin_address, SUM(errors) AS errors,
		SUM(requests) AS requests, SUM(errors)::float / NULLIF(SUM(requests), 0) AS rate
		FROM analytics.origin_health_metrics_minute
		WHERE cluster_id=$1 AND minute > NOW() - ($2 * INTERVAL '1 minute')
		GROUP BY site_id, origin_address
		HAVING SUM(requests) >= $3 AND SUM(errors)::float / NULLIF(SUM(requests), 0) >= $4`,
		func(r AnalyticsRow) error {
			var item row
			if err := r.Scan(&item.SiteID, &item.Origin, &item.Errors, &item.Requests, &item.Rate); err != nil {
				return err
			}
			rows = append(rows, item)
			return nil
		}, clusterID, window, minRequests, threshold)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(rows))
	for _, item := range rows {
		ids = append(ids, item.SiteID)
	}
	names := siteNames(ctx, db, ids)
	observations := make([]Observation, 0, len(rows))
	for _, item := range rows {
		name := names[item.SiteID]
		if name == "" {
			name = item.SiteID
		}
		observations = append(observations, Observation{
			Key:   item.SiteID + "/" + item.Origin,
			Title: fmt.Sprintf("Origin %s for site %s returned %.1f%% errors", item.Origin, name, item.Rate*100),
			Detail: map[string]any{
				"site_id": item.SiteID, "site_name": name, "origin": item.Origin,
				"errors": item.Errors, "requests": item.Requests,
				"rate": item.Rate, "threshold": threshold, "window_minutes": window,
			},
		})
	}
	return observations, nil
}

type nodeResourceUsageRow struct {
	NodeID string   `db:"node_id"`
	CPU    *float64 `db:"cpu"`
	Memory *float64 `db:"memory"`
}

func evaluateNodeResourceUsage(ctx context.Context, db *client.Client, clusterID string, params Params, analytics AnalyticsQuerier) ([]Observation, error) {
	if analytics == nil {
		return nil, nil
	}
	window := params.Int("window_minutes", 10)
	cpuThreshold := params.Float("cpu_threshold", 90)
	memoryThreshold := params.Float("memory_threshold", 92)
	var rows []nodeResourceUsageRow
	err := analytics.QueryAnalytics(ctx, `SELECT node_id, AVG(cpu_usage_percent) AS cpu,
		100.0 * AVG(memory_used_bytes) / NULLIF(AVG(memory_total_bytes), 0) AS memory
		FROM analytics.node_runtime_metrics_minute
		WHERE cluster_id=$1 AND minute > NOW() - ($2 * INTERVAL '1 minute')
		GROUP BY node_id
		HAVING AVG(cpu_usage_percent) >= $3
		OR 100.0 * AVG(memory_used_bytes) / NULLIF(AVG(memory_total_bytes), 0) >= $4`,
		func(r AnalyticsRow) error {
			var item nodeResourceUsageRow
			if err := r.Scan(&item.NodeID, &item.CPU, &item.Memory); err != nil {
				return err
			}
			rows = append(rows, item)
			return nil
		}, clusterID, window, cpuThreshold, memoryThreshold)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(rows))
	for _, item := range rows {
		ids = append(ids, item.NodeID)
	}
	names := nodeNames(ctx, db, ids)
	observations := make([]Observation, 0, len(rows))
	for _, item := range rows {
		name := names[item.NodeID]
		if name == "" {
			name = item.NodeID
		}
		observations = append(observations, nodeResourceUsageObservation(item, name, window, cpuThreshold, memoryThreshold))
	}
	return observations, nil
}

func nodeResourceUsageObservation(item nodeResourceUsageRow, name string, window int, cpuThreshold, memoryThreshold float64) Observation {
	exceeded := make([]string, 0, 2)
	if item.CPU != nil && *item.CPU >= cpuThreshold {
		exceeded = append(exceeded, fmt.Sprintf("CPU at %.0f%%", *item.CPU))
	}
	if item.Memory != nil && *item.Memory >= memoryThreshold {
		exceeded = append(exceeded, fmt.Sprintf("memory at %.0f%%", *item.Memory))
	}
	detail := map[string]any{
		"node_id": item.NodeID, "node_name": name,
		"cpu_threshold": cpuThreshold, "memory_threshold": memoryThreshold,
		"window_minutes": window,
	}
	if item.CPU != nil {
		detail["cpu_percent"] = *item.CPU
	}
	if item.Memory != nil {
		detail["memory_percent"] = *item.Memory
	}
	return Observation{
		Key:    item.NodeID,
		Title:  fmt.Sprintf("Node %s averaged %s above threshold", name, strings.Join(exceeded, " and ")),
		Detail: detail,
	}
}

func siteNames(ctx context.Context, db *client.Client, ids []string) map[string]string {
	return lookupNames(ctx, db, "SELECT id, name FROM sites WHERE id = ANY($1)", ids)
}

func nodeNames(ctx context.Context, db *client.Client, ids []string) map[string]string {
	return lookupNames(ctx, db, "SELECT id, name FROM nodes WHERE id = ANY($1)", ids)
}

func lookupNames(ctx context.Context, db *client.Client, sql string, ids []string) map[string]string {
	result := make(map[string]string, len(ids))
	if len(ids) == 0 {
		return result
	}
	rows, err := client.Raw[struct {
		ID   string `db:"id"`
		Name string `db:"name"`
	}](ctx, db, sql, ids)
	if err != nil {
		return result
	}
	for _, row := range rows {
		result[row.ID] = row.Name
	}
	return result
}

// DetailJSON encodes observation details for storage.
func DetailJSON(detail map[string]any) json.RawMessage {
	if len(detail) == 0 {
		return json.RawMessage("{}")
	}
	encoded, err := json.Marshal(detail)
	if err != nil {
		return json.RawMessage(`{"error":"detail encode failed"}`)
	}
	return encoded
}
