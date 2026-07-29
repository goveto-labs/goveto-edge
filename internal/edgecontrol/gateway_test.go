package edgecontrol

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/encoding"

	"goveto-edge/internal/edgeprotocol"
)

func TestGatewayRegistersGzipAndKeepsLegacyLogAck(t *testing.T) {
	if encoding.GetCompressor("gzip") == nil {
		t.Fatal("gateway cannot decompress gzip agent messages")
	}
	message := acceptedLogAck(42)
	if message.LogsAck == nil || !message.LogsAck.Accepted || message.LogsAck.Through != 42 {
		t.Fatalf("structured log acknowledgement is missing: %#v", message)
	}
	if message.LogsAckThrough == nil || *message.LogsAckThrough != 42 {
		t.Fatalf("legacy agent acknowledgement is missing: %#v", message)
	}
}

func TestObserveAgentLogQueueTracksBacklogAndDrain(t *testing.T) {
	gateway := NewGateway(nil, nil, nil, nil, nil)
	active := &session{
		logQueue: agentLogQueueState{nonEmptySince: time.Now().Add(-2 * time.Minute)},
	}
	gateway.observeAgentLogQueue("node-1", active, edgeprotocol.AgentHeartbeat{
		QueueRecords: 12,
		QueueBytes:   4096,
	})
	if active.logQueue.lastWarning.IsZero() || active.logQueue.records != 12 || active.logQueue.bytes != 4096 {
		t.Fatalf("backlog was not observed: %#v", active.logQueue)
	}
	gateway.observeAgentLogQueue("node-1", active, edgeprotocol.AgentHeartbeat{})
	if !active.logQueue.nonEmptySince.IsZero() || !active.logQueue.lastWarning.IsZero() {
		t.Fatalf("drained queue state was not reset: %#v", active.logQueue)
	}
}

func TestDispatchTaskUsesSeparateTypedIdempotencyParameter(t *testing.T) {
	if !strings.Contains(dispatchTaskSQL, "'PENDING', $6") {
		t.Fatalf("dispatch SQL does not use a separate idempotency parameter: %s", dispatchTaskSQL)
	}
	if strings.Contains(dispatchTaskSQL, "'PENDING', $1") {
		t.Fatalf("dispatch SQL reuses the UUID parameter for a text column: %s", dispatchTaskSQL)
	}
}

func TestDispatchTaskStateAllowsPendingNullResult(t *testing.T) {
	state := dispatchTaskState{Status: "PENDING"}
	if state.Result != nil || state.Error != nil {
		t.Fatalf("pending task state should allow null result and error: %#v", state)
	}
}

func TestValidateLogBatch(t *testing.T) {
	valid := edgeprotocol.AgentLogBatch{
		FirstID: 10, Through: 11, Bytes: 100,
		Records: []edgeprotocol.LogRecord{{ID: 10}, {ID: 11}},
	}
	if err := validateLogBatch(valid); err != nil {
		t.Fatal(err)
	}
	invalid := valid
	invalid.Through = 12
	if err := validateLogBatch(invalid); err == nil {
		t.Fatal("mismatched acknowledgement cursor was accepted")
	}
	invalid = valid
	invalid.Records[1].ID = 9
	if err := validateLogBatch(invalid); err == nil {
		t.Fatal("decreasing record IDs were accepted")
	}
	oversized := edgeprotocol.AgentLogBatch{
		Through: 1,
		Records: []edgeprotocol.LogRecord{{ID: 1, Payload: json.RawMessage(`{"padding":"` + strings.Repeat("x", maxLogBatchBytes) + `"}`)}},
	}
	if err := validateLogBatch(oversized); err == nil {
		t.Fatal("batch with an understated byte count exceeded the gateway limit")
	}
}

func TestDisconnectCancelsLocalSessionWithoutDatabase(t *testing.T) {
	gateway := NewGateway(nil, nil, nil, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	active := &session{cancel: cancel, wake: make(chan struct{}, 1), owner: "test"}
	gateway.register("node-1", active)

	gateway.Disconnect(context.Background(), "node-1")
	select {
	case <-ctx.Done():
	default:
		t.Fatal("disconnect did not cancel the local session")
	}
}

func TestWakeSignalsLocalSessionWithoutDatabase(t *testing.T) {
	gateway := NewGateway(nil, nil, nil, nil, nil)
	active := &session{cancel: func() {}, wake: make(chan struct{}, 1), owner: "test"}
	gateway.register("node-1", active)

	gateway.signal(context.Background(), "wake", "node-1")
	select {
	case <-active.wake:
	default:
		t.Fatal("wake did not signal the local session")
	}
}

func TestRegisterReplacesPreviousSession(t *testing.T) {
	gateway := NewGateway(nil, nil, nil, nil, nil)
	firstCtx, firstCancel := context.WithCancel(context.Background())
	first := &session{cancel: firstCancel, wake: make(chan struct{}, 1), owner: "first"}
	gateway.register("node-1", first)

	secondCtx, secondCancel := context.WithCancel(context.Background())
	defer secondCancel()
	second := &session{cancel: secondCancel, wake: make(chan struct{}, 1), owner: "second"}
	gateway.register("node-1", second)

	select {
	case <-firstCtx.Done():
	default:
		t.Fatal("register did not cancel the previous session")
	}
	select {
	case <-secondCtx.Done():
		t.Fatal("register cancelled the current session")
	default:
	}

	gateway.unregister("node-1", first)
	gateway.mu.Lock()
	active := gateway.sessions["node-1"]
	gateway.mu.Unlock()
	if active != second {
		t.Fatal("unregister removed the active session using a stale pointer")
	}
}

func TestApplyEventIgnoresUnknownKinds(t *testing.T) {
	gateway := NewGateway(nil, nil, nil, nil, nil)
	active := &session{cancel: func() {}, wake: make(chan struct{}, 1), owner: "test"}
	gateway.register("node-1", active)
	gateway.applyEvent(gatewayEvent{Kind: "noop", NodeID: "node-1"})
	select {
	case <-active.wake:
		t.Fatal("unknown event kind should not wake the session")
	default:
	}
}

func TestWakeIsNonBlockingWhenAlreadySignaled(t *testing.T) {
	gateway := NewGateway(nil, nil, nil, nil, nil)
	active := &session{cancel: func() {}, wake: make(chan struct{}, 1), owner: "test"}
	gateway.register("node-1", active)
	gateway.wakeLocal("node-1")
	gateway.wakeLocal("node-1")
	select {
	case <-active.wake:
	default:
		t.Fatal("expected one buffered wake signal")
	}
	select {
	case <-active.wake:
		t.Fatal("wake channel should stay non-blocking with capacity 1")
	default:
	}
}

func TestResetTimerDrainsExpiredChannel(t *testing.T) {
	timer := time.NewTimer(time.Millisecond)
	defer timer.Stop()
	time.Sleep(5 * time.Millisecond)
	resetTimer(timer, 50*time.Millisecond)
	select {
	case <-timer.C:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("reset timer did not fire")
	}
}

func TestNullableHelpers(t *testing.T) {
	if nullableJSON(nil) != nil {
		t.Fatal("empty json should become nil")
	}
	if nullableJSON(json.RawMessage(`{}`)) == nil {
		t.Fatal("non-empty json should be preserved")
	}
	if nullableString("") != nil {
		t.Fatal("empty string should become nil")
	}
	if nullableString("err") != "err" {
		t.Fatal("non-empty string should be preserved")
	}
}

func TestAbandonedDispatchCancellationCoversPendingAndRunningTasks(t *testing.T) {
	for _, fragment := range []string{
		"status='CANCELLED'",
		"cancel_requested_at=NOW()",
		"status IN ('PENDING','RUNNING')",
	} {
		if !strings.Contains(cancelAbandonedTaskSQL, fragment) {
			t.Fatalf("dispatch cleanup SQL missing %q: %s", fragment, cancelAbandonedTaskSQL)
		}
	}
}

func TestClaimTasksPrioritizesGeoIPSynchronization(t *testing.T) {
	for _, fragment := range []string{
		"ORDER BY CASE WHEN t.kind = $5 THEN 0 ELSE 1 END",
		"t.created_at",
		"FOR UPDATE OF t SKIP LOCKED",
	} {
		if !strings.Contains(claimTasksSQL, fragment) {
			t.Fatalf("agent task claim SQL missing %q: %s", fragment, claimTasksSQL)
		}
	}
}
