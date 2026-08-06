// Package dnssync reconciles desired CDN DNS records with external providers.
package dnssync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"goveto-edge/internal/dnsprovider"
	"goveto-edge/internal/jobqueue"
	"goveto-edge/internal/node"
	"goveto-edge/internal/storage/gen/client"
	"goveto-edge/internal/storage/gen/model"
	"goveto-edge/internal/storage/gen/query"
)

const (
	jobRetention = 30 * 24 * time.Hour

	NodeDNSOfflineGracePeriod = 5 * time.Minute
)

var ErrDNSNotConfigured = errors.New("DNS provider is not configured")

// EndpointConfig returns the cluster primary-hostname DNS provider configuration.
func EndpointConfig(ctx context.Context, db *client.Client, clusterID string) (*model.DNSProviderConfig, error) {
	return db.DNSProviderConfig.FindFirst(ctx,
		query.DNSProviderConfig.ClusterId.Equals(clusterID),
		query.DNSProviderConfig.Kind.Equals(model.DNSProviderKindENDPOINT),
	)
}

type Service struct {
	db         *client.Client
	cipher     *node.CredentialCipher
	httpClient *http.Client
	jobs       *jobqueue.Manager
}

func New(db *client.Client, cipher *node.CredentialCipher) *Service {
	return &Service{db: db, cipher: cipher, httpClient: &http.Client{Timeout: 20 * time.Second}, jobs: jobqueue.New(db)}
}

// LockClusterTx serializes configuration changes and reconciliation for a cluster.
// The lock is released automatically when the supplied transaction ends.
func LockClusterTx(ctx context.Context, db *client.Client, clusterID string) error {
	type lockRow struct {
		Locked bool `db:"locked"`
	}
	_, err := client.Raw[lockRow](
		ctx,
		db,
		"SELECT pg_advisory_xact_lock(hashtextextended($1, 0)), TRUE AS locked",
		clusterID,
	)
	return err
}

func (s *Service) Enqueue(
	ctx context.Context,
	clusterID string,
	siteID *string,
	action model.DNSSyncAction,
) (*model.DNSSyncJob, error) {
	var job *model.DNSSyncJob
	err := s.db.Tx(ctx, func(tx *client.Client) error {
		var err error
		job, err = s.EnqueueTx(ctx, tx, clusterID, siteID, action)
		return err
	})
	return job, err
}

// EnqueueTx creates at most one pending follow-up job for a cluster. A new
// pending job is allowed while another job is running so changes that happen
// during reconciliation cannot be lost.
func (s *Service) EnqueueTx(
	ctx context.Context,
	db *client.Client,
	clusterID string,
	siteID *string,
	action model.DNSSyncAction,
) (*model.DNSSyncJob, error) {
	if err := LockClusterTx(ctx, db, clusterID); err != nil {
		return nil, err
	}
	active, err := db.DNSSyncJob.Query().
		Where(
			query.DNSSyncJob.ClusterId.Equals(clusterID),
			query.DNSSyncJob.Status.Equals(model.JobStatusPENDING),
		).
		OrderBy(query.DNSSyncJob.CreatedAt.Asc()).
		First(ctx)
	if err != nil {
		return nil, err
	}
	if active != nil {
		return active, nil
	}

	now := time.Now()
	sets := []query.DNSSyncJobSetClause{
		query.DNSSyncJob.ClusterId.Set(clusterID),
		query.DNSSyncJob.Action.Set(action),
		query.DNSSyncJob.Status.Set(model.JobStatusPENDING),
		query.DNSSyncJob.UpdatedAt.Set(now),
	}
	if siteID != nil {
		sets = append(sets, query.DNSSyncJob.SiteId.Set(*siteID))
	}
	return db.DNSSyncJob.Create().Set(sets...).Do(ctx)
}

// CancelActiveTx cancels obsolete pending jobs while holding the cluster lock.
func (s *Service) CancelActiveTx(ctx context.Context, db *client.Client, clusterID, reason string) error {
	if err := LockClusterTx(ctx, db, clusterID); err != nil {
		return err
	}
	payload, _ := json.Marshal(map[string]string{"error": reason})
	_, err := db.RawExec(ctx, `WITH cancelled AS (
		UPDATE dns_sync_jobs SET status='CANCELLED', cancel_requested_at=NOW(), error=$2,
			result_json=$3, lease_owner=NULL, lease_until=NULL, heartbeat_at=NULL, updated_at=NOW()
			WHERE cluster_id=$1 AND status IN ('PENDING','RUNNING') RETURNING id, attempts
	) UPDATE job_executions e SET status='CANCELLED', finished_at=NOW(), heartbeat_at=NOW(),
		error=COALESCE(e.error,$2) FROM cancelled c WHERE e.job_type=$4 AND e.job_id=c.id
		AND e.attempt=c.attempts AND e.status='RUNNING'`, clusterID, reason, payload, string(jobqueue.DNS))
	return err
}

// EnqueueNodeIPIfChanged compares the desired node A/AAAA records with the
// provider before creating a job. Equal sets, including two empty sets, are a
// no-op.
func (s *Service) EnqueueNodeIPIfChanged(
	ctx context.Context,
	clusterID string,
) (*model.DNSSyncJob, error) {
	cluster, err := s.db.Cluster.FindUnique(ctx, query.Cluster.Id.Equals(clusterID))
	if err != nil {
		return nil, err
	}
	if cluster == nil || cluster.PrimaryHostname == nil || *cluster.PrimaryHostname == "" {
		return nil, nil
	}
	config, err := EndpointConfig(ctx, s.db, clusterID)
	if err != nil {
		return nil, err
	}
	if config == nil || !config.Enabled {
		return nil, nil
	}
	// Older versions managed Site domain CNAMEs in the cluster provider. Force
	// one full reconciliation so those records are removed from both the
	// provider and the local managed-record table.
	managed, err := s.db.DNSManagedRecord.Query().
		Where(query.DNSManagedRecord.ClusterId.Equals(clusterID)).
		Do(ctx)
	if err != nil {
		return nil, err
	}
	for _, record := range managed {
		if record.SiteDomainId != nil {
			return s.Enqueue(ctx, clusterID, nil, model.DNSSyncActionUPSERT_CLUSTER)
		}
	}
	plain, err := s.cipher.Decrypt(config.CredentialsEncrypted)
	if err != nil {
		return nil, fmt.Errorf("decrypt DNS credentials: %w", err)
	}
	provider, err := dnsprovider.New(
		config.Provider,
		config.Zone,
		value(config.ZoneId),
		[]byte(plain),
		s.httpClient,
	)
	if err != nil {
		return nil, err
	}
	desired, err := s.desiredNodeRecords(ctx, cluster, config, provider.SupportsLines())
	if err != nil {
		return nil, err
	}
	remote, err := provider.ListRecords(ctx, *cluster.PrimaryHostname)
	if err != nil {
		return nil, err
	}
	if sameRecordSet(desired, remote) {
		return nil, nil
	}
	return s.Enqueue(ctx, clusterID, nil, model.DNSSyncActionUPSERT_CLUSTER)
}

// DeleteConfiguration synchronously removes records managed by this service and
// then deletes the local provider configuration. It intentionally does not
// create a synchronization job.
func (s *Service) DeleteConfiguration(ctx context.Context, clusterID string) error {
	unlock, err := s.lockCluster(ctx, clusterID)
	if err != nil {
		return err
	}
	defer unlock()

	config, err := EndpointConfig(ctx, s.db, clusterID)
	if err != nil {
		return err
	}
	if config == nil {
		return ErrDNSNotConfigured
	}
	plain, err := s.cipher.Decrypt(config.CredentialsEncrypted)
	if err != nil {
		return fmt.Errorf("decrypt DNS credentials: %w", err)
	}
	provider, err := dnsprovider.New(
		config.Provider,
		config.Zone,
		value(config.ZoneId),
		[]byte(plain),
		s.httpClient,
	)
	if err != nil {
		return err
	}
	records, err := s.db.DNSManagedRecord.Query().
		Where(query.DNSManagedRecord.ClusterId.Equals(clusterID)).
		Do(ctx)
	if err != nil {
		return err
	}
	for index := range records {
		item := &records[index]
		if err := provider.Delete(ctx, dnsprovider.Record{
			ID:       value(item.ProviderRecordId),
			Hostname: item.Hostname,
			Type:     item.Type,
			Value:    item.Value,
			Line:     item.DnsLineKey,
		}); err != nil {
			return fmt.Errorf("delete DNS record %s: %w", item.Hostname, err)
		}
		if _, err := s.db.DNSManagedRecord.Delete().
			Where(query.DNSManagedRecord.Id.Equals(item.Id)).
			Do(ctx); err != nil {
			return err
		}
	}

	return s.db.Tx(ctx, func(tx *client.Client) error {
		now := time.Now()
		payload, _ := json.Marshal(map[string]string{"error": "DNS configuration deleted"})
		if _, err := tx.DNSSyncJob.Update().
			Where(
				query.DNSSyncJob.ClusterId.Equals(clusterID),
				query.DNSSyncJob.Status.In(model.JobStatusPENDING, model.JobStatusRUNNING),
			).
			Set(
				query.DNSSyncJob.Status.Set(model.JobStatusCANCELLED),
				query.DNSSyncJob.LeaseUntil.SetNull(),
				query.DNSSyncJob.ResultJson.Set(payload),
				query.DNSSyncJob.UpdatedAt.Set(now),
			).
			DoMany(ctx); err != nil {
			return err
		}
		if _, err := tx.RawExec(
			ctx,
			`DELETE FROM node_dns_lines
			 WHERE dns_line_id IN (SELECT id FROM dns_lines WHERE cluster_id=$1)`,
			clusterID,
		); err != nil {
			return err
		}
		if _, err := tx.DNSLine.Delete().
			Where(query.DNSLine.ClusterId.Equals(clusterID)).
			DoMany(ctx); err != nil {
			return err
		}
		// Keep ACME zones; only remove the cluster primary endpoint provider.
		if _, err := tx.DNSProviderConfig.Delete().
			Where(
				query.DNSProviderConfig.ClusterId.Equals(clusterID),
				query.DNSProviderConfig.Kind.Equals(model.DNSProviderKindENDPOINT),
			).
			DoMany(ctx); err != nil {
			return err
		}
		_, err = tx.Cluster.Update().
			Where(query.Cluster.Id.Equals(clusterID)).
			Set(
				query.Cluster.PrimaryHostname.SetNull(),
				query.Cluster.UpdatedAt.Set(now),
			).
			Do(ctx)
		return err
	})
}

func (s *Service) lockCluster(ctx context.Context, clusterID string) (func(), error) {
	conn, err := s.db.DB().Conn(ctx)
	if err != nil {
		return nil, err
	}
	var locked bool
	if err := conn.QueryRowContext(
		ctx,
		"SELECT pg_advisory_lock(hashtextextended($1, 0)) IS NULL",
		clusterID,
	).Scan(&locked); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return func() {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		var released bool
		_ = conn.QueryRowContext(
			cleanupCtx,
			"SELECT pg_advisory_unlock(hashtextextended($1, 0))",
			clusterID,
		).Scan(&released)
		_ = conn.Close()
	}, nil
}

func (s *Service) Run(ctx context.Context) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	reconcile := time.NewTicker(5 * time.Minute)
	defer reconcile.Stop()

	s.enqueuePeriodic(ctx)
	s.runOne(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.runOne(ctx)
		case <-reconcile.C:
			s.enqueuePeriodic(ctx)
		}
	}
}

func (s *Service) enqueuePeriodic(ctx context.Context) {
	_, _ = s.db.RawExec(
		ctx,
		"DELETE FROM dns_sync_jobs WHERE status IN ('SUCCEEDED', 'FAILED', 'DEAD_LETTER', 'CANCELLED') AND updated_at < NOW() - ($1 * INTERVAL '1 second')",
		int64(jobRetention/time.Second),
	)
	configs, err := s.db.DNSProviderConfig.Query().
		Where(
			query.DNSProviderConfig.Enabled.Equals(true),
			query.DNSProviderConfig.Kind.Equals(model.DNSProviderKindENDPOINT),
		).
		Do(ctx)
	if err != nil {
		return
	}
	for _, config := range configs {
		_, _ = s.EnqueueNodeIPIfChanged(ctx, config.ClusterId)
	}
}

func (s *Service) runOne(ctx context.Context) {
	_, err := s.jobs.RunOne(ctx, jobqueue.DNS, 0, func(runCtx context.Context, lease jobqueue.Lease) jobqueue.Outcome {
		job, loadErr := s.db.DNSSyncJob.FindUnique(runCtx, query.DNSSyncJob.Id.Equals(lease.ID))
		if loadErr != nil || job == nil {
			if loadErr == nil {
				loadErr = errors.New("DNS sync job not found")
			}
			return jobqueue.Outcome{Err: loadErr, Retryable: true}
		}
		unlock, locked, lockErr := s.tryClusterLock(runCtx, job.ClusterId)
		if lockErr != nil {
			return jobqueue.Outcome{Err: lockErr, Retryable: true}
		}
		if !locked {
			return jobqueue.Outcome{
				Err:          errors.New("DNS cluster lock is busy"),
				RequeueAfter: 2 * time.Second,
			}
		}
		defer unlock()
		var executionErr error
		switch job.Action {
		case model.DNSSyncActionDELETE_CLUSTER:
			executionErr = s.deleteAll(runCtx, job.ClusterId)
		default:
			executionErr = s.reconcile(runCtx, job.ClusterId)
		}
		return jobqueue.Outcome{Result: map[string]any{"cluster_id": job.ClusterId, "action": job.Action}, Err: executionErr, Retryable: executionErr != nil}
	})
	if err != nil && !errors.Is(err, context.Canceled) {
		slog.Warn("DNS job worker", "error", err)
	}
}

func (s *Service) tryClusterLock(
	ctx context.Context,
	clusterID string,
) (unlock func(), locked bool, err error) {
	conn, err := s.db.DB().Conn(ctx)
	if err != nil {
		return nil, false, err
	}
	if err = conn.QueryRowContext(
		ctx,
		"SELECT pg_try_advisory_lock(hashtextextended($1, 0))",
		clusterID,
	).Scan(&locked); err != nil {
		_ = conn.Close()
		return nil, false, err
	}
	if !locked {
		_ = conn.Close()
		return func() {}, false, nil
	}
	return func() {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		var released bool
		_ = conn.QueryRowContext(
			cleanupCtx,
			"SELECT pg_advisory_unlock(hashtextextended($1, 0))",
			clusterID,
		).Scan(&released)
		_ = conn.Close()
	}, true, nil
}

type desiredRecord struct {
	dnsprovider.Record
	DNSLineID, NodeID *string
}

func (s *Service) desiredNodeRecords(
	ctx context.Context,
	cluster *model.Cluster,
	config *model.DNSProviderConfig,
	supportsLines bool,
) ([]dnsprovider.Record, error) {
	result := make([]dnsprovider.Record, 0)
	seen := map[string]bool{}
	nodes, err := s.dnsEligibleNodes(ctx, cluster.Id, time.Now())
	if err != nil {
		return nil, err
	}
	for _, currentNode := range nodes {
		addresses, err := s.db.NodeAddress.Query().
			Where(query.NodeAddress.NodeId.Equals(currentNode.Id)).
			OrderBy(query.NodeAddress.CreatedAt.Asc()).
			Do(ctx)
		if err != nil {
			return nil, err
		}
		lineCodes := []string{"default"}
		if supportsLines {
			links, err := s.db.NodeDNSLine.Query().
				Where(query.NodeDNSLine.NodeId.Equals(currentNode.Id)).
				Do(ctx)
			if err != nil {
				return nil, err
			}
			if len(links) > 0 {
				lineCodes = make([]string, 0, len(links))
				for _, link := range links {
					line, err := s.db.DNSLine.FindUnique(
						ctx,
						query.DNSLine.Id.Equals(link.DnsLineId),
					)
					if err != nil {
						return nil, err
					}
					if line == nil {
						return nil, fmt.Errorf("DNS line %q not found", link.DnsLineId)
					}
					lineCodes = append(lineCodes, normalizeLineKey(line.ProviderCode))
				}
			}
		}
		for _, address := range addresses {
			if net.ParseIP(address.Address) == nil {
				continue
			}
			recordType := model.DNSRecordTypeA
			if strings.Contains(address.Address, ":") {
				recordType = model.DNSRecordTypeAAAA
			}
			for _, lineCode := range lineCodes {
				record := dnsprovider.Record{
					Hostname: *cluster.PrimaryHostname,
					Type:     recordType,
					Value:    address.Address,
					Line:     lineCode,
					TTL:      config.DefaultTtl,
					Proxied:  config.Proxied,
				}
				key := nodeRecordKey(record)
				if !seen[key] {
					seen[key] = true
					result = append(result, record)
				}
			}
		}
	}
	return result, nil
}

func (s *Service) dnsEligibleNodes(ctx context.Context, clusterID string, now time.Time) ([]model.Node, error) {
	nodes, err := s.db.Node.Query().
		Where(
			query.Node.ClusterId.Equals(clusterID),
			query.Node.Status.In(model.NodeStatusONLINE, model.NodeStatusOFFLINE),
		).
		Do(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]model.Node, 0, len(nodes))
	for _, currentNode := range nodes {
		if !nodeEligibleForDNS(currentNode, now) {
			continue
		}
		if currentNode.Status == model.NodeStatusOFFLINE {
			credential, err := s.db.NodeCredential.FindUnique(
				ctx,
				query.NodeCredential.NodeId.Equals(currentNode.Id),
			)
			if err != nil {
				return nil, err
			}
			if credential == nil || credential.RevokedAt != nil {
				continue
			}
		}
		result = append(result, currentNode)
	}
	return result, nil
}

// Keep a recently disconnected node in DNS while the Agent has a chance to
// reconnect. Administrative disable and credential revocation bypass this path.
func nodeEligibleForDNS(currentNode model.Node, now time.Time) bool {
	if currentNode.Status == model.NodeStatusONLINE {
		return true
	}
	return currentNode.Status == model.NodeStatusOFFLINE &&
		currentNode.HeartbeatAt != nil &&
		currentNode.UpdatedAt.After(now.Add(-NodeDNSOfflineGracePeriod))
}

func sameRecordSet(desired, remote []dnsprovider.Record) bool {
	desiredSet := map[string]bool{}
	for _, record := range desired {
		desiredSet[nodeRecordKey(record)] = true
	}
	remoteCounts := map[string]int{}
	for _, record := range remote {
		remoteCounts[nodeRecordKey(record)]++
	}
	if len(desiredSet) != len(remoteCounts) {
		return false
	}
	for key := range desiredSet {
		if remoteCounts[key] != 1 {
			return false
		}
	}
	return true
}

func syncRemoteNodeRecords(
	ctx context.Context,
	provider dnsprovider.Provider,
	hostname string,
	desired []dnsprovider.Record,
) error {
	remote, err := provider.ListRecords(ctx, hostname)
	if err != nil {
		return err
	}
	desiredSet := map[string]bool{}
	for _, record := range desired {
		desiredSet[nodeRecordKey(record)] = true
	}
	kept := map[string]bool{}
	var errs []error
	for _, record := range remote {
		key := nodeRecordKey(record)
		if desiredSet[key] && !kept[key] {
			kept[key] = true
			continue
		}
		if err := provider.Delete(ctx, record); err != nil {
			errs = append(errs, fmt.Errorf("delete stale node DNS record %s: %w", record.Value, err))
		}
	}
	return errors.Join(errs...)
}

func nodeRecordKey(record dnsprovider.Record) string {
	value := strings.TrimSpace(record.Value)
	if ip := net.ParseIP(value); ip != nil {
		value = ip.String()
	}
	return strings.Join([]string{
		string(record.Type),
		value,
		normalizeLineKey(record.Line),
	}, "\x00")
}

func (s *Service) reconcile(ctx context.Context, clusterID string) error {
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
	if !config.Enabled {
		return errors.New("DNS provider is disabled")
	}
	plain, err := s.cipher.Decrypt(config.CredentialsEncrypted)
	if err != nil {
		return fmt.Errorf("decrypt DNS credentials: %w", err)
	}
	provider, err := dnsprovider.New(
		config.Provider,
		config.Zone,
		value(config.ZoneId),
		[]byte(plain),
		s.httpClient,
	)
	if err != nil {
		return err
	}
	desired, err := s.desired(ctx, cluster, config, provider.SupportsLines())
	if err != nil {
		return err
	}
	if err := s.apply(ctx, clusterID, provider, desired); err != nil {
		return err
	}
	nodeRecords := make([]dnsprovider.Record, 0)
	for _, record := range desired {
		if record.NodeID != nil {
			nodeRecords = append(nodeRecords, record.Record)
		}
	}
	return syncRemoteNodeRecords(ctx, provider, *cluster.PrimaryHostname, nodeRecords)
}

func (s *Service) deleteAll(ctx context.Context, clusterID string) error {
	config, err := EndpointConfig(ctx, s.db, clusterID)
	if err != nil {
		return err
	}
	// A stale delete job must never remove records after the configuration has
	// been enabled again.
	if config == nil || config.Enabled {
		return nil
	}
	plain, err := s.cipher.Decrypt(config.CredentialsEncrypted)
	if err != nil {
		return fmt.Errorf("decrypt DNS credentials: %w", err)
	}
	provider, err := dnsprovider.New(
		config.Provider,
		config.Zone,
		value(config.ZoneId),
		[]byte(plain),
		s.httpClient,
	)
	if err != nil {
		return err
	}
	records, err := s.db.DNSManagedRecord.Query().
		Where(query.DNSManagedRecord.ClusterId.Equals(clusterID)).
		Do(ctx)
	if err != nil {
		return err
	}

	var errs []error
	for index := range records {
		item := &records[index]
		record := dnsprovider.Record{
			ID:       value(item.ProviderRecordId),
			Hostname: item.Hostname,
			Type:     item.Type,
			Value:    item.Value,
			Line:     item.DnsLineKey,
		}
		if deleteErr := provider.Delete(ctx, record); deleteErr != nil {
			updateSets := []query.DNSManagedRecordSetClause{
				query.DNSManagedRecord.Status.Set(model.DNSRecordStatusDELETING),
				query.DNSManagedRecord.LastError.Set(deleteErr.Error()),
				query.DNSManagedRecord.UpdatedAt.Set(time.Now()),
			}
			if item.ProviderRecordId != nil {
				updateSets = append(updateSets, query.DNSManagedRecord.ProviderRecordId.SetNull())
			}
			if _, updateErr := s.db.DNSManagedRecord.Update().
				Where(query.DNSManagedRecord.Id.Equals(item.Id)).
				Set(updateSets...).
				Do(ctx); updateErr != nil {
				errs = append(errs, updateErr)
			}
			errs = append(errs, fmt.Errorf("delete DNS record %s: %w", item.Hostname, deleteErr))
			continue
		}
		if _, deleteErr := s.db.DNSManagedRecord.Delete().
			Where(query.DNSManagedRecord.Id.Equals(item.Id)).
			Do(ctx); deleteErr != nil {
			errs = append(errs, deleteErr)
		}
	}
	return errors.Join(errs...)
}

func (s *Service) desired(
	ctx context.Context,
	cluster *model.Cluster,
	config *model.DNSProviderConfig,
	supportsLines bool,
) ([]desiredRecord, error) {
	result := []desiredRecord{}
	nodes, err := s.dnsEligibleNodes(ctx, cluster.Id, time.Now())
	if err != nil {
		return nil, err
	}
	for _, currentNode := range nodes {
		addresses, err := s.db.NodeAddress.Query().
			Where(query.NodeAddress.NodeId.Equals(currentNode.Id)).
			OrderBy(query.NodeAddress.CreatedAt.Asc()).
			Do(ctx)
		if err != nil {
			return nil, err
		}

		lines := []*model.DNSLine{nil}
		if supportsLines {
			links, linkErr := s.db.NodeDNSLine.Query().
				Where(query.NodeDNSLine.NodeId.Equals(currentNode.Id)).
				Do(ctx)
			if linkErr != nil {
				return nil, linkErr
			}
			if len(links) > 0 {
				lines = nil
				for _, link := range links {
					line, lineErr := s.db.DNSLine.FindUnique(
						ctx,
						query.DNSLine.Id.Equals(link.DnsLineId),
					)
					if lineErr != nil {
						return nil, lineErr
					}
					if line == nil {
						return nil, fmt.Errorf("DNS line %q not found", link.DnsLineId)
					}
					lines = append(lines, line)
				}
			}
		}
		for _, address := range addresses {
			if net.ParseIP(address.Address) == nil {
				continue
			}
			recordType := model.DNSRecordTypeA
			if strings.Contains(address.Address, ":") {
				recordType = model.DNSRecordTypeAAAA
			}
			for _, line := range lines {
				var lineID *string
				lineCode := "default"
				if line != nil {
					lineID = &line.Id
					lineCode = normalizeLineKey(line.ProviderCode)
				}
				nodeID := currentNode.Id
				result = append(result, desiredRecord{
					Record: dnsprovider.Record{
						Hostname: *cluster.PrimaryHostname,
						Type:     recordType,
						Value:    address.Address,
						Line:     lineCode,
						TTL:      config.DefaultTtl,
						Proxied:  config.Proxied,
					},
					DNSLineID: lineID,
					NodeID:    &nodeID,
				})
			}
		}
	}

	// Site domains are customer-owned DNS names. They are intentionally not
	// managed through the cluster provider; customers point them at the cluster
	// primary hostname with CNAME or an equivalent apex alias record.
	return result, nil
}

func (s *Service) apply(
	ctx context.Context,
	clusterID string,
	provider dnsprovider.Provider,
	desired []desiredRecord,
) error {
	existing, err := s.db.DNSManagedRecord.Query().
		Where(query.DNSManagedRecord.ClusterId.Equals(clusterID)).
		Do(ctx)
	if err != nil {
		return err
	}
	byKey := map[string]*model.DNSManagedRecord{}
	for index := range existing {
		item := &existing[index]
		k := key(item.Hostname, item.Type, item.Value, item.DnsLineKey)
		current := byKey[k]
		if current == nil || (current.ProviderRecordId == nil && item.ProviderRecordId != nil) {
			byKey[k] = item
		}
	}

	processed := map[string]struct{}{}
	retained := map[string]struct{}{}
	var errs []error
	for _, item := range desired {
		lineKey := normalizeLineKey(item.Line)
		k := key(item.Hostname, item.Type, item.Value, lineKey)
		if _, duplicate := processed[k]; duplicate {
			continue
		}
		processed[k] = struct{}{}
		current := byKey[k]
		if current == nil {
			now := time.Now()
			sets := []query.DNSManagedRecordSetClause{
				query.DNSManagedRecord.ClusterId.Set(clusterID),
				query.DNSManagedRecord.Hostname.Set(item.Hostname),
				query.DNSManagedRecord.Type.Set(item.Type),
				query.DNSManagedRecord.Value.Set(item.Value),
				query.DNSManagedRecord.DnsLineKey.Set(lineKey),
				query.DNSManagedRecord.Status.Set(model.DNSRecordStatusPENDING),
				query.DNSManagedRecord.UpdatedAt.Set(now),
			}
			if item.DNSLineID != nil {
				sets = append(sets, query.DNSManagedRecord.DnsLineId.Set(*item.DNSLineID))
			}
			if item.NodeID != nil {
				sets = append(sets, query.DNSManagedRecord.NodeId.Set(*item.NodeID))
			}
			current, err = s.db.DNSManagedRecord.Create().Set(sets...).Do(ctx)
			if err != nil {
				errs = append(errs, fmt.Errorf("create managed DNS record %s: %w", item.Hostname, err))
				continue
			}
			byKey[k] = current
		}
		retained[current.Id] = struct{}{}

		item.ID = value(current.ProviderRecordId)
		externalID, syncErr := provider.Upsert(ctx, item.Record)
		if syncErr == nil && strings.TrimSpace(externalID) == "" {
			syncErr = errors.New("DNS provider returned an empty record ID")
		}
		if syncErr != nil {
			updateSets := []query.DNSManagedRecordSetClause{
				query.DNSManagedRecord.Status.Set(model.DNSRecordStatusFAILED),
				query.DNSManagedRecord.LastError.Set(syncErr.Error()),
				query.DNSManagedRecord.UpdatedAt.Set(time.Now()),
			}
			if current.ProviderRecordId != nil {
				updateSets = append(updateSets, query.DNSManagedRecord.ProviderRecordId.SetNull())
			}
			if _, updateErr := s.db.DNSManagedRecord.Update().
				Where(query.DNSManagedRecord.Id.Equals(current.Id)).
				Set(updateSets...).
				Do(ctx); updateErr != nil {
				errs = append(errs, updateErr)
			}
			errs = append(errs, fmt.Errorf("upsert DNS record %s: %w", item.Hostname, syncErr))
			continue
		}

		updateSets := []query.DNSManagedRecordSetClause{
			query.DNSManagedRecord.ProviderRecordId.Set(externalID),
			query.DNSManagedRecord.Status.Set(model.DNSRecordStatusSYNCED),
			query.DNSManagedRecord.LastError.SetNull(),
			query.DNSManagedRecord.LastSyncedAt.Set(time.Now()),
			query.DNSManagedRecord.UpdatedAt.Set(time.Now()),
		}
		updateSets = append(updateSets, query.DNSManagedRecord.SiteDomainId.SetNull())
		if item.DNSLineID != nil {
			updateSets = append(updateSets, query.DNSManagedRecord.DnsLineId.Set(*item.DNSLineID))
		} else {
			updateSets = append(updateSets, query.DNSManagedRecord.DnsLineId.SetNull())
		}
		if item.NodeID != nil {
			updateSets = append(updateSets, query.DNSManagedRecord.NodeId.Set(*item.NodeID))
		} else {
			updateSets = append(updateSets, query.DNSManagedRecord.NodeId.SetNull())
		}
		if _, updateErr := s.db.DNSManagedRecord.Update().
			Where(query.DNSManagedRecord.Id.Equals(current.Id)).
			Set(updateSets...).
			Do(ctx); updateErr != nil {
			errs = append(errs, updateErr)
		}
	}

	for index := range existing {
		item := &existing[index]
		if _, keep := retained[item.Id]; keep {
			continue
		}
		k := key(item.Hostname, item.Type, item.Value, item.DnsLineKey)
		if _, wanted := processed[k]; wanted {
			canonical := byKey[k]
			if canonical != nil &&
				canonical.Id != item.Id &&
				value(canonical.ProviderRecordId) != "" &&
				value(canonical.ProviderRecordId) == value(item.ProviderRecordId) {
				if _, deleteErr := s.db.DNSManagedRecord.Delete().
					Where(query.DNSManagedRecord.Id.Equals(item.Id)).
					Do(ctx); deleteErr != nil {
					errs = append(errs, deleteErr)
				}
				continue
			}
		}
		record := dnsprovider.Record{
			ID:       value(item.ProviderRecordId),
			Hostname: item.Hostname,
			Type:     item.Type,
			Value:    item.Value,
			Line:     item.DnsLineKey,
		}
		if deleteErr := provider.Delete(ctx, record); deleteErr != nil {
			updateSets := []query.DNSManagedRecordSetClause{
				query.DNSManagedRecord.Status.Set(model.DNSRecordStatusDELETING),
				query.DNSManagedRecord.LastError.Set(deleteErr.Error()),
				query.DNSManagedRecord.UpdatedAt.Set(time.Now()),
			}
			if item.ProviderRecordId != nil {
				updateSets = append(updateSets, query.DNSManagedRecord.ProviderRecordId.SetNull())
			}
			if _, updateErr := s.db.DNSManagedRecord.Update().
				Where(query.DNSManagedRecord.Id.Equals(item.Id)).
				Set(updateSets...).
				Do(ctx); updateErr != nil {
				errs = append(errs, updateErr)
			}
			errs = append(errs, fmt.Errorf("delete DNS record %s: %w", item.Hostname, deleteErr))
			continue
		}
		if _, deleteErr := s.db.DNSManagedRecord.Delete().
			Where(query.DNSManagedRecord.Id.Equals(item.Id)).
			Do(ctx); deleteErr != nil {
			errs = append(errs, deleteErr)
		}
	}
	return errors.Join(errs...)
}

func normalizeLineKey(line string) string {
	line = strings.ToLower(strings.TrimSpace(line))
	if line == "" {
		return "default"
	}
	return line
}

func key(host string, kind model.DNSRecordType, target, line string) string {
	return strings.ToLower(host) + "|" + string(kind) + "|" +
		strings.ToLower(target) + "|" + normalizeLineKey(line)
}

func value(input *string) string {
	if input == nil {
		return ""
	}
	return *input
}
