package edgeagent

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"
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
	limit := uint64(len(all[1].Payload) + 100)
	if uint64(len(all[0].Payload))+100 <= limit {
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
