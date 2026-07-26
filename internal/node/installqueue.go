package node

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	"goveto-edge/internal/jobqueue"
	"goveto-edge/internal/storage/gen/client"
	"goveto-edge/internal/storage/gen/model"
	"goveto-edge/internal/storage/gen/query"
)

const installJobTimeout = 10 * time.Minute

// InstallQueue persists installation requests in PostgreSQL. Credentials are
// deliberately not copied into the payload; workers resolve encrypted state at
// execution time.
type InstallQueue struct {
	db   *client.Client
	jobs *jobqueue.Manager
}

func NewInstallQueue(db *client.Client) *InstallQueue {
	return &InstallQueue{db: db, jobs: jobqueue.New(db)}
}

func (q *InstallQueue) Enqueue(ctx context.Context, nodeID string) error {
	payload, err := json.Marshal(struct {
		NodeID string `json:"node_id"`
	}{NodeID: nodeID})
	if err != nil {
		return fmt.Errorf("encode node install payload: %w", err)
	}
	return q.db.Tx(ctx, func(tx *client.Client) error {
		if _, lockErr := tx.RawExec(ctx, "SELECT pg_advisory_xact_lock(hashtext($1))", "install:"+nodeID); lockErr != nil {
			return lockErr
		}
		active, queryErr := tx.InstallJob.Query().Where(
			query.InstallJob.NodeId.Equals(nodeID),
			query.InstallJob.Status.In(model.JobStatusPENDING, model.JobStatusRUNNING),
			query.InstallJob.CancelRequestedAt.IsNull(),
		).First(ctx)
		if queryErr != nil {
			return queryErr
		}
		if active != nil {
			return nil
		}
		_, createErr := tx.InstallJob.Create().Set(
			query.InstallJob.NodeId.Set(nodeID),
			query.InstallJob.Payload.Set(payload),
			query.InstallJob.Status.Set(model.JobStatusPENDING),
			query.InstallJob.IdempotencyKey.Set("install:"+nodeID+":"+uuid.NewString()),
		).Do(ctx)
		return createErr
	})
}

func (q *InstallQueue) Delete(ctx context.Context, nodeID string) error {
	return q.DeleteTx(ctx, q.db, nodeID)
}

func (q *InstallQueue) DeleteTx(ctx context.Context, db *client.Client, nodeID string) error {
	_, err := db.RawExec(ctx, `UPDATE install_jobs SET cancel_requested_at=NOW(),
		status=CASE WHEN status='PENDING' THEN 'CANCELLED' ELSE status END, updated_at=NOW()
		WHERE node_id=$1 AND status IN ('PENDING','RUNNING')`, nodeID)
	return err
}

func (q *InstallQueue) RunOne(
	ctx context.Context,
	handler func(context.Context, jobqueue.Lease) jobqueue.Outcome,
) (bool, error) {
	// Manager starts this deadline when the job is claimed, so queueing time is
	// not charged against the installation attempt.
	return q.jobs.RunOne(ctx, jobqueue.Install, installJobTimeout, handler)
}
