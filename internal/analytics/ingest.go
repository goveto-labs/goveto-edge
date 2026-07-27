package analytics

import (
	"context"
	"encoding/json"
	"net"
	"strings"
	"time"

	"goveto-edge/internal/edgeprotocol"
	"goveto-edge/internal/node"
	"goveto-edge/internal/storage/gen/client"
	"goveto-edge/internal/storage/gen/query"
)

type Ingest struct {
	db    *client.Client
	store *Store
}

func NewIngest(db *client.Client, _ *node.CredentialCipher, s *Store) *Ingest {
	return &Ingest{db: db, store: s}
}

func (i *Ingest) Consume(ctx context.Context, nodeID string, records []edgeprotocol.LogRecord) error {
	nodeRecord, err := i.db.Node.FindUnique(ctx, query.Node.Id.Equals(nodeID))
	if err != nil {
		return err
	}
	if nodeRecord == nil {
		return nil
	}
	return i.consume(ctx, nodeRecord.ClusterId, nodeID, records)
}

func (i *Ingest) consume(ctx context.Context, clusterID, nodeID string, records []edgeprotocol.LogRecord) error {
	clusterSites, err := i.db.Site.Query().Where(query.Site.ClusterId.Equals(clusterID)).Do(ctx)
	if err != nil {
		return err
	}

	sites := map[string]string{}
	if len(clusterSites) > 0 {
		siteIDs := make([]string, 0, len(clusterSites))
		for _, site := range clusterSites {
			siteIDs = append(siteIDs, site.Id)
		}
		domains, err := i.db.SiteDomain.Query().Where(query.SiteDomain.SiteId.In(siteIDs...)).Do(ctx)
		if err != nil {
			return err
		}
		for _, d := range domains {
			sites[strings.ToLower(d.Hostname)] = d.SiteId
		}
	}

	events := make([]WebRequestLog, 0, len(records))
	for _, r := range records {
		if r.Type == "origin_health" {
			if metric, ok := decodeOriginHealth(r.Payload, clusterID, nodeID); ok {
				if err := i.store.InsertOriginHealth(ctx, metric); err != nil {
					return err
				}
			}
			continue
		}
		if r.Type == "node_runtime" {
			var m struct {
				Minute              time.Time `json:"minute"`
				CPU                 float32   `json:"cpu_usage_percent"`
				MemoryUsed          uint64    `json:"memory_used_bytes"`
				MemoryTotal         uint64    `json:"memory_total_bytes"`
				Load1               float32   `json:"load_1"`
				Load5               float32   `json:"load_5"`
				Load15              float32   `json:"load_15"`
				Connections         uint64    `json:"connections"`
				CacheUsed           uint64    `json:"cache_used_bytes"`
				CacheDirectory      string    `json:"cache_directory"`
				CacheEntries        uint64    `json:"cache_entries"`
				CacheHits           uint64    `json:"cache_hits"`
				CacheMisses         uint64    `json:"cache_misses"`
				CacheStaleHits      uint64    `json:"cache_stale_hits"`
				CacheEvictions      uint64    `json:"cache_evictions"`
				CacheRejectedWrites uint64    `json:"cache_rejected_writes"`
				CacheCorruptions    uint64    `json:"cache_corruptions"`
				CacheHitRate        float32   `json:"cache_hit_rate"`
				CacheCapacityRatio  float32   `json:"cache_capacity_ratio"`
				CacheAlerts         []string  `json:"cache_alerts"`
				DiskUsed            uint64    `json:"disk_used_bytes"`
				DiskTotal           uint64    `json:"disk_total_bytes"`
			}
			if json.Unmarshal(r.Payload, &m) == nil {
				if err := i.store.InsertRuntime(ctx, NodeRuntimeMetric{
					Minute:              m.Minute,
					ClusterID:           clusterID,
					NodeID:              nodeID,
					CPU:                 m.CPU,
					MemoryUsed:          m.MemoryUsed,
					MemoryTotal:         m.MemoryTotal,
					Load1:               m.Load1,
					Load5:               m.Load5,
					Load15:              m.Load15,
					Connections:         m.Connections,
					CacheUsed:           m.CacheUsed,
					CacheDirectory:      m.CacheDirectory,
					CacheEntries:        m.CacheEntries,
					CacheHits:           m.CacheHits,
					CacheMisses:         m.CacheMisses,
					CacheStaleHits:      m.CacheStaleHits,
					CacheEvictions:      m.CacheEvictions,
					CacheRejectedWrites: m.CacheRejectedWrites,
					CacheCorruptions:    m.CacheCorruptions,
					CacheHitRate:        m.CacheHitRate,
					CacheCapacityRatio:  m.CacheCapacityRatio,
					CacheAlerts:         m.CacheAlerts,
					DiskUsed:            m.DiskUsed,
					DiskTotal:           m.DiskTotal,
				}); err != nil {
					return err
				}
			}
			continue
		}

		if r.Type != "access" && r.Type != "caddy" {
			continue
		}

		var probe struct {
			Request struct {
				Host string `json:"host"`
			} `json:"request"`
		}
		if json.Unmarshal(r.Payload, &probe) != nil {
			continue
		}

		host := probe.Request.Host
		if h, _, e := net.SplitHostPort(host); e == nil {
			host = h
		}

		siteID := sites[strings.ToLower(host)]
		if siteID == "" {
			continue
		}

		event, e := ParseAccess(r.Payload, clusterID, nodeID, siteID)
		if e == nil {
			events = append(events, event)
		}
	}

	return i.store.Insert(ctx, events)
}

func decodeOriginHealth(payload []byte, clusterID, nodeID string) (OriginHealthMetric, bool) {
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
	if json.Unmarshal(payload, &metric) != nil || metric.SiteID == "" || metric.OriginAddress == "" || metric.Minute.IsZero() {
		return OriginHealthMetric{}, false
	}
	return OriginHealthMetric{
		Minute: metric.Minute, ClusterID: clusterID, NodeID: nodeID, SiteID: metric.SiteID,
		OriginAddress: metric.OriginAddress, Healthy: metric.Healthy, Available: metric.Available,
		Fails: metric.Fails, Requests: metric.Requests, Errors: metric.Errors,
		AverageLatencyMS: metric.AverageLatencyMS, ErrorRate: metric.ErrorRate,
	}, true
}
