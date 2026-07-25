package certmanager

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/mholt/acmez/v3/acme"

	"goveto-edge/internal/dnsprovider"
	"goveto-edge/internal/storage/gen/model"
	"goveto-edge/internal/storage/gen/query"
)

type challengeState struct {
	PublishJobs []string
	DNSRecord   *dnsprovider.Record
}

type challengeSolver struct {
	service     *Service
	certificate *model.Certificate
}

func (s *challengeSolver) Present(ctx context.Context, challenge acme.Challenge) error {
	domain := strings.TrimPrefix(strings.ToLower(challenge.Identifier.Value), "*.")
	expiresAt := time.Now().UTC().Add(30 * time.Minute)
	state := challengeState{}
	if challenge.Type == acme.ChallengeTypeDNS01 {
		provider, err := s.service.dnsProvider(ctx, s.certificate.ClusterId)
		if err != nil {
			return err
		}
		record := dnsprovider.Record{Hostname: strings.TrimSuffix(challenge.DNS01TXTRecordName(), "."), Type: model.DNSRecordTypeTXT, Value: challenge.DNS01KeyAuthorization(), Line: "default", TTL: 60}
		id, err := provider.Upsert(ctx, record)
		if err != nil {
			return fmt.Errorf("present DNS-01 challenge: %w", err)
		}
		record.ID = id
		state.DNSRecord = &record
		_, err = s.service.db.ACMEChallenge.Create().Set(
			query.ACMEChallenge.CertificateId.Set(s.certificate.Id), query.ACMEChallenge.ClusterId.Set(s.certificate.ClusterId),
			query.ACMEChallenge.Type.Set(model.ACMEChallengeTypeDNS_01), query.ACMEChallenge.Status.Set(model.ACMEChallengeStatusPRESENTED),
			query.ACMEChallenge.Domain.Set(domain), query.ACMEChallenge.Token.Set(challenge.Token),
			query.ACMEChallenge.DnsName.Set(record.Hostname), query.ACMEChallenge.DnsValue.Set(record.Value),
			query.ACMEChallenge.ProviderRef.Set(id), query.ACMEChallenge.ExpiresAt.Set(expiresAt),
		).Do(ctx)
		if err != nil {
			_ = provider.Delete(ctx, record)
			return err
		}
	} else if challenge.Type == acme.ChallengeTypeHTTP01 {
		item, err := s.service.db.ACMEChallenge.Create().Set(
			query.ACMEChallenge.CertificateId.Set(s.certificate.Id), query.ACMEChallenge.ClusterId.Set(s.certificate.ClusterId),
			query.ACMEChallenge.Type.Set(model.ACMEChallengeTypeHTTP_01), query.ACMEChallenge.Status.Set(model.ACMEChallengeStatusPRESENTED),
			query.ACMEChallenge.Domain.Set(domain), query.ACMEChallenge.Token.Set(challenge.Token),
			query.ACMEChallenge.KeyAuth.Set(challenge.KeyAuthorization), query.ACMEChallenge.ExpiresAt.Set(expiresAt),
		).Do(ctx)
		if err != nil {
			return err
		}
		state.PublishJobs, err = s.publishHTTPChallenge(ctx, item)
		if err != nil {
			_, _ = s.service.db.ACMEChallenge.Delete().Where(query.ACMEChallenge.Id.Equals(item.Id)).DoMany(ctx)
			return err
		}
	} else {
		return fmt.Errorf("unsupported ACME challenge %q", challenge.Type)
	}
	s.service.httpState.Store(challenge.Token, state)
	return nil
}

func (s *challengeSolver) Wait(ctx context.Context, challenge acme.Challenge) error {
	value, ok := s.service.httpState.Load(challenge.Token)
	if !ok {
		return errors.New("ACME challenge state is unavailable")
	}
	state := value.(challengeState)
	if len(state.PublishJobs) > 0 {
		return s.service.waitPublishJobs(ctx, state.PublishJobs, 5*time.Minute)
	}
	if state.DNSRecord != nil {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			values, err := net.DefaultResolver.LookupTXT(ctx, state.DNSRecord.Hostname)
			if err == nil {
				for _, value := range values {
					if value == state.DNSRecord.Value {
						return nil
					}
				}
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-ticker.C:
			}
		}
	}
	return nil
}

func (s *challengeSolver) CleanUp(ctx context.Context, challenge acme.Challenge) error {
	value, _ := s.service.httpState.LoadAndDelete(challenge.Token)
	if challenge.Type == acme.ChallengeTypeDNS01 && value != nil {
		state := value.(challengeState)
		if state.DNSRecord != nil {
			provider, err := s.service.dnsProvider(ctx, s.certificate.ClusterId)
			if err == nil {
				err = provider.Delete(ctx, *state.DNSRecord)
			}
			if err != nil {
				return err
			}
		}
	}
	items, err := s.service.db.ACMEChallenge.Query().Where(
		query.ACMEChallenge.CertificateId.Equals(s.certificate.Id), query.ACMEChallenge.Token.Equals(challenge.Token),
	).Do(ctx)
	if err != nil {
		return err
	}
	for _, item := range items {
		_, _ = s.service.db.ACMEChallenge.Delete().Where(query.ACMEChallenge.Id.Equals(item.Id)).DoMany(ctx)
		if item.Type == model.ACMEChallengeTypeHTTP_01 {
			_, _ = s.publishHTTPChallenge(ctx, &item)
		}
	}
	return nil
}

func (s *challengeSolver) publishHTTPChallenge(ctx context.Context, challenge *model.ACMEChallenge) ([]string, error) {
	domains, err := s.service.db.SiteDomain.Query().Where(query.SiteDomain.Hostname.Equals(challenge.Domain)).Do(ctx)
	if err != nil {
		return nil, err
	}
	if len(domains) == 0 {
		return nil, fmt.Errorf("HTTP-01 domain %q is not attached to a site", challenge.Domain)
	}
	jobs := make([]string, 0, len(domains))
	for _, domain := range domains {
		job, enqueueErr := s.service.publisher.Enqueue(ctx, domain.SiteId)
		if enqueueErr != nil {
			return jobs, enqueueErr
		}
		jobs = append(jobs, job.Id)
	}
	return jobs, nil
}
