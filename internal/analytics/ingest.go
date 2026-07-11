package analytics

import (
	"context"
	"encoding/json"
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

	a, err := i.db.NodeAddress.Query().Where(
		query.NodeAddress.NodeId.Equals(nodeID),
		query.NodeAddress.Primary.Equals(true),
	).First(ctx)
	if err != nil {
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
	_ = client.PullLogs(ctx, func(ctx context.Context, records []edgeprotocol.LogRecord) error {
		return i.consume(ctx, clusterID, nodeID, records)
	})
}

func (i *Ingest) consume(ctx context.Context, clusterID, nodeID string, records []edgeprotocol.LogRecord) error {
	domains, err := i.db.SiteDomain.Query().Do(ctx)
	if err != nil {
		return err
	}

	sites := map[string]string{}
	for _, d := range domains {
		sites[strings.ToLower(d.Hostname)] = d.SiteId
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
				CacheUsed      uint64    `json:"cache_used_bytes"`
				CacheMax       uint64    `json:"cache_max_bytes"`
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
					CacheUsed:      m.CacheUsed,
					CacheMax:       m.CacheMax,
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
