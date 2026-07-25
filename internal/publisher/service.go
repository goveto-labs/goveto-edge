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

type Service struct {
	db          *client.Client
	cipher      *node.CredentialCipher
	gateway     *edgecontrol.Gateway
	concurrency int
}

func New(db *client.Client, cipher *node.CredentialCipher, gateway *edgecontrol.Gateway) *Service {
	return &Service{db: db, cipher: cipher, gateway: gateway, concurrency: 8}
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
	var job *model.PublishJob
	err := s.db.Tx(ctx, func(tx *client.Client) error {
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

			job, err = tx.PublishJob.Update().
				Where(
					query.PublishJob.Id.Equals(pending.Id),
					query.PublishJob.Status.Equals(model.JobStatusPENDING),
				).
				Set(query.PublishJob.Targets.Set(targetJSON)).
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

		job, err = tx.PublishJob.Create().
			Set(
				query.PublishJob.SiteId.Set(siteID),
				query.PublishJob.Version.Set(version),
				query.PublishJob.Targets.Set(targetJSON),
				query.PublishJob.Status.Set(model.JobStatusPENDING),
			).
			Do(ctx)
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
	jobs, err := client.Raw[model.PublishJob](ctx, s.db, `UPDATE publish_jobs SET status = 'RUNNING', updated_at = NOW()
		WHERE id = (SELECT id FROM publish_jobs WHERE status = 'PENDING' ORDER BY created_at FOR UPDATE SKIP LOCKED LIMIT 1)
		RETURNING id, site_id, version, targets, status, result_json, created_at, updated_at`)
	if err != nil || len(jobs) == 0 {
		return
	}
	job := &jobs[0]
	_ = s.execute(ctx, job)
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

func (s *Service) execute(ctx context.Context, job *model.PublishJob) error {
	site, err := s.db.Site.FindUnique(ctx, query.Site.Id.Equals(job.SiteId))
	if err != nil {
		return s.finish(ctx, job.Id, model.JobStatusFAILED, nil, err)
	}

	version, err := s.db.ConfigVersion.Query().
		Where(
			query.ConfigVersion.SiteId.Equals(job.SiteId),
			query.ConfigVersion.Version.Equals(job.Version),
		).
		First(ctx)
	if err != nil {
		return s.finish(ctx, job.Id, model.JobStatusFAILED, nil, err)
	}

	var config edgeprotocol.SiteConfig
	var targets []target
	if err = json.Unmarshal(version.ConfigJson, &config); err != nil {
		return s.finish(ctx, job.Id, model.JobStatusFAILED, nil, err)
	}
	if err = json.Unmarshal(job.Targets, &targets); err != nil {
		return s.finish(ctx, job.Id, model.JobStatusFAILED, nil, err)
	}
	results := s.fanout(ctx, config, targets)
	succeeded, failed := successfulTargets(results, targets)
	if len(failed) == 0 {
		if _, err = s.db.Site.Update().
			Where(query.Site.Id.Equals(site.Id)).
			Set(query.Site.Version.Set(job.Version)).
			Do(ctx); err != nil {
			return s.finish(ctx, job.Id, model.JobStatusFAILED, results, err)
		}

		if _, err = s.db.ConfigVersion.Update().
			Where(
				query.ConfigVersion.SiteId.Equals(site.Id),
				query.ConfigVersion.Version.Equals(job.Version),
			).
			Set(query.ConfigVersion.Status.Set(model.ConfigStatusPUBLISHED)).
			DoMany(ctx); err != nil {
			return s.finish(ctx, job.Id, model.JobStatusFAILED, results, err)
		}

		for _, item := range succeeded {
			if err = s.setNodeVersion(
				ctx, item.NodeID, site.Id, job.Version, model.ConfigStatusPUBLISHED,
			); err != nil {
				return s.finish(ctx, job.Id, model.JobStatusFAILED, results, err)
			}
		}
		return s.finish(ctx, job.Id, model.JobStatusSUCCEEDED, results, nil)
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
	if _, err = s.db.ConfigVersion.Update().
		Where(
			query.ConfigVersion.SiteId.Equals(site.Id),
			query.ConfigVersion.Version.Equals(job.Version),
		).
		Set(query.ConfigVersion.Status.Set(model.ConfigStatusFAILED)).
		DoMany(ctx); err != nil {
		return s.finish(ctx, job.Id, model.JobStatusFAILED, results, err)
	}

	rollbackComplete := allSucceeded(rollbackResults)
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
		return s.finish(ctx, job.Id, model.JobStatusFAILED, results, err)
	}

	if rollbackComplete {
		if _, err = s.db.Site.Update().
			Where(query.Site.Id.Equals(site.Id)).
			Set(query.Site.Version.Set(rollbackVersion)).
			Do(ctx); err != nil {
			return s.finish(ctx, job.Id, model.JobStatusFAILED, results, err)
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
	return s.finish(ctx, job.Id, model.JobStatusFAILED, results, errors.New(message))
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

func (s *Service) finish(ctx context.Context, jobID string, status model.JobStatus, results any, executionErr error) error {
	payload, _ := json.Marshal(map[string]any{"results": results, "error": errorString(executionErr)})
	_, err := s.db.PublishJob.Update().
		Where(query.PublishJob.Id.Equals(jobID)).
		Set(
			query.PublishJob.Status.Set(status),
			query.PublishJob.ResultJson.Set(payload),
		).
		Do(ctx)
	if err != nil {
		return err
	}
	return executionErr
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
	for _, item := range domains {
		config.Domains = append(config.Domains, item.Hostname)
	}
	for _, item := range backends {
		config.Origins = append(config.Origins, edgeprotocol.OriginConfig{
			Protocol:   strings.ToLower(string(item.Protocol)),
			Address:    item.Address,
			HostHeader: value(item.HostHeader),
			Weight:     item.Weight,
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
		config.Certificates = append(config.Certificates, edgeprotocol.CertificateConfig{
			CertificatePEM: certificate.CertPem,
			PrivateKeyPEM:  certificate.PrivateKeyPem,
		})
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
