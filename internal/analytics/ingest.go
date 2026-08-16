package analytics

import (
	"context"
	"encoding/json"
	"log/slog"
	"math"
	"net"
	"strconv"
	"strings"
	"time"

	"goveto-edge/internal/edgeprotocol"
	"goveto-edge/internal/node"
	"goveto-edge/internal/storage/gen/client"
	"goveto-edge/internal/storage/gen/model"
	"goveto-edge/internal/storage/gen/query"
	"goveto-edge/internal/telemetry"

	"github.com/google/uuid"
	"golang.org/x/net/http/httpguts"
)

const (
	originHealthMaxPastAge   = 31 * 24 * time.Hour
	originHealthMaxFutureAge = 5 * time.Minute
	originHealthMaxLatencyMS = float64((24 * time.Hour) / time.Millisecond)
	originHealthMaxAddress   = 512
)

// LogSink receives raw agent log records for best-effort fan-out to external
// systems (e.g. Kafka logpush). Implementations must never block the caller
// or fail the batch; drops are reported through their own metrics.
type LogSink interface {
	Enqueue(ctx context.Context, clusterID, nodeID string, records []edgeprotocol.LogRecord)
}

type Ingest struct {
	db         *client.Client
	store      *Store
	concurrent chan struct{}
	archive    LogArchive
	logpush    LogSink
	geoIP      *geoIPEnricher
}

func (i *Ingest) SetArchive(archive LogArchive) { i.archive = archive }

func (i *Ingest) SetLogpush(sink LogSink) { i.logpush = sink }

func (i *Ingest) ConfigureGeoIP(cityPath, asnPath string) {
	i.geoIP = newGeoIPEnricher(cityPath, asnPath)
}

func NewIngest(db *client.Client, _ *node.CredentialCipher, s *Store) *Ingest {
	return NewIngestWithConcurrency(db, s, 4)
}

func NewIngestWithConcurrency(db *client.Client, s *Store, concurrency int) *Ingest {
	if concurrency < 1 {
		concurrency = 1
	}
	return &Ingest{
		db: db, store: s, concurrent: make(chan struct{}, concurrency),
		geoIP: newGeoIPEnricher(""),
	}
}

func (i *Ingest) Consume(ctx context.Context, nodeID string, records []edgeprotocol.LogRecord) error {
	select {
	case i.concurrent <- struct{}{}:
		defer func() { <-i.concurrent }()
	case <-ctx.Done():
		return ctx.Err()
	}
	nodeRecord, err := i.db.Node.FindUnique(ctx, query.Node.Id.Equals(nodeID))
	if err != nil {
		return err
	}
	if nodeRecord == nil {
		return nil
	}
	return i.consume(ctx, nodeRecord.ClusterId, nodeID, records)
}

func (i *Ingest) consume(ctx context.Context, clusterID, nodeID string, records []edgeprotocol.LogRecord) (err error) {
	started := time.Now()
	defer func() {
		telemetry.IngestBatchDuration.Observe(time.Since(started).Seconds())
		result := "success"
		if err != nil {
			result = "error"
		}
		telemetry.IngestBatchesTotal.WithLabelValues(result).Inc()
	}()
	for _, r := range records {
		telemetry.IngestRecordsTotal.WithLabelValues(metricRecordType(r.Type)).Inc()
	}
	directSites, err := i.resolveSites(ctx, clusterID, records)
	if err != nil {
		return err
	}
	originMetrics, invalidOriginMetrics := decodeOriginHealthBatch(records, clusterID, nodeID, time.Now().UTC())
	authorizedOriginSites, err := i.resolveOriginHealthSites(ctx, clusterID, nodeID, originMetrics)
	if err != nil {
		return err
	}

	events := make([]WebRequestLog, 0, len(records))
	unauthorizedOriginMetrics := 0
	for _, r := range records {
		if r.Type == "origin_health" {
			continue
		}
		if r.Type == "node_runtime" {
			var m struct {
				Minute                 time.Time `json:"minute"`
				CPU                    float32   `json:"cpu_usage_percent"`
				MemoryUsed             uint64    `json:"memory_used_bytes"`
				MemoryTotal            uint64    `json:"memory_total_bytes"`
				Load1                  float32   `json:"load_1"`
				Load5                  float32   `json:"load_5"`
				Load15                 float32   `json:"load_15"`
				Connections            uint64    `json:"connections"`
				CacheUsed              uint64    `json:"cache_used_bytes"`
				CacheDirectory         string    `json:"cache_directory"`
				CacheEntries           uint64    `json:"cache_entries"`
				CacheHits              uint64    `json:"cache_hits"`
				CacheMisses            uint64    `json:"cache_misses"`
				CacheStaleHits         uint64    `json:"cache_stale_hits"`
				CacheEvictions         uint64    `json:"cache_evictions"`
				CacheRejectedWrites    uint64    `json:"cache_rejected_writes"`
				CacheStreamEncodeDrops uint64    `json:"cache_stream_encode_drops"`
				CacheCorruptions       uint64    `json:"cache_corruptions"`
				CacheHitRate           float32   `json:"cache_hit_rate"`
				CacheCapacityRatio     float32   `json:"cache_capacity_ratio"`
				CacheAlerts            []string  `json:"cache_alerts"`
				DiskUsed               uint64    `json:"disk_used_bytes"`
				DiskTotal              uint64    `json:"disk_total_bytes"`
			}
			if json.Unmarshal(r.Payload, &m) == nil {
				if err := i.store.InsertRuntime(ctx, NodeRuntimeMetric{
					Minute:                 m.Minute,
					ClusterID:              clusterID,
					NodeID:                 nodeID,
					CPU:                    m.CPU,
					MemoryUsed:             m.MemoryUsed,
					MemoryTotal:            m.MemoryTotal,
					Load1:                  m.Load1,
					Load5:                  m.Load5,
					Load15:                 m.Load15,
					Connections:            m.Connections,
					CacheUsed:              m.CacheUsed,
					CacheDirectory:         m.CacheDirectory,
					CacheEntries:           m.CacheEntries,
					CacheHits:              m.CacheHits,
					CacheMisses:            m.CacheMisses,
					CacheStaleHits:         m.CacheStaleHits,
					CacheEvictions:         m.CacheEvictions,
					CacheRejectedWrites:    m.CacheRejectedWrites,
					CacheStreamEncodeDrops: m.CacheStreamEncodeDrops,
					CacheCorruptions:       m.CacheCorruptions,
					CacheHitRate:           m.CacheHitRate,
					CacheCapacityRatio:     m.CacheCapacityRatio,
					CacheAlerts:            m.CacheAlerts,
					DiskUsed:               m.DiskUsed,
					DiskTotal:              m.DiskTotal,
				}); err != nil {
					return err
				}
			}
			continue
		}

		if r.Type != "access" && r.Type != "caddy" {
			continue
		}

		if r.SiteID == "" || !directSites[r.SiteID] {
			continue
		}

		event, e := ParseAccess(r.Payload, clusterID, nodeID, r.SiteID)
		if e == nil {
			if !accessLogHasTimestamp(r.Payload) && !r.CreatedAt.IsZero() {
				event.EventTime = r.CreatedAt.UTC()
			}
			event.SourceLogID = r.ID
			event.ConfigVersion = r.ConfigVersion
			events = append(events, event)
		}
	}
	for _, metric := range originMetrics {
		if !authorizedOriginSites[metric.SiteID] {
			unauthorizedOriginMetrics++
			continue
		}
		if err := i.store.InsertOriginHealth(ctx, metric); err != nil {
			return err
		}
	}
	if invalidOriginMetrics > 0 || unauthorizedOriginMetrics > 0 {
		slog.Warn("rejected origin health metrics",
			"cluster_id", clusterID,
			"node_id", nodeID,
			"invalid", invalidOriginMetrics,
			"unauthorized", unauthorizedOriginMetrics,
		)
	}

	if i.archive != nil {
		if err := i.archive.Write(ctx, clusterID, nodeID, records); err != nil {
			return err
		}
	}
	if i.logpush != nil {
		i.logpush.Enqueue(ctx, clusterID, nodeID, records)
	}
	i.geoIP.enrich(events)
	inserted, err := i.store.Insert(ctx, events)
	if err != nil {
		return err
	}
	i.store.publish(inserted)
	return nil
}

func metricRecordType(recordType string) string {
	switch recordType {
	case "access", "caddy", "node_runtime", "origin_health":
		return recordType
	default:
		return "unknown"
	}
}

func accessLogHasTimestamp(payload []byte) bool {
	var record struct {
		Timestamp float64 `json:"ts"`
	}
	return json.Unmarshal(payload, &record) == nil && record.Timestamp != 0
}

func (i *Ingest) resolveSites(
	ctx context.Context,
	clusterID string,
	records []edgeprotocol.LogRecord,
) (map[string]bool, error) {
	directIDs := map[string]struct{}{}
	for _, record := range records {
		if record.Type != "access" && record.Type != "caddy" {
			continue
		}
		if record.SiteID != "" {
			directIDs[record.SiteID] = struct{}{}
		}
	}

	direct := make(map[string]bool, len(directIDs))
	if len(directIDs) > 0 {
		ids := make([]string, 0, len(directIDs))
		for id := range directIDs {
			ids = append(ids, id)
		}
		sites, err := i.db.Site.Query().Where(
			query.Site.ClusterId.Equals(clusterID), query.Site.Id.In(ids...),
		).Do(ctx)
		if err != nil {
			return nil, err
		}
		for _, site := range sites {
			direct[site.Id] = true
		}
	}

	return direct, nil
}

func (i *Ingest) resolveOriginHealthSites(
	ctx context.Context,
	clusterID, nodeID string,
	metrics []OriginHealthMetric,
) (map[string]bool, error) {
	ids := originHealthSiteIDs(metrics)
	if len(ids) == 0 {
		return map[string]bool{}, nil
	}

	sites, err := i.db.Site.Query().Where(
		query.Site.ClusterId.Equals(clusterID),
		query.Site.Id.In(ids...),
	).Do(ctx)
	if err != nil {
		return nil, err
	}
	clusterSites := make(map[string]bool, len(sites))
	for _, site := range sites {
		clusterSites[site.Id] = true
	}

	versions, err := i.db.NodeSiteConfigVersion.Query().Where(
		query.NodeSiteConfigVersion.NodeId.Equals(nodeID),
		query.NodeSiteConfigVersion.SiteId.In(ids...),
		query.NodeSiteConfigVersion.Status.In(
			model.ConfigStatusPUBLISHED,
			model.ConfigStatusROLLED_BACK,
		),
	).Do(ctx)
	if err != nil {
		return nil, err
	}
	nodeSites := make(map[string]bool, len(versions))
	for _, version := range versions {
		nodeSites[version.SiteId] = true
	}
	return intersectOriginHealthSites(clusterSites, nodeSites), nil
}

func originHealthSiteIDs(metrics []OriginHealthMetric) []string {
	seen := make(map[string]struct{}, len(metrics))
	ids := make([]string, 0, len(metrics))
	for _, metric := range metrics {
		if _, ok := seen[metric.SiteID]; ok {
			continue
		}
		seen[metric.SiteID] = struct{}{}
		ids = append(ids, metric.SiteID)
	}
	return ids
}

func intersectOriginHealthSites(clusterSites, nodeSites map[string]bool) map[string]bool {
	result := make(map[string]bool, min(len(clusterSites), len(nodeSites)))
	for siteID := range clusterSites {
		if nodeSites[siteID] {
			result[siteID] = true
		}
	}
	return result
}

func decodeOriginHealthBatch(
	records []edgeprotocol.LogRecord,
	clusterID, nodeID string,
	now time.Time,
) ([]OriginHealthMetric, int) {
	metrics := make([]OriginHealthMetric, 0)
	rejected := 0
	for _, record := range records {
		if record.Type != "origin_health" {
			continue
		}
		metric, ok := decodeOriginHealth(record.Payload, clusterID, nodeID, now)
		if !ok {
			rejected++
			continue
		}
		metrics = append(metrics, metric)
	}
	return metrics, rejected
}

func decodeOriginHealth(payload []byte, clusterID, nodeID string, now time.Time) (OriginHealthMetric, bool) {
	var metric struct {
		Minute           time.Time `json:"minute"`
		SiteID           string    `json:"site_id"`
		OriginAddress    string    `json:"origin_address"`
		Healthy          bool      `json:"healthy"`
		Available        bool      `json:"available"`
		Fails            int       `json:"fails"`
		Requests         uint64    `json:"requests"`
		Errors           uint64    `json:"errors"`
		AverageLatencyMS float64   `json:"average_latency_ms"`
		ErrorRate        float64   `json:"error_rate"`
	}
	if json.Unmarshal(payload, &metric) != nil {
		return OriginHealthMetric{}, false
	}
	result := OriginHealthMetric{
		Minute: metric.Minute, ClusterID: clusterID, NodeID: nodeID, SiteID: metric.SiteID,
		OriginAddress: metric.OriginAddress, Healthy: metric.Healthy, Available: metric.Available,
		Fails: metric.Fails, Requests: metric.Requests, Errors: metric.Errors,
		AverageLatencyMS: metric.AverageLatencyMS, ErrorRate: metric.ErrorRate,
	}
	if !validOriginHealthMetric(&result, now) {
		return OriginHealthMetric{}, false
	}
	return result, true
}

func validOriginHealthMetric(metric *OriginHealthMetric, now time.Time) bool {
	metric.SiteID = strings.TrimSpace(metric.SiteID)
	metric.OriginAddress = strings.TrimSpace(metric.OriginAddress)
	metric.Minute = metric.Minute.UTC()
	siteID, err := uuid.Parse(metric.SiteID)
	if err != nil {
		return false
	}
	metric.SiteID = siteID.String()
	if metric.Minute.IsZero() || !metric.Minute.Equal(metric.Minute.Truncate(time.Minute)) ||
		metric.Minute.Before(now.Add(-originHealthMaxPastAge)) ||
		metric.Minute.After(now.Add(originHealthMaxFutureAge)) ||
		!validOriginHealthAddress(metric.OriginAddress) ||
		metric.Fails < 0 || int64(metric.Fails) > math.MaxInt32 ||
		metric.Requests > math.MaxInt64 || metric.Errors > metric.Requests ||
		math.IsNaN(metric.AverageLatencyMS) || math.IsInf(metric.AverageLatencyMS, 0) ||
		metric.AverageLatencyMS < 0 || metric.AverageLatencyMS > originHealthMaxLatencyMS ||
		math.IsNaN(metric.ErrorRate) || math.IsInf(metric.ErrorRate, 0) ||
		metric.ErrorRate < 0 || metric.ErrorRate > 1 {
		return false
	}
	if metric.Requests == 0 {
		if metric.AverageLatencyMS != 0 {
			return false
		}
		metric.ErrorRate = 0
		return true
	}
	metric.ErrorRate = float64(metric.Errors) / float64(metric.Requests)
	return true
}

func validOriginHealthAddress(address string) bool {
	if address == "" || len(address) > originHealthMaxAddress {
		return false
	}
	if network, dialAddress, found := strings.Cut(address, "/"); found {
		if network != "tcp4" && network != "tcp6" {
			return false
		}
		address = dialAddress
	}
	host, port, err := net.SplitHostPort(address)
	if err != nil || host == "" || len(host) > 253 || !httpguts.ValidHostHeader(host) {
		return false
	}
	portNumber, err := strconv.Atoi(port)
	return err == nil && portNumber > 0 && portNumber <= 65535
}
