package edgeagent

import (
	"encoding/json"
	"path/filepath"
	"testing"
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
