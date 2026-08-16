package logpush

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/twmb/franz-go/pkg/kgo"

	"goveto-edge/internal/edgeprotocol"
	"goveto-edge/internal/storage/gen/model"
)

type fakeProducer struct {
	mu      sync.Mutex
	records []*kgo.Record
	failErr error
	// block, when non-nil, makes Produce wait so tests can fill the queue.
	block chan struct{}
	// entered is closed when the first Produce call starts.
	entered     chan struct{}
	enteredOnce sync.Once
	closed      bool
}

func (f *fakeProducer) Produce(ctx context.Context, record *kgo.Record, onDone func(*kgo.Record, error)) {
	if f.entered != nil {
		f.enteredOnce.Do(func() { close(f.entered) })
	}
	if f.block != nil {
		select {
		case <-f.block:
		case <-ctx.Done():
			onDone(record, ctx.Err())
			return
		}
	}
	f.mu.Lock()
	f.records = append(f.records, record)
	failErr := f.failErr
	f.mu.Unlock()
	onDone(record, failErr)
}

func (f *fakeProducer) Close() {
	f.mu.Lock()
	f.closed = true
	f.mu.Unlock()
}

func (f *fakeProducer) produced() []*kgo.Record {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*kgo.Record(nil), f.records...)
}

func testDestination(name string, logTypes ...string) Destination {
	wants := map[string]bool{}
	for _, logType := range logTypes {
		wants[logType] = true
	}
	return Destination{
		ID: "dest-1", ClusterID: "cluster-1", Name: name,
		Brokers: []string{"kafka:9092"}, Topic: "edge-logs",
		LogTypes: wants,
	}
}

func newTestWorker(d *Dispatcher, dest Destination, prod producer, queueSize int) *destinationWorker {
	worker := &destinationWorker{
		dest:     dest,
		producer: prod,
		queue:    make(chan *kgo.Record, queueSize),
		quit:     make(chan struct{}),
		done:     make(chan struct{}),
	}
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		worker.run()
	}()
	return worker
}

func installWorkers(d *Dispatcher, clusterID string, workers ...*destinationWorker) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.states[clusterID] = &clusterDestinations{
		expiresAt: time.Now().Add(time.Minute),
		workers:   workers,
	}
}

func TestEnqueueFiltersTypesAndBuildsRecord(t *testing.T) {
	d := NewDispatcher(nil, nil, 16, time.Millisecond)
	fake := &fakeProducer{}
	dest := testDestination("t-filter", LogTypeAccess)
	t.Cleanup(func() { forgetDestinationMetrics(dest) })
	worker := newTestWorker(d, dest, fake, 16)
	installWorkers(d, "cluster-1", worker)
	defer d.Close()

	records := []edgeprotocol.LogRecord{
		{ID: 7, Type: LogTypeAccess, SiteID: "site-9", Payload: json.RawMessage(`{"status":200}`)},
		{ID: 8, Type: LogTypeNodeRuntime, Payload: json.RawMessage(`{}`)},
	}
	d.Enqueue(context.Background(), "cluster-1", "node-1", records)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && len(fake.produced()) == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	produced := fake.produced()
	if len(produced) != 1 {
		t.Fatalf("produced %d records, want 1 (only access type)", len(produced))
	}
	message := produced[0]
	if message.Topic != "edge-logs" {
		t.Errorf("topic = %q", message.Topic)
	}
	if string(message.Key) != "site-9" {
		t.Errorf("key = %q, want site id", message.Key)
	}
	headers := map[string]string{}
	for _, header := range message.Headers {
		headers[header.Key] = string(header.Value)
	}
	if headers["cluster_id"] != "cluster-1" || headers["type"] != "access" || headers["source_log_id"] != "7" {
		t.Errorf("headers = %v", headers)
	}
	var decoded map[string]any
	if err := json.Unmarshal(message.Value, &decoded); err != nil {
		t.Fatalf("value is not JSON: %v", err)
	}
	if decoded["site_id"] != "site-9" {
		t.Errorf("value site_id = %v", decoded["site_id"])
	}
	if got := testutil.ToFloat64(recordsDelivered.WithLabelValues("cluster-1", "dest-1", "t-filter")); got != 1 {
		t.Errorf("delivered = %v, want 1", got)
	}
}

func TestEnqueueFallsBackToNodeKey(t *testing.T) {
	d := NewDispatcher(nil, nil, 16, time.Millisecond)
	fake := &fakeProducer{}
	dest := testDestination("t-nodekey", LogTypeCaddy)
	t.Cleanup(func() { forgetDestinationMetrics(dest) })
	worker := newTestWorker(d, dest, fake, 16)
	installWorkers(d, "cluster-1", worker)
	defer d.Close()

	d.Enqueue(context.Background(), "cluster-1", "node-7", []edgeprotocol.LogRecord{
		{ID: 1, Type: LogTypeCaddy, Payload: json.RawMessage(`{}`)},
	})
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && len(fake.produced()) == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	if produced := fake.produced(); len(produced) != 1 || string(produced[0].Key) != "node-7" {
		t.Fatalf("produced = %v, want one record keyed by node id", produced)
	}
}

func TestEnqueueDropsWhenQueueFull(t *testing.T) {
	d := NewDispatcher(nil, nil, 1, time.Millisecond)
	fake := &fakeProducer{block: make(chan struct{}), entered: make(chan struct{})}
	dest := testDestination("t-full", LogTypeAccess)
	t.Cleanup(func() { forgetDestinationMetrics(dest) })
	worker := newTestWorker(d, dest, fake, 1)
	installWorkers(d, "cluster-1", worker)

	// The worker takes the first record and blocks inside Produce; one more
	// fills the queue; the rest must be dropped without blocking the caller.
	d.Enqueue(context.Background(), "cluster-1", "node-1", []edgeprotocol.LogRecord{
		{ID: 1, Type: LogTypeAccess, Payload: json.RawMessage(`{}`)},
	})
	select {
	case <-fake.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("worker never started producing")
	}
	records := []edgeprotocol.LogRecord{
		{ID: 2, Type: LogTypeAccess, Payload: json.RawMessage(`{}`)},
		{ID: 3, Type: LogTypeAccess, Payload: json.RawMessage(`{}`)},
		{ID: 4, Type: LogTypeAccess, Payload: json.RawMessage(`{}`)},
	}
	done := make(chan struct{})
	go func() {
		d.Enqueue(context.Background(), "cluster-1", "node-1", records)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Enqueue blocked on a full queue")
	}

	close(fake.block)
	d.Close()
	if got := testutil.ToFloat64(recordsDropped.WithLabelValues("cluster-1", "dest-1", "t-full", "queue_full")); got != 2 {
		t.Errorf("queue_full drops = %v, want 2", got)
	}
}

func TestEnqueueDoesNotBlockOnDestinationRefresh(t *testing.T) {
	d := NewDispatcher(nil, nil, 16, time.Millisecond)
	started := make(chan struct{})
	release := make(chan struct{})
	d.loadWorkers = func(ctx context.Context, _ string) ([]*destinationWorker, error) {
		close(started)
		select {
		case <-release:
			return nil, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	done := make(chan struct{})
	go func() {
		d.Enqueue(context.Background(), "cluster-refresh", "node-1", []edgeprotocol.LogRecord{
			{ID: 1, Type: LogTypeAccess, Payload: json.RawMessage(`{}`)},
		})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(250 * time.Millisecond):
		t.Fatal("Enqueue waited for the destination refresh")
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("destination refresh did not start")
	}
	close(release)
	d.Close()
}

func TestEnqueueDropsRecordOverByteBudget(t *testing.T) {
	dest := testDestination("t-byte-budget", LogTypeAccess)
	t.Cleanup(func() { forgetDestinationMetrics(dest) })
	worker := &destinationWorker{
		dest:  dest,
		queue: make(chan *kgo.Record, 1),
		quit:  make(chan struct{}),
	}
	worker.queuedBytes.Store(maxBufferedBytesPerStage - 1)
	worker.enqueue("cluster-1", "node-1", []edgeprotocol.LogRecord{
		{ID: 1, Type: LogTypeAccess, Payload: json.RawMessage(`{}`)},
	})

	if got := len(worker.queue); got != 0 {
		t.Fatalf("queue length = %d, want 0", got)
	}
	if got := worker.queuedBytes.Load(); got != maxBufferedBytesPerStage-1 {
		t.Fatalf("reserved bytes = %d, want %d", got, maxBufferedBytesPerStage-1)
	}
	if got := testutil.ToFloat64(recordsDropped.WithLabelValues("cluster-1", "dest-1", "t-byte-budget", "queue_bytes")); got != 1 {
		t.Errorf("queue_bytes drops = %v, want 1", got)
	}
}

func TestEnqueueAfterWorkerStopIsDropped(t *testing.T) {
	d := NewDispatcher(nil, nil, 1, time.Millisecond)
	dest := testDestination("t-stopped", LogTypeAccess)
	t.Cleanup(func() { forgetDestinationMetrics(dest) })
	worker := newTestWorker(d, dest, &fakeProducer{}, 1)
	worker.stop()

	worker.enqueue("cluster-1", "node-1", []edgeprotocol.LogRecord{
		{ID: 1, Type: LogTypeAccess, Payload: json.RawMessage(`{}`)},
	})
	if got := len(worker.queue); got != 0 {
		t.Fatalf("queue length = %d after stop, want 0", got)
	}
	if got := testutil.ToFloat64(recordsDropped.WithLabelValues("cluster-1", "dest-1", "t-stopped", "stopped")); got != 1 {
		t.Errorf("stopped drops = %v, want 1", got)
	}
	d.Close()
}

func TestDestinationMetricsAreIsolatedAndCleanedUp(t *testing.T) {
	first := testDestination("shared-name", LogTypeAccess)
	first.ClusterID = "metrics-cluster-a"
	first.ID = "metrics-dest-a"
	second := testDestination("shared-name", LogTypeAccess)
	second.ClusterID = "metrics-cluster-b"
	second.ID = "metrics-dest-b"
	initializeDestinationMetrics(first)
	initializeDestinationMetrics(second)
	defer forgetDestinationMetrics(second)

	recordsEnqueued.WithLabelValues(destinationLabels(first)...).Inc()
	recordsEnqueued.WithLabelValues(destinationLabels(second)...).Add(2)
	recordsDropped.WithLabelValues(append(destinationLabels(first), "queue_full")...).Inc()
	recordsDropped.WithLabelValues(append(destinationLabels(second), "queue_full")...).Add(3)
	queueDepth.WithLabelValues(destinationLabels(first)...).Set(4)
	queueDepth.WithLabelValues(destinationLabels(second)...).Set(5)
	forgetDestinationMetrics(first)

	if recordsEnqueued.DeleteLabelValues(destinationLabels(first)...) {
		t.Error("first destination enqueued metric was not cleaned up")
	}
	if recordsDropped.DeleteLabelValues(append(destinationLabels(first), "queue_full")...) {
		t.Error("first destination dropped metric was not cleaned up")
	}
	if queueDepth.DeleteLabelValues(destinationLabels(first)...) {
		t.Error("first destination queue metric was not cleaned up")
	}
	if got := testutil.ToFloat64(recordsEnqueued.WithLabelValues(destinationLabels(second)...)); got != 2 {
		t.Errorf("second destination enqueued = %v, want 2", got)
	}
	if got := testutil.ToFloat64(recordsDropped.WithLabelValues(append(destinationLabels(second), "queue_full")...)); got != 3 {
		t.Errorf("second destination dropped = %v, want 3", got)
	}
	if got := testutil.ToFloat64(queueDepth.WithLabelValues(destinationLabels(second)...)); got != 5 {
		t.Errorf("second destination queue depth = %v, want 5", got)
	}
}

func TestEnqueueWithoutDestinationsIsNoop(t *testing.T) {
	d := NewDispatcher(nil, nil, 16, time.Millisecond)
	defer d.Close()
	// No state installed for the cluster and no DB to load from: workersFor
	// returns nil and Enqueue must not panic.
	d.states["cluster-x"] = &clusterDestinations{expiresAt: time.Now().Add(time.Minute)}
	d.Enqueue(context.Background(), "cluster-x", "node-1", []edgeprotocol.LogRecord{
		{ID: 1, Type: LogTypeAccess, Payload: json.RawMessage(`{}`)},
	})
}

func TestInvalidateStopsWorkers(t *testing.T) {
	d := NewDispatcher(nil, nil, 16, time.Millisecond)
	fake := &fakeProducer{}
	worker := newTestWorker(d, testDestination("t-invalidate", LogTypeAccess), fake, 16)
	installWorkers(d, "cluster-1", worker)

	d.Invalidate("cluster-1")
	if !fake.closed {
		t.Error("Invalidate must close the destination producer")
	}
	d.mu.Lock()
	_, exists := d.states["cluster-1"]
	d.mu.Unlock()
	if exists {
		t.Error("Invalidate must drop the cached cluster state")
	}
	d.Close()
}

func TestCloseStopsAllWorkers(t *testing.T) {
	d := NewDispatcher(nil, nil, 16, time.Millisecond)
	first := &fakeProducer{}
	second := &fakeProducer{}
	installWorkers(d, "cluster-1",
		newTestWorker(d, testDestination("t-close-a", LogTypeAccess), first, 16),
	)
	installWorkers(d, "cluster-2",
		newTestWorker(d, testDestination("t-close-b", LogTypeAccess), second, 16),
	)
	d.Close()
	if !first.closed || !second.closed {
		t.Errorf("Close must close all producers (first=%t second=%t)", first.closed, second.closed)
	}
	// Enqueue after Close is a no-op.
	d.Enqueue(context.Background(), "cluster-1", "node-1", []edgeprotocol.LogRecord{
		{ID: 1, Type: LogTypeAccess, Payload: json.RawMessage(`{}`)},
	})
}

func TestDestinationFromModelRoundTrip(t *testing.T) {
	mechanism := SASLSCRAMSHA256
	ciphertext := "encrypted-payload"
	credentials := `{"username":"edge","password":"secret"}`
	item := &model.LogpushDestination{
		Id: "dest-1", ClusterId: "cluster-1", Name: "warehouse",
		Brokers: "kafka-1:9092, kafka-2:9092", Topic: "edge-logs",
		LogTypes:             json.RawMessage(`["access"]`),
		TlsEnabled:           true,
		SaslMechanism:        &mechanism,
		CredentialsEncrypted: &ciphertext,
		Enabled:              true,
	}
	dest, err := DestinationFromModel(item, credentials)
	if err != nil {
		t.Fatal(err)
	}
	if dest.SASLMechanism != mechanism || dest.Username != "edge" || dest.Password != "secret" {
		t.Fatalf("dest = %#v", dest)
	}
	if len(dest.Brokers) != 2 {
		t.Fatalf("brokers = %v", dest.Brokers)
	}
	if !dest.Wants(LogTypeAccess) || dest.Wants(LogTypeCaddy) {
		t.Fatal("log type filter mismatch")
	}
}
