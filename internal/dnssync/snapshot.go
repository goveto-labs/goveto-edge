package dnssync

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/google/uuid"

	"goveto-edge/internal/dnsprovider"
	"goveto-edge/internal/storage/gen/model"
	"goveto-edge/internal/storage/gen/query"
)

// snapshotKeep limits how many snapshots are retained per cluster.
const snapshotKeep = 20

// snapshotRecord is the persisted form of one published DNS record. The
// provider record id is stored so a rollback reuses provider records instead
// of recreating them.
type snapshotRecord struct {
	Hostname         string `json:"hostname"`
	Type             string `json:"type"`
	Value            string `json:"value"`
	Line             string `json:"line"`
	TTL              int    `json:"ttl"`
	Proxied          bool   `json:"proxied,omitempty"`
	ProviderRecordID string `json:"provider_record_id,omitempty"`
	DNSLineID        string `json:"dns_line_id,omitempty"`
	NodeID           string `json:"node_id,omitempty"`
}

// writeSnapshot persists the record set produced by a successful
// reconciliation as a rollback point, then prunes old snapshots. A payload
// identical to the latest snapshot is skipped so unchanged reconciliations do
// not pollute the rollback history with duplicate entries.
func (s *Service) writeSnapshot(ctx context.Context, clusterID string, desired []desiredRecord) error {
	rows, err := s.db.DNSManagedRecord.Query().
		Where(query.DNSManagedRecord.ClusterId.Equals(clusterID)).
		Do(ctx)
	if err != nil {
		return err
	}
	providerIDByKey := make(map[string]string, len(rows))
	for index := range rows {
		if rows[index].ProviderRecordId != nil {
			providerIDByKey[key(rows[index].Hostname, rows[index].Type, rows[index].Value, rows[index].DnsLineKey)] = *rows[index].ProviderRecordId
		}
	}
	items := make([]snapshotRecord, 0, len(desired))
	for _, item := range desired {
		lineKey := normalizeLineKey(item.Line)
		record := snapshotRecord{
			Hostname: item.Hostname,
			Type:     string(item.Type),
			Value:    item.Value,
			Line:     lineKey,
			TTL:      item.TTL,
			Proxied:  item.Proxied,
		}
		record.ProviderRecordID = providerIDByKey[key(item.Hostname, item.Type, item.Value, lineKey)]
		if item.DNSLineID != nil {
			record.DNSLineID = *item.DNSLineID
		}
		if item.NodeID != nil {
			record.NodeID = *item.NodeID
		}
		items = append(items, record)
	}
	normalizeSnapshotRecords(items)
	payload, err := json.Marshal(items)
	if err != nil {
		return err
	}
	latest, err := s.latestSnapshots(ctx, clusterID, 1)
	if err != nil {
		return err
	}
	if len(latest) > 0 {
		unchanged, compareErr := sameSnapshotRecords(latest[0].RecordsJson, items)
		if compareErr == nil && unchanged {
			return nil
		}
	}
	if _, err = s.db.DNSSyncSnapshot.Create().Set(
		query.DNSSyncSnapshot.Id.Set(uuid.NewString()),
		query.DNSSyncSnapshot.ClusterId.Set(clusterID),
		query.DNSSyncSnapshot.RecordsJson.Set(payload),
	).Do(ctx); err != nil {
		return err
	}
	return s.pruneSnapshots(ctx, clusterID)
}

// sameSnapshotRecords compares the decoded record sets instead of their JSON
// bytes. PostgreSQL JSONB rewrites whitespace and object key order on storage,
// and reconciliation does not guarantee an input order.
func sameSnapshotRecords(payload json.RawMessage, current []snapshotRecord) (bool, error) {
	var persisted []snapshotRecord
	if err := json.Unmarshal(payload, &persisted); err != nil {
		return false, err
	}
	current = slices.Clone(current)
	normalizeSnapshotRecords(persisted)
	normalizeSnapshotRecords(current)
	return slices.Equal(persisted, current), nil
}

// normalizeSnapshotRecords gives the complete persisted representation a
// stable order. Comparing every field also preserves duplicate records rather
// than accidentally treating the snapshots as sets.
func normalizeSnapshotRecords(records []snapshotRecord) {
	slices.SortFunc(records, compareSnapshotRecords)
}

func compareSnapshotRecords(left, right snapshotRecord) int {
	if result := cmp.Compare(left.Hostname, right.Hostname); result != 0 {
		return result
	}
	if result := cmp.Compare(left.Type, right.Type); result != 0 {
		return result
	}
	if result := cmp.Compare(left.Value, right.Value); result != 0 {
		return result
	}
	if result := cmp.Compare(left.Line, right.Line); result != 0 {
		return result
	}
	if result := cmp.Compare(left.TTL, right.TTL); result != 0 {
		return result
	}
	if left.Proxied != right.Proxied {
		if !left.Proxied {
			return -1
		}
		return 1
	}
	if result := cmp.Compare(left.ProviderRecordID, right.ProviderRecordID); result != 0 {
		return result
	}
	if result := cmp.Compare(left.DNSLineID, right.DNSLineID); result != 0 {
		return result
	}
	return cmp.Compare(left.NodeID, right.NodeID)
}

// latestSnapshots returns the newest snapshots of a cluster. The id
// tie-breaker keeps the order deterministic when rows share a created_at
// timestamp.
func (s *Service) latestSnapshots(ctx context.Context, clusterID string, take int) ([]model.DNSSyncSnapshot, error) {
	return s.db.DNSSyncSnapshot.Query().
		Where(query.DNSSyncSnapshot.ClusterId.Equals(clusterID)).
		OrderBy(query.DNSSyncSnapshot.CreatedAt.Desc()).
		OrderBy(query.DNSSyncSnapshot.Id.Desc()).
		Take(take).
		Do(ctx)
}

func (s *Service) pruneSnapshots(ctx context.Context, clusterID string) error {
	kept, err := s.latestSnapshots(ctx, clusterID, snapshotKeep)
	if err != nil {
		return err
	}
	if len(kept) < snapshotKeep {
		return nil
	}
	// Delete by id rather than by created_at so rows tied with the cutoff
	// timestamp are still pruned.
	keptIDs := make([]string, 0, len(kept))
	for _, snapshot := range kept {
		keptIDs = append(keptIDs, snapshot.Id)
	}
	_, err = s.db.DNSSyncSnapshot.Delete().
		Where(
			query.DNSSyncSnapshot.ClusterId.Equals(clusterID),
			query.DNSSyncSnapshot.Id.NotIn(keptIDs...),
		).
		DoMany(ctx)
	return err
}

// RollbackAvailable reports whether the cluster has an earlier snapshot to
// roll back to.
func (s *Service) RollbackAvailable(ctx context.Context, clusterID string) (bool, error) {
	snapshots, err := s.latestSnapshots(ctx, clusterID, 2)
	if err != nil {
		return false, err
	}
	return len(snapshots) >= 2, nil
}

// rollback restores the record set from the snapshot before the latest one,
// undoing the most recent published DNS change. The restored state is appended
// as a new snapshot so repeated rollbacks walk further back through history.
func (s *Service) rollback(ctx context.Context, clusterID string) error {
	cluster, err := s.db.Cluster.FindUnique(ctx, query.Cluster.Id.Equals(clusterID))
	if err != nil {
		return err
	}
	if cluster == nil {
		return fmt.Errorf("cluster %q not found", clusterID)
	}
	if cluster.PrimaryHostname == nil || *cluster.PrimaryHostname == "" {
		return errors.New("cluster primary hostname is not configured")
	}
	config, err := EndpointConfig(ctx, s.db, clusterID)
	if err != nil {
		return err
	}
	if config == nil {
		return errors.New("DNS provider is not configured")
	}
	// Rollback is a recovery tool and intentionally works while the provider
	// is disabled: enqueueing already accepts it in that state.
	provider, err := s.providerFor(config)
	if err != nil {
		return err
	}
	snapshots, err := s.latestSnapshots(ctx, clusterID, 2)
	if err != nil {
		return err
	}
	if len(snapshots) < 2 {
		return errors.New("no earlier DNS snapshot to roll back to")
	}
	var items []snapshotRecord
	if err = json.Unmarshal(snapshots[1].RecordsJson, &items); err != nil {
		return fmt.Errorf("decode DNS snapshot %s: %w", snapshots[1].Id, err)
	}
	desired := make([]desiredRecord, 0, len(items))
	for _, item := range items {
		record := desiredRecord{
			Record: dnsprovider.Record{
				ID:       item.ProviderRecordID,
				Hostname: item.Hostname,
				Type:     model.DNSRecordType(item.Type),
				Value:    item.Value,
				Line:     item.Line,
				TTL:      item.TTL,
				Proxied:  item.Proxied,
			},
		}
		if item.DNSLineID != "" {
			lineID := item.DNSLineID
			record.DNSLineID = &lineID
		}
		if item.NodeID != "" {
			nodeID := item.NodeID
			record.NodeID = &nodeID
		}
		desired = append(desired, record)
	}
	remote, err := provider.ListRecords(ctx, *cluster.PrimaryHostname)
	if err != nil {
		return err
	}
	deletedKeys, err := s.apply(ctx, clusterID, provider, desired, remote)
	if err != nil {
		return err
	}
	nodeRecords := make([]dnsprovider.Record, 0, len(desired))
	for _, record := range desired {
		nodeRecords = append(nodeRecords, record.Record)
	}
	if err = syncRemoteNodeRecords(ctx, provider, *cluster.PrimaryHostname, nodeRecords, remainingRemote(remote, deletedKeys)); err != nil {
		return err
	}
	return s.writeSnapshot(ctx, clusterID, desired)
}
