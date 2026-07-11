package purge

import (
	"context"
	"encoding/json"
	"errors"
	"net"
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
	concurrency int
}

func New(db *client.Client, cipher *node.CredentialCipher) *Service {
	return &Service{db: db, cipher: cipher, concurrency: 8}
}

func (s *Service) Enqueue(ctx context.Context, siteID string, purgeType model.PurgeType, value *string) (*model.PurgeJob, error) {
	request := edgeprotocol.PurgeRequest{SiteID: siteID, Type: string(purgeType)}
	if value != nil {
		request.Values = []string{*value}
	}
	if err := request.Validate(); err != nil {
		return nil, err
	}

	sets := []query.PurgeJobSetClause{
		query.PurgeJob.SiteId.Set(siteID),
		query.PurgeJob.Type.Set(purgeType),
		query.PurgeJob.Status.Set(model.JobStatusPENDING),
	}
	if value != nil {
		sets = append(sets, query.PurgeJob.Value.Set(*value))
	}
	return s.db.PurgeJob.Create().Set(sets...).Do(ctx)
}

func (s *Service) Run(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
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
	jobs, err := client.Raw[model.PurgeJob](ctx, s.db, `UPDATE purge_jobs SET status = 'RUNNING', updated_at = NOW()
		WHERE id = (SELECT id FROM purge_jobs WHERE status = 'PENDING' ORDER BY created_at FOR UPDATE SKIP LOCKED LIMIT 1)
		RETURNING id, site_id, type, value, status, result_json, created_at, updated_at`)
	if err != nil || len(jobs) == 0 {
		return
	}
	s.execute(ctx, &jobs[0])
}

type target struct{ NodeID, Address, Key string }
type Result struct {
	NodeID  string `json:"node_id"`
	Success bool   `json:"success"`
	Error   string `json:"error,omitempty"`
}

func (s *Service) execute(ctx context.Context, job *model.PurgeJob) {
	site, err := s.db.Site.FindUnique(ctx, query.Site.Id.Equals(job.SiteId))
	if err != nil {
		s.finish(ctx, job.Id, nil, err)
		return
	}

	targets, err := s.targets(ctx, site.ClusterId)
	if err != nil {
		s.finish(ctx, job.Id, nil, err)
		return
	}

	request := edgeprotocol.PurgeRequest{SiteID: site.Id, Type: string(job.Type)}
	if job.Value != nil {
		request.Values = []string{*job.Value}
	}

	results := make([]Result, len(targets))
	semaphore := make(chan struct{}, s.concurrency)
	var wg sync.WaitGroup
	for i, item := range targets {
		i, item := i, item
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case semaphore <- struct{}{}:
				defer func() { <-semaphore }()
			case <-ctx.Done():
				results[i] = Result{NodeID: item.NodeID, Error: ctx.Err().Error()}
				return
			}

			err := edgecontrol.New(
				"http://"+net.JoinHostPort(item.Address, "80"),
				item.NodeID,
				item.Key,
			).PurgeSite(ctx, request)
			results[i] = Result{NodeID: item.NodeID, Success: err == nil}
			if err != nil {
				results[i].Error = err.Error()
			}
		}()
	}
	wg.Wait()

	for _, result := range results {
		if !result.Success {
			s.finish(ctx, job.Id, results, errors.New("one or more nodes failed cache invalidation"))
			return
		}
	}
	s.finish(ctx, job.Id, results, nil)
}

func (s *Service) targets(ctx context.Context, clusterID string) ([]target, error) {
	nodes, err := s.db.Node.Query().
		Where(query.Node.ClusterId.Equals(clusterID)).
		Do(ctx)
	if err != nil {
		return nil, err
	}

	result := make([]target, 0, len(nodes))
	for _, n := range nodes {
		if n.Status == model.NodeStatusDISABLED {
			continue
		}

		address, err := s.db.NodeAddress.Query().
			Where(
				query.NodeAddress.NodeId.Equals(n.Id),
				query.NodeAddress.Primary.Equals(true),
			).
			First(ctx)
		if err != nil {
			return nil, err
		}

		credential, err := s.db.NodeCredential.FindUnique(ctx, query.NodeCredential.NodeId.Equals(n.Id))
		if err != nil {
			return nil, err
		}

		key, err := s.cipher.Decrypt(credential.CommunicationKeyEncrypted)
		if err != nil {
			return nil, err
		}
		result = append(result, target{NodeID: n.Id, Address: address.Address, Key: key})
	}

	if len(result) == 0 {
		return nil, errors.New("cluster has no purgeable nodes")
	}
	return result, nil
}

func (s *Service) finish(ctx context.Context, id string, results []Result, executionErr error) {
	status := model.JobStatusSUCCEEDED
	message := ""
	if executionErr != nil {
		status = model.JobStatusFAILED
		message = executionErr.Error()
	}

	payload, _ := json.Marshal(map[string]any{"results": results, "error": message})
	_, _ = s.db.PurgeJob.Update().
		Where(query.PurgeJob.Id.Equals(id)).
		Set(
			query.PurgeJob.Status.Set(status),
			query.PurgeJob.ResultJson.Set(payload),
		).
		Do(ctx)
}
