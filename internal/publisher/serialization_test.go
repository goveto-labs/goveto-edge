package publisher

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"goveto-edge/internal/edgeprotocol"
	"goveto-edge/internal/jobqueue"
	"goveto-edge/internal/node"
	"goveto-edge/internal/storage/gen/client"
	"goveto-edge/internal/storage/gen/model"
	"goveto-edge/internal/storage/gen/query"
	"goveto-edge/internal/testutil"
)

type testDispatcher func(context.Context, string, string, any, any) error

func (d testDispatcher) Dispatch(ctx context.Context, nodeID, kind string, payload, result any) error {
	return d(ctx, nodeID, kind, payload, result)
}

func TestFirstPublishRollbackAndQueuedVersionIntegration(t *testing.T) {
	db := testutil.Database(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	user, err := db.User.Create().Set(query.User.Email.Set("publisher@example.test"), query.User.Name.Set("Publisher"), query.User.PasswordHash.Set("x")).Do(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cluster, err := db.Cluster.Create().Set(query.Cluster.CreatorId.Set(user.Id), query.Cluster.Name.Set("test")).Do(ctx)
	if err != nil {
		t.Fatal(err)
	}
	pool, err := db.OriginPool.Create().Set(query.OriginPool.ClusterId.Set(cluster.Id), query.OriginPool.Name.Set("origin")).Do(ctx)
	if err != nil {
		t.Fatal(err)
	}
	site, err := db.Site.Create().Set(query.Site.ClusterId.Set(cluster.Id), query.Site.CreatorId.Set(user.Id), query.Site.OriginPoolId.Set(pool.Id), query.Site.Name.Set("site"), query.Site.Version.Set(0)).Do(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var targets []target
	for _, name := range []string{"one", "two"} {
		n, err := db.Node.Create().Set(query.Node.ClusterId.Set(cluster.Id), query.Node.Name.Set(name)).Do(ctx)
		if err != nil {
			t.Fatal(err)
		}
		targets = append(targets, target{NodeID: n.Id})
	}
	cipher, err := node.NewCredentialCipher("MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")
	if err != nil {
		t.Fatal(err)
	}
	s := New(db, cipher, nil)
	targetJSON, _ := json.Marshal(targets)
	createJob := func(version int64) *model.PublishJob {
		t.Helper()
		config := edgeprotocol.SiteConfig{SiteID: site.Id, Version: uint64(version), Domains: []string{"example.test"}}
		if err := db.Tx(ctx, func(tx *client.Client) error { return s.createSnapshot(ctx, tx, config) }); err != nil {
			t.Fatal(err)
		}
		job, err := db.PublishJob.Create().Set(query.PublishJob.SiteId.Set(site.Id), query.PublishJob.Version.Set(version), query.PublishJob.Targets.Set(targetJSON)).Do(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return job
	}
	first, queued := createJob(1), createJob(2)
	// Fail after the agent accepted compensation, while persisting its final
	// status. Recovery must resume that plan, even after another empty outcome.
	if _, err := db.RawExec(ctx, `CREATE FUNCTION fail_rollback_finalize() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			IF NEW.version = 3 AND NEW.status = 'ROLLED_BACK' THEN
				RAISE EXCEPTION 'injected rollback persistence failure';
			END IF;
			RETURN NEW;
		END $$`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.RawExec(ctx, `CREATE TRIGGER fail_rollback_finalize BEFORE UPDATE ON config_versions
		FOR EACH ROW EXECUTE FUNCTION fail_rollback_finalize()`); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	var mu sync.Mutex
	versions := map[string]uint64{}
	s.gateway = testDispatcher(func(ctx context.Context, nodeID, kind string, payload, result any) error {
		config := payload.(edgeprotocol.SiteConfig)
		if config.Version == 1 {
			once.Do(func() { close(entered) })
			select {
			case <-release:
			case <-ctx.Done():
				return ctx.Err()
			}
			if nodeID == targets[1].NodeID {
				return errors.New("injected first-publish rejection")
			}
		}
		stored, err := db.ConfigVersion.Query().Where(query.ConfigVersion.SiteId.Equals(site.Id), query.ConfigVersion.Version.Equals(int64(config.Version))).First(ctx)
		if err != nil || stored == nil {
			return errors.New("snapshot was not committed before dispatch")
		}
		if config.Version == 3 && !config.Disabled {
			return errors.New("first publish rollback did not disable the site")
		}
		mu.Lock()
		defer mu.Unlock()
		if config.Version < versions[nodeID] {
			return errors.New("older config delivered after compensation")
		}
		versions[nodeID] = config.Version
		return nil
	})
	done := make(chan jobqueue.Outcome, 1)
	go func() {
		var outcome jobqueue.Outcome
		_, err := jobqueue.New(db).RunOne(ctx, jobqueue.Publish, time.Minute, func(runCtx context.Context, lease jobqueue.Lease) jobqueue.Outcome {
			outcome = s.execute(runCtx, first)
			return outcome
		})
		if err != nil {
			outcome = jobqueue.Outcome{Err: err, Retryable: true}
		}
		done <- outcome
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	contender := New(db, cipher, nil)
	contender.gateway = s.gateway
	claimed, err := jobqueue.New(db).RunOne(ctx, jobqueue.Publish, time.Minute, func(context.Context, jobqueue.Lease) jobqueue.Outcome {
		return jobqueue.Outcome{Err: errors.New("claimed a later version before the active publish finished")}
	})
	if err != nil || claimed {
		t.Fatalf("second replica claimed queued job: claimed=%t err=%v", claimed, err)
	}
	if outcome := contender.execute(ctx, queued); outcome.RequeueAfter == 0 {
		t.Fatalf("second replica was not serialized: %+v", outcome)
	}
	close(release)
	var outcome jobqueue.Outcome
	select {
	case outcome = <-done:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if outcome.Err == nil || !outcome.Retryable || outcome.Compensation == nil {
		t.Fatalf("first publish outcome=%+v", outcome)
	}
	recovery, err := db.PublishJob.FindUnique(ctx, query.PublishJob.Id.Equals(first.Id))
	if err != nil || recovery == nil || recovery.Status != model.JobStatusPENDING || recovery.CompensationJson == nil {
		t.Fatalf("persisted recovery=%+v err=%v", recovery, err)
	}
	makeRunnable := func() {
		t.Helper()
		if _, err := db.RawExec(ctx, "UPDATE publish_jobs SET next_attempt_at=NOW() WHERE id=$1", first.Id); err != nil {
			t.Fatal(err)
		}
	}
	makeRunnable()
	claimed, err = jobqueue.New(db).RunOne(ctx, jobqueue.Publish, time.Minute, func(context.Context, jobqueue.Lease) jobqueue.Outcome {
		return jobqueue.Outcome{Err: errors.New("injected temporary job load failure"), Retryable: true}
	})
	if err != nil || !claimed {
		t.Fatalf("recovery claim=%t err=%v", claimed, err)
	}
	retained, err := db.PublishJob.FindUnique(ctx, query.PublishJob.Id.Equals(first.Id))
	if err != nil || retained == nil || retained.CompensationJson == nil || !bytes.Equal(*retained.CompensationJson, *recovery.CompensationJson) {
		t.Fatalf("recovery plan lost after empty outcome: %+v err=%v", retained, err)
	}
	if _, err := db.RawExec(ctx, "DROP TRIGGER fail_rollback_finalize ON config_versions"); err != nil {
		t.Fatal(err)
	}
	makeRunnable()
	claimed, err = jobqueue.New(db).RunOne(ctx, jobqueue.Publish, time.Minute, func(runCtx context.Context, lease jobqueue.Lease) jobqueue.Outcome {
		outcome = contender.execute(runCtx, first)
		return outcome
	})
	if err != nil || !claimed || outcome.Err == nil || outcome.Retryable {
		t.Fatalf("resumed rollback: claimed=%t outcome=%+v err=%v", claimed, outcome, err)
	}
	current, err := db.Site.FindUnique(ctx, query.Site.Id.Equals(site.Id))
	if err != nil || current.Version != 3 {
		t.Fatalf("compensated site=%+v err=%v", current, err)
	}
	rebased, err := db.PublishJob.FindUnique(ctx, query.PublishJob.Id.Equals(queued.Id))
	if err != nil || rebased.Version != 4 {
		t.Fatalf("queued job=%+v err=%v", rebased, err)
	}
	// Crash/retry resumes the committed compensation, never version 1.
	if replay := s.execute(ctx, first); replay.Err == nil {
		t.Fatal("business failure changed to success on replay")
	}
	if outcome := contender.execute(ctx, queued); outcome.Err != nil {
		t.Fatalf("queued publish: %+v", outcome)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, target := range targets {
		if versions[target.NodeID] != 4 {
			t.Fatalf("node version=%d", versions[target.NodeID])
		}
	}
}
