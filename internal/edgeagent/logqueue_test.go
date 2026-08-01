package edgeagent

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
)

func TestLogQueuePersistsUntilAck(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logs.db")
	queue, err := OpenLogQueue(path)
	if err != nil {
		t.Fatal(err)
	}
	id, err := queue.Append(LogRecord{Type: "access", Payload: json.RawMessage(`{"status":200}`)})
	if err != nil {
		t.Fatal(err)
	}
	if err := queue.Close(); err != nil {
		t.Fatal(err)
	}
	queue, err = OpenLogQueue(path)
	if err != nil {
		t.Fatal(err)
	}
	defer queue.Close()
	batch, err := queue.Batch(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(batch) != 1 || batch[0].ID != id {
		t.Fatalf("unexpected persisted batch: %#v", batch)
	}
	if err := queue.Ack(id); err != nil {
		t.Fatal(err)
	}
	batch, err = queue.Batch(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(batch) != 0 {
		t.Fatal("acknowledged record was not deleted")
	}
}

func TestLogQueueRebuildsUnsupportedRecordFormat(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logs.db")
	db, err := bolt.Open(path, 0o600, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Update(func(tx *bolt.Tx) error {
		logs, createErr := tx.CreateBucket(logsBucket)
		if createErr != nil {
			return createErr
		}
		if createErr = logs.Put([]byte("legacy"), []byte(`{"id":1}`)); createErr != nil {
			return createErr
		}
		meta, createErr := tx.CreateBucket(metaBucket)
		if createErr != nil {
			return createErr
		}
		return meta.Put(queueVersionKey, []byte{0})
	}); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	queue, err := OpenLogQueue(path)
	if err != nil {
		t.Fatal(err)
	}
	defer queue.Close()
	stats, err := queue.Stats()
	if err != nil {
		t.Fatal(err)
	}
	if stats.Records != 0 || stats.Bytes != 0 {
		t.Fatalf("unsupported queue state was not rebuilt: %#v", stats)
	}
}

func TestLogQueueRejectsEmptyPayload(t *testing.T) {
	queue, err := OpenLogQueue(filepath.Join(t.TempDir(), "logs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer queue.Close()
	if _, err := queue.Append(LogRecord{Type: "access"}); err == nil {
		t.Fatal("expected empty payload rejection")
	}
}

func TestLogQueueEvictsOldestWhenOverMaxBytes(t *testing.T) {
	queue, err := OpenLogQueue(filepath.Join(t.TempDir(), "logs.db"), 180)
	if err != nil {
		t.Fatal(err)
	}
	defer queue.Close()
	first, err := queue.Append(LogRecord{Type: "a", Payload: json.RawMessage(`{"n":1,"pad":"xxxxxxxxxxxxxxxxxxxx"}`)})
	if err != nil {
		t.Fatal(err)
	}
	second, err := queue.Append(LogRecord{Type: "b", Payload: json.RawMessage(`{"n":2,"pad":"xxxxxxxxxxxxxxxxxxxx"}`)})
	if err != nil {
		t.Fatal(err)
	}
	third, err := queue.Append(LogRecord{Type: "c", Payload: json.RawMessage(`{"n":3,"pad":"xxxxxxxxxxxxxxxxxxxx"}`)})
	if err != nil {
		t.Fatal(err)
	}
	batch, err := queue.Batch(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(batch) == 0 {
		t.Fatal("expected retained records")
	}
	for _, record := range batch {
		if record.ID == first {
			t.Fatalf("oldest record %d should have been evicted; batch=%#v", first, batch)
		}
	}
	ids := map[uint64]bool{}
	for _, record := range batch {
		ids[record.ID] = true
	}
	if !ids[second] && !ids[third] {
		t.Fatalf("expected newer records retained: second=%d third=%d batch=%#v", second, third, batch)
	}
}

func TestLogQueueBatchRespectsLimit(t *testing.T) {
	queue, err := OpenLogQueue(filepath.Join(t.TempDir(), "logs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer queue.Close()
	for i := 0; i < 5; i++ {
		payload, _ := json.Marshal(map[string]int{"i": i})
		if _, err := queue.Append(LogRecord{Type: "access", Payload: payload}); err != nil {
			t.Fatal(err)
		}
	}
	batch, err := queue.Batch(2)
	if err != nil {
		t.Fatal(err)
	}
	if len(batch) != 2 {
		t.Fatalf("expected 2 records, got %d", len(batch))
	}
}

func TestLogQueueNotifiesWaiters(t *testing.T) {
	queue, err := OpenLogQueue(filepath.Join(t.TempDir(), "logs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer queue.Close()
	select {
	case <-queue.Wait():
		t.Fatal("notify channel should start empty")
	default:
	}
	if _, err := queue.Append(LogRecord{Type: "access", Payload: json.RawMessage(`{"ok":true}`)}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-queue.Wait():
	case <-time.After(time.Second):
		t.Fatal("expected notify after append")
	}
}

func TestLogQueueAckPartial(t *testing.T) {
	queue, err := OpenLogQueue(filepath.Join(t.TempDir(), "logs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer queue.Close()
	id1, err := queue.Append(LogRecord{Type: "a", Payload: json.RawMessage(`{"n":1}`)})
	if err != nil {
		t.Fatal(err)
	}
	id2, err := queue.Append(LogRecord{Type: "b", Payload: json.RawMessage(`{"n":2}`)})
	if err != nil {
		t.Fatal(err)
	}
	if err := queue.Ack(id1); err != nil {
		t.Fatal(err)
	}
	batch, err := queue.Batch(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(batch) != 1 || batch[0].ID != id2 {
		t.Fatalf("unexpected batch after partial ack: %#v", batch)
	}
}

func TestLogQueueSizedBatchAndStats(t *testing.T) {
	queue, err := OpenLogQueue(filepath.Join(t.TempDir(), "logs.db"), 4096)
	if err != nil {
		t.Fatal(err)
	}
	defer queue.Close()
	for i := range 3 {
		payload, _ := json.Marshal(map[string]any{"index": i, "padding": "xxxxxxxxxxxxxxxxxxxx"})
		if _, err := queue.Append(LogRecord{Type: "access", Payload: payload}); err != nil {
			t.Fatal(err)
		}
	}
	batch, bytes, err := queue.BatchSized(3, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(batch) != 0 || bytes != 0 {
		t.Fatalf("oversized first record must not produce an invalid batch: records=%d bytes=%d", len(batch), bytes)
	}
	stats, err := queue.Stats()
	if err != nil {
		t.Fatal(err)
	}
	if stats.Records != 3 || stats.Bytes == 0 || stats.OldestID == 0 || stats.NewestID < stats.OldestID {
		t.Fatalf("unexpected queue stats: %#v", stats)
	}
}

func TestLogQueueDropsOversizedHeadWithoutBlockingLaterRecords(t *testing.T) {
	queue, err := OpenLogQueue(filepath.Join(t.TempDir(), "logs.db"), 4096)
	if err != nil {
		t.Fatal(err)
	}
	defer queue.Close()
	if _, err := queue.Append(LogRecord{Type: "access", Payload: json.RawMessage(`{"padding":"xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"}`)}); err != nil {
		t.Fatal(err)
	}
	if _, err := queue.Append(LogRecord{Type: "access", Payload: json.RawMessage(`{"ok":true}`)}); err != nil {
		t.Fatal(err)
	}
	all, err := queue.Batch(2)
	if err != nil {
		t.Fatal(err)
	}
	largeEncoded, err := encodeLogEnvelope(all[0], time.Now())
	if err != nil {
		t.Fatal(err)
	}
	smallEncoded, err := encodeLogEnvelope(all[1], time.Now())
	if err != nil {
		t.Fatal(err)
	}
	limit := uint64(len(smallEncoded) + 4)
	if uint64(len(largeEncoded)+4) <= limit {
		t.Fatal("test records do not exercise an oversized head")
	}
	dropped, err := queue.DropOversizedHead(limit)
	if err != nil {
		t.Fatal(err)
	}
	if dropped != 1 {
		t.Fatalf("unexpected oversized drop count: %d", dropped)
	}
	batch, _, err := queue.BatchSized(2, limit)
	if err != nil {
		t.Fatal(err)
	}
	if len(batch) != 1 || string(batch[0].Payload) != `{"ok":true}` {
		t.Fatalf("later record remained blocked: %#v", batch)
	}
	stats, err := queue.Stats()
	if err != nil {
		t.Fatal(err)
	}
	if stats.DroppedRecords != 1 || stats.Records != 1 {
		t.Fatalf("unexpected queue stats after oversized drop: %#v", stats)
	}
}

func TestLogQueueCountsCapacityDrops(t *testing.T) {
	queue, err := OpenLogQueue(filepath.Join(t.TempDir(), "logs.db"), 180)
	if err != nil {
		t.Fatal(err)
	}
	defer queue.Close()
	for range 4 {
		_, _ = queue.Append(LogRecord{Type: "access", Payload: json.RawMessage(`{"padding":"xxxxxxxxxxxxxxxxxxxx"}`)})
	}
	stats, err := queue.Stats()
	if err != nil {
		t.Fatal(err)
	}
	if stats.DroppedRecords == 0 {
		t.Fatalf("expected capacity eviction count: %#v", stats)
	}
}

func TestLogQueueAppendBatchUsesContinuousIDsAndOneNotification(t *testing.T) {
	queue, err := OpenLogQueue(filepath.Join(t.TempDir(), "logs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer queue.Close()
	records := []LogRecord{
		{Type: "a", Payload: json.RawMessage(`{"n":1}`)},
		{Type: "b", Payload: json.RawMessage(`{"n":2}`)},
		{Type: "c", Payload: json.RawMessage(`{"n":3}`)},
	}
	ids, err := queue.AppendBatch(records)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 3 || ids[1] != ids[0]+1 || ids[2] != ids[1]+1 {
		t.Fatalf("non-contiguous IDs: %v", ids)
	}
	select {
	case <-queue.Wait():
	case <-time.After(time.Second):
		t.Fatal("batch did not notify waiters")
	}
	select {
	case <-queue.Wait():
		t.Fatal("batch emitted more than one notification")
	default:
	}
}

func TestLogQueueAppendBatchIsAtomic(t *testing.T) {
	queue, err := OpenLogQueue(filepath.Join(t.TempDir(), "logs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer queue.Close()
	_, err = queue.AppendBatch([]LogRecord{
		{Type: "valid", Payload: json.RawMessage(`{"ok":true}`)},
		{Type: "invalid", Payload: json.RawMessage(`{`)},
	})
	if err == nil {
		t.Fatal("invalid record did not fail the batch")
	}
	batch, err := queue.Batch(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(batch) != 0 {
		t.Fatalf("failed batch was partially committed: %#v", batch)
	}
	id, err := queue.Append(LogRecord{Type: "next", Payload: json.RawMessage(`{"ok":true}`)})
	if err != nil {
		t.Fatal(err)
	}
	if id != 1 {
		t.Fatalf("failed transaction consumed sequence IDs: got %d", id)
	}
}

func TestAccessPipelineDropsNewestAtRecordLimitAndFlushes(t *testing.T) {
	queue, err := OpenLogQueue(filepath.Join(t.TempDir(), "logs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer queue.Close()
	if err := queue.StartAccessPipeline(LogPolicy{SampleRate: 1}, AccessLogConfig{
		BufferBytes: 1024, BufferRecords: 2, BatchBytes: 1024, BatchRecords: 10, FlushInterval: time.Hour,
	}); err != nil {
		t.Fatal(err)
	}
	for index, payload := range []json.RawMessage{json.RawMessage(`{"n":1}`), json.RawMessage(`{"n":2}`), json.RawMessage(`{"n":3}`)} {
		enqueued := queue.EnqueueAccess(LogRecord{Type: "access", Payload: payload})
		if enqueued != (index < 2) {
			t.Fatalf("enqueue %d=%t", index, enqueued)
		}
	}
	stats, err := queue.Stats()
	if err != nil {
		t.Fatal(err)
	}
	if stats.MemoryBufferRecords != 2 || stats.MemoryDroppedRecords != 1 || stats.DroppedRecords != 1 {
		t.Fatalf("unexpected buffered stats: %#v", stats)
	}
	shutdownContext, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := queue.ShutdownAccess(shutdownContext); err != nil {
		t.Fatal(err)
	}
	batch, err := queue.Batch(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(batch) != 2 || string(batch[0].Payload) != `{"n":1}` || string(batch[1].Payload) != `{"n":2}` {
		t.Fatalf("newest record was not dropped: %#v", batch)
	}
}

func TestAccessPipelineEnforcesByteLimit(t *testing.T) {
	queue, err := OpenLogQueue(filepath.Join(t.TempDir(), "logs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer queue.Close()
	payload := json.RawMessage(`{"padding":"xxxxxxxx"}`)
	if err := queue.StartAccessPipeline(LogPolicy{SampleRate: 1}, AccessLogConfig{
		BufferBytes: uint64(len(payload)*2 - 1), BufferRecords: 10, BatchBytes: 1024, BatchRecords: 10, FlushInterval: time.Hour,
	}); err != nil {
		t.Fatal(err)
	}
	if !queue.EnqueueAccess(LogRecord{Type: "access", Payload: payload}) {
		t.Fatal("first record was rejected")
	}
	if queue.EnqueueAccess(LogRecord{Type: "access", Payload: payload}) {
		t.Fatal("record exceeding the byte limit was accepted")
	}
}

func TestAccessPipelineRetriesFailedBatch(t *testing.T) {
	queue, err := OpenLogQueue(filepath.Join(t.TempDir(), "logs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer queue.Close()
	if err := queue.StartAccessPipeline(LogPolicy{SampleRate: 1}, AccessLogConfig{
		BufferBytes: 1024, BufferRecords: 10, BatchBytes: 1024, BatchRecords: 2, FlushInterval: time.Hour,
	}); err != nil {
		t.Fatal(err)
	}
	var attempts atomic.Int32
	queue.accessMu.Lock()
	queue.accessPersistOverride = func(records []LogRecord) ([]uint64, error) {
		if attempts.Add(1) < 3 {
			return nil, errors.New("temporary write failure")
		}
		return queue.AppendBatch(records)
	}
	queue.accessMu.Unlock()
	queue.EnqueueAccess(LogRecord{Type: "access", Payload: json.RawMessage(`{"n":1}`)})
	queue.EnqueueAccess(LogRecord{Type: "access", Payload: json.RawMessage(`{"n":2}`)})
	shutdownContext, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := queue.ShutdownAccess(shutdownContext); err != nil {
		t.Fatal(err)
	}
	stats, err := queue.Stats()
	if err != nil {
		t.Fatal(err)
	}
	if attempts.Load() != 3 || stats.CommittedBatches != 1 || stats.CommittedRecords != 2 || stats.LastPersistError != "temporary write failure" {
		t.Fatalf("unexpected retry stats: attempts=%d stats=%#v", attempts.Load(), stats)
	}
}

func TestAccessPipelineShutdownTimeoutReportsRemainingRecords(t *testing.T) {
	queue, err := OpenLogQueue(filepath.Join(t.TempDir(), "logs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer queue.Close()
	if err := queue.StartAccessPipeline(LogPolicy{SampleRate: 1}, AccessLogConfig{
		BufferBytes: 1024, BufferRecords: 10, BatchBytes: 1024, BatchRecords: 1, FlushInterval: time.Hour,
	}); err != nil {
		t.Fatal(err)
	}
	queue.accessMu.Lock()
	queue.accessPersistOverride = func([]LogRecord) ([]uint64, error) { return nil, errors.New("disk unavailable") }
	queue.accessMu.Unlock()
	queue.EnqueueAccess(LogRecord{Type: "access", Payload: json.RawMessage(`{"ok":true}`)})
	shutdownContext, cancel := context.WithTimeout(t.Context(), 25*time.Millisecond)
	defer cancel()
	err = queue.ShutdownAccess(shutdownContext)
	if err == nil || !strings.Contains(err.Error(), "remaining records=1") {
		t.Fatalf("unexpected shutdown error: %v", err)
	}
	stats, statsErr := queue.Stats()
	if statsErr != nil {
		t.Fatal(statsErr)
	}
	if stats.MemoryBufferRecords != 1 || stats.LastPersistError != "disk unavailable" {
		t.Fatalf("unexpected timeout stats: %#v", stats)
	}
}

func TestAccessPipelineRedactsBeforePersistence(t *testing.T) {
	queue, err := OpenLogQueue(filepath.Join(t.TempDir(), "logs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer queue.Close()
	policy := LogPolicy{SampleRate: 1, RedactQuery: true, AnonymizeIP: true, RedactedHeaders: map[string]struct{}{"authorization": {}}}
	if err := queue.StartAccessPipeline(policy, AccessLogConfig{
		BufferBytes: 4096, BufferRecords: 10, BatchBytes: 4096, BatchRecords: 10, FlushInterval: time.Hour,
	}); err != nil {
		t.Fatal(err)
	}
	raw := json.RawMessage(`{"request":{"uri":"/account?token=secret","client_ip":"192.0.2.129","headers":{"Authorization":["Bearer secret"]}},"status":200}`)
	if !queue.EnqueueAccess(LogRecord{Type: "access", Payload: raw}) {
		t.Fatal("access record was not enqueued")
	}
	before, err := queue.Batch(10)
	if err != nil || len(before) != 0 {
		t.Fatalf("raw access record reached durable queue before worker flush: %#v, %v", before, err)
	}
	shutdownContext, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := queue.ShutdownAccess(shutdownContext); err != nil {
		t.Fatal(err)
	}
	after, err := queue.Batch(10)
	if err != nil || len(after) != 1 {
		t.Fatalf("unexpected persisted records: %#v, %v", after, err)
	}
	for _, secret := range []string{"token=secret", "192.0.2.129", "Bearer secret"} {
		if strings.Contains(string(after[0].Payload), secret) {
			t.Fatalf("persisted access record contains %q: %s", secret, after[0].Payload)
		}
	}
}

func TestAccessPipelineDoesNotReparseEncoderSanitizedRecord(t *testing.T) {
	queue, err := OpenLogQueue(filepath.Join(t.TempDir(), "logs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer queue.Close()
	if err = queue.StartAccessPipeline(LogPolicy{SampleRate: 1, RedactQuery: true}, AccessLogConfig{
		BufferBytes: 4096, BufferRecords: 10, BatchBytes: 4096, BatchRecords: 10, FlushInterval: time.Hour,
	}); err != nil {
		t.Fatal(err)
	}
	payload := json.RawMessage(`{"request":{"uri":"/already-encoded?marker=kept"},"status":200}`)
	if !queue.EnqueueSanitizedAccess(LogRecord{Type: "access", Payload: payload}) {
		t.Fatal("sanitized access record was not enqueued")
	}
	shutdownContext, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err = queue.ShutdownAccess(shutdownContext); err != nil {
		t.Fatal(err)
	}
	records, err := queue.Batch(10)
	if err != nil || len(records) != 1 {
		t.Fatalf("persisted records=%#v error=%v", records, err)
	}
	if string(records[0].Payload) != string(payload) {
		t.Fatalf("sanitized payload was parsed again: %s", records[0].Payload)
	}
}

func TestAccessPipelineConcurrentEnqueue(t *testing.T) {
	queue, err := OpenLogQueue(filepath.Join(t.TempDir(), "logs.db"), 1<<30)
	if err != nil {
		t.Fatal(err)
	}
	defer queue.Close()
	if err := queue.StartAccessPipeline(LogPolicy{SampleRate: 1}, AccessLogConfig{
		BufferBytes: 1 << 20, BufferRecords: 2000, BatchBytes: 4096, BatchRecords: 32, FlushInterval: time.Millisecond,
	}); err != nil {
		t.Fatal(err)
	}
	var workers sync.WaitGroup
	var rejected atomic.Uint64
	for worker := range 8 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for index := range 100 {
				payload, _ := json.Marshal(map[string]int{"worker": worker, "index": index})
				if !queue.EnqueueAccess(LogRecord{Type: "access", Payload: payload}) {
					rejected.Add(1)
				}
			}
		}()
	}
	workers.Wait()
	shutdownContext, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := queue.ShutdownAccess(shutdownContext); err != nil {
		t.Fatal(err)
	}
	stats, err := queue.Stats()
	if err != nil {
		t.Fatal(err)
	}
	if rejected.Load() != 0 || stats.Records != 800 || stats.MemoryBufferRecords != 0 || stats.AverageBatchSize < 16 {
		t.Fatalf("concurrent enqueue lost records: rejected=%d stats=%#v", rejected.Load(), stats)
	}
}

func TestAgentLogSinkUsesSiteMetadataForAccessClassification(t *testing.T) {
	queue, err := OpenLogQueue(filepath.Join(t.TempDir(), "logs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer queue.Close()
	if err := queue.StartAccessPipeline(LogPolicy{SampleRate: 1}, AccessLogConfig{
		BufferBytes: 1024, BufferRecords: 10, BatchBytes: 1024, BatchRecords: 10, FlushInterval: time.Hour,
	}); err != nil {
		t.Fatal(err)
	}
	sink := agentLogSink{queue: queue}
	if err := sink.WriteCaddyLog("site-a", 42, time.Now().UTC(), json.RawMessage(`{"message":"not decoded on request path"}`)); err != nil {
		t.Fatal(err)
	}
	if err := sink.WriteCaddyLog("", 0, time.Now().UTC(), json.RawMessage(`{"request":{},"status":200}`)); err == nil {
		t.Fatal("missing site ID was accepted")
	}
	before, err := queue.Batch(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != 0 {
		t.Fatalf("access log was persisted before the async flush: %#v", before)
	}
	shutdownContext, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := queue.ShutdownAccess(shutdownContext); err != nil {
		t.Fatal(err)
	}
	after, err := queue.Batch(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 1 || after[0].Type != "access" || after[0].SiteID != "site-a" || after[0].ConfigVersion != 42 {
		t.Fatalf("site log was not asynchronously classified as access: %#v", after)
	}
}
