// Package publisher renders, publishes, and rolls back edge site configuration.
package publisher

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"goveto-edge/internal/audit"
	"goveto-edge/internal/certmanager"
	"goveto-edge/internal/edgecontrol"
	"goveto-edge/internal/edgeprotocol"
	"goveto-edge/internal/jobqueue"
	"goveto-edge/internal/node"
	"goveto-edge/internal/storage/gen/client"
	"goveto-edge/internal/storage/gen/model"
	"goveto-edge/internal/storage/gen/query"
)

type Service struct {
	db          *client.Client
	cipher      *node.CredentialCipher
	gateway     *edgecontrol.Gateway
	jobs        *jobqueue.Manager
	concurrency int
}

func New(db *client.Client, cipher *node.CredentialCipher, gateway *edgecontrol.Gateway) *Service {
	return &Service{db: db, cipher: cipher, gateway: gateway, jobs: jobqueue.New(db), concurrency: 8}
}

// EnqueueCluster republishes every site after the cluster's available node set
// changes, for example when a freshly installed node becomes online.
func (s *Service) EnqueueCluster(ctx context.Context, clusterID string) error {
	sites, err := s.db.Site.Query().
		Where(query.Site.ClusterId.Equals(clusterID)).
		Do(ctx)
	if err != nil {
		return err
	}
	var enqueueErrors []error
	for index := range sites {
		if _, enqueueErr := s.Enqueue(ctx, sites[index].Id); enqueueErr != nil {
			enqueueErrors = append(enqueueErrors, fmt.Errorf("site %s: %w", sites[index].Id, enqueueErr))
		}
	}
	return errors.Join(enqueueErrors...)
}

func (s *Service) Enqueue(ctx context.Context, siteID string) (*model.PublishJob, error) {
	return s.EnqueueIdempotent(ctx, siteID, "")
}

func (s *Service) EnqueueIdempotent(ctx context.Context, siteID, idempotencyKey string) (*model.PublishJob, error) {
	if err := jobqueue.ValidateIdempotencyKey(idempotencyKey); err != nil {
		return nil, err
	}
	var job *model.PublishJob
	err := s.db.Tx(ctx, func(tx *client.Client) error {
		if idempotencyKey != "" {
			if _, err := tx.RawExec(ctx, "SELECT pg_advisory_xact_lock(hashtext($1))", "publish:"+idempotencyKey); err != nil {
				return err
			}
			existing, err := tx.PublishJob.Query().Where(query.PublishJob.IdempotencyKey.Equals(&idempotencyKey)).First(ctx)
			if err != nil {
				return err
			}
			if existing != nil {
				if existing.SiteId != siteID {
					return fmt.Errorf("%w: key is already used by another publish request", jobqueue.ErrIdempotencyConflict)
				}
				job = existing
				return nil
			}
		}
		// Serialize version allocation and the active-job check per site.
		if _, err := tx.RawExec(ctx, "SELECT pg_advisory_xact_lock(hashtext($1))", siteID); err != nil {
			return err
		}

		site, err := tx.Site.FindUnique(ctx, query.Site.Id.Equals(siteID))
		if err != nil {
			return err
		}
		if site == nil {
			return fmt.Errorf("site %s not found", siteID)
		}

		pending, pendingErr := tx.PublishJob.Query().
			Where(
				query.PublishJob.SiteId.Equals(siteID),
				query.PublishJob.Status.Equals(model.JobStatusPENDING),
			).
			OrderBy(query.PublishJob.CreatedAt.Desc()).
			First(ctx)
		if pendingErr != nil {
			return pendingErr
		}

		var latest *model.ConfigVersion
		if pending == nil {
			latest, err = tx.ConfigVersion.Query().
				Where(query.ConfigVersion.SiteId.Equals(siteID)).
				OrderBy(query.ConfigVersion.Version.Desc()).
				First(ctx)
			if err != nil {
				return err
			}
		}
		version := nextPublishVersion(site.Version, pending, latest)

		config, targets, err := s.buildWith(tx, ctx, site, uint64(version))
		if err != nil {
			return err
		}

		configJSON, err := json.Marshal(config)
		if err != nil {
			return err
		}

		targetJSON, err := json.Marshal(targets)
		if err != nil {
			return err
		}

		hash := sha256.Sum256(configJSON)
		if pending != nil {
			if _, err = tx.ConfigVersion.Update().
				Where(
					query.ConfigVersion.SiteId.Equals(siteID),
					query.ConfigVersion.Version.Equals(version),
					query.ConfigVersion.Status.Equals(model.ConfigStatusDRAFT),
				).
				Set(
					query.ConfigVersion.ConfigJson.Set(configJSON),
					query.ConfigVersion.Hash.Set(hex.EncodeToString(hash[:])),
				).
				DoMany(ctx); err != nil {
				return err
			}

			sets := []query.PublishJobSetClause{query.PublishJob.Targets.Set(targetJSON)}
			if idempotencyKey != "" && pending.IdempotencyKey == nil {
				sets = append(sets, query.PublishJob.IdempotencyKey.Set(idempotencyKey))
			}
			job, err = tx.PublishJob.Update().
				Where(
					query.PublishJob.Id.Equals(pending.Id),
					query.PublishJob.Status.Equals(model.JobStatusPENDING),
				).
				Set(sets...).
				Do(ctx)
			return err
		}

		if _, err = tx.ConfigVersion.Create().
			Set(
				query.ConfigVersion.SiteId.Set(siteID),
				query.ConfigVersion.Version.Set(version),
				query.ConfigVersion.ConfigJson.Set(configJSON),
				query.ConfigVersion.Hash.Set(hex.EncodeToString(hash[:])),
				query.ConfigVersion.Status.Set(model.ConfigStatusDRAFT),
			).
			Do(ctx); err != nil {
			return err
		}

		sets := []query.PublishJobSetClause{
			query.PublishJob.SiteId.Set(siteID),
			query.PublishJob.Version.Set(version),
			query.PublishJob.Targets.Set(targetJSON),
			query.PublishJob.Status.Set(model.JobStatusPENDING),
		}
		if idempotencyKey != "" {
			sets = append(sets, query.PublishJob.IdempotencyKey.Set(idempotencyKey))
		}
		job, err = tx.PublishJob.Create().Set(sets...).Do(ctx)
		return err
	})
	return job, err
}

func nextPublishVersion(siteVersion int64, pending *model.PublishJob, latest *model.ConfigVersion) int64 {
	if pending != nil {
		return pending.Version
	}
	version := siteVersion + 1
	if latest != nil && latest.Version >= version {
		return latest.Version + 1
	}
	return version
}

func (s *Service) Run(ctx context.Context) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.runOne(ctx)
		}
	}
}

func (s *Service) runOne(ctx context.Context) {
	_, err := s.jobs.RunOne(ctx, jobqueue.Publish, 15*time.Minute, func(runCtx context.Context, lease jobqueue.Lease) jobqueue.Outcome {
		job, loadErr := s.db.PublishJob.FindUnique(runCtx, query.PublishJob.Id.Equals(lease.ID))
		if loadErr != nil || job == nil {
			if loadErr == nil {
				loadErr = errors.New("publish job not found")
			}
			return jobqueue.Outcome{Err: loadErr, Retryable: true}
		}
		return s.execute(runCtx, job)
	})
	if err != nil && !errors.Is(err, context.Canceled) {
		slog.Warn("publish job worker", "error", err)
	}
}

type target struct {
	NodeID string `json:"node_id"`
}
type targetResult struct {
	NodeID     string `json:"node_id"`
	Success    bool   `json:"success"`
	RolledBack bool   `json:"rolled_back,omitempty"`
	Error      string `json:"error,omitempty"`
}

func (s *Service) execute(ctx context.Context, job *model.PublishJob) jobqueue.Outcome {
	site, err := s.db.Site.FindUnique(ctx, query.Site.Id.Equals(job.SiteId))
	if err != nil {
		return publishOutcome(model.JobStatusFAILED, nil, err, true)
	}
	if site == nil {
		err = errors.New("publish site not found")
		return publishOutcome(model.JobStatusFAILED, nil, err, false)
	}

	version, err := s.db.ConfigVersion.Query().
		Where(
			query.ConfigVersion.SiteId.Equals(job.SiteId),
			query.ConfigVersion.Version.Equals(job.Version),
		).
		First(ctx)
	if err != nil {
		return publishOutcome(model.JobStatusFAILED, nil, err, true)
	}
	if version == nil {
		err = errors.New("publish config version not found")
		return publishOutcome(model.JobStatusFAILED, nil, err, false)
	}

	var config edgeprotocol.SiteConfig
	var targets []target
	if err = json.Unmarshal(version.ConfigJson, &config); err != nil {
		return publishOutcome(model.JobStatusFAILED, nil, err, false)
	}
	if err = json.Unmarshal(job.Targets, &targets); err != nil {
		return publishOutcome(model.JobStatusFAILED, nil, err, false)
	}
	results := s.fanout(ctx, config, targets)
	succeeded, failed := successfulTargets(results, targets)
	if len(failed) == 0 {
		if _, err = s.db.Site.Update().
			Where(query.Site.Id.Equals(site.Id)).
			Set(query.Site.Version.Set(job.Version)).
			Do(ctx); err != nil {
			return publishOutcome(model.JobStatusFAILED, results, err, true)
		}

		if _, err = s.db.ConfigVersion.Update().
			Where(
				query.ConfigVersion.SiteId.Equals(site.Id),
				query.ConfigVersion.Version.Equals(job.Version),
			).
			Set(query.ConfigVersion.Status.Set(model.ConfigStatusPUBLISHED)).
			DoMany(ctx); err != nil {
			return publishOutcome(model.JobStatusFAILED, results, err, true)
		}

		for _, item := range succeeded {
			if err = s.setNodeVersion(
				ctx, item.NodeID, site.Id, job.Version, model.ConfigStatusPUBLISHED,
			); err != nil {
				return publishOutcome(model.JobStatusFAILED, results, err, true)
			}
		}
		return publishOutcome(model.JobStatusSUCCEEDED, results, nil, false)
	}
	if len(succeeded) == 0 {
		if _, err = s.db.ConfigVersion.Update().
			Where(
				query.ConfigVersion.SiteId.Equals(site.Id),
				query.ConfigVersion.Version.Equals(job.Version),
			).
			Set(query.ConfigVersion.Status.Set(model.ConfigStatusFAILED)).
			DoMany(ctx); err != nil {
			return publishOutcome(model.JobStatusFAILED, results, err, true)
		}
		return publishOutcome(
			model.JobStatusFAILED,
			results,
			errors.New("all nodes rejected the configuration"),
			false,
		)
	}

	rollbackVersion := job.Version + 1
	rollback := edgeprotocol.SiteConfig{
		SiteID:   site.Id,
		Version:  uint64(rollbackVersion),
		Disabled: true,
	}
	previous, previousErr := s.db.ConfigVersion.Query().
		Where(
			query.ConfigVersion.SiteId.Equals(site.Id),
			query.ConfigVersion.Status.Equals(model.ConfigStatusPUBLISHED),
		).
		OrderBy(query.ConfigVersion.Version.Desc()).
		First(ctx)
	if previousErr == nil {
		_ = json.Unmarshal(previous.ConfigJson, &rollback)
		rollback.Version = uint64(rollbackVersion)
	}

	rollbackResults := s.fanout(ctx, rollback, succeeded)
	for index := range results {
		for _, rolled := range rollbackResults {
			if results[index].NodeID == rolled.NodeID {
				results[index].RolledBack = rolled.Success
				if !rolled.Success {
					results[index].Error += "; rollback: " + rolled.Error
				}
			}
		}
	}

	rollbackJSON, _ := json.Marshal(rollback)
	rollbackHash := sha256.Sum256(rollbackJSON)
	rollbackComplete := allSucceeded(rollbackResults)
	if _, err = s.db.ConfigVersion.Update().
		Where(
			query.ConfigVersion.SiteId.Equals(site.Id),
			query.ConfigVersion.Version.Equals(job.Version),
		).
		Set(query.ConfigVersion.Status.Set(model.ConfigStatusFAILED)).
		DoMany(ctx); err != nil {
		s.recordRollback(ctx, site.Id, job.Version, rollbackVersion, results, rollbackResults, rollbackComplete, err)
		return publishOutcome(model.JobStatusFAILED, results, err, true)
	}

	rollbackStatus := model.ConfigStatusFAILED
	if rollbackComplete {
		rollbackStatus = model.ConfigStatusROLLED_BACK
	}

	if _, err = s.db.ConfigVersion.Create().
		Set(
			query.ConfigVersion.SiteId.Set(site.Id),
			query.ConfigVersion.Version.Set(rollbackVersion),
			query.ConfigVersion.ConfigJson.Set(rollbackJSON),
			query.ConfigVersion.Hash.Set(hex.EncodeToString(rollbackHash[:])),
			query.ConfigVersion.Status.Set(rollbackStatus),
		).
		Do(ctx); err != nil {
		s.recordRollback(ctx, site.Id, job.Version, rollbackVersion, results, rollbackResults, rollbackComplete, err)
		return publishOutcome(model.JobStatusFAILED, results, err, true)
	}

	if rollbackComplete {
		if _, err = s.db.Site.Update().
			Where(query.Site.Id.Equals(site.Id)).
			Set(query.Site.Version.Set(rollbackVersion)).
			Do(ctx); err != nil {
			s.recordRollback(ctx, site.Id, job.Version, rollbackVersion, results, rollbackResults, rollbackComplete, err)
			return publishOutcome(model.JobStatusFAILED, results, err, true)
		}
	}

	for _, rolled := range rollbackResults {
		if rolled.Success {
			_ = s.setNodeVersion(
				ctx, rolled.NodeID, site.Id, rollbackVersion, model.ConfigStatusROLLED_BACK,
			)
		}
	}

	message := "one or more nodes rejected the configuration; successful nodes were rolled back"
	if !rollbackComplete {
		message = "one or more nodes rejected the configuration; rollback was incomplete"
	}
	publishErr := errors.New(message)
	var rollbackErr error
	if !rollbackComplete {
		rollbackErr = publishErr
	}
	s.recordRollback(ctx, site.Id, job.Version, rollbackVersion, results, rollbackResults, rollbackComplete, rollbackErr)
	// An agent rejection is a business failure. Retrying the same immutable
	// version would repeat side effects and collide with the rollback version.
	return publishOutcome(model.JobStatusFAILED, results, publishErr, false)
}

func (s *Service) recordRollback(
	ctx context.Context,
	siteID string,
	failedVersion, rollbackVersion int64,
	publishResults, rollbackResults []targetResult,
	complete bool,
	rollbackErr error,
) {
	audit.RecordSystem(
		ctx, audit.New(s.db), "site.rollback", "site", siteID,
		map[string]any{"failed_version": failedVersion, "publish_results": publishResults},
		map[string]any{"rollback_version": rollbackVersion, "complete": complete, "rollback_results": rollbackResults},
		rollbackErr,
	)
}

func allSucceeded(results []targetResult) bool {
	for _, result := range results {
		if !result.Success {
			return false
		}
	}
	return true
}

func (s *Service) fanout(ctx context.Context, config edgeprotocol.SiteConfig, targets []target) []targetResult {
	results := make([]targetResult, len(targets))
	semaphore := make(chan struct{}, s.concurrency)
	var wg sync.WaitGroup
	for index, item := range targets {
		index, item := index, item
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case semaphore <- struct{}{}:
				defer func() { <-semaphore }()
			case <-ctx.Done():
				results[index] = targetResult{NodeID: item.NodeID, Error: ctx.Err().Error()}
				return
			}
			dispatchCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
			defer cancel()
			var applied edgecontrol.ApplySiteResult
			err := s.gateway.Dispatch(dispatchCtx, item.NodeID, edgeprotocol.TaskApplySiteConfig, config, &applied)
			results[index] = targetResult{NodeID: item.NodeID, Success: err == nil}
			if err != nil {
				results[index].Error = err.Error()
			}
		}()
	}
	wg.Wait()
	return results
}

func successfulTargets(results []targetResult, targets []target) ([]target, []targetResult) {
	byID := map[string]target{}
	for _, item := range targets {
		byID[item.NodeID] = item
	}
	success := make([]target, 0)
	failed := make([]targetResult, 0)
	for _, result := range results {
		if result.Success {
			success = append(success, byID[result.NodeID])
		} else {
			failed = append(failed, result)
		}
	}
	return success, failed
}

func publishOutcome(status model.JobStatus, results any, executionErr error, retryable bool) jobqueue.Outcome {
	result := map[string]any{"results": results, "error": errorString(executionErr)}
	var compensation any
	if targetResults, ok := results.([]targetResult); ok {
		for _, item := range targetResults {
			if item.RolledBack {
				retryable = false
				compensation = map[string]any{"rollback_results": targetResults}
				break
			}
		}
	}
	if status == model.JobStatusSUCCEEDED {
		retryable = false
	}
	return jobqueue.Outcome{Result: result, Compensation: compensation, Err: executionErr, Retryable: retryable}
}
func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func (s *Service) build(ctx context.Context, site *model.Site, version uint64) (edgeprotocol.SiteConfig, []target, error) {
	return s.buildWith(s.db, ctx, site, version)
}

func (s *Service) buildWith(db *client.Client, ctx context.Context, site *model.Site, version uint64) (edgeprotocol.SiteConfig, []target, error) {
	domains, err := db.SiteDomain.Query().
		Where(query.SiteDomain.SiteId.Equals(site.Id)).
		Do(ctx)
	if err != nil {
		return edgeprotocol.SiteConfig{}, nil, err
	}

	listener, err := db.SiteListenerConfig.FindUnique(ctx, query.SiteListenerConfig.SiteId.Equals(site.Id))
	if err != nil {
		return edgeprotocol.SiteConfig{}, nil, err
	}

	pool, err := db.OriginPool.FindUnique(ctx, query.OriginPool.Id.Equals(site.OriginPoolId))
	if err != nil {
		return edgeprotocol.SiteConfig{}, nil, err
	}

	backends, err := db.OriginBackend.Query().
		Where(
			query.OriginBackend.OriginPoolId.Equals(pool.Id),
			query.OriginBackend.Enabled.Equals(true),
		).
		Do(ctx)
	if err != nil {
		return edgeprotocol.SiteConfig{}, nil, err
	}

	config := edgeprotocol.SiteConfig{
		SiteID:    site.Id,
		Version:   version,
		Scheduler: pool.Scheduler,
		Listener: edgeprotocol.ListenerConfig{
			HTTPEnabled:           listener.HttpEnabled,
			HTTPPort:              listener.HttpPort,
			RedirectHTTPToHTTPS:   listener.RedirectHttpToHttps,
			HTTPSEnabled:          listener.HttpsEnabled,
			HTTPSPort:             listener.HttpsPort,
			HTTP2Enabled:          listener.Http2Enabled,
			HTTP3Enabled:          listener.Http3Enabled,
			TLSMinVersion:         string(listener.TlsMinVersion),
			HSTSEnabled:           listener.HstsEnabled,
			HSTSMaxAge:            listener.HstsMaxAge,
			HSTSIncludeSubdomains: listener.HstsIncludeSubdomains,
			HSTSPreload:           listener.HstsPreload,
			OCSPStaplingEnabled:   listener.OcspStaplingEnabled,
		},
	}
	config.OriginPolicy, err = edgeprotocol.DecodeOriginPolicy(pool.Governance, pool.Headers, pool.HealthUri, pool.Timeout)
	if err != nil {
		return edgeprotocol.SiteConfig{}, nil, fmt.Errorf("origin pool %s: %w", pool.Id, err)
	}
	for _, item := range domains {
		config.Domains = append(config.Domains, item.Hostname)
	}
	for _, item := range backends {
		config.Origins = append(config.Origins, edgeprotocol.OriginConfig{
			Protocol:   strings.ToLower(string(item.Protocol)),
			Address:    item.Address,
			HostHeader: value(item.HostHeader),
			Weight:     item.Weight,
			Priority:   item.Priority,
		})
	}

	links, err := db.SiteCertificate.Query().
		Where(query.SiteCertificate.SiteId.Equals(site.Id)).
		Do(ctx)
	if err != nil {
		return config, nil, err
	}
	for _, link := range links {
		certificate, err := db.Certificate.FindUnique(ctx, query.Certificate.Id.Equals(link.CertificateId))
		if err != nil {
			return config, nil, err
		}
		if certificate.CertPem == nil || certificate.ExpiresAt == nil || !time.Now().UTC().Before(*certificate.ExpiresAt) {
			return config, nil, fmt.Errorf("certificate %s is unavailable or expired", certificate.Id)
		}
		certificateDomains, err := certmanager.DecodeDomains(certificate.DomainsJson)
		if err != nil {
			return config, nil, err
		}
		if err = certmanager.CoversDomains(certificateDomains, config.Domains); err != nil {
			return config, nil, fmt.Errorf("certificate %s: %w", certificate.Id, err)
		}
		privateKey, err := certmanager.DecryptPrivateKey(s.cipher, certificate)
		if err != nil {
			return config, nil, err
		}
		config.Certificates = append(config.Certificates, edgeprotocol.CertificateConfig{
			CertificatePEM: *certificate.CertPem,
			PrivateKeyPEM:  privateKey,
		})
	}
	if len(config.Domains) > 0 {
		challenges, challengeErr := db.ACMEChallenge.Query().Where(
			query.ACMEChallenge.ClusterId.Equals(site.ClusterId),
			query.ACMEChallenge.Type.Equals(model.ACMEChallengeTypeHTTP_01),
			query.ACMEChallenge.Status.Equals(model.ACMEChallengeStatusPRESENTED),
			query.ACMEChallenge.Domain.In(config.Domains...),
			query.ACMEChallenge.ExpiresAt.Gt(time.Now().UTC()),
		).Do(ctx)
		if challengeErr != nil {
			return config, nil, challengeErr
		}
		for _, challenge := range challenges {
			if challenge.KeyAuth != nil {
				config.ACMEChallenges = append(config.ACMEChallenges, edgeprotocol.ACMEChallengeConfig{
					Domain: challenge.Domain, Token: challenge.Token, KeyAuth: *challenge.KeyAuth,
				})
			}
		}
	}
	normalizeListenerForCertificates(&config)

	if site.PolicyId != nil {
		policy, err := db.Policy.FindUnique(ctx, query.Policy.Id.Equals(*site.PolicyId))
		if err != nil {
			return config, nil, err
		}
		if err = json.Unmarshal(policy.CacheJson, &config.Cache); err != nil {
			return config, nil, fmt.Errorf("decode cache policy: %w", err)
		}
		if err = json.Unmarshal(policy.CompressionJson, &config.Compression); err != nil {
			return config, nil, fmt.Errorf("decode compression policy: %w", err)
		}
		if err = json.Unmarshal(policy.WafJson, &config.WAF); err != nil {
			return config, nil, fmt.Errorf("decode WAF policy: %w", err)
		}
		if config.WAF == nil {
			config.WAF = map[string]any{}
		}
		config.WAF["challenge_secret"] = wafChallengeSecret(s.cipher, site.Id)
		if err = json.Unmarshal(policy.AccessJson, &config.Access); err != nil {
			return config, nil, fmt.Errorf("decode access policy: %w", err)
		}
		if err = json.Unmarshal(policy.CcJson, &config.RateLimit); err != nil {
			return config, nil, fmt.Errorf("decode rate-limit policy: %w", err)
		}
	}

	nodes, err := db.Node.Query().
		Where(
			query.Node.ClusterId.Equals(site.ClusterId),
			query.Node.Status.Equals(model.NodeStatusONLINE),
		).
		Do(ctx)
	if err != nil {
		return config, nil, err
	}

	targets := make([]target, 0, len(nodes))
	for _, n := range nodes {
		credential, credentialErr := db.NodeCredential.FindUnique(ctx, query.NodeCredential.NodeId.Equals(n.Id))
		if credentialErr != nil {
			return config, nil, errors.New("node " + n.Id + " has no management credential")
		}
		if credential == nil || credential.RevokedAt != nil {
			continue
		}
		targets = append(targets, target{NodeID: n.Id})
	}
	if len(targets) == 0 {
		return config, nil, errors.New("cluster has no publishable nodes")
	}
	return config, targets, nil
}

func wafChallengeSecret(cipher *node.CredentialCipher, siteID string) string {
	return base64.RawURLEncoding.EncodeToString(
		cipher.Derive("goveto-edge/waf-challenge/v2\x00" + siteID),
	)
}

// Sites can be created without a certificate. Keep those sites reachable over
// HTTP instead of sending an invalid HTTPS configuration that the agent rejects.
func normalizeListenerForCertificates(config *edgeprotocol.SiteConfig) {
	if len(config.Certificates) > 0 || !config.Listener.HTTPSEnabled {
		return
	}
	config.Listener.HTTPEnabled = true
	config.Listener.RedirectHTTPToHTTPS = false
	config.Listener.HTTPSEnabled = false
	config.Listener.HTTP2Enabled = false
	config.Listener.HTTP3Enabled = false
	config.Listener.HSTSEnabled = false
	config.Listener.HSTSIncludeSubdomains = false
	config.Listener.HSTSPreload = false
	config.Listener.OCSPStaplingEnabled = false
}

func value(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func (s *Service) setNodeVersion(ctx context.Context, nodeID, siteID string, version int64, status model.ConfigStatus) error {
	_, err := s.db.RawExec(ctx, `INSERT INTO node_site_config_versions (node_id, site_id, version, status, updated_at)
		VALUES ($1, $2, $3, $4, NOW())
		ON CONFLICT (node_id, site_id) DO UPDATE
		SET version = EXCLUDED.version, status = EXCLUDED.status, updated_at = NOW()`, nodeID, siteID, version, status)
	return err
}
