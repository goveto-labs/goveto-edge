package certmanager

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/mholt/acmez/v3"
	"github.com/mholt/acmez/v3/acme"

	"goveto-edge/internal/audit"
	"goveto-edge/internal/dnsprovider"
	"goveto-edge/internal/jobqueue"
	"goveto-edge/internal/node"
	"goveto-edge/internal/outboundhttp"
	"goveto-edge/internal/storage/gen/client"
	"goveto-edge/internal/storage/gen/model"
	"goveto-edge/internal/storage/gen/query"
)

const reconcileTerminalCertificateJobsSQL = `WITH latest AS (
	SELECT DISTINCT ON (certificate_id) certificate_id, operation, status, error FROM certificate_jobs
	ORDER BY certificate_id, updated_at DESC
) UPDATE certificates c SET status=(CASE WHEN j.operation='REVOKE' THEN
	CASE WHEN c.revoked_at IS NOT NULL THEN 'REVOKED' ELSE 'REVOCATION_FAILED' END
	WHEN j.status='CANCELLED' THEN
	CASE WHEN c.expires_at IS NULL THEN 'PENDING'
		WHEN c.expires_at<=NOW() THEN 'EXPIRED'
		WHEN c.expires_at<=NOW()+(c.renew_before_days*INTERVAL '1 day') THEN 'EXPIRING'
		ELSE c.status::text END
	ELSE 'RENEWAL_FAILED' END)::"CertificateStatus",
	last_renewal_error=CASE WHEN j.operation='REVOKE' OR j.status='CANCELLED' THEN NULL
		ELSE COALESCE(j.error, 'certificate lifecycle job ended without completing') END,
	last_revocation_error=CASE WHEN j.operation='REVOKE' THEN
		COALESCE(j.error, c.last_revocation_error, 'certificate revocation job ended without completing')
		ELSE c.last_revocation_error END,
	updated_at=NOW() FROM latest j WHERE c.id=j.certificate_id
	AND j.status IN ('FAILED','DEAD_LETTER','CANCELLED')
	AND c.status IN ('PENDING','DEPLOYING','REVOKING') AND NOT EXISTS (
		SELECT 1 FROM certificate_jobs active WHERE active.certificate_id=c.id
		AND active.status IN ('PENDING','RUNNING'))`

const defaultACMEDirectory = "https://acme-v02.api.letsencrypt.org/directory"

const maxACMEResponseBytes = 2 << 20

var ErrCertificateMustBeRevoked = errors.New("issued ACME certificate must be revoked before deletion")
var ErrCertificateNotFound = errors.New("certificate not found")
var ErrCertificateNotRevocable = errors.New("certificate cannot be revoked through ACME")
var ErrCertificateAlreadyRevoked = errors.New("certificate has already been revoked")
var ErrCertificateLifecycleBusy = errors.New("certificate already has an active lifecycle operation")

var revocationReasons = map[string]int{
	"UNSPECIFIED":            acme.ReasonUnspecified,
	"KEY_COMPROMISE":         acme.ReasonKeyCompromise,
	"CA_COMPROMISE":          acme.ReasonCACompromise,
	"AFFILIATION_CHANGED":    acme.ReasonAffiliationChanged,
	"SUPERSEDED":             acme.ReasonSuperseded,
	"CESSATION_OF_OPERATION": acme.ReasonCessationOfOperation,
	"CERTIFICATE_HOLD":       acme.ReasonCertificateHold,
	"PRIVILEGE_WITHDRAWN":    acme.ReasonPrivilegeWithdrawn,
	"AA_COMPROMISE":          acme.ReasonAACompromise,
}

func ParseRevocationReason(value string) (int, error) {
	reason, ok := revocationReasons[strings.ToUpper(strings.TrimSpace(value))]
	if !ok {
		return 0, errors.New("invalid revocation reason")
	}
	return reason, nil
}

type Publisher interface {
	Enqueue(context.Context, string) (*model.PublishJob, error)
}

type Service struct {
	db             *client.Client
	cipher         *node.CredentialCipher
	publisher      Publisher
	jobs           *jobqueue.Manager
	outboundPolicy *outboundhttp.Policy
	acmeHTTPClient *http.Client
	httpState      sync.Map
}

func New(db *client.Client, cipher *node.CredentialCipher, publisher Publisher) *Service {
	policy := outboundhttp.NewPolicy()
	return &Service{
		db: db, cipher: cipher, publisher: publisher, jobs: jobqueue.New(db),
		outboundPolicy: policy, acmeHTTPClient: newACMEHTTPClient(policy),
	}
}

type acmeRoundTripper struct {
	policy *outboundhttp.Policy
	next   http.RoundTripper
}

func (transport acmeRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	if err := transport.policy.ValidateURL(request.Context(), request.URL, "https"); err != nil {
		return nil, fmt.Errorf("validate ACME endpoint: %w", err)
	}
	response, err := transport.next.RoundTrip(request)
	if err != nil {
		return nil, err
	}
	response.Body = http.MaxBytesReader(nil, response.Body, maxACMEResponseBytes)
	return response, nil
}

func newACMEHTTPClient(policy *outboundhttp.Policy) *http.Client {
	httpClient := policy.Client()
	httpClient.Transport = acmeRoundTripper{policy: policy, next: httpClient.Transport}
	return httpClient
}

// ValidateACMEDirectory rejects custom ACME servers that are not public HTTPS destinations.
func (s *Service) ValidateACMEDirectory(ctx context.Context, directory string) error {
	target, err := url.Parse(strings.TrimSpace(directory))
	if err != nil {
		return fmt.Errorf("parse ACME directory URL: %w", err)
	}
	if err = s.outboundPolicy.ValidateURL(ctx, target, "https"); err != nil {
		return fmt.Errorf("validate ACME directory URL: %w", err)
	}
	return nil
}

func (s *Service) acmeClient(ctx context.Context, directory string) (*acme.Client, error) {
	if err := s.ValidateACMEDirectory(ctx, directory); err != nil {
		return nil, err
	}
	return &acme.Client{Directory: directory, HTTPClient: s.acmeHTTPClient}, nil
}

func (s *Service) EncryptPrivateKey(clusterID, certificateID, privateKey string) (string, error) {
	return EncryptPrivateKey(s.cipher, clusterID, certificateID, privateKey)
}

func (s *Service) RewrapSecrets(ctx context.Context) error {
	certificates, err := s.db.Certificate.Query().Do(ctx)
	if err != nil {
		return err
	}
	for index := range certificates {
		if certificates[index].PrivateKeyEncrypted == "" {
			continue
		}
		wrapped, changed, rewrapErr := RewrapPrivateKey(s.cipher, &certificates[index])
		if rewrapErr != nil {
			return fmt.Errorf("rewrap certificate %s: %w", certificates[index].Id, rewrapErr)
		}
		if changed {
			if _, err = s.db.Certificate.Update().Where(query.Certificate.Id.Equals(certificates[index].Id)).Set(query.Certificate.PrivateKeyEncrypted.Set(wrapped)).Do(ctx); err != nil {
				return err
			}
		}
	}
	accounts, err := s.db.ACMEAccount.Query().Do(ctx)
	if err != nil {
		return err
	}
	for index := range accounts {
		account := &accounts[index]
		wrapped, changed, rewrapErr := s.cipher.RewrapScoped(accountScope(account.ClusterId, account.DirectoryUrl, account.Email), account.PrivateKeyEncrypted)
		if rewrapErr != nil {
			return fmt.Errorf("rewrap ACME account %s: %w", account.Id, rewrapErr)
		}
		if changed {
			if _, err = s.db.ACMEAccount.Update().Where(query.ACMEAccount.Id.Equals(account.Id)).Set(query.ACMEAccount.PrivateKeyEncrypted.Set(wrapped)).Do(ctx); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Service) Run(ctx context.Context) {
	go s.reconcileTerminalJobs(ctx)
	jobTicker := time.NewTicker(2 * time.Second)
	lifecycleTicker := time.NewTicker(time.Hour)
	defer jobTicker.Stop()
	defer lifecycleTicker.Stop()
	s.reconcileLifecycle(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-jobTicker.C:
			s.runOne(ctx)
		case <-lifecycleTicker.C:
			s.reconcileLifecycle(ctx)
		}
	}
}

func (s *Service) reconcileTerminalJobs(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		_, err := s.db.RawExec(ctx, reconcileTerminalCertificateJobsSQL)
		if err != nil && ctx.Err() == nil {
			slog.Warn("reconcile terminal certificate jobs", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Service) Enqueue(ctx context.Context, certificateID string, operation model.CertificateOperation) (*model.CertificateJob, error) {
	job, _, err := s.enqueue(ctx, certificateID, operation)
	return job, err
}

func (s *Service) enqueue(ctx context.Context, certificateID string, operation model.CertificateOperation) (*model.CertificateJob, bool, error) {
	var job *model.CertificateJob
	created := false
	err := s.db.Tx(ctx, func(tx *client.Client) error {
		if _, err := tx.RawExec(ctx, "SELECT pg_advisory_xact_lock(hashtext($1))", "certificate-job:"+certificateID); err != nil {
			return err
		}
		active, err := tx.CertificateJob.Query().Where(
			query.CertificateJob.CertificateId.Equals(certificateID),
			query.CertificateJob.Status.In(model.JobStatusPENDING, model.JobStatusRUNNING),
		).OrderBy(query.CertificateJob.CreatedAt.Desc()).First(ctx)
		if err != nil {
			return err
		}
		if active != nil {
			if active.Operation != operation {
				return fmt.Errorf("%w: active %s operation", ErrCertificateLifecycleBusy, active.Operation)
			}
			job = active
			return nil
		}
		job, err = tx.CertificateJob.Create().Set(
			query.CertificateJob.CertificateId.Set(certificateID),
			query.CertificateJob.Operation.Set(operation),
		).Do(ctx)
		created = err == nil
		return err
	})
	return job, created, err
}

func (s *Service) EnqueueRevocation(ctx context.Context, certificateID string, reason int) (*model.CertificateJob, error) {
	var job *model.CertificateJob
	err := s.db.Tx(ctx, func(tx *client.Client) error {
		if _, err := tx.RawExec(ctx, "SELECT pg_advisory_xact_lock(hashtext($1))", "certificate-job:"+certificateID); err != nil {
			return err
		}
		certificate, err := tx.Certificate.FindUnique(ctx, query.Certificate.Id.Equals(certificateID))
		if err != nil {
			return err
		}
		if certificate == nil {
			return ErrCertificateNotFound
		}
		if certificate.Source != model.CertificateSourceACME || certificate.CertPem == nil {
			return ErrCertificateNotRevocable
		}
		if certificate.RevokedAt != nil {
			linked, err := tx.SiteCertificate.Query().Where(query.SiteCertificate.CertificateId.Equals(certificateID)).Count(ctx)
			if err != nil {
				return err
			}
			if linked == 0 {
				return ErrCertificateAlreadyRevoked
			}
			// ACME revocation already landed but site cleanup did not; allow
			// the idempotent re-entry below to finish removing the links.
		}
		active, err := tx.CertificateJob.Query().Where(
			query.CertificateJob.CertificateId.Equals(certificateID),
			query.CertificateJob.Status.In(model.JobStatusPENDING, model.JobStatusRUNNING),
		).OrderBy(query.CertificateJob.CreatedAt.Desc()).First(ctx)
		if err != nil {
			return err
		}
		if active != nil {
			if active.Operation != model.CertificateOperationREVOKE {
				return fmt.Errorf("%w: active %s operation", ErrCertificateLifecycleBusy, active.Operation)
			}
			job = active
			return nil
		}
		if _, err = tx.Certificate.Update().Where(query.Certificate.Id.Equals(certificateID)).Set(
			query.Certificate.RevocationReason.Set(reason),
			query.Certificate.Status.Set(model.CertificateStatusREVOKING),
			query.Certificate.LastRevocationError.SetNull(),
		).Do(ctx); err != nil {
			return err
		}
		job, err = tx.CertificateJob.Create().Set(
			query.CertificateJob.CertificateId.Set(certificateID),
			query.CertificateJob.Operation.Set(model.CertificateOperationREVOKE),
		).Do(ctx)
		return err
	})
	return job, err
}

func (s *Service) Delete(ctx context.Context, certificateID string) error {
	certificate, err := s.db.Certificate.FindUnique(ctx, query.Certificate.Id.Equals(certificateID))
	if err != nil {
		return err
	}
	if certificate == nil {
		return errors.New("certificate not found")
	}
	if certificate.Source == model.CertificateSourceACME && certificate.CertPem != nil && certificate.RevokedAt == nil {
		return ErrCertificateMustBeRevoked
	}
	active, err := s.db.CertificateJob.Query().Where(
		query.CertificateJob.CertificateId.Equals(certificateID),
		query.CertificateJob.Status.In(model.JobStatusPENDING, model.JobStatusRUNNING),
	).First(ctx)
	if err != nil {
		return err
	}
	if active != nil {
		return errors.New("certificate has an active lifecycle job")
	}
	links, err := s.db.SiteCertificate.Query().Where(query.SiteCertificate.CertificateId.Equals(certificateID)).Do(ctx)
	if err != nil {
		return err
	}
	if _, err = s.db.SiteCertificate.Delete().Where(query.SiteCertificate.CertificateId.Equals(certificateID)).DoMany(ctx); err != nil {
		return err
	}
	jobs := make([]string, 0, len(links))
	for _, link := range links {
		job, enqueueErr := s.publisher.Enqueue(ctx, link.SiteId)
		if enqueueErr != nil {
			err = enqueueErr
			break
		}
		jobs = append(jobs, job.Id)
	}
	if err == nil {
		err = s.waitPublishJobs(ctx, jobs, 5*time.Minute)
	}
	if err != nil {
		for _, link := range links {
			_, _ = s.db.SiteCertificate.Create().Set(query.SiteCertificate.SiteId.Set(link.SiteId), query.SiteCertificate.CertificateId.Set(link.CertificateId)).Do(ctx)
		}
		for _, link := range links {
			_, _ = s.publisher.Enqueue(ctx, link.SiteId)
		}
		return fmt.Errorf("remove certificate publication: %w", err)
	}
	_, err = s.db.Certificate.Delete().Where(query.Certificate.Id.Equals(certificateID)).DoMany(ctx)
	return err
}

func (s *Service) HTTPChallenge(ctx context.Context, token string) (string, bool, error) {
	item, err := s.db.ACMEChallenge.Query().Where(
		query.ACMEChallenge.Token.Equals(token), query.ACMEChallenge.Type.Equals(model.ACMEChallengeTypeHTTP_01),
		query.ACMEChallenge.Status.Equals(model.ACMEChallengeStatusPRESENTED), query.ACMEChallenge.ExpiresAt.Gt(time.Now().UTC()),
	).First(ctx)
	if err != nil || item == nil || item.KeyAuth == nil {
		return "", false, err
	}
	return *item.KeyAuth, true, nil
}

func (s *Service) runOne(ctx context.Context) {
	_, err := s.jobs.RunOne(ctx, jobqueue.Certificate, time.Hour, func(runCtx context.Context, lease jobqueue.Lease) jobqueue.Outcome {
		job, loadErr := s.db.CertificateJob.FindUnique(runCtx, query.CertificateJob.Id.Equals(lease.ID))
		if loadErr != nil || job == nil {
			if loadErr == nil {
				loadErr = errors.New("certificate job not found")
			}
			return jobqueue.Outcome{Err: loadErr, Retryable: true}
		}
		executionErr := s.execute(runCtx, job)
		if executionErr != nil {
			slog.Warn("certificate lifecycle job", "job_id", job.Id, "certificate_id", job.CertificateId, "error", executionErr)
		}
		return jobqueue.Outcome{
			Result: map[string]any{"completed_at": time.Now().UTC()}, Err: executionErr,
			Retryable: executionErr != nil, RetryAfter: certificateRetryDelay(lease.Attempt),
		}
	})
	if err != nil && !errors.Is(err, context.Canceled) {
		slog.Warn("certificate job worker", "error", err)
	}
}

func certificateRetryDelay(attempt int) time.Duration {
	return time.Duration(1<<min(attempt, 5)) * time.Minute
}

func (s *Service) execute(ctx context.Context, job *model.CertificateJob) error {
	certificate, err := s.db.Certificate.FindUnique(ctx, query.Certificate.Id.Equals(job.CertificateId))
	if err != nil || certificate == nil {
		if err == nil {
			err = errors.New("certificate not found")
		}
		return err
	}
	if job.Operation == model.CertificateOperationREPUBLISH {
		return s.publishCertificate(ctx, certificate)
	}
	if job.Operation == model.CertificateOperationREVOKE {
		return s.revokeCertificate(ctx, certificate)
	}
	if certificate.Source != model.CertificateSourceACME {
		return errors.New("only ACME certificates can be issued or renewed")
	}
	now := time.Now().UTC()
	_, _ = s.db.Certificate.Update().Where(query.Certificate.Id.Equals(certificate.Id)).Set(
		query.Certificate.Status.Set(model.CertificateStatusPENDING),
		query.Certificate.LastRenewalAttemptAt.Set(now),
	).Do(ctx)

	material, err := s.obtainACME(ctx, certificate)
	if err != nil {
		return err
	}
	if err = s.validateAttachedDomains(ctx, certificate.Id, material.Domains); err != nil {
		return err
	}
	if err = s.storeMaterial(ctx, certificate, material, model.CertificateStatusDEPLOYING); err != nil {
		return err
	}
	updated, err := s.db.Certificate.FindUnique(ctx, query.Certificate.Id.Equals(certificate.Id))
	if err != nil {
		return err
	}
	if err = s.publishCertificate(ctx, updated); err != nil {
		return err
	}
	return nil
}

func (s *Service) revokeCertificate(ctx context.Context, certificate *model.Certificate) error {
	if certificate.Source != model.CertificateSourceACME {
		return errors.New("only ACME certificates can be revoked through ACME")
	}
	if certificate.CertPem == nil || strings.TrimSpace(*certificate.CertPem) == "" {
		return errors.New("certificate material is unavailable")
	}
	reason := acme.ReasonUnspecified
	if certificate.RevocationReason != nil {
		reason = *certificate.RevocationReason
	}
	now := time.Now().UTC()
	_, _ = s.db.Certificate.Update().Where(query.Certificate.Id.Equals(certificate.Id)).Set(
		query.Certificate.Status.Set(model.CertificateStatusREVOKING),
		query.Certificate.LastRevocationAttemptAt.Set(now),
		query.Certificate.LastRevocationError.SetNull(),
	).Do(ctx)

	if certificate.RevokedAt == nil {
		certificates, err := ParsePEMCertificates(*certificate.CertPem)
		if err != nil {
			return s.recordRevocationError(ctx, certificate.Id, err)
		}
		privateKeyPEM, err := DecryptPrivateKey(s.cipher, certificate)
		if err != nil {
			return s.recordRevocationError(ctx, certificate.Id, err)
		}
		pair, err := tls.X509KeyPair([]byte(*certificate.CertPem), []byte(privateKeyPEM))
		if err != nil {
			return s.recordRevocationError(ctx, certificate.Id, errors.New("certificate private key is invalid"))
		}
		signer, ok := pair.PrivateKey.(crypto.Signer)
		if !ok {
			return s.recordRevocationError(ctx, certificate.Id, errors.New("certificate private key cannot sign revocation request"))
		}
		directory := defaultACMEDirectory
		if certificate.AcmeDirectoryUrl != nil && strings.TrimSpace(*certificate.AcmeDirectoryUrl) != "" {
			directory = strings.TrimSpace(*certificate.AcmeDirectoryUrl)
		}
		acmeClient, err := s.acmeClient(ctx, directory)
		if err != nil {
			return s.recordRevocationError(ctx, certificate.Id, err)
		}
		err = revokeACMECertificate(ctx, acmeClient, certificates[0], signer, reason)
		if err != nil {
			return s.recordRevocationError(ctx, certificate.Id, fmt.Errorf("revoke ACME certificate: %w", err))
		}
		certificate.RevokedAt = &now
		_, err = s.db.Certificate.Update().Where(query.Certificate.Id.Equals(certificate.Id)).Set(
			query.Certificate.Status.Set(model.CertificateStatusREVOKED),
			query.Certificate.RevokedAt.Set(now),
			query.Certificate.RevocationReason.Set(reason),
			query.Certificate.AutoRenew.Set(false),
			query.Certificate.LastRevocationError.SetNull(),
		).Do(ctx)
		if err != nil {
			return err
		}
	}

	links, err := s.db.SiteCertificate.Query().Where(query.SiteCertificate.CertificateId.Equals(certificate.Id)).Do(ctx)
	if err != nil {
		return s.recordRevocationError(ctx, certificate.Id, err)
	}
	jobs := make([]string, 0, len(links))
	for _, link := range links {
		job, enqueueErr := s.publisher.Enqueue(ctx, link.SiteId)
		if enqueueErr != nil {
			return s.recordRevocationError(ctx, certificate.Id, enqueueErr)
		}
		jobs = append(jobs, job.Id)
	}
	if err = s.waitPublishJobs(ctx, jobs, 5*time.Minute); err != nil {
		return s.recordRevocationError(ctx, certificate.Id, fmt.Errorf("remove revoked certificate from sites: %w", err))
	}
	if _, err = s.db.SiteCertificate.Delete().Where(query.SiteCertificate.CertificateId.Equals(certificate.Id)).DoMany(ctx); err != nil {
		return s.recordRevocationError(ctx, certificate.Id, err)
	}
	_, err = s.db.Certificate.Update().Where(query.Certificate.Id.Equals(certificate.Id)).Set(
		query.Certificate.Status.Set(model.CertificateStatusREVOKED),
		query.Certificate.LastRevocationError.SetNull(),
	).Do(ctx)
	return err
}

func revokeACMECertificate(ctx context.Context, client *acme.Client, certificate *x509.Certificate, key crypto.Signer, reason int) error {
	err := client.RevokeCertificate(ctx, acme.Account{}, certificate, key, reason)
	var problem acme.Problem
	if errors.As(err, &problem) && problem.Type == acme.ProblemTypeAlreadyRevoked {
		return nil
	}
	return err
}

func (s *Service) recordRevocationError(ctx context.Context, certificateID string, err error) error {
	_, _ = s.db.Certificate.Update().Where(query.Certificate.Id.Equals(certificateID)).Set(
		query.Certificate.LastRevocationError.Set(err.Error()),
	).Do(ctx)
	return err
}

func (s *Service) StoreManual(ctx context.Context, certificate *model.Certificate, material Material) error {
	if err := s.validateAttachedDomains(ctx, certificate.Id, material.Domains); err != nil {
		return err
	}
	if err := s.storeMaterial(ctx, certificate, material, model.CertificateStatusDEPLOYING); err != nil {
		return err
	}
	updated, err := s.db.Certificate.FindUnique(ctx, query.Certificate.Id.Equals(certificate.Id))
	if err != nil {
		return err
	}
	return s.publishCertificate(ctx, updated)
}

func (s *Service) validateAttachedDomains(ctx context.Context, certificateID string, certificateDomains []string) error {
	links, err := s.db.SiteCertificate.Query().Where(query.SiteCertificate.CertificateId.Equals(certificateID)).Do(ctx)
	if err != nil {
		return err
	}
	for _, link := range links {
		domains, queryErr := s.db.SiteDomain.Query().Where(query.SiteDomain.SiteId.Equals(link.SiteId)).Do(ctx)
		if queryErr != nil {
			return queryErr
		}
		names := make([]string, 0, len(domains))
		for _, domain := range domains {
			names = append(names, domain.Hostname)
		}
		if coverErr := CoversDomains(certificateDomains, names); coverErr != nil {
			return fmt.Errorf("site %s: %w", link.SiteId, coverErr)
		}
	}
	return nil
}

func (s *Service) storeMaterial(ctx context.Context, certificate *model.Certificate, material Material, status model.CertificateStatus) error {
	encrypted, err := EncryptPrivateKey(s.cipher, certificate.ClusterId, certificate.Id, material.PrivateKeyPEM)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	_, err = s.db.Certificate.Update().Where(query.Certificate.Id.Equals(certificate.Id)).Set(
		query.Certificate.Status.Set(status), query.Certificate.CertPem.Set(material.CertificatePEM),
		query.Certificate.PrivateKeyEncrypted.Set(encrypted),
		query.Certificate.Fingerprint.Set(material.Fingerprint), query.Certificate.SerialNumber.Set(material.SerialNumber),
		query.Certificate.DomainsJson.Set(EncodeDomains(material.Domains)),
		query.Certificate.NotBefore.Set(material.NotBefore), query.Certificate.ExpiresAt.Set(material.ExpiresAt),
		query.Certificate.Issuer.Set(material.Issuer), query.Certificate.KeyAlgorithm.Set(material.KeyAlgorithm),
		query.Certificate.LastIssuedAt.Set(now), query.Certificate.LastRenewalError.SetNull(),
	).Do(ctx)
	return err
}

func (s *Service) publishCertificate(ctx context.Context, certificate *model.Certificate) error {
	jobs, err := s.enqueueSites(ctx, certificate.Id)
	if err == nil {
		err = s.waitPublishJobs(ctx, jobs, 5*time.Minute)
	}
	if err == nil {
		now := time.Now().UTC()
		_, updateErr := s.db.Certificate.Update().Where(query.Certificate.Id.Equals(certificate.Id)).Set(
			query.Certificate.Status.Set(statusAt(certificate.ExpiresAt, certificate.RenewBeforeDays, now)),
			query.Certificate.LastPublishedAt.Set(now), query.Certificate.LastPublishError.SetNull(),
		).Do(ctx)
		return updateErr
	}
	_, _ = s.db.Certificate.Update().Where(query.Certificate.Id.Equals(certificate.Id)).Set(
		query.Certificate.Status.Set(model.CertificateStatusDEPLOYMENT_FAILED),
		query.Certificate.LastPublishError.Set(err.Error()),
	).Do(ctx)
	return err
}

func (s *Service) enqueueSites(ctx context.Context, certificateID string) ([]string, error) {
	links, err := s.db.SiteCertificate.Query().Where(query.SiteCertificate.CertificateId.Equals(certificateID)).Do(ctx)
	if err != nil {
		return nil, err
	}
	jobs := make([]string, 0, len(links))
	for _, link := range links {
		job, enqueueErr := s.publisher.Enqueue(ctx, link.SiteId)
		if enqueueErr != nil {
			return jobs, enqueueErr
		}
		jobs = append(jobs, job.Id)
	}
	return jobs, nil
}

func (s *Service) waitPublishJobs(ctx context.Context, ids []string, timeout time.Duration) error {
	if len(ids) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	remaining := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		remaining[id] = struct{}{}
	}
	for len(remaining) > 0 {
		for id := range remaining {
			job, err := s.db.PublishJob.FindUnique(ctx, query.PublishJob.Id.Equals(id))
			if err != nil {
				return err
			}
			if job == nil {
				return fmt.Errorf("publish job %s disappeared", id)
			}
			switch job.Status {
			case model.JobStatusSUCCEEDED:
				delete(remaining, id)
			case model.JobStatusFAILED, model.JobStatusDEAD_LETTER, model.JobStatusCANCELLED:
				return fmt.Errorf("publish job %s ended with %s", id, job.Status)
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
	return nil
}

func (s *Service) reconcileLifecycle(ctx context.Context) {
	items, err := s.db.Certificate.Query().Do(ctx)
	if err != nil {
		return
	}
	now := time.Now().UTC()
	for index := range items {
		certificate := &items[index]
		if certificate.RevokedAt != nil || certificate.Status == model.CertificateStatusREVOKED || certificate.Status == model.CertificateStatusREVOKING || certificate.Status == model.CertificateStatusREVOCATION_FAILED {
			continue
		}
		status := statusAt(certificate.ExpiresAt, certificate.RenewBeforeDays, now)
		if certificate.Status != model.CertificateStatusRENEWAL_FAILED && certificate.Status != model.CertificateStatusDEPLOYMENT_FAILED && certificate.Status != model.CertificateStatusPENDING && certificate.Status != model.CertificateStatusDEPLOYING && certificate.Status != status {
			_, _ = s.db.Certificate.Update().Where(query.Certificate.Id.Equals(certificate.Id)).Set(query.Certificate.Status.Set(status)).Do(ctx)
			if status == model.CertificateStatusEXPIRING || status == model.CertificateStatusEXPIRED {
				slog.Warn("certificate expiry alert", "certificate_id", certificate.Id, "cluster_id", certificate.ClusterId, "status", status, "expires_at", certificate.ExpiresAt)
			}
		}
		if certificate.Source == model.CertificateSourceACME && certificate.AutoRenew && certificate.ExpiresAt != nil && !now.Before(certificate.ExpiresAt.AddDate(0, 0, -certificate.RenewBeforeDays)) {
			job, created, enqueueErr := s.enqueue(ctx, certificate.Id, model.CertificateOperationRENEW)
			if created || enqueueErr != nil {
				audit.RecordSystem(
					ctx, audit.New(s.db), "certificate.auto_renew", "certificate", certificate.Id,
					certificate, job, enqueueErr,
				)
			}
		}
	}
	_, _ = s.db.ACMEChallenge.Delete().Where(query.ACMEChallenge.ExpiresAt.Lt(now)).DoMany(ctx)
}

func statusAt(expiresAt *time.Time, renewBeforeDays int, now time.Time) model.CertificateStatus {
	if expiresAt == nil {
		return model.CertificateStatusPENDING
	}
	if !now.Before(*expiresAt) {
		return model.CertificateStatusEXPIRED
	}
	if !now.Before(expiresAt.AddDate(0, 0, -renewBeforeDays)) {
		return model.CertificateStatusEXPIRING
	}
	return model.CertificateStatusACTIVE
}

func (s *Service) obtainACME(ctx context.Context, certificate *model.Certificate) (Material, error) {
	domains, err := DecodeDomains(certificate.DomainsJson)
	if err != nil {
		return Material{}, err
	}
	directory := defaultACMEDirectory
	if certificate.AcmeDirectoryUrl != nil && strings.TrimSpace(*certificate.AcmeDirectoryUrl) != "" {
		directory = strings.TrimSpace(*certificate.AcmeDirectoryUrl)
	}
	acmeClient, err := s.acmeClient(ctx, directory)
	if err != nil {
		return Material{}, err
	}
	if certificate.AcmeEmail == nil || strings.TrimSpace(*certificate.AcmeEmail) == "" {
		return Material{}, errors.New("ACME email is required")
	}
	account, err := s.loadOrCreateAccount(ctx, acmeClient, certificate.ClusterId, directory, strings.TrimSpace(*certificate.AcmeEmail))
	if err != nil {
		return Material{}, err
	}
	solver := &challengeSolver{service: s, certificate: certificate}
	solvers := map[string]acmez.Solver{}
	challengeType := model.ACMEChallengeTypeHTTP_01
	if certificate.AcmeChallengeType != nil {
		challengeType = *certificate.AcmeChallengeType
	}
	if challengeType == model.ACMEChallengeTypeDNS_01 {
		solvers[acme.ChallengeTypeDNS01] = solver
	} else {
		for _, domain := range domains {
			if strings.HasPrefix(domain, "*.") {
				return Material{}, errors.New("wildcard certificates require DNS-01")
			}
		}
		solvers[acme.ChallengeTypeHTTP01] = solver
	}
	client := acmez.Client{Client: acmeClient, ChallengeSolvers: solvers}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return Material{}, err
	}
	chains, err := client.ObtainCertificateForSANs(ctx, account, key, domains)
	if err != nil {
		return Material{}, fmt.Errorf("obtain ACME certificate: %w", err)
	}
	if len(chains) == 0 {
		return Material{}, errors.New("ACME server returned no certificate chain")
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return Material{}, err
	}
	privateKey := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}))
	return ValidateMaterial(string(chains[0].ChainPEM), privateKey, time.Now().UTC())
}

func (s *Service) loadOrCreateAccount(ctx context.Context, acmeClient *acme.Client, clusterID, directory, email string) (acme.Account, error) {
	stored, err := s.db.ACMEAccount.Query().Where(
		query.ACMEAccount.ClusterId.Equals(clusterID), query.ACMEAccount.DirectoryUrl.Equals(directory), query.ACMEAccount.Email.Equals(email),
	).First(ctx)
	if err != nil {
		return acme.Account{}, err
	}
	if stored != nil {
		plain, decryptErr := s.cipher.DecryptScoped(accountScope(clusterID, directory, email), stored.PrivateKeyEncrypted)
		if decryptErr != nil {
			return acme.Account{}, decryptErr
		}
		block, _ := pem.Decode([]byte(plain))
		if block == nil {
			return acme.Account{}, errors.New("invalid encrypted ACME account key")
		}
		key, parseErr := x509.ParsePKCS8PrivateKey(block.Bytes)
		if parseErr != nil {
			return acme.Account{}, parseErr
		}
		var account acme.Account
		if parseErr = json.Unmarshal(stored.AccountJson, &account); parseErr != nil {
			return acme.Account{}, parseErr
		}
		signer, ok := key.(crypto.Signer)
		if !ok {
			return acme.Account{}, errors.New("ACME account key is not a signing key")
		}
		account.PrivateKey = signer
		return account, nil
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return acme.Account{}, err
	}
	account, err := acmeClient.NewAccount(ctx, acme.Account{Contact: []string{"mailto:" + email}, TermsOfServiceAgreed: true, PrivateKey: key})
	if err != nil {
		return acme.Account{}, fmt.Errorf("register ACME account: %w", err)
	}
	keyDER, _ := x509.MarshalPKCS8PrivateKey(key)
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	encrypted, err := s.cipher.EncryptScoped(accountScope(clusterID, directory, email), string(keyPEM))
	if err != nil {
		return acme.Account{}, err
	}
	accountJSON, _ := json.Marshal(account)
	_, err = s.db.ACMEAccount.Create().Set(
		query.ACMEAccount.ClusterId.Set(clusterID), query.ACMEAccount.DirectoryUrl.Set(directory),
		query.ACMEAccount.Email.Set(email), query.ACMEAccount.PrivateKeyEncrypted.Set(encrypted),
		query.ACMEAccount.AccountJson.Set(accountJSON),
	).Do(ctx)
	return account, err
}

func accountScope(clusterID, directory, email string) string {
	return "goveto-edge/acme-account/v1\x00" + clusterID + "\x00" + directory + "\x00" + email
}

func (s *Service) dnsProviderForDomain(ctx context.Context, clusterID, domain string) (dnsprovider.Provider, *model.DNSProviderConfig, error) {
	config, err := MatchDNSZone(ctx, s.db, clusterID, domain)
	if err != nil {
		return nil, nil, err
	}
	plain, err := s.cipher.Decrypt(config.CredentialsEncrypted)
	if err != nil {
		return nil, nil, fmt.Errorf("decrypt DNS provider credentials: %w", err)
	}
	zoneID := ""
	if config.ZoneId != nil {
		zoneID = *config.ZoneId
	}
	provider, err := dnsprovider.New(config.Provider, config.Zone, zoneID, []byte(plain), nil)
	if err != nil {
		return nil, nil, err
	}
	return provider, config, nil
}

// MatchDNSZone selects the enabled DNS provider zone that best covers domain
// using longest-suffix matching across ENDPOINT and ACME zones.
func MatchDNSZone(ctx context.Context, db *client.Client, clusterID, domain string) (*model.DNSProviderConfig, error) {
	configs, err := db.DNSProviderConfig.Query().Where(
		query.DNSProviderConfig.ClusterId.Equals(clusterID),
		query.DNSProviderConfig.Enabled.Equals(true),
	).Do(ctx)
	if err != nil {
		return nil, err
	}
	return selectBestDNSZone(configs, domain)
}

func selectBestDNSZone(configs []model.DNSProviderConfig, domain string) (*model.DNSProviderConfig, error) {
	domain = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(domain)), "*.")
	domain = strings.TrimSuffix(domain, ".")
	if domain == "" {
		return nil, errors.New("domain is required for DNS-01")
	}
	enabled := make([]model.DNSProviderConfig, 0, len(configs))
	for _, config := range configs {
		if config.Enabled {
			enabled = append(enabled, config)
		}
	}
	if len(enabled) == 0 {
		return nil, errors.New("no enabled DNS provider zones are configured")
	}
	var best *model.DNSProviderConfig
	bestLen := -1
	for index := range enabled {
		zone := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(enabled[index].Zone)), ".")
		if zone == "" {
			continue
		}
		if domain == zone || strings.HasSuffix(domain, "."+zone) {
			if len(zone) > bestLen {
				best = &enabled[index]
				bestLen = len(zone)
			}
		}
	}
	if best == nil {
		return nil, fmt.Errorf("no DNS zone covers %q", domain)
	}
	return best, nil
}

// EnsureDNSZonesCoverDomains verifies every domain is covered by an enabled DNS zone.
func EnsureDNSZonesCoverDomains(ctx context.Context, db *client.Client, clusterID string, domains []string) error {
	for _, domain := range domains {
		if _, err := MatchDNSZone(ctx, db, clusterID, domain); err != nil {
			return err
		}
	}
	return nil
}
