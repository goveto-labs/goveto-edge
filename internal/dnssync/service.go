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
	"sort"
	"strings"
	"sync"
	"time"

	"goveto-edge/internal/dnsprovider"
	"goveto-edge/internal/jobqueue"
	"goveto-edge/internal/node"
	"goveto-edge/internal/storage/gen/client"
	"goveto-edge/internal/storage/gen/model"
	"goveto-edge/internal/storage/gen/query"
)

const (
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
	policy     SchedulerPolicy

	providerMu    sync.Mutex
	providerCache map[string]cachedDNSProvider
}

// Options configures optional service behavior. The zero value applies
// production defaults.
type Options struct {
	// Scheduler overrides the DNS node scheduling guardrails. Unset fields
	// (zero values and nil pointers) fall back to DefaultSchedulerPolicy;
	// placement is always taken from the per-cluster DNS configuration.
	Scheduler SchedulerPolicy
}

// cachedDNSProvider reuses a constructed provider while the encrypted
// credentials and configuration revision are unchanged. This avoids repeated
// secret decryption on the periodic reconciliation path, where every enabled
// cluster builds a provider every five minutes.
type cachedDNSProvider struct {
	credentials string
	updatedAt   time.Time
	provider    dnsprovider.Provider
}

func New(db *client.Client, cipher *node.CredentialCipher, options ...Options) *Service {
	service := &Service{
		db:            db,
		cipher:        cipher,
		httpClient:    &http.Client{Timeout: 20 * time.Second},
		jobs:          jobqueue.New(db),
		providerCache: map[string]cachedDNSProvider{},
		policy:        DefaultSchedulerPolicy(),
	}
	if len(options) > 0 {
		service.policy = options[0].Scheduler.WithDefaults()
	}
	return service
}

// providerFor returns a provider for the configuration, reusing the cached
// instance while the stored credentials and configuration revision are
// unchanged. Provider structs are stateless, so sharing an instance across
// goroutines is safe.
func (s *Service) providerFor(config *model.DNSProviderConfig) (dnsprovider.Provider, error) {
	if config == nil {
		return nil, errors.New("DNS provider is not configured")
	}
	s.providerMu.Lock()
	defer s.providerMu.Unlock()
	if entry, ok := s.providerCache[config.Id]; ok && entry.provider != nil &&
		entry.credentials == config.CredentialsEncrypted &&
		entry.updatedAt.Equal(config.UpdatedAt) {
		return entry.provider, nil
	}
	plain, err := s.cipher.Decrypt(config.CredentialsEncrypted)
	if err != nil {
		return nil, fmt.Errorf("decrypt DNS credentials: %w", err)
	}
	provider, err := dnsprovider.New(config.Provider, config.Zone, value(config.ZoneId), []byte(plain), s.httpClient)
	if err != nil {
		return nil, err
	}
	s.providerCache[config.Id] = cachedDNSProvider{
		credentials: config.CredentialsEncrypted,
		updatedAt:   config.UpdatedAt,
		provider:    provider,
	}
	return provider, nil
}

func (s *Service) RewrapSecrets(ctx context.Context) error {
	configs, err := s.db.DNSProviderConfig.Query().Do(ctx)
	if err != nil {
		return err
	}
	for index := range configs {
		wrapped, changed, rewrapErr := s.cipher.Rewrap(configs[index].CredentialsEncrypted)
		if rewrapErr != nil {
			return fmt.Errorf("rewrap DNS provider %s: %w", configs[index].Id, rewrapErr)
		}
		if changed {
			if _, err = s.db.DNSProviderConfig.Update().Where(query.DNSProviderConfig.Id.Equals(configs[index].Id)).Set(query.DNSProviderConfig.CredentialsEncrypted.Set(wrapped)).Do(ctx); err != nil {
				return err
			}
		}
	}
	return nil
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

// EnqueueLatestTx makes a cluster-wide configuration change immediately
// runnable, replacing any pending work with the latest desired action.
func (s *Service) EnqueueLatestTx(
	ctx context.Context,
	db *client.Client,
	clusterID string,
	action model.DNSSyncAction,
) (*model.DNSSyncJob, error) {
	return s.enqueueTx(ctx, db, clusterID, nil, action, true)
}

// EnqueueTx creates at most one pending follow-up job for a cluster. A new
// pending job is allowed while another job is running so changes that happen
// during reconciliation cannot be lost. Rollback requests are exempt from
// coalescing: each one walks one snapshot back and needs its own job.
func (s *Service) EnqueueTx(
	ctx context.Context,
	db *client.Client,
	clusterID string,
	siteID *string,
	action model.DNSSyncAction,
) (*model.DNSSyncJob, error) {
	return s.enqueueTx(ctx, db, clusterID, siteID, action, false)
}

type pendingDNSJob struct {
	ID     string              `db:"id"`
	SiteID *string             `db:"site_id"`
	Action model.DNSSyncAction `db:"action"`
}

func (s *Service) enqueueTx(
	ctx context.Context,
	db *client.Client,
	clusterID string,
	siteID *string,
	action model.DNSSyncAction,
	refreshPending bool,
) (*model.DNSSyncJob, error) {
	if err := LockClusterTx(ctx, db, clusterID); err != nil {
		return nil, err
	}
	if siteID == nil && isClusterAction(action) {
		config, err := EndpointConfig(ctx, db, clusterID)
		if err != nil {
			return nil, err
		}
		var configured bool
		action, configured = resolveClusterAction(action, config)
		if !configured {
			return nil, nil
		}
	}
	// Rollback is a relative operation: two identical requests mean "go back
	// two steps", so a rollback never coalesces with pending work and always
	// creates its own job.
	if action != model.DNSSyncActionROLLBACK_CLUSTER {
		active, err := client.Raw[pendingDNSJob](ctx, db, `SELECT id, site_id, action
			FROM dns_sync_jobs WHERE cluster_id=$1 AND status='PENDING'
			ORDER BY created_at ASC FOR UPDATE LIMIT 1`, clusterID)
		if err != nil {
			return nil, err
		}
		if len(active) > 0 {
			current := active[0]
			coalescedAction, pendingSiteID, changed := coalescePendingAction(
				model.DNSSyncJob{Action: current.Action, SiteId: current.SiteID},
				siteID,
				action,
			)
			if !changed && !refreshPending {
				return db.DNSSyncJob.FindUnique(ctx, query.DNSSyncJob.Id.Equals(current.ID))
			}
			return db.DNSSyncJob.Update().
				Where(query.DNSSyncJob.Id.Equals(current.ID)).
				Set(refreshPendingJobSets(coalescedAction, pendingSiteID, time.Now())...).
				Do(ctx)
		}
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

func isClusterAction(action model.DNSSyncAction) bool {
	return action == model.DNSSyncActionUPSERT_CLUSTER ||
		action == model.DNSSyncActionDELETE_CLUSTER ||
		action == model.DNSSyncActionROLLBACK_CLUSTER
}

func resolveClusterAction(
	requested model.DNSSyncAction,
	config *model.DNSProviderConfig,
) (model.DNSSyncAction, bool) {
	if !isClusterAction(requested) {
		return requested, true
	}
	if config == nil {
		return "", false
	}
	// Rollback is independent of the enabled flag: it restores a previously
	// published state and only requires an existing configuration.
	if requested == model.DNSSyncActionROLLBACK_CLUSTER {
		return requested, true
	}
	if config.Enabled {
		return model.DNSSyncActionUPSERT_CLUSTER, true
	}
	return model.DNSSyncActionDELETE_CLUSTER, true
}

func refreshPendingJobSets(
	action model.DNSSyncAction,
	siteID *string,
	now time.Time,
) []query.DNSSyncJobSetClause {
	sets := []query.DNSSyncJobSetClause{
		query.DNSSyncJob.Action.Set(action),
		query.DNSSyncJob.Attempts.Set(0),
		query.DNSSyncJob.NextAttemptAt.Set(now),
		query.DNSSyncJob.LeaseOwner.SetNull(),
		query.DNSSyncJob.LeaseUntil.SetNull(),
		query.DNSSyncJob.HeartbeatAt.SetNull(),
		query.DNSSyncJob.CancelRequestedAt.SetNull(),
		query.DNSSyncJob.TimeoutAt.SetNull(),
		query.DNSSyncJob.ResultJson.SetNull(),
		query.DNSSyncJob.CompensationJson.SetNull(),
		query.DNSSyncJob.Error.SetNull(),
		query.DNSSyncJob.UpdatedAt.Set(now),
	}
	if siteID == nil {
		return append(sets, query.DNSSyncJob.SiteId.SetNull())
	}
	return append(sets, query.DNSSyncJob.SiteId.Set(*siteID))
}

// A cluster-wide request describes the latest desired state and supersedes any
// pending work for the cluster. Site-scoped requests keep an existing pending
// cluster job because a full reconciliation already covers them.
func coalescePendingAction(
	active model.DNSSyncJob,
	siteID *string,
	action model.DNSSyncAction,
) (model.DNSSyncAction, *string, bool) {
	if siteID == nil && (active.Action != action || active.SiteId != nil) {
		return action, nil, true
	}
	return active.Action, active.SiteId, false
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
	provider, err := s.providerFor(config)
	if err != nil {
		return nil, err
	}
	desired, err := s.desiredNodeRecords(ctx, cluster, config, dnsprovider.SupportsLines(provider))
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
	provider, err := s.providerFor(config)
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

	err = s.db.Tx(ctx, func(tx *client.Client) error {
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
	if err != nil {
		return err
	}
	// The configuration is gone; a cached provider built from it must never
	// be reused.
	s.providerMu.Lock()
	delete(s.providerCache, config.Id)
	s.providerMu.Unlock()
	return nil
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
			// Probing every enabled cluster can take a while; run it beside
			// the job loop instead of blocking job processing.
			go s.enqueuePeriodic(ctx)
		}
	}
}

// periodicProbeTimeout bounds a single cluster's provider comparison so one
// slow vendor API cannot stall the whole periodic sweep.
const periodicProbeTimeout = 45 * time.Second

// periodicProbeConcurrency limits how many clusters are probed in parallel.
const periodicProbeConcurrency = 4

// removalPaceInterval is the delay before the follow-up pass that finishes
// removals deferred by MaxRemovalRatio pacing.
const removalPaceInterval = time.Minute

func (s *Service) enqueuePeriodic(ctx context.Context) {
	configs, err := s.db.DNSProviderConfig.Query().
		Where(
			query.DNSProviderConfig.Enabled.Equals(true),
			query.DNSProviderConfig.Kind.Equals(model.DNSProviderKindENDPOINT),
		).
		Do(ctx)
	if err != nil {
		return
	}
	semaphore := make(chan struct{}, periodicProbeConcurrency)
	var group sync.WaitGroup
	for _, config := range configs {
		group.Add(1)
		semaphore <- struct{}{}
		go func(clusterID string) {
			defer group.Done()
			defer func() { <-semaphore }()
			probeCtx, cancel := context.WithTimeout(ctx, periodicProbeTimeout)
			defer cancel()
			if _, err := s.EnqueueNodeIPIfChanged(probeCtx, clusterID); err != nil && ctx.Err() == nil {
				slog.Warn("periodic DNS drift check", "cluster_id", clusterID, "error", err)
			}
		}(config.ClusterId)
	}
	group.Wait()
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
		var requeueAfter time.Duration
		switch job.Action {
		case model.DNSSyncActionDELETE_CLUSTER:
			executionErr = s.deleteAll(runCtx, job.ClusterId)
		case model.DNSSyncActionROLLBACK_CLUSTER:
			executionErr = s.rollback(runCtx, job.ClusterId)
		default:
			var deferred bool
			deferred, executionErr = s.reconcile(runCtx, job.ClusterId)
			if executionErr == nil && deferred {
				// Removal pacing kept some dropped nodes published for this
				// pass. Requeue directly instead of relying on drift
				// detection: the deferred records are part of the desired
				// set, so no drift will ever be observed for them.
				requeueAfter = removalPaceInterval
			}
		}
		outcome := jobqueue.Outcome{
			Result:       map[string]any{"cluster_id": job.ClusterId, "action": job.Action},
			Err:          executionErr,
			Retryable:    executionErr != nil,
			RequeueAfter: requeueAfter,
		}
		if executionErr != nil {
			// Rejected credentials are permanent: retrying with the same
			// secrets cannot succeed, so fail immediately for visibility.
			if dnsprovider.IsInvalidCredentials(executionErr) {
				outcome.Retryable = false
			} else if retryAfter := dnsprovider.RateLimitRetryAfter(executionErr); retryAfter > 0 {
				// Honor the provider's retry hint instead of the default
				// exponential backoff so throttled jobs stop hammering the API.
				outcome.RetryAfter = retryAfter
			}
		}
		return outcome
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

// scheduledRecords computes the desired node record set for the cluster,
// applying scheduling policy (admission hysteresis, placement, removal
// limiting) on top of the DNS-eligible candidates. The boolean result reports
// whether removal pacing deferred nodes to a follow-up pass.
func (s *Service) scheduledRecords(
	ctx context.Context,
	cluster *model.Cluster,
	config *model.DNSProviderConfig,
	supportsLines bool,
) ([]desiredRecord, bool, error) {
	candidates, err := s.dnsCandidates(ctx, cluster.Id, supportsLines)
	if err != nil {
		return nil, false, err
	}
	published, err := s.publishedNodeIDs(ctx, cluster.Id)
	if err != nil {
		return nil, false, err
	}
	policy := s.policy
	policy.Placement = config.Placement
	selected := Select(candidates, published, policy, time.Now())
	selected, deferred, err := s.paceRemovals(ctx, candidates, selected, published, policy, supportsLines)
	if err != nil {
		return nil, false, err
	}

	result := make([]desiredRecord, 0)
	seen := map[string]bool{}
	// Iterate in selection order for stable output: Select keeps candidate
	// order and paceRemovals appends deferred nodes at the end.
	for _, item := range selected {
		for _, address := range item.Addresses {
			if net.ParseIP(address.Address) == nil {
				continue
			}
			recordType := model.DNSRecordTypeA
			if strings.Contains(address.Address, ":") {
				recordType = model.DNSRecordTypeAAAA
			}
			lines := item.Lines
			if len(lines) == 0 {
				lines = []*model.DNSLine{nil}
			}
			for _, line := range lines {
				var lineID *string
				lineCode := "default"
				if line != nil {
					lineID = &line.Id
					lineCode = normalizeLineKey(line.ProviderCode)
				}
				record := dnsprovider.Record{
					Hostname: *cluster.PrimaryHostname,
					Type:     recordType,
					Value:    dnsprovider.CanonicalValue(recordType, address.Address),
					Line:     lineCode,
					TTL:      config.DefaultTtl,
					Proxied:  config.Proxied,
				}
				key := nodeRecordKey(record)
				if seen[key] {
					continue
				}
				seen[key] = true
				nodeID := item.Node.Id
				result = append(result, desiredRecord{Record: record, DNSLineID: lineID, NodeID: &nodeID})
			}
		}
	}

	// Site domains are customer-owned DNS names. They are intentionally not
	// managed through the cluster provider; customers point them at the cluster
	// primary hostname with CNAME or an equivalent apex alias record.
	return result, deferred, nil
}

// paceRemovals reapplies removal pacing after Select: at most
// MaxRemovalRatio of the currently published nodes may leave DNS in one pass.
// Every published node missing from the selection counts toward the cap,
// whether it was evicted by placement or left the eligible set entirely
// (offline beyond the grace period, disabled, or revoked). Dropped-but-
// deferred nodes are re-added to the selection for this pass so traffic
// drains in bounded steps. When no eligible node remains at all, the
// deferral is skipped: records pointing at dead nodes must always be removed.
//
// The boolean result reports whether any removal was deferred. The caller
// must schedule a follow-up pass in that case: deferred nodes are part of the
// desired set, so drift detection alone would see no change and the deferral
// would never complete.
func (s *Service) paceRemovals(
	ctx context.Context,
	candidates []NodeCandidate,
	selected []NodeCandidate,
	published map[string]bool,
	policy SchedulerPolicy,
	supportsLines bool,
) ([]NodeCandidate, bool, error) {
	if len(selected) == 0 || len(published) == 0 {
		return selected, false, nil
	}
	policy = policy.WithDefaults()
	candidateByID := make(map[string]NodeCandidate, len(candidates))
	for _, item := range candidates {
		candidateByID[item.Node.Id] = item
	}
	droppedIDs := droppedFromSelection(published, selected)
	if len(droppedIDs) == 0 {
		return selected, false, nil
	}
	allowed := allowedRemovals(len(published), policy.MaxRemovalRatio)
	if len(droppedIDs) <= allowed {
		return selected, false, nil
	}
	dropped, err := s.droppedNodes(ctx, droppedIDs, candidateByID)
	if err != nil {
		return nil, false, err
	}
	deferred := deferredRemovals(dropped, len(dropped)-allowed)
	if len(deferred) == 0 {
		return selected, false, nil
	}
	result := append([]NodeCandidate(nil), selected...)
	for _, node := range deferred {
		item, ok := candidateByID[node.Id]
		if !ok {
			// The node left the eligible set, so its addresses and lines are
			// loaded directly to keep its records published for one more pass.
			item, err = s.nodeCandidate(ctx, node, supportsLines)
			if err != nil {
				return nil, false, err
			}
		}
		result = append(result, item)
	}
	return result, true, nil
}

// droppedFromSelection returns the sorted ids that are published but absent
// from the selection. Every one counts toward the MaxRemovalRatio pacing cap,
// whether the node was evicted by placement or left the eligible candidate
// set entirely (offline beyond the grace period, disabled, or revoked). The
// order is sorted so the deferral choice is deterministic on equal UpdatedAt
// values.
func droppedFromSelection(published map[string]bool, selected []NodeCandidate) []string {
	selectedIDs := make(map[string]bool, len(selected))
	for _, item := range selected {
		selectedIDs[item.Node.Id] = true
	}
	dropped := make([]string, 0, len(published))
	for id := range published {
		if !selectedIDs[id] {
			dropped = append(dropped, id)
		}
	}
	sort.Strings(dropped)
	return dropped
}

// droppedNodes resolves dropped published ids to nodes, loading the ones that
// are no longer part of the eligible candidate set.
func (s *Service) droppedNodes(
	ctx context.Context,
	droppedIDs []string,
	candidateByID map[string]NodeCandidate,
) ([]model.Node, error) {
	dropped := make([]model.Node, 0, len(droppedIDs))
	missing := make([]string, 0)
	for _, id := range droppedIDs {
		if item, ok := candidateByID[id]; ok {
			dropped = append(dropped, item.Node)
		} else {
			missing = append(missing, id)
		}
	}
	if len(missing) == 0 {
		return dropped, nil
	}
	// A published id without a node row means the node was deleted outright;
	// it simply never enters the deferred set, so its records are removed
	// immediately instead of blocking reconciliation.
	nodes, err := s.db.Node.Query().Where(query.Node.Id.In(missing...)).Do(ctx)
	if err != nil {
		return nil, err
	}
	return append(dropped, nodes...), nil
}

// dnsCandidates loads DNS-eligible nodes with their addresses and lines.
func (s *Service) dnsCandidates(ctx context.Context, clusterID string, supportsLines bool) ([]NodeCandidate, error) {
	nodes, err := s.dnsEligibleNodes(ctx, clusterID, time.Now())
	if err != nil {
		return nil, err
	}
	candidates := make([]NodeCandidate, 0, len(nodes))
	for _, currentNode := range nodes {
		candidate, err := s.nodeCandidate(ctx, currentNode, supportsLines)
		if err != nil {
			return nil, err
		}
		candidates = append(candidates, candidate)
	}
	return candidates, nil
}

// nodeCandidate loads the addresses and DNS lines of a single node.
func (s *Service) nodeCandidate(ctx context.Context, currentNode model.Node, supportsLines bool) (NodeCandidate, error) {
	addresses, err := s.db.NodeAddress.Query().
		Where(query.NodeAddress.NodeId.Equals(currentNode.Id)).
		OrderBy(query.NodeAddress.CreatedAt.Asc()).
		Do(ctx)
	if err != nil {
		return NodeCandidate{}, err
	}
	lines := []*model.DNSLine{nil}
	if supportsLines {
		links, err := s.db.NodeDNSLine.Query().
			Where(query.NodeDNSLine.NodeId.Equals(currentNode.Id)).
			Do(ctx)
		if err != nil {
			return NodeCandidate{}, err
		}
		if len(links) > 0 {
			lines = nil
			for _, link := range links {
				line, err := s.db.DNSLine.FindUnique(
					ctx,
					query.DNSLine.Id.Equals(link.DnsLineId),
				)
				if err != nil {
					return NodeCandidate{}, err
				}
				if line == nil {
					return NodeCandidate{}, fmt.Errorf("DNS line %q not found", link.DnsLineId)
				}
				lines = append(lines, line)
			}
		}
	}
	return NodeCandidate{Node: currentNode, Addresses: addresses, Lines: lines}, nil
}

// publishedNodeIDs returns the distinct node ids currently tracked by managed
// DNS records.
func (s *Service) publishedNodeIDs(ctx context.Context, clusterID string) (map[string]bool, error) {
	records, err := s.db.DNSManagedRecord.Query().
		Where(query.DNSManagedRecord.ClusterId.Equals(clusterID)).
		Do(ctx)
	if err != nil {
		return nil, err
	}
	result := make(map[string]bool, len(records))
	for _, record := range records {
		if record.NodeId != nil {
			result[*record.NodeId] = true
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
	remote []dnsprovider.Record,
) error {
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

// reconcile publishes the desired record set. The boolean result reports
// whether removal pacing deferred any node, in which case the caller must
// schedule a follow-up pass to finish draining.
func (s *Service) reconcile(ctx context.Context, clusterID string) (bool, error) {
	cluster, err := s.db.Cluster.FindUnique(ctx, query.Cluster.Id.Equals(clusterID))
	if err != nil {
		return false, err
	}
	if cluster == nil {
		return false, fmt.Errorf("cluster %q not found", clusterID)
	}
	if cluster.PrimaryHostname == nil || *cluster.PrimaryHostname == "" {
		return false, errors.New("cluster primary hostname is not configured")
	}
	config, err := EndpointConfig(ctx, s.db, clusterID)
	if err != nil {
		return false, err
	}
	if config == nil {
		return false, errors.New("DNS provider is not configured")
	}
	if !config.Enabled {
		return false, errors.New("DNS provider is disabled")
	}
	provider, err := s.providerFor(config)
	if err != nil {
		return false, err
	}
	desired, deferred, err := s.desired(ctx, cluster, config, dnsprovider.SupportsLines(provider))
	if err != nil {
		return false, err
	}
	// One remote listing drives both the diff in apply and stale-record
	// cleanup, halving the read API calls per reconciliation. Keys deleted by
	// apply are filtered out of the listing before the cleanup pass.
	remote, err := provider.ListRecords(ctx, *cluster.PrimaryHostname)
	if err != nil {
		return false, err
	}
	deletedKeys, err := s.apply(ctx, clusterID, provider, desired, remote)
	if err != nil {
		return false, err
	}
	nodeRecords := make([]dnsprovider.Record, 0)
	for _, record := range desired {
		if record.NodeID != nil {
			nodeRecords = append(nodeRecords, record.Record)
		}
	}
	if err := syncRemoteNodeRecords(ctx, provider, *cluster.PrimaryHostname, nodeRecords, remainingRemote(remote, deletedKeys)); err != nil {
		return false, err
	}
	// Persist the published state so a bad change can be rolled back.
	return deferred, s.writeSnapshot(ctx, clusterID, desired)
}

// remainingRemote drops the remote records apply already deleted, so stale
// record cleanup does not issue a second delete for them.
func remainingRemote(remote []dnsprovider.Record, deletedKeys map[string]bool) []dnsprovider.Record {
	if len(deletedKeys) == 0 {
		return remote
	}
	remaining := make([]dnsprovider.Record, 0, len(remote))
	for _, record := range remote {
		if !deletedKeys[nodeRecordKey(record)] {
			remaining = append(remaining, record)
		}
	}
	return remaining
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
	provider, err := s.providerFor(config)
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
			if item.ProviderRecordId != nil && dnsprovider.IsRecordNotFound(deleteErr) {
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
) ([]desiredRecord, bool, error) {
	return s.scheduledRecords(ctx, cluster, config, supportsLines)
}

// desiredNodeRecords is the record-only projection of scheduledRecords, used
// to compare the desired set against the provider without sync metadata.
func (s *Service) desiredNodeRecords(
	ctx context.Context,
	cluster *model.Cluster,
	config *model.DNSProviderConfig,
	supportsLines bool,
) ([]dnsprovider.Record, error) {
	records, _, err := s.scheduledRecords(ctx, cluster, config, supportsLines)
	if err != nil {
		return nil, err
	}
	result := make([]dnsprovider.Record, 0, len(records))
	for _, record := range records {
		result = append(result, record.Record)
	}
	return result, nil
}

// apply converges the managed-record table and the provider toward the
// desired set. It returns the node-record keys whose remote records were
// deleted, so the caller can exclude them from further cleanup passes.
func (s *Service) apply(
	ctx context.Context,
	clusterID string,
	provider dnsprovider.Provider,
	desired []desiredRecord,
	remote []dnsprovider.Record,
) (map[string]bool, error) {
	existing, err := s.db.DNSManagedRecord.Query().
		Where(query.DNSManagedRecord.ClusterId.Equals(clusterID)).
		Do(ctx)
	if err != nil {
		return nil, err
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
	// Index the remote records once so unchanged entries skip provider calls
	// entirely instead of paying a find + rewrite per record.
	remoteByKey := make(map[string]dnsprovider.Record, len(remote))
	for _, record := range remote {
		remoteKey := nodeRecordKey(record)
		if _, seen := remoteByKey[remoteKey]; !seen {
			remoteByKey[remoteKey] = record
		}
	}

	processed := map[string]struct{}{}
	retained := map[string]struct{}{}
	deletedKeys := map[string]bool{}
	proxiedAware := provider.Capabilities().Proxied
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
		var externalID string
		var syncErr error
		if remoteRecord, remoteExists := remoteByKey[nodeRecordKey(item.Record)]; remoteExists && remoteRecord.ID != "" {
			tracked := current.ProviderRecordId != nil && *current.ProviderRecordId == remoteRecord.ID
			untracked := current.ProviderRecordId == nil
			// The proxied flag only distinguishes records when the provider
			// supports it; other providers never serve proxied records.
			sameProxy := !proxiedAware || remoteRecord.Proxied == item.Record.Proxied
			if (tracked || untracked) && sameProxy && remoteRecord.TTL == dnsprovider.ExpectedTTL(item.Record) {
				// The provider already serves exactly this record; adopt or keep
				// it without a write. Adoption also covers records created before
				// they were tracked locally.
				externalID = remoteRecord.ID
			} else {
				externalID, syncErr = provider.Upsert(ctx, item.Record)
			}
		} else {
			externalID, syncErr = provider.Upsert(ctx, item.Record)
		}
		if syncErr == nil && strings.TrimSpace(externalID) == "" {
			syncErr = errors.New("DNS provider returned an empty record ID")
		}
		if syncErr != nil {
			updateSets := []query.DNSManagedRecordSetClause{
				query.DNSManagedRecord.Status.Set(model.DNSRecordStatusFAILED),
				query.DNSManagedRecord.LastError.Set(syncErr.Error()),
				query.DNSManagedRecord.UpdatedAt.Set(time.Now()),
			}
			if current.ProviderRecordId != nil && dnsprovider.IsRecordNotFound(syncErr) {
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
			if item.ProviderRecordId != nil && dnsprovider.IsRecordNotFound(deleteErr) {
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
		deletedKeys[nodeRecordKey(record)] = true
		if _, deleteErr := s.db.DNSManagedRecord.Delete().
			Where(query.DNSManagedRecord.Id.Equals(item.Id)).
			Do(ctx); deleteErr != nil {
			errs = append(errs, deleteErr)
		}
	}
	return deletedKeys, errors.Join(errs...)
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
		strings.ToLower(dnsprovider.CanonicalValue(kind, target)) + "|" + normalizeLineKey(line)
}

func value(input *string) string {
	if input == nil {
		return ""
	}
	return *input
}
