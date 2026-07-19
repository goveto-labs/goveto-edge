package analytics

import (
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"strings"
	"sync"
	"time"

	"goveto-edge/internal/edgecontrol"
	"goveto-edge/internal/edgeprotocol"
	"goveto-edge/internal/node"
	"goveto-edge/internal/storage/gen/client"
	"goveto-edge/internal/storage/gen/model"
	"goveto-edge/internal/storage/gen/query"
)

type Ingest struct {
	db      *client.Client
	cipher  *node.CredentialCipher
	store   *Store
	mu      sync.Mutex
	running map[string]struct{}
}

func NewIngest(db *client.Client, c *node.CredentialCipher, s *Store) *Ingest {
	return &Ingest{
		db:      db,
		cipher:  c,
		store:   s,
		running: map[string]struct{}{},
	}
}

func (i *Ingest) Run(ctx context.Context) {
	t := time.NewTicker(15 * time.Second)
	defer t.Stop()

	for {
		i.reconcile(ctx)

		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (i *Ingest) reconcile(ctx context.Context) {
	nodes, err := i.db.Node.Query().Where(query.Node.Status.Equals(model.NodeStatusONLINE)).Do(ctx)
	if err != nil {
		slog.Error("query analytics nodes", "error", err)
		return
	}

	for _, n := range nodes {
		i.mu.Lock()
		_, ok := i.running[n.Id]
		if !ok {
			i.running[n.Id] = struct{}{}
		}
		i.mu.Unlock()

		if !ok {
			go i.pull(ctx, n.Id, n.ClusterId)
		}
	}
}

func (i *Ingest) pull(ctx context.Context, nodeID, clusterID string) {
	defer func() {
		i.mu.Lock()
		delete(i.running, nodeID)
		i.mu.Unlock()
	}()

	a, err := i.db.NodeAddress.Query().
		Where(query.NodeAddress.NodeId.Equals(nodeID)).
		OrderBy(query.NodeAddress.CreatedAt.Asc()).
		First(ctx)
	if err != nil || a == nil {
		return
	}

	cr, err := i.db.NodeCredential.FindUnique(ctx, query.NodeCredential.NodeId.Equals(nodeID))
	if err != nil {
		return
	}

	key, err := i.cipher.Decrypt(cr.CommunicationKeyEncrypted)
	if err != nil {
		return
	}

	client := edgecontrol.New("http://"+net.JoinHostPort(a.Address, "80"), nodeID, key)
	err = client.PullLogs(ctx, func(ctx context.Context, records []edgeprotocol.LogRecord) error {
		return i.consume(ctx, clusterID, nodeID, records)
	})
	if err != nil && ctx.Err() == nil {
		slog.Error("pull node analytics", "cluster_id", clusterID, "node_id", nodeID, "error", err)
	}
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
		if r.Type == "node_runtime" {
			var m struct {
				Minute         time.Time `json:"minute"`
				CPU            float32   `json:"cpu_usage_percent"`
				MemoryUsed     uint64    `json:"memory_used_bytes"`
				MemoryTotal    uint64    `json:"memory_total_bytes"`
				Load1          float32   `json:"load_1"`
				Load5          float32   `json:"load_5"`
				Load15         float32   `json:"load_15"`
				Connections    uint64    `json:"connections"`
				CacheUsed      uint64    `json:"cache_used_bytes"`
				CacheDirectory string    `json:"cache_directory"`
				DiskUsed       uint64    `json:"disk_used_bytes"`
				DiskTotal      uint64    `json:"disk_total_bytes"`
			}
			if json.Unmarshal(r.Payload, &m) == nil {
				if err := i.store.InsertRuntime(ctx, NodeRuntimeMetric{
					Minute:         m.Minute,
					ClusterID:      clusterID,
					NodeID:         nodeID,
					CPU:            m.CPU,
					MemoryUsed:     m.MemoryUsed,
					MemoryTotal:    m.MemoryTotal,
					Load1:          m.Load1,
					Load5:          m.Load5,
					Load15:         m.Load15,
					Connections:    m.Connections,
					CacheUsed:      m.CacheUsed,
					CacheDirectory: m.CacheDirectory,
					DiskUsed:       m.DiskUsed,
					DiskTotal:      m.DiskTotal,
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
