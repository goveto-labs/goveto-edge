package logpush

import (
	"context"
	"encoding/json"
	"log/slog"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"

	"goveto-edge/internal/edgeprotocol"
	"goveto-edge/internal/node"
	"goveto-edge/internal/storage/gen/client"
	"goveto-edge/internal/storage/gen/query"
)

// produceRecordTimeout bounds how long a worker waits for a record to enter
// the client's produce buffer; on expiry the client fails the record and the
// worker moves on, keeping a stalled broker from wedging the queue.
const (
	produceRecordTimeout     = 10 * time.Second
	destinationCacheTTL      = 30 * time.Second
	destinationLoadTimeout   = 5 * time.Second
	destinationLoadRetry     = 2 * time.Second
	maxBufferedBytesPerStage = 16 << 20
	maxKafkaBufferedRecords  = 4096
	maxQueueRecords          = 65536
)

// CredentialScope is the encryption scope under which a destination's SASL
// credentials are sealed. It is shared with the management API.
func CredentialScope(clusterID, destinationID string) string {
	return "logpush-destination:" + clusterID + ":" + destinationID
}

// Dispatcher fans agent log records out to a cluster's logpush destinations.
// Delivery is best-effort: Enqueue never blocks the caller and never fails;
// drops are counted in metrics instead. An at-least-once outbox can later sit
// behind the same Enqueue contract without changing callers.
type Dispatcher struct {
	db        *client.Client
	cipher    *node.CredentialCipher
	queueSize int
	cacheTTL  time.Duration

	// newProducer is replaceable in tests.
	newProducer func(Destination) (producer, error)
	// loadWorkers is replaceable in tests to exercise cache refreshes without a database.
	loadWorkers func(context.Context, string) ([]*destinationWorker, error)

	mu     sync.Mutex
	states map[string]*clusterDestinations
	closed bool
	wg     sync.WaitGroup
	loadWG sync.WaitGroup
}

type clusterDestinations struct {
	expiresAt  time.Time
	workers    []*destinationWorker
	generation uint64
	loading    bool
}

type destinationWorker struct {
	dest        Destination
	producer    producer
	queue       chan *kgo.Record
	quit        chan struct{}
	done        chan struct{}
	stopOnce    sync.Once
	enqueueMu   sync.RWMutex
	stopped     bool
	queuedBytes atomic.Int64
}

// NewDispatcher builds a dispatcher reading destinations from the ORM client
// and decrypting credentials with cipher. queueSize bounds each destination's
// in-memory backlog; linger controls Kafka produce batching.
func NewDispatcher(db *client.Client, cipher *node.CredentialCipher, queueSize int, linger time.Duration) *Dispatcher {
	if queueSize < 1 {
		queueSize = 1
	} else if queueSize > maxQueueRecords {
		queueSize = maxQueueRecords
	}
	dispatcher := &Dispatcher{
		db:        db,
		cipher:    cipher,
		queueSize: queueSize,
		cacheTTL:  destinationCacheTTL,
		newProducer: func(dest Destination) (producer, error) {
			bufferedRecords := min(queueSize, maxKafkaBufferedRecords)
			return newKafkaProducer(dest, linger, bufferedRecords, maxBufferedBytesPerStage)
		},
		states: map[string]*clusterDestinations{},
	}
	if db != nil {
		dispatcher.loadWorkers = dispatcher.load
	}
	return dispatcher
}

// Enqueue queues the records for every enabled destination of the cluster
// that subscribes to their type. It never blocks: a full destination queue
// drops the record and increments the dropped metric.
func (d *Dispatcher) Enqueue(_ context.Context, clusterID, nodeID string, records []edgeprotocol.LogRecord) {
	if d == nil || len(records) == 0 {
		return
	}
	for _, worker := range d.workersFor(clusterID) {
		worker.enqueue(clusterID, nodeID, records)
	}
}

// Invalidate schedules an immediate refresh while callers keep using the
// current immutable worker snapshot. CRUD writes call this explicitly.
func (d *Dispatcher) Invalidate(clusterID string) {
	if d == nil {
		return
	}
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return
	}
	state := d.states[clusterID]
	if state == nil {
		state = &clusterDestinations{}
		d.states[clusterID] = state
	}
	state.generation++
	state.expiresAt = time.Time{}
	if d.loadWorkers == nil {
		workers := state.workers
		delete(d.states, clusterID)
		d.mu.Unlock()
		stopWorkers(workers)
		return
	}
	d.startRefreshLocked(clusterID, state)
	d.mu.Unlock()
}

// Close stops all workers and waits for in-flight produce requests to finish.
func (d *Dispatcher) Close() {
	if d == nil {
		return
	}
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return
	}
	d.closed = true
	states := d.states
	d.states = map[string]*clusterDestinations{}
	d.mu.Unlock()
	for _, state := range states {
		stopWorkers(state.workers)
	}
	d.loadWG.Wait()
	d.wg.Wait()
}

func stopWorkers(workers []*destinationWorker) {
	for _, worker := range workers {
		worker.stop()
	}
}

func (d *Dispatcher) workersFor(clusterID string) []*destinationWorker {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return nil
	}
	state := d.states[clusterID]
	if state == nil {
		state = &clusterDestinations{}
		d.states[clusterID] = state
	}
	if time.Now().After(state.expiresAt) {
		d.startRefreshLocked(clusterID, state)
	}
	return state.workers
}

func (d *Dispatcher) startRefreshLocked(clusterID string, state *clusterDestinations) {
	if d.closed || d.loadWorkers == nil || state.loading {
		return
	}
	state.loading = true
	generation := state.generation
	d.loadWG.Add(1)
	go func() {
		defer d.loadWG.Done()
		ctx, cancel := context.WithTimeout(context.Background(), destinationLoadTimeout)
		defer cancel()
		workers, err := d.loadWorkers(ctx, clusterID)
		d.finishRefresh(clusterID, generation, workers, err)
	}()
}

func (d *Dispatcher) finishRefresh(clusterID string, generation uint64, workers []*destinationWorker, loadErr error) {
	d.mu.Lock()
	state := d.states[clusterID]
	if d.closed || state == nil {
		d.mu.Unlock()
		stopWorkers(workers)
		return
	}
	if state.generation != generation {
		state.loading = false
		d.startRefreshLocked(clusterID, state)
		d.mu.Unlock()
		stopWorkers(workers)
		return
	}
	state.loading = false
	if loadErr != nil {
		state.expiresAt = time.Now().Add(destinationLoadRetry)
		d.mu.Unlock()
		stopWorkers(workers)
		slog.Warn("refresh logpush destinations", "cluster_id", clusterID, "error", loadErr)
		return
	}
	stale := state.workers
	state.workers = workers
	state.expiresAt = time.Now().Add(d.cacheTTL)
	d.mu.Unlock()

	added, removed := changedDestinations(stale, workers)
	for _, dest := range added {
		initializeDestinationMetrics(dest)
	}
	stopWorkers(stale)
	for _, dest := range removed {
		forgetDestinationMetrics(dest)
	}
}

func changedDestinations(previous, current []*destinationWorker) (added, removed []Destination) {
	previousSet := make(map[string]Destination, len(previous))
	currentSet := make(map[string]Destination, len(current))
	for _, worker := range previous {
		previousSet[worker.dest.ClusterID+"\x00"+worker.dest.ID+"\x00"+worker.dest.Name] = worker.dest
	}
	for _, worker := range current {
		key := worker.dest.ClusterID + "\x00" + worker.dest.ID + "\x00" + worker.dest.Name
		currentSet[key] = worker.dest
		if _, exists := previousSet[key]; !exists {
			added = append(added, worker.dest)
		}
	}
	for key, dest := range previousSet {
		if _, exists := currentSet[key]; !exists {
			removed = append(removed, dest)
		}
	}
	return added, removed
}

func (d *Dispatcher) load(ctx context.Context, clusterID string) ([]*destinationWorker, error) {
	items, err := d.db.LogpushDestination.Query().
		Where(
			query.LogpushDestination.ClusterId.Equals(clusterID),
			query.LogpushDestination.Enabled.Equals(true),
		).
		OrderBy(query.LogpushDestination.CreatedAt.Asc()).
		Take(MaxDestinationsPerCluster + 1).
		Do(ctx)
	if err != nil {
		return nil, err
	}
	if len(items) > MaxDestinationsPerCluster {
		slog.Warn("enabled logpush destinations exceed runtime limit", "cluster_id", clusterID,
			"configured", len(items), "limit", MaxDestinationsPerCluster)
		items = items[:MaxDestinationsPerCluster]
	}
	workers := make([]*destinationWorker, 0, len(items))
	for index := range items {
		item := &items[index]
		credentialsJSON := ""
		if item.CredentialsEncrypted != nil && *item.CredentialsEncrypted != "" {
			if d.cipher == nil {
				slog.Warn("logpush destination has credentials but no cipher", "destination_id", item.Id)
				continue
			}
			credentialsJSON, err = d.cipher.DecryptScoped(CredentialScope(item.ClusterId, item.Id), *item.CredentialsEncrypted)
			if err != nil {
				slog.Warn("decrypt logpush credentials", "destination_id", item.Id, "error", err)
				continue
			}
		}
		dest, err := DestinationFromModel(item, credentialsJSON)
		if err != nil {
			slog.Warn("parse logpush destination", "destination_id", item.Id, "error", err)
			continue
		}
		prod, err := d.newProducer(dest)
		if err != nil {
			slog.Warn("build logpush producer", "destination_id", item.Id, "error", err)
			continue
		}
		worker := &destinationWorker{
			dest:     dest,
			producer: prod,
			queue:    make(chan *kgo.Record, d.queueSize),
			quit:     make(chan struct{}),
			done:     make(chan struct{}),
		}
		d.wg.Add(1)
		go func() {
			defer d.wg.Done()
			worker.run()
		}()
		workers = append(workers, worker)
	}
	return workers, nil
}

func (w *destinationWorker) enqueue(clusterID, nodeID string, records []edgeprotocol.LogRecord) {
	labels := destinationLabels(w.dest)
	for index := range records {
		record := &records[index]
		if !w.dest.Wants(record.Type) {
			continue
		}
		value, err := json.Marshal(record)
		if err != nil {
			recordsDropped.WithLabelValues(append(labels, "encode_error")...).Inc()
			continue
		}
		key := record.SiteID
		if key == "" {
			key = nodeID
		}
		message := &kgo.Record{
			Topic: w.dest.Topic,
			Key:   []byte(key),
			Value: value,
			Headers: []kgo.RecordHeader{
				{Key: "cluster_id", Value: []byte(clusterID)},
				{Key: "type", Value: []byte(record.Type)},
				{Key: "source_log_id", Value: []byte(strconv.FormatUint(record.ID, 10))},
			},
		}
		w.enqueueMu.RLock()
		if w.stopped {
			w.enqueueMu.RUnlock()
			recordsDropped.WithLabelValues(append(labels, "stopped")...).Inc()
			continue
		}
		messageBytes := bufferedRecordBytes(message)
		if !w.reserveBytes(messageBytes) {
			w.enqueueMu.RUnlock()
			recordsDropped.WithLabelValues(append(labels, "queue_bytes")...).Inc()
			continue
		}
		select {
		case w.queue <- message:
			recordsEnqueued.WithLabelValues(labels...).Inc()
			queueDepth.WithLabelValues(labels...).Inc()
		case <-w.quit:
			w.queuedBytes.Add(-messageBytes)
			recordsDropped.WithLabelValues(append(labels, "stopped")...).Inc()
		default:
			w.queuedBytes.Add(-messageBytes)
			recordsDropped.WithLabelValues(append(labels, "queue_full")...).Inc()
		}
		w.enqueueMu.RUnlock()
	}
}

func (w *destinationWorker) reserveBytes(size int64) bool {
	for {
		current := w.queuedBytes.Load()
		if current+size > maxBufferedBytesPerStage {
			return false
		}
		if w.queuedBytes.CompareAndSwap(current, current+size) {
			return true
		}
	}
}

func bufferedRecordBytes(record *kgo.Record) int64 {
	size := len(record.Key) + len(record.Value) + 64
	for _, header := range record.Headers {
		size += len(header.Key) + len(header.Value)
	}
	return int64(size)
}

func (w *destinationWorker) run() {
	defer close(w.done)
	labels := destinationLabels(w.dest)
	for {
		select {
		case message := <-w.queue:
			w.queuedBytes.Add(-bufferedRecordBytes(message))
			queueDepth.WithLabelValues(labels...).Dec()
			produceCtx, cancel := context.WithTimeout(context.Background(), produceRecordTimeout)
			w.producer.Produce(produceCtx, message, func(_ *kgo.Record, err error) {
				cancel()
				if err != nil {
					recordsDropped.WithLabelValues(append(labels, "produce_error")...).Inc()
					destinationUp.WithLabelValues(labels...).Set(0)
					return
				}
				recordsDelivered.WithLabelValues(labels...).Inc()
				destinationUp.WithLabelValues(labels...).Set(1)
			})
		case <-w.quit:
			// Abandon buffered records: best-effort delivery makes no
			// durability promise, and draining could block shutdown for as
			// long as a broker stays down.
			dropped := len(w.queue)
			if dropped > 0 {
				recordsDropped.WithLabelValues(append(labels, "shutdown")...).Add(float64(dropped))
				queueDepth.WithLabelValues(labels...).Sub(float64(dropped))
			}
			w.queuedBytes.Store(0)
			return
		}
	}
}

func (w *destinationWorker) stop() {
	w.stopOnce.Do(func() {
		w.enqueueMu.Lock()
		w.stopped = true
		close(w.quit)
		w.enqueueMu.Unlock()
	})
	<-w.done
	w.producer.Close()
}
