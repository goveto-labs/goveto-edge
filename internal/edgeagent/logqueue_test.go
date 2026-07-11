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
