// Package jobqueue provides the shared PostgreSQL lease protocol used by
// control-plane background jobs.
package jobqueue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"

	"goveto-edge/internal/storage/gen/client"
)

type Kind string

const (
	Publish     Kind = "PUBLISH"
	Purge       Kind = "PURGE"
	Install     Kind = "INSTALL"
	DNS         Kind = "DNS"
	Certificate Kind = "CERTIFICATE"
)

var tables = map[Kind]string{
	Publish: "publish_jobs", Purge: "purge_jobs", Install: "install_jobs",
	DNS: "dns_sync_jobs", Certificate: "certificate_jobs",
}

const (
	defaultLease           = 45 * time.Second
	heartbeatEvery         = 10 * time.Second
	cancellationPoll       = 2 * time.Second
	sweepEvery             = 30 * time.Second
	maxBackoff             = 5 * time.Minute
	maxIdempotencyKeyBytes = 128
	sweepableJobPredicate  = "(status='PENDING' OR (status='RUNNING' AND (lease_until IS NULL OR lease_until<=NOW())))"
)

var (
	ErrLeaseLost             = errors.New("job lease is stale or owned by another worker")
	ErrLeaseUncertain        = errors.New("job lease could not be verified before expiry")
	ErrNotCancellable        = errors.New("job is not cancellable")
	ErrNotReplayable         = errors.New("job is not replayable")
	ErrInvalidIdempotencyKey = errors.New("invalid idempotency key")
	ErrIdempotencyConflict   = errors.New("idempotency key conflicts with an existing request")
)

type Manager struct {
	db        *client.Client
	workerID  string
	lease     time.Duration
	sweepMu   sync.Mutex
	nextSweep map[Kind]time.Time
}

type Lease struct {
	Kind        Kind
	ID          string
	WorkerID    string
	Attempt     int
	MaxAttempts int
	TimeoutAt   *time.Time
	LeaseUntil  time.Time
}

type Outcome struct {
	Result       any
	Compensation any
	Err          error
	Retryable    bool
	// RetryAfter overrides the default retry backoff while still consuming an
	// attempt. A deterministic jitter is applied to spread concurrent retries.
	RetryAfter time.Duration
	// RequeueAfter returns the job to PENDING without consuming an attempt.
	// It is intended for contention before the handler starts useful work.
	RequeueAfter time.Duration
}

type executionRow struct {
	ID          string     `db:"id"`
	Attempts    int        `db:"attempts"`
	MaxAttempts int        `db:"max_attempts"`
	TimeoutAt   *time.Time `db:"timeout_at"`
	LeaseUntil  time.Time  `db:"lease_until"`
}

type leaseState struct {
	CancelRequestedAt *time.Time `db:"cancel_requested_at"`
	LeaseUntil        time.Time  `db:"lease_until"`
}

func New(db *client.Client) *Manager {
	return &Manager{db: db, workerID: uuid.NewString(), lease: defaultLease, nextSweep: make(map[Kind]time.Time)}
}

func (m *Manager) WorkerID() string { return m.workerID }

// RunOne claims at most one job and runs it with heartbeat and cancellation
// propagation. A false return means no runnable job was available.
func (m *Manager) RunOne(
	ctx context.Context,
	kind Kind,
	defaultTimeout time.Duration,
	handler func(context.Context, Lease) Outcome,
) (bool, error) {
	lease, err := m.claim(ctx, kind, defaultTimeout)
	if err != nil || lease == nil {
		return false, err
	}

	var runCtx context.Context
	var cancel context.CancelFunc
	if lease.TimeoutAt != nil {
		runCtx, cancel = context.WithDeadline(ctx, *lease.TimeoutAt)
	} else if defaultTimeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, defaultTimeout)
	} else {
		runCtx, cancel = context.WithCancel(ctx)
	}
	defer cancel()

	heartbeatDone := make(chan struct{})
	leaseLost := make(chan error, 1)
	go m.heartbeat(runCtx, *lease, cancel, heartbeatDone, leaseLost)
	// The handler outcome remains authoritative after the execution deadline:
	// retrying a completed side effect can duplicate certificate or publish work.
	outcome := invokeHandler(runCtx, *lease, handler)
	cancel()
	<-heartbeatDone

	if ctx.Err() != nil {
		// Shutdown is not an execution failure. The expired lease will be picked
		// up by another control-plane replica.
		return true, ctx.Err()
	}
	select {
	case err = <-leaseLost:
		return true, err
	default:
	}
	finishCtx, finishCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer finishCancel()
	return true, m.finish(finishCtx, *lease, outcome)
}

func invokeHandler(ctx context.Context, lease Lease, handler func(context.Context, Lease) Outcome) (outcome Outcome) {
	defer func() {
		if recovered := recover(); recovered != nil {
			outcome = Outcome{Err: fmt.Errorf("job worker panic: %v", recovered), Retryable: true}
		}
	}()
	return handler(ctx, lease)
}

func (m *Manager) claim(ctx context.Context, kind Kind, defaultTimeout time.Duration) (*Lease, error) {
	table, err := tableFor(kind)
	if err != nil {
		return nil, err
	}
	if err = m.sweepIfDue(ctx, kind, table); err != nil {
		return nil, err
	}
	var claimed []executionRow
	err = m.db.Tx(ctx, func(tx *client.Client) error {
		claimed, err = client.Raw[executionRow](ctx, tx, claimSQL(table),
			m.workerID, m.lease.Seconds(), defaultTimeout.Seconds())
		if err != nil || len(claimed) == 0 {
			return err
		}
		if _, err = tx.RawExec(ctx, `UPDATE job_executions SET status='FAILED', finished_at=NOW(),
			heartbeat_at=NOW(), error=COALESCE(error, 'worker lease expired')
			WHERE job_type=$1 AND job_id=$2 AND status='RUNNING' AND attempt<$3`,
			string(kind), claimed[0].ID, claimed[0].Attempts); err != nil {
			return err
		}
		_, err = tx.RawExec(ctx, `INSERT INTO job_executions
			(id, job_type, job_id, attempt, worker_id, status, started_at, heartbeat_at)
			VALUES ($1,$2,$3,$4,$5,'RUNNING',NOW(),NOW())`, uuid.NewString(), string(kind),
			claimed[0].ID, claimed[0].Attempts, m.workerID)
		return err
	})
	if err != nil || len(claimed) == 0 {
		return nil, err
	}
	row := claimed[0]
	return &Lease{Kind: kind, ID: row.ID, WorkerID: m.workerID, Attempt: row.Attempts,
		MaxAttempts: row.MaxAttempts, TimeoutAt: row.TimeoutAt, LeaseUntil: row.LeaseUntil}, nil
}

func claimSQL(table string) string {
	return fmt.Sprintf(`WITH picked AS (
		SELECT id FROM %s WHERE cancel_requested_at IS NULL AND attempts<max_attempts
		AND (timeout_at IS NULL OR timeout_at>NOW()) AND ((status='PENDING' AND next_attempt_at<=NOW())
		OR (status='RUNNING' AND (lease_until IS NULL OR lease_until<=NOW())))
		ORDER BY next_attempt_at, created_at FOR UPDATE SKIP LOCKED LIMIT 1)
		UPDATE %s j SET status='RUNNING', attempts=attempts+1, lease_owner=$1,
		lease_until=NOW()+($2*INTERVAL '1 second'), heartbeat_at=NOW(),
		timeout_at=COALESCE(timeout_at, CASE WHEN $3>0 THEN NOW()+($3*INTERVAL '1 second') END),
		updated_at=NOW()
		FROM picked WHERE j.id=picked.id
		RETURNING j.id, j.attempts, j.max_attempts, j.timeout_at, j.lease_until`, table, table)
}

func (m *Manager) sweepIfDue(ctx context.Context, kind Kind, table string) error {
	m.sweepMu.Lock()
	defer m.sweepMu.Unlock()
	now := time.Now()
	if m.nextSweep == nil {
		m.nextSweep = make(map[Kind]time.Time)
	}
	if now.Before(m.nextSweep[kind]) {
		return nil
	}
	if err := m.sweep(ctx, kind, table); err != nil {
		return err
	}
	m.nextSweep[kind] = now.Add(sweepEvery)
	return nil
}

func (m *Manager) sweep(ctx context.Context, kind Kind, table string) error {
	type lockRow struct {
		Locked bool `db:"locked"`
	}
	return m.db.Tx(ctx, func(tx *client.Client) error {
		locks, err := client.Raw[lockRow](ctx, tx,
			"SELECT pg_try_advisory_xact_lock(hashtext($1)) AS locked", "jobqueue-sweep:"+string(kind))
		if err != nil || len(locks) != 1 || !locks[0].Locked {
			return err
		}
		if _, err = tx.RawExec(ctx, fmt.Sprintf(`UPDATE %s SET status='CANCELLED',
			error=COALESCE(error, 'job cancellation requested'), lease_owner=NULL,
			lease_until=NULL, heartbeat_at=NULL, updated_at=NOW()
			WHERE cancel_requested_at IS NOT NULL AND %s`, table, sweepableJobPredicate)); err != nil {
			return err
		}
		if _, err = tx.RawExec(ctx, fmt.Sprintf(`UPDATE job_executions e SET status='CANCELLED',
			finished_at=NOW(), heartbeat_at=NOW(), error=COALESCE(e.error, 'job cancellation requested')
			WHERE e.job_type=$1 AND e.status='RUNNING' AND EXISTS (SELECT 1 FROM %s j
			WHERE j.id=e.job_id AND j.status='CANCELLED' AND j.cancel_requested_at IS NOT NULL
			AND j.attempts=e.attempt)`, table), string(kind)); err != nil {
			return err
		}
		if _, err = tx.RawExec(ctx, fmt.Sprintf(`UPDATE %s SET status='DEAD_LETTER',
			error=COALESCE(error, 'job execution deadline expired'), lease_owner=NULL,
			lease_until=NULL, heartbeat_at=NULL, updated_at=NOW()
			WHERE timeout_at<=NOW() AND %s`, table, sweepableJobPredicate)); err != nil {
			return err
		}
		if _, err = tx.RawExec(ctx, fmt.Sprintf(`UPDATE job_executions e SET status='DEAD_LETTER',
			finished_at=NOW(), heartbeat_at=NOW(), error=COALESCE(e.error, 'job execution deadline expired')
			WHERE e.job_type=$1 AND e.status='RUNNING' AND EXISTS (SELECT 1 FROM %s j
			WHERE j.id=e.job_id AND j.status='DEAD_LETTER' AND j.timeout_at<=NOW()
			AND j.attempts=e.attempt)`, table), string(kind)); err != nil {
			return err
		}
		if _, err = tx.RawExec(ctx, fmt.Sprintf(`UPDATE %s SET status='DEAD_LETTER',
			error=COALESCE(error, 'maximum delivery attempts exceeded'), lease_owner=NULL,
			lease_until=NULL, heartbeat_at=NULL, updated_at=NOW()
			WHERE attempts>=max_attempts AND %s`, table, sweepableJobPredicate)); err != nil {
			return err
		}
		if _, err = tx.RawExec(ctx, fmt.Sprintf(`UPDATE job_executions e SET status='DEAD_LETTER',
			finished_at=NOW(), heartbeat_at=NOW(), error=COALESCE(e.error, 'maximum delivery attempts exceeded')
			WHERE e.job_type=$1 AND e.status='RUNNING' AND EXISTS (SELECT 1 FROM %s j
			WHERE j.id=e.job_id AND j.status='DEAD_LETTER' AND j.attempts>=j.max_attempts
			AND j.attempts=e.attempt)`, table), string(kind)); err != nil {
			return err
		}
		return nil
	})
}

func (m *Manager) heartbeat(
	ctx context.Context,
	lease Lease,
	cancel context.CancelFunc,
	done chan<- struct{},
	lost chan<- error,
) {
	defer close(done)
	ticker := time.NewTicker(heartbeatEvery)
	defer ticker.Stop()
	poll := time.NewTicker(cancellationPoll)
	defer poll.Stop()
	leaseValidUntil := lease.LeaseUntil
	if leaseValidUntil.IsZero() {
		leaseValidUntil = time.Now().Add(m.lease)
	}
	renewPending := false
	probe := func(renewLease bool) bool {
		var state leaseState
		var err error
		if renewLease {
			state, err = m.renew(ctx, lease)
		} else {
			state, err = m.state(ctx, lease)
		}
		if err != nil {
			if ctx.Err() != nil {
				return false
			}
			if renewLease {
				renewPending = true
			}
			if terminalErr := leaseProbeFailure(err, time.Now(), leaseValidUntil); terminalErr != nil {
				lost <- terminalErr
				cancel()
				return false
			}
			return true
		}
		if renewLease {
			leaseValidUntil = state.LeaseUntil
			renewPending = false
		}
		if state.CancelRequestedAt != nil {
			cancel()
			return false
		}
		return true
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if !probe(true) {
				return
			}
		case <-poll.C:
			renewLease := renewPending || time.Until(leaseValidUntil) <= heartbeatEvery
			if !probe(renewLease) {
				return
			}
		}
	}
}

func leaseProbeFailure(err error, now, leaseValidUntil time.Time) error {
	if errors.Is(err, ErrLeaseLost) {
		return ErrLeaseLost
	}
	if now.Before(leaseValidUntil) {
		return nil
	}
	return fmt.Errorf("%w: %w", ErrLeaseUncertain, err)
}

func (m *Manager) renew(ctx context.Context, lease Lease) (leaseState, error) {
	table, _ := tableFor(lease.Kind)
	rows, err := client.Raw[leaseState](ctx, m.db, fmt.Sprintf(`UPDATE %s SET
		lease_until=NOW()+($4*INTERVAL '1 second'), heartbeat_at=NOW(), updated_at=NOW()
		WHERE id=$1 AND status='RUNNING' AND lease_owner=$2 AND attempts=$3
		RETURNING cancel_requested_at, lease_until`, table), lease.ID, lease.WorkerID, lease.Attempt, m.lease.Seconds())
	if err != nil {
		return leaseState{}, err
	}
	if len(rows) != 1 {
		return leaseState{}, ErrLeaseLost
	}
	_, _ = m.db.RawExec(ctx, `UPDATE job_executions SET heartbeat_at=NOW()
		WHERE job_type=$1 AND job_id=$2 AND attempt=$3 AND worker_id=$4 AND status='RUNNING'`,
		string(lease.Kind), lease.ID, lease.Attempt, lease.WorkerID)
	return rows[0], nil
}

func (m *Manager) state(ctx context.Context, lease Lease) (leaseState, error) {
	table, _ := tableFor(lease.Kind)
	rows, err := client.Raw[leaseState](ctx, m.db, fmt.Sprintf(`SELECT cancel_requested_at, lease_until FROM %s
		WHERE id=$1 AND status='RUNNING' AND lease_owner=$2 AND attempts=$3`, table),
		lease.ID, lease.WorkerID, lease.Attempt)
	if err != nil {
		return leaseState{}, err
	}
	if len(rows) != 1 {
		return leaseState{}, ErrLeaseLost
	}
	return rows[0], nil
}

func (m *Manager) finish(ctx context.Context, lease Lease, outcome Outcome) error {
	table, _ := tableFor(lease.Kind)
	status, nextAttempt, message, requeue := outcomeDecision(time.Now().UTC(), lease, outcome)
	resultJSON := nullableJSON(outcome.Result)
	compensationJSON := nullableJSON(outcome.Compensation)
	return m.db.Tx(ctx, func(tx *client.Client) error {
		var cancelled bool
		rows, queryErr := client.Raw[leaseState](ctx, tx, fmt.Sprintf(`SELECT cancel_requested_at FROM %s
			WHERE id=$1 AND status='RUNNING' AND lease_owner=$2 AND attempts=$3 FOR UPDATE`, table),
			lease.ID, lease.WorkerID, lease.Attempt)
		if queryErr != nil {
			return queryErr
		}
		if len(rows) != 1 {
			return ErrLeaseLost
		}
		cancelled = rows[0].CancelRequestedAt != nil
		if cancelled {
			status = "CANCELLED"
			requeue = false
			if message == "" {
				message = "job cancellation requested"
			}
		}
		affected, updateErr := tx.RawExec(ctx, fmt.Sprintf(`UPDATE %s SET status=$4,
			next_attempt_at=$5, lease_owner=NULL, lease_until=NULL, heartbeat_at=NULL,
			result_json=$6, compensation_json=$7, error=$8,
			attempts=GREATEST(attempts-$9, 0), updated_at=NOW()
			WHERE id=$1 AND status='RUNNING' AND lease_owner=$2 AND attempts=$3`, table),
			lease.ID, lease.WorkerID, lease.Attempt, status, nextAttempt, resultJSON, compensationJSON,
			nullableString(message), boolInt(requeue))
		if updateErr != nil {
			return updateErr
		}
		if affected != 1 {
			return ErrLeaseLost
		}
		executionStatus := status
		if status == "PENDING" {
			executionStatus = "FAILED"
		}
		_, updateErr = tx.RawExec(ctx, `UPDATE job_executions SET status=$5, finished_at=NOW(),
			result_json=$6, error=$7, heartbeat_at=NOW()
			WHERE job_type=$1 AND job_id=$2 AND attempt=$3 AND worker_id=$4 AND status='RUNNING'`,
			string(lease.Kind), lease.ID, lease.Attempt, lease.WorkerID, executionStatus,
			resultJSON, nullableString(message))
		return updateErr
	})
}

func outcomeDecision(now time.Time, lease Lease, outcome Outcome) (string, time.Time, string, bool) {
	status := "SUCCEEDED"
	nextAttempt := now
	message := ""
	requeue := outcome.RequeueAfter > 0
	if requeue {
		status = "PENDING"
		nextAttempt = nextAttempt.Add(outcome.RequeueAfter)
	}
	if outcome.Err == nil {
		return status, nextAttempt, message, requeue
	}
	message = outcome.Err.Error()
	if lease.TimeoutAt != nil && !now.Before(*lease.TimeoutAt) && (outcome.Retryable || requeue) {
		return "DEAD_LETTER", nextAttempt, message, false
	}
	if requeue {
		return status, nextAttempt, message, true
	}
	if outcome.Retryable && lease.Attempt < lease.MaxAttempts {
		return "PENDING", nextAttempt.Add(retryDelay(lease, outcome.RetryAfter)), message, false
	}
	if outcome.Retryable {
		return "DEAD_LETTER", nextAttempt, message, false
	}
	return "FAILED", nextAttempt, message, false
}

func (m *Manager) Cancel(ctx context.Context, kind Kind, id string) error {
	table, err := tableFor(kind)
	if err != nil {
		return err
	}
	affected, err := m.db.RawExec(ctx, fmt.Sprintf(`UPDATE %s SET
		cancel_requested_at=NOW(), status=CASE WHEN status='PENDING' THEN 'CANCELLED' ELSE status END,
		updated_at=NOW() WHERE id=$1 AND status IN ('PENDING','RUNNING')`, table), id)
	if err != nil {
		return err
	}
	if affected != 1 {
		return ErrNotCancellable
	}
	return nil
}

func (m *Manager) Replay(ctx context.Context, kind Kind, id string) error {
	table, err := tableFor(kind)
	if err != nil {
		return err
	}
	affected, err := m.db.RawExec(ctx, fmt.Sprintf(`UPDATE %s SET status='PENDING', attempts=0,
		next_attempt_at=NOW(), lease_owner=NULL, lease_until=NULL, heartbeat_at=NULL,
		cancel_requested_at=NULL, timeout_at=NULL, result_json=NULL, compensation_json=NULL,
		error=NULL, updated_at=NOW()
		WHERE id=$1 AND status IN ('FAILED','DEAD_LETTER','CANCELLED')`, table), id)
	if err != nil {
		return err
	}
	if affected != 1 {
		return ErrNotReplayable
	}
	return nil
}

func tableFor(kind Kind) (string, error) {
	table, ok := tables[kind]
	if !ok {
		return "", fmt.Errorf("unsupported job kind %q", kind)
	}
	return table, nil
}

// ValidateIdempotencyKey keeps user-controlled keys safe for indexes, logs,
// and advisory-lock identifiers. Empty keys mean idempotency is not requested.
func ValidateIdempotencyKey(value string) error {
	if len(value) > maxIdempotencyKeyBytes {
		return fmt.Errorf("%w: exceeds %d bytes", ErrInvalidIdempotencyKey, maxIdempotencyKeyBytes)
	}
	for index := 0; index < len(value); index++ {
		if value[index] < 0x21 || value[index] > 0x7e {
			return fmt.Errorf("%w: must contain only visible ASCII characters", ErrInvalidIdempotencyKey)
		}
	}
	return nil
}

func backoff(attempt int) time.Duration {
	if attempt >= 9 {
		return maxBackoff
	}
	if attempt < 0 {
		attempt = 0
	}
	return time.Second << attempt
}

func retryDelay(lease Lease, override time.Duration) time.Duration {
	delay := override
	if delay <= 0 {
		delay = backoff(lease.Attempt)
	}
	if lease.ID == "" {
		return delay
	}
	hash := fnv.New32a()
	_, _ = hash.Write([]byte(lease.ID))
	_, _ = hash.Write([]byte{byte(lease.Attempt)})
	factor := int64(80 + hash.Sum32()%41)
	jittered := time.Duration(int64(delay) * factor / 100)
	if override <= 0 && jittered > maxBackoff {
		return maxBackoff
	}
	return jittered
}

func nullableJSON(value any) any {
	if value == nil {
		return nil
	}
	payload, err := json.Marshal(value)
	if err != nil {
		slog.Warn("encode job result", "error", err)
		return nil
	}
	return payload
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
