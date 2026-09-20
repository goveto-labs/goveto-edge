package publisher

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"goveto-edge/internal/edgeprotocol"
	"goveto-edge/internal/jobqueue"
	"goveto-edge/internal/storage/gen/client"
	"goveto-edge/internal/storage/gen/model"
	"goveto-edge/internal/storage/gen/query"
)

type rollbackPlan struct {
	Version int64          `json:"rollback_version"`
	Targets []target       `json:"rollback_targets"`
	Results []targetResult `json:"publish_results"`
}

func (s *Service) execute(ctx context.Context, job *model.PublishJob) (outcome jobqueue.Outcome) {
	// This transaction owns only the execution lock. Snapshots and recovery
	// plans commit through separate transactions before any agent sees them.
	err := s.db.Tx(ctx, func(lock *client.Client) error {
		rows, err := client.Raw[struct {
			Locked bool `db:"locked"`
		}](ctx, lock,
			"SELECT pg_try_advisory_xact_lock(hashtext($1)) AS locked", "publish-execute:"+job.SiteId)
		if err != nil {
			return err
		}
		if len(rows) != 1 || !rows[0].Locked {
			outcome = jobqueue.Outcome{RequeueAfter: time.Second}
			return nil
		}
		// A compensation may have moved a queued job to a later version.
		// Reload deliberately goes through s.db (another pool connection), not
		// the lock client: this read must not be tied to the lock transaction.
		current, err := s.db.PublishJob.FindUnique(ctx, query.PublishJob.Id.Equals(job.Id))
		if err != nil {
			return err
		}
		if current == nil {
			return errors.New("publish job not found")
		}
		outcome = s.executeLocked(ctx, current)
		return nil
	})
	if err != nil {
		// The handler's business transactions are deliberately separate from
		// this lock-only transaction. If committing the lock transaction fails
		// after a compensation plan was persisted, retain that plan and retry it
		// instead of replacing it with an empty outcome. An outcome without a
		// compensation (e.g. every node rejected the config) is a terminal
		// business failure and must not be retried.
		if outcome.Compensation != nil {
			outcome.Err = err
			outcome.Retryable = true
			return outcome
		}
		return publishOutcome(model.JobStatusFAILED, nil, err, true)
	}
	return outcome
}

func (s *Service) reserveRollback(ctx context.Context, job *model.PublishJob, results []targetResult, targets []target) (plan rollbackPlan, err error) {
	err = s.db.Tx(ctx, func(tx *client.Client) error {
		// Use exactly the ordinary publication allocation lock.
		if _, err := tx.RawExec(ctx, "SELECT pg_advisory_xact_lock(hashtext($1))", job.SiteId); err != nil {
			return err
		}
		latest, err := tx.ConfigVersion.Query().Where(query.ConfigVersion.SiteId.Equals(job.SiteId)).OrderBy(query.ConfigVersion.Version.Desc()).First(ctx)
		if err != nil {
			return err
		}
		plan = rollbackPlan{Version: nextPublishVersion(job.Version, nil, latest), Results: results, Targets: targets}
		previous, err := tx.ConfigVersion.Query().Where(
			query.ConfigVersion.SiteId.Equals(job.SiteId),
			query.ConfigVersion.Status.In(model.ConfigStatusPUBLISHED, model.ConfigStatusROLLED_BACK),
			query.ConfigVersion.Version.Lt(job.Version),
		).OrderBy(query.ConfigVersion.Version.Desc()).First(ctx)
		if err != nil {
			return err
		}
		rollback := s.rollbackSnapshot(job.SiteId, plan.Version, previous)
		if err = s.createSnapshot(ctx, tx, rollback); err != nil {
			return err
		}

		// Requests queued while this publish was running must follow its
		// compensation. Keep their old snapshots immutable and reserve copies.
		queued, err := tx.PublishJob.Query().Where(
			query.PublishJob.SiteId.Equals(job.SiteId), query.PublishJob.Version.Gt(job.Version),
			query.PublishJob.Status.In(model.JobStatusPENDING, model.JobStatusRUNNING),
		).OrderBy(query.PublishJob.Version.Asc()).Do(ctx)
		if err != nil {
			return err
		}
		for index, pending := range queued {
			old, err := tx.ConfigVersion.Query().Where(query.ConfigVersion.SiteId.Equals(job.SiteId), query.ConfigVersion.Version.Equals(pending.Version)).First(ctx)
			if err != nil {
				return err
			}
			if old == nil {
				return errors.New("queued publish snapshot not found")
			}
			config, err := s.decodeStoredConfig(old.ConfigJson)
			if err != nil {
				return err
			}
			config.Version = uint64(plan.Version + int64(index) + 1)
			if err = s.createSnapshot(ctx, tx, config); err != nil {
				return err
			}
			if _, err = tx.PublishJob.Update().Where(query.PublishJob.Id.Equals(pending.Id)).Set(query.PublishJob.Version.Set(int64(config.Version))).Do(ctx); err != nil {
				return err
			}
			if _, err = tx.ConfigVersion.Update().Where(query.ConfigVersion.Id.Equals(old.Id)).Set(query.ConfigVersion.Status.Set(model.ConfigStatusFAILED)).Do(ctx); err != nil {
				return err
			}
		}
		encoded, err := json.Marshal(plan)
		if err != nil {
			return err
		}
		_, err = tx.PublishJob.Update().Where(query.PublishJob.Id.Equals(job.Id)).Set(query.PublishJob.CompensationJson.Set(encoded)).Do(ctx)
		return err
	})
	return plan, err
}

func (s *Service) createSnapshot(ctx context.Context, tx *client.Client, config edgeprotocol.SiteConfig) error {
	encoded, err := s.sealedConfigJSON(config)
	if err != nil {
		return err
	}
	hash := semanticConfigHash(config)
	_, err = tx.ConfigVersion.Create().Set(
		query.ConfigVersion.SiteId.Set(config.SiteID), query.ConfigVersion.Version.Set(int64(config.Version)),
		query.ConfigVersion.ConfigJson.Set(encoded), query.ConfigVersion.Hash.Set(hex.EncodeToString(hash[:])),
		query.ConfigVersion.Status.Set(model.ConfigStatusDRAFT),
	).Do(ctx)
	return err
}
