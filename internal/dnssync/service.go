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
	"goveto-edge/internal/node"
	"goveto-edge/internal/storage/gen/client"
	"goveto-edge/internal/storage/gen/model"
	"goveto-edge/internal/storage/gen/query"
)

const (
	jobLease     = 2 * time.Minute
	jobRetention = 30 * 24 * time.Hour
)

type Service struct {
	db         *client.Client
	cipher     *node.CredentialCipher
	httpClient *http.Client
}

func New(db *client.Client, cipher *node.CredentialCipher) *Service {
	return &Service{db: db, cipher: cipher, httpClient: &http.Client{Timeout: 20 * time.Second}}
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
	_, err := db.DNSSyncJob.Update().
		Where(
			query.DNSSyncJob.ClusterId.Equals(clusterID),
			query.DNSSyncJob.Status.In(model.JobStatusPENDING, model.JobStatusRUNNING),
		).
		Set(
			query.DNSSyncJob.Status.Set(model.JobStatusCANCELLED),
			query.DNSSyncJob.LeaseUntil.SetNull(),
			query.DNSSyncJob.ResultJson.Set(payload),
			query.DNSSyncJob.UpdatedAt.Set(time.Now()),
		).
		DoMany(ctx)
	return err
}

func (s *Service) EnqueueIfConfigured(
	ctx context.Context,
	clusterID string,
	siteID *string,
	action model.DNSSyncAction,
) (*model.DNSSyncJob, error) {
	config, err := s.db.DNSProviderConfig.FindUnique(
		ctx,
		query.DNSProviderConfig.ClusterId.Equals(clusterID),
	)
	if err != nil {
		return nil, err
	}
	if config == nil || !config.Enabled {
		return nil, nil
	}
	return s.Enqueue(ctx, clusterID, siteID, action)
}

func (s *Service) EnqueueSiteIfConfigured(
	ctx context.Context,
	clusterID, siteID string,
) (*model.DNSSyncJob, error) {
	return s.EnqueueIfConfigured(ctx, clusterID, &siteID, model.DNSSyncActionUPSERT_SITE)
}

func (s *Service) Run(ctx context.Context) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	reconcile := time.NewTicker(5 * time.Minute)
	defer reconcile.Stop()

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
		"DELETE FROM dns_sync_jobs WHERE status IN ('SUCCEEDED', 'FAILED', 'CANCELLED') AND updated_at < NOW() - ($1 * INTERVAL '1 second')",
		int64(jobRetention/time.Second),
	)
	configs, err := s.db.DNSProviderConfig.Query().
		Where(query.DNSProviderConfig.Enabled.Equals(true)).
		Do(ctx)
	if err != nil {
		return
	}
	for _, config := range configs {
		_, _ = s.Enqueue(ctx, config.ClusterId, nil, model.DNSSyncActionRECONCILE)
	}
}

func (s *Service) runOne(ctx context.Context) {
	var claimed *model.DNSSyncJob
	defer func() {
		if recovered := recover(); recovered != nil {
			panicErr := fmt.Errorf("DNS sync worker panic: %v", recovered)
			slog.Error("DNS sync worker panic", "error", panicErr)
			if claimed != nil {
				s.finishDetached(ctx, claimed, panicErr)
			}
		}
	}()

	s.failExpiredJobs(ctx)
	jobs, err := client.Raw[model.DNSSyncJob](
		ctx,
		s.db,
		`UPDATE dns_sync_jobs
		 SET status='RUNNING',
		     attempts=attempts+1,
		     lease_until=NOW() + ($1 * INTERVAL '1 second'),
		     updated_at=NOW()
		 WHERE id=(
			SELECT id
			FROM dns_sync_jobs
			WHERE (status='PENDING' AND next_attempt_at<=NOW())
			   OR (status='RUNNING' AND lease_until<=NOW() AND attempts<max_attempts)
			ORDER BY next_attempt_at, created_at
			FOR UPDATE SKIP LOCKED
			LIMIT 1
		 )
		 RETURNING id, cluster_id, site_id, action, status, attempts, max_attempts,
		           next_attempt_at, lease_until, result_json, created_at, updated_at`,
		int64(jobLease/time.Second),
	)
	if err != nil || len(jobs) == 0 {
		return
	}
	job := &jobs[0]
	claimed = job

	unlock, locked, err := s.tryClusterLock(ctx, job.ClusterId)
	if err != nil {
		s.finishDetached(ctx, job, err)
		return
	}
	if !locked {
		s.requeueContended(ctx, job)
		return
	}
	defer unlock()

	if err = s.renewLease(ctx, job.Id); err == nil {
		switch job.Action {
		case model.DNSSyncActionDELETE_CLUSTER:
			err = s.deleteAll(ctx, job.Id, job.ClusterId)
		default:
			err = s.reconcile(ctx, job.Id, job.ClusterId)
		}
	}
	s.finishDetached(ctx, job, err)
}

func (s *Service) failExpiredJobs(ctx context.Context) {
	_, _ = s.db.RawExec(
		ctx,
		`UPDATE dns_sync_jobs
		 SET status='FAILED',
		     lease_until=NULL,
		     result_json=jsonb_build_object('error', 'worker lease expired after maximum attempts'),
		     updated_at=NOW()
		 WHERE status='RUNNING'
		   AND lease_until<=NOW()
		   AND attempts>=max_attempts`,
	)
}

func (s *Service) requeueContended(ctx context.Context, job *model.DNSSyncJob) {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	_, _ = s.db.RawExec(
		cleanupCtx,
		`UPDATE dns_sync_jobs
		 SET status='PENDING',
		     attempts=GREATEST(attempts-1, 0),
		     next_attempt_at=NOW() + INTERVAL '2 seconds',
		     lease_until=NULL,
		     updated_at=NOW()
		 WHERE id=$1 AND status='RUNNING'`,
		job.Id,
	)
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

func (s *Service) renewLease(ctx context.Context, jobID string) error {
	now := time.Now()
	updated, err := s.db.DNSSyncJob.Update().
		Where(
			query.DNSSyncJob.Id.Equals(jobID),
			query.DNSSyncJob.Status.Equals(model.JobStatusRUNNING),
		).
		Set(
			query.DNSSyncJob.LeaseUntil.Set(now.Add(jobLease)),
			query.DNSSyncJob.UpdatedAt.Set(now),
		).
		DoMany(ctx)
	if err != nil {
		return err
	}
	if updated == 0 {
		return errors.New("DNS sync job lease was lost")
	}
	return nil
}

func (s *Service) finishDetached(
	ctx context.Context,
	job *model.DNSSyncJob,
	executionErr error,
) {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	s.finish(cleanupCtx, job, executionErr)
}

func (s *Service) finish(ctx context.Context, job *model.DNSSyncJob, executionErr error) {
	status := model.JobStatusSUCCEEDED
	result := map[string]any{"error": ""}
	sets := []query.DNSSyncJobSetClause{query.DNSSyncJob.LeaseUntil.SetNull()}
	if executionErr != nil {
		result["error"] = executionErr.Error()
		if job.Attempts < job.MaxAttempts {
			status = model.JobStatusPENDING
			delay := time.Duration(1<<min(job.Attempts, 8)) * time.Second
			sets = append(sets, query.DNSSyncJob.NextAttemptAt.Set(time.Now().Add(delay)))
		} else {
			status = model.JobStatusFAILED
		}
	}
	payload, _ := json.Marshal(result)
	sets = append(
		sets,
		query.DNSSyncJob.Status.Set(status),
		query.DNSSyncJob.ResultJson.Set(payload),
		query.DNSSyncJob.UpdatedAt.Set(time.Now()),
	)
	_, _ = s.db.DNSSyncJob.Update().
		Where(
			query.DNSSyncJob.Id.Equals(job.Id),
			query.DNSSyncJob.Status.Equals(model.JobStatusRUNNING),
		).
		Set(sets...).
		Do(ctx)
}

type desiredRecord struct {
	dnsprovider.Record
	SiteDomainID, DNSLineID, NodeID *string
}

func (s *Service) reconcile(ctx context.Context, jobID, clusterID string) error {
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
	config, err := s.db.DNSProviderConfig.FindUnique(
		ctx,
		query.DNSProviderConfig.ClusterId.Equals(clusterID),
	)
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
	desired, err := s.desired(ctx, jobID, cluster, config, provider.SupportsLines())
	if err != nil {
		return err
	}
	return s.apply(ctx, jobID, clusterID, provider, desired)
}

func (s *Service) deleteAll(ctx context.Context, jobID, clusterID string) error {
	config, err := s.db.DNSProviderConfig.FindUnique(
		ctx,
		query.DNSProviderConfig.ClusterId.Equals(clusterID),
	)
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
		if err := s.renewLease(ctx, jobID); err != nil {
			return err
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

func (s *Service) desired(
	ctx context.Context,
	jobID string,
	cluster *model.Cluster,
	config *model.DNSProviderConfig,
	supportsLines bool,
) ([]desiredRecord, error) {
	result := []desiredRecord{}
	nodes, err := s.db.Node.Query().
		Where(
			query.Node.ClusterId.Equals(cluster.Id),
			query.Node.Status.Equals(model.NodeStatusONLINE),
		).
		Do(ctx)
	if err != nil {
		return nil, err
	}
	for _, currentNode := range nodes {
		if err := s.renewLease(ctx, jobID); err != nil {
			return nil, err
		}
		address, err := s.db.NodeAddress.Query().
			Where(
				query.NodeAddress.NodeId.Equals(currentNode.Id),
				query.NodeAddress.Primary.Equals(true),
			).
			First(ctx)
		if err != nil {
			return nil, err
		}
		if address == nil {
			continue
		}
		ip := net.ParseIP(address.Address)
		if ip == nil {
			continue
		}
		recordType := model.DNSRecordTypeA
		if strings.Contains(address.Address, ":") {
			recordType = model.DNSRecordTypeAAAA
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

	sites, err := s.db.Site.Query().
		Where(
			query.Site.ClusterId.Equals(cluster.Id),
			query.Site.Status.Equals(model.SiteStatusACTIVE),
		).
		Do(ctx)
	if err != nil {
		return nil, err
	}
	for _, site := range sites {
		if err := s.renewLease(ctx, jobID); err != nil {
			return nil, err
		}
		domains, domainErr := s.db.SiteDomain.Query().
			Where(query.SiteDomain.SiteId.Equals(site.Id)).
			Do(ctx)
		if domainErr != nil {
			return nil, domainErr
		}
		for _, domain := range domains {
			id := domain.Id
			result = append(result, desiredRecord{
				Record: dnsprovider.Record{
					Hostname: domain.Hostname,
					Type:     model.DNSRecordTypeCNAME,
					Value:    *cluster.PrimaryHostname,
					Line:     "default",
					TTL:      config.DefaultTtl,
					Proxied:  config.Proxied,
				},
				SiteDomainID: &id,
			})
		}
	}
	return result, nil
}

func (s *Service) apply(
	ctx context.Context,
	jobID, clusterID string,
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
			if item.SiteDomainID != nil {
				sets = append(sets, query.DNSManagedRecord.SiteDomainId.Set(*item.SiteDomainID))
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
		if err := s.renewLease(ctx, jobID); err != nil {
			return err
		}

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
		if item.SiteDomainID != nil {
			updateSets = append(updateSets, query.DNSManagedRecord.SiteDomainId.Set(*item.SiteDomainID))
		} else {
			updateSets = append(updateSets, query.DNSManagedRecord.SiteDomainId.SetNull())
		}
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
		if err := s.renewLease(ctx, jobID); err != nil {
			return err
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
