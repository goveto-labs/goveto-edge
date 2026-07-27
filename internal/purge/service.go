package purge

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"goveto-edge/internal/edgecontrol"
	"goveto-edge/internal/edgeprotocol"
	"goveto-edge/internal/jobqueue"
	"goveto-edge/internal/storage/gen/client"
	"goveto-edge/internal/storage/gen/model"
	"goveto-edge/internal/storage/gen/query"
)

type Service struct {
	db          *client.Client
	gateway     *edgecontrol.Gateway
	jobs        *jobqueue.Manager
	concurrency int
}

var ErrInvalidRequest = errors.New("invalid purge request")

func New(db *client.Client, gateway *edgecontrol.Gateway) *Service {
	return &Service{db: db, gateway: gateway, jobs: jobqueue.New(db), concurrency: 8}
}

func (s *Service) Enqueue(ctx context.Context, siteID string, purgeType model.PurgeType, value *string) (*model.PurgeJob, error) {
	return s.EnqueueIdempotent(ctx, siteID, purgeType, value, "")
}

func (s *Service) EnqueueIdempotent(ctx context.Context, siteID string, purgeType model.PurgeType, value *string, idempotencyKey string) (*model.PurgeJob, error) {
	if err := jobqueue.ValidateIdempotencyKey(idempotencyKey); err != nil {
		return nil, err
	}
	request := edgeprotocol.PurgeRequest{SiteID: siteID, Type: string(purgeType)}
	if value != nil {
		request.Values = []string{*value}
	}
	if err := request.Validate(); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidRequest, err)
	}

	var job *model.PurgeJob
	err := s.db.Tx(ctx, func(tx *client.Client) error {
		if idempotencyKey != "" {
			if _, err := tx.RawExec(ctx, "SELECT pg_advisory_xact_lock(hashtext($1))", "purge:"+idempotencyKey); err != nil {
				return err
			}
			existing, err := tx.PurgeJob.Query().Where(query.PurgeJob.IdempotencyKey.Equals(&idempotencyKey)).First(ctx)
			if err != nil {
				return err
			}
			if existing != nil {
				if existing.SiteId != siteID || existing.Type != purgeType || !sameString(existing.Value, value) {
					return fmt.Errorf("%w: key is already used by another purge request", jobqueue.ErrIdempotencyConflict)
				}
				job = existing
				return nil
			}
		}
		sets := []query.PurgeJobSetClause{
			query.PurgeJob.SiteId.Set(siteID), query.PurgeJob.Type.Set(purgeType),
			query.PurgeJob.Status.Set(model.JobStatusPENDING),
		}
		if value != nil {
			sets = append(sets, query.PurgeJob.Value.Set(*value))
		}
		if idempotencyKey != "" {
			sets = append(sets, query.PurgeJob.IdempotencyKey.Set(idempotencyKey))
		}
		var err error
		job, err = tx.PurgeJob.Create().Set(sets...).Do(ctx)
		return err
	})
	return job, err
}

func sameString(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
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
	_, err := s.jobs.RunOne(ctx, jobqueue.Purge, 10*time.Minute, func(runCtx context.Context, lease jobqueue.Lease) jobqueue.Outcome {
		job, loadErr := s.db.PurgeJob.FindUnique(runCtx, query.PurgeJob.Id.Equals(lease.ID))
		if loadErr != nil || job == nil {
			if loadErr == nil {
				loadErr = errors.New("purge job not found")
			}
			return jobqueue.Outcome{Err: loadErr, Retryable: true}
		}
		return s.execute(runCtx, job)
	})
	if err != nil && !errors.Is(err, context.Canceled) {
		slog.Warn("purge job worker", "error", err)
	}
}

type target struct{ NodeID string }
type Result struct {
	NodeID  string `json:"node_id"`
	Success bool   `json:"success"`
	Objects int    `json:"objects"`
	Error   string `json:"error,omitempty"`
}

func (s *Service) execute(ctx context.Context, job *model.PurgeJob) jobqueue.Outcome {
	site, err := s.db.Site.FindUnique(ctx, query.Site.Id.Equals(job.SiteId))
	if err != nil {
		return jobqueue.Outcome{Err: err, Retryable: true}
	}
	if site == nil {
		return jobqueue.Outcome{Err: errors.New("site not found")}
	}

	targets, err := s.targets(ctx, site.ClusterId)
	if err != nil {
		return jobqueue.Outcome{Err: err, Retryable: true}
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

			dispatchCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
			defer cancel()
			var purgeResult edgeprotocol.PurgeResult
			err := s.gateway.Dispatch(dispatchCtx, item.NodeID, edgeprotocol.TaskPurgeSite, request, &purgeResult)
			results[i] = Result{NodeID: item.NodeID, Success: err == nil, Objects: purgeResult.Objects}
			if err != nil {
				results[i].Error = err.Error()
			}
		}()
	}
	wg.Wait()

	for _, result := range results {
		if !result.Success {
			return jobqueue.Outcome{Result: map[string]any{"results": results}, Err: errors.New("one or more nodes failed cache invalidation"), Retryable: true}
		}
	}
	return jobqueue.Outcome{Result: map[string]any{"results": results}}
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

		credential, err := s.db.NodeCredential.FindUnique(ctx, query.NodeCredential.NodeId.Equals(n.Id))
		if err != nil {
			return nil, err
		}
		if credential == nil || credential.RevokedAt != nil {
			continue
		}
		result = append(result, target{NodeID: n.Id})
	}

	if len(result) == 0 {
		return nil, errors.New("cluster has no purgeable nodes")
	}
	return result, nil
}
