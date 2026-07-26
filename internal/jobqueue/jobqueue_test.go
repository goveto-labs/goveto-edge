package jobqueue

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestInvokeHandlerRecoversPanics(t *testing.T) {
	outcome := invokeHandler(context.Background(), Lease{}, func(context.Context, Lease) Outcome {
		panic("broken handler")
	})
	if outcome.Err == nil || !outcome.Retryable {
		t.Fatalf("panic outcome = %#v", outcome)
	}
}

func TestInvokeHandlerPreservesSuccessfulOutcome(t *testing.T) {
	want := map[string]bool{"ok": true}
	outcome := invokeHandler(context.Background(), Lease{}, func(context.Context, Lease) Outcome {
		return Outcome{Result: want}
	})
	if outcome.Err != nil || outcome.Result == nil {
		t.Fatalf("successful outcome = %#v", outcome)
	}
}

func TestBackoffIsExponentialAndCapped(t *testing.T) {
	if got := backoff(1); got != 2*time.Second {
		t.Fatalf("first retry delay = %s", got)
	}
	if got := backoff(8); got != 256*time.Second {
		t.Fatalf("eighth retry delay = %s", got)
	}
	if got := backoff(20); got != 5*time.Minute {
		t.Fatalf("capped retry delay = %s", got)
	}
}

func TestTableForRejectsUntrustedNames(t *testing.T) {
	if _, err := tableFor(Kind("publish_jobs; DROP TABLE users")); err == nil {
		t.Fatal("untrusted table name was accepted")
	}
	if table, err := tableFor(Publish); err != nil || table != "publish_jobs" {
		t.Fatalf("publish table = %q, %v", table, err)
	}
}

func TestValidateIdempotencyKey(t *testing.T) {
	if err := ValidateIdempotencyKey("request-1234:publish"); err != nil {
		t.Fatalf("valid key rejected: %v", err)
	}
	if err := ValidateIdempotencyKey(""); err != nil {
		t.Fatalf("empty key rejected: %v", err)
	}
	if err := ValidateIdempotencyKey("contains space"); err == nil {
		t.Fatal("key with whitespace was accepted")
	}
	if err := ValidateIdempotencyKey(strings.Repeat("a", maxIdempotencyKeyBytes+1)); err == nil {
		t.Fatal("oversized key was accepted")
	}
}

func TestOutcomeDecisionRequeuesContentionWithoutConsumingAttempt(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	status, next, message, requeue := outcomeDecision(now, Lease{Attempt: 8, MaxAttempts: 8}, Outcome{
		Err: errors.New("lock busy"), RequeueAfter: 2 * time.Second,
	})
	if status != "PENDING" || !requeue || !next.Equal(now.Add(2*time.Second)) || message != "lock busy" {
		t.Fatalf("contention decision = %q, %s, %q, %v", status, next, message, requeue)
	}
}

func TestOutcomeDecisionDistinguishesTerminalFailures(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	lease := Lease{Attempt: 3, MaxAttempts: 3}
	status, _, _, _ := outcomeDecision(now, lease, Outcome{Err: errors.New("temporary"), Retryable: true})
	if status != "DEAD_LETTER" {
		t.Fatalf("exhausted retry status = %q", status)
	}
	status, _, _, _ = outcomeDecision(now, lease, Outcome{Err: errors.New("permanent")})
	if status != "FAILED" {
		t.Fatalf("permanent failure status = %q", status)
	}
}

func TestOutcomeDecisionDoesNotRetryPastDeadline(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	deadline := now.Add(-time.Second)
	lease := Lease{Attempt: 1, MaxAttempts: 3, TimeoutAt: &deadline}
	status, _, _, _ := outcomeDecision(now, lease, Outcome{Err: errors.New("temporary"), Retryable: true})
	if status != "DEAD_LETTER" {
		t.Fatalf("expired retry status = %q", status)
	}
	status, _, _, _ = outcomeDecision(now, lease, Outcome{Result: "completed"})
	if status != "SUCCEEDED" {
		t.Fatalf("successful outcome after deadline = %q", status)
	}
}

func TestClaimInitializesDeadlineFromClaimTime(t *testing.T) {
	statement := claimSQL("install_jobs")
	if !strings.Contains(statement,
		"timeout_at=COALESCE(timeout_at, CASE WHEN $3>0 THEN NOW()+($3*INTERVAL '1 second') END)") {
		t.Fatalf("claim does not initialize timeout from claim time: %s", statement)
	}
}

func TestSweepableJobPredicateProtectsActiveLeases(t *testing.T) {
	want := "(status='PENDING' OR (status='RUNNING' AND (lease_until IS NULL OR lease_until<=NOW())))"
	if sweepableJobPredicate != want {
		t.Fatalf("sweep predicate can terminate an actively leased job: %s", sweepableJobPredicate)
	}
}

func TestLeaseProbeFailureDistinguishesTransientAndLostLeases(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	databaseErr := errors.New("database unavailable")
	if err := leaseProbeFailure(databaseErr, now, now.Add(time.Second)); err != nil {
		t.Fatalf("transient error inside lease window = %v", err)
	}
	if err := leaseProbeFailure(ErrLeaseLost, now, now.Add(time.Second)); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("confirmed lease loss = %v", err)
	}
	err := leaseProbeFailure(databaseErr, now, now)
	if !errors.Is(err, ErrLeaseUncertain) || !errors.Is(err, databaseErr) {
		t.Fatalf("expired uncertain lease = %v", err)
	}
}

func TestManagerTracksSweepDeadlinesPerKind(t *testing.T) {
	manager := New(nil)
	publishDeadline := time.Unix(100, 0).UTC()
	manager.nextSweep[Publish] = publishDeadline
	if !manager.nextSweep[DNS].IsZero() {
		t.Fatalf("publish sweep throttled DNS until %s", manager.nextSweep[DNS])
	}
}

func TestRetryDelayAppliesDeterministicJitter(t *testing.T) {
	lease := Lease{ID: "job-1", Attempt: 3}
	first := retryDelay(lease, 0)
	second := retryDelay(lease, 0)
	if first != second || first < 6400*time.Millisecond || first > 9600*time.Millisecond {
		t.Fatalf("jittered delay = %s, repeated = %s", first, second)
	}
}
