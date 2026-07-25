package certmanager

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/mholt/acmez/v3"
	"github.com/mholt/acmez/v3/acme"

	"goveto-edge/internal/dnsprovider"
	"goveto-edge/internal/node"
	"goveto-edge/internal/storage/gen/client"
	"goveto-edge/internal/storage/gen/model"
	"goveto-edge/internal/storage/gen/query"
)

const defaultACMEDirectory = "https://acme-v02.api.letsencrypt.org/directory"

type Publisher interface {
	Enqueue(context.Context, string) (*model.PublishJob, error)
}

type Service struct {
	db        *client.Client
	cipher    *node.CredentialCipher
	publisher Publisher
	httpState sync.Map
}

func New(db *client.Client, cipher *node.CredentialCipher, publisher Publisher) *Service {
	return &Service{db: db, cipher: cipher, publisher: publisher}
}

func (s *Service) Run(ctx context.Context) {
	jobTicker := time.NewTicker(2 * time.Second)
	lifecycleTicker := time.NewTicker(time.Hour)
	defer jobTicker.Stop()
	defer lifecycleTicker.Stop()
	s.migrateLegacyPrivateKeys(ctx)
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

func (s *Service) migrateLegacyPrivateKeys(ctx context.Context) {
	items, err := s.db.Certificate.Query().Do(ctx)
	if err != nil {
		slog.Warn("load legacy certificate keys", "error", err)
		return
	}
	for index := range items {
		certificate := &items[index]
		if certificate.PrivateKeyPem == nil || *certificate.PrivateKeyPem == "" {
			continue
		}
		encrypted, encryptErr := EncryptPrivateKey(s.cipher, certificate.ClusterId, certificate.Id, *certificate.PrivateKeyPem)
		if encryptErr != nil {
			slog.Warn("encrypt legacy certificate key", "certificate_id", certificate.Id, "error", encryptErr)
			continue
		}
		sets := []query.CertificateSetClause{
			query.Certificate.PrivateKeyEncrypted.Set(encrypted), query.Certificate.PrivateKeyPem.SetNull(),
		}
		if certificate.CertPem != nil {
			material, validationErr := ValidateMaterial(*certificate.CertPem, *certificate.PrivateKeyPem, time.Now().UTC())
			if validationErr != nil {
				material, validationErr = InspectCertificatePEM(*certificate.CertPem)
			}
			if validationErr == nil {
				sets = append(sets,
					query.Certificate.Source.Set(model.CertificateSourceMANUAL),
					query.Certificate.Status.Set(statusAt(&material.ExpiresAt, certificate.RenewBeforeDays, time.Now().UTC())),
					query.Certificate.Fingerprint.Set(material.Fingerprint), query.Certificate.SerialNumber.Set(material.SerialNumber),
					query.Certificate.DomainsJson.Set(EncodeDomains(material.Domains)), query.Certificate.NotBefore.Set(material.NotBefore),
					query.Certificate.ExpiresAt.Set(material.ExpiresAt), query.Certificate.Issuer.Set(material.Issuer),
					query.Certificate.KeyAlgorithm.Set(material.KeyAlgorithm),
				)
			} else {
				slog.Warn("validate legacy certificate", "certificate_id", certificate.Id, "error", validationErr)
			}
		}
		if _, updateErr := s.db.Certificate.Update().Where(query.Certificate.Id.Equals(certificate.Id)).Set(sets...).Do(ctx); updateErr != nil {
			slog.Warn("persist encrypted legacy certificate key", "certificate_id", certificate.Id, "error", updateErr)
		}
	}
}

func (s *Service) Enqueue(ctx context.Context, certificateID string, operation model.CertificateOperation) (*model.CertificateJob, error) {
	active, err := s.db.CertificateJob.Query().Where(
		query.CertificateJob.CertificateId.Equals(certificateID),
		query.CertificateJob.Status.In(model.JobStatusPENDING, model.JobStatusRUNNING),
	).OrderBy(query.CertificateJob.CreatedAt.Desc()).First(ctx)
	if err != nil {
		return nil, err
	}
	if active != nil {
		return active, nil
	}
	return s.db.CertificateJob.Create().Set(
		query.CertificateJob.CertificateId.Set(certificateID),
		query.CertificateJob.Operation.Set(operation),
	).Do(ctx)
}

func (s *Service) Delete(ctx context.Context, certificateID string) error {
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
	jobs, err := client.Raw[model.CertificateJob](ctx, s.db, `UPDATE certificate_jobs SET status = 'RUNNING',
		attempts = attempts + 1, lease_until = NOW() + INTERVAL '1 hour', updated_at = NOW()
		WHERE id = (SELECT id FROM certificate_jobs
			WHERE (status = 'PENDING' OR (status = 'RUNNING' AND lease_until < NOW()))
			AND next_attempt_at <= NOW() ORDER BY created_at FOR UPDATE SKIP LOCKED LIMIT 1)
		RETURNING id, certificate_id, operation, status, attempts, max_attempts, next_attempt_at,
			lease_until, result_json, error, created_at, updated_at`)
	if err != nil || len(jobs) == 0 {
		return
	}
	job := &jobs[0]
	if err = s.execute(ctx, job); err != nil {
		slog.Warn("certificate lifecycle job", "job_id", job.Id, "certificate_id", job.CertificateId, "error", err)
		s.failJob(ctx, job, err)
		return
	}
	result, _ := json.Marshal(map[string]any{"completed_at": time.Now().UTC()})
	_, _ = s.db.CertificateJob.Update().Where(query.CertificateJob.Id.Equals(job.Id)).Set(
		query.CertificateJob.Status.Set(model.JobStatusSUCCEEDED),
		query.CertificateJob.LeaseUntil.SetNull(), query.CertificateJob.Error.SetNull(),
		query.CertificateJob.ResultJson.Set(result),
	).Do(ctx)
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
		return s.publishCertificate(ctx, certificate, nil)
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
	old := *certificate
	if err = s.storeMaterial(ctx, certificate, material, model.CertificateStatusDEPLOYING); err != nil {
		return err
	}
	updated, err := s.db.Certificate.FindUnique(ctx, query.Certificate.Id.Equals(certificate.Id))
	if err != nil {
		return err
	}
	if err = s.publishCertificate(ctx, updated, &old); err != nil {
		return err
	}
	return nil
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
	return s.publishCertificate(ctx, updated, certificate)
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
		query.Certificate.PrivateKeyEncrypted.Set(encrypted), query.Certificate.PrivateKeyPem.SetNull(),
		query.Certificate.Fingerprint.Set(material.Fingerprint), query.Certificate.SerialNumber.Set(material.SerialNumber),
		query.Certificate.DomainsJson.Set(EncodeDomains(material.Domains)),
		query.Certificate.NotBefore.Set(material.NotBefore), query.Certificate.ExpiresAt.Set(material.ExpiresAt),
		query.Certificate.Issuer.Set(material.Issuer), query.Certificate.KeyAlgorithm.Set(material.KeyAlgorithm),
		query.Certificate.LastIssuedAt.Set(now), query.Certificate.LastRenewalError.SetNull(),
	).Do(ctx)
	return err
}

func (s *Service) publishCertificate(ctx context.Context, certificate, rollback *model.Certificate) error {
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
	if rollback != nil && rollback.CertPem != nil {
		if restoreErr := s.restoreMaterial(ctx, rollback); restoreErr != nil {
			err = errors.Join(err, fmt.Errorf("restore previous certificate: %w", restoreErr))
		} else if rollbackJobs, enqueueErr := s.enqueueSites(ctx, rollback.Id); enqueueErr != nil {
			err = errors.Join(err, fmt.Errorf("enqueue rollback: %w", enqueueErr))
		} else if waitErr := s.waitPublishJobs(ctx, rollbackJobs, 5*time.Minute); waitErr != nil {
			err = errors.Join(err, fmt.Errorf("publish rollback: %w", waitErr))
		}
	}
	_, _ = s.db.Certificate.Update().Where(query.Certificate.Id.Equals(certificate.Id)).Set(
		query.Certificate.Status.Set(model.CertificateStatusDEPLOYMENT_FAILED),
		query.Certificate.LastPublishError.Set(err.Error()),
	).Do(ctx)
	return err
}

func (s *Service) restoreMaterial(ctx context.Context, old *model.Certificate) error {
	sets := []query.CertificateSetClause{
		query.Certificate.Status.Set(old.Status), query.Certificate.DomainsJson.Set(old.DomainsJson),
		query.Certificate.AutoRenew.Set(old.AutoRenew), query.Certificate.RenewBeforeDays.Set(old.RenewBeforeDays),
	}
	sets = appendOptionalCertificateSets(sets, old)
	_, err := s.db.Certificate.Update().Where(query.Certificate.Id.Equals(old.Id)).Set(sets...).Do(ctx)
	return err
}

func appendOptionalCertificateSets(sets []query.CertificateSetClause, old *model.Certificate) []query.CertificateSetClause {
	if old.CertPem != nil {
		sets = append(sets, query.Certificate.CertPem.Set(*old.CertPem))
	} else {
		sets = append(sets, query.Certificate.CertPem.SetNull())
	}
	if old.PrivateKeyPem != nil {
		sets = append(sets, query.Certificate.PrivateKeyPem.Set(*old.PrivateKeyPem))
	} else {
		sets = append(sets, query.Certificate.PrivateKeyPem.SetNull())
	}
	if old.PrivateKeyEncrypted != nil {
		sets = append(sets, query.Certificate.PrivateKeyEncrypted.Set(*old.PrivateKeyEncrypted))
	} else {
		sets = append(sets, query.Certificate.PrivateKeyEncrypted.SetNull())
	}
	if old.Fingerprint != nil {
		sets = append(sets, query.Certificate.Fingerprint.Set(*old.Fingerprint))
	} else {
		sets = append(sets, query.Certificate.Fingerprint.SetNull())
	}
	if old.SerialNumber != nil {
		sets = append(sets, query.Certificate.SerialNumber.Set(*old.SerialNumber))
	} else {
		sets = append(sets, query.Certificate.SerialNumber.SetNull())
	}
	if old.NotBefore != nil {
		sets = append(sets, query.Certificate.NotBefore.Set(*old.NotBefore))
	} else {
		sets = append(sets, query.Certificate.NotBefore.SetNull())
	}
	if old.ExpiresAt != nil {
		sets = append(sets, query.Certificate.ExpiresAt.Set(*old.ExpiresAt))
	} else {
		sets = append(sets, query.Certificate.ExpiresAt.SetNull())
	}
	if old.Issuer != nil {
		sets = append(sets, query.Certificate.Issuer.Set(*old.Issuer))
	} else {
		sets = append(sets, query.Certificate.Issuer.SetNull())
	}
	if old.KeyAlgorithm != nil {
		sets = append(sets, query.Certificate.KeyAlgorithm.Set(*old.KeyAlgorithm))
	} else {
		sets = append(sets, query.Certificate.KeyAlgorithm.SetNull())
	}
	return sets
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
			case model.JobStatusFAILED, model.JobStatusCANCELLED:
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

func (s *Service) failJob(ctx context.Context, job *model.CertificateJob, executionErr error) {
	status := model.JobStatusPENDING
	next := time.Now().UTC().Add(time.Duration(math.Pow(2, float64(job.Attempts))) * time.Minute)
	if job.Attempts >= job.MaxAttempts {
		status = model.JobStatusFAILED
	}
	_, _ = s.db.CertificateJob.Update().Where(query.CertificateJob.Id.Equals(job.Id)).Set(
		query.CertificateJob.Status.Set(status), query.CertificateJob.NextAttemptAt.Set(next),
		query.CertificateJob.LeaseUntil.SetNull(), query.CertificateJob.Error.Set(executionErr.Error()),
	).Do(ctx)
	if status == model.JobStatusFAILED {
		_, _ = s.db.Certificate.Update().Where(query.Certificate.Id.Equals(job.CertificateId)).Set(
			query.Certificate.Status.Set(model.CertificateStatusRENEWAL_FAILED),
			query.Certificate.LastRenewalError.Set(executionErr.Error()),
		).Do(ctx)
	}
}

func (s *Service) reconcileLifecycle(ctx context.Context) {
	items, err := s.db.Certificate.Query().Do(ctx)
	if err != nil {
		return
	}
	now := time.Now().UTC()
	for index := range items {
		certificate := &items[index]
		status := statusAt(certificate.ExpiresAt, certificate.RenewBeforeDays, now)
		if certificate.Status != model.CertificateStatusRENEWAL_FAILED && certificate.Status != model.CertificateStatusDEPLOYMENT_FAILED && certificate.Status != model.CertificateStatusPENDING && certificate.Status != model.CertificateStatusDEPLOYING && certificate.Status != status {
			_, _ = s.db.Certificate.Update().Where(query.Certificate.Id.Equals(certificate.Id)).Set(query.Certificate.Status.Set(status)).Do(ctx)
			if status == model.CertificateStatusEXPIRING || status == model.CertificateStatusEXPIRED {
				slog.Warn("certificate expiry alert", "certificate_id", certificate.Id, "cluster_id", certificate.ClusterId, "status", status, "expires_at", certificate.ExpiresAt)
			}
		}
		if certificate.Source == model.CertificateSourceACME && certificate.AutoRenew && certificate.ExpiresAt != nil && !now.Before(certificate.ExpiresAt.AddDate(0, 0, -certificate.RenewBeforeDays)) {
			_, _ = s.Enqueue(ctx, certificate.Id, model.CertificateOperationRENEW)
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
	if certificate.AcmeEmail == nil || strings.TrimSpace(*certificate.AcmeEmail) == "" {
		return Material{}, errors.New("ACME email is required")
	}
	account, err := s.loadOrCreateAccount(ctx, certificate.ClusterId, directory, strings.TrimSpace(*certificate.AcmeEmail))
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
	client := acmez.Client{Client: &acme.Client{Directory: directory}, ChallengeSolvers: solvers}
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

func (s *Service) loadOrCreateAccount(ctx context.Context, clusterID, directory, email string) (acme.Account, error) {
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
	client := acme.Client{Directory: directory}
	account, err := client.NewAccount(ctx, acme.Account{Contact: []string{"mailto:" + email}, TermsOfServiceAgreed: true, PrivateKey: key})
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

func (s *Service) dnsProvider(ctx context.Context, clusterID string) (dnsprovider.Provider, error) {
	config, err := s.db.DNSProviderConfig.FindUnique(ctx, query.DNSProviderConfig.ClusterId.Equals(clusterID))
	if err != nil {
		return nil, err
	}
	if config == nil || !config.Enabled {
		return nil, errors.New("DNS provider is not configured or disabled")
	}
	plain, err := s.cipher.Decrypt(config.CredentialsEncrypted)
	if err != nil {
		return nil, fmt.Errorf("decrypt DNS provider credentials: %w", err)
	}
	zoneID := ""
	if config.ZoneId != nil {
		zoneID = *config.ZoneId
	}
	return dnsprovider.New(config.Provider, config.Zone, zoneID, []byte(plain), nil)
}
