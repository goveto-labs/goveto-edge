package analytics

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"goveto-edge/internal/edgeprotocol"
)

type recordingObjectStore struct {
	key    string
	object ArchiveObject
}

func (s *recordingObjectStore) Put(_ context.Context, key string, object ArchiveObject) error {
	s.key, s.object = key, object
	return nil
}

func TestGzipNDJSONArchiveUsesResumableBatchKey(t *testing.T) {
	objects := &recordingObjectStore{}
	archive := NewGzipNDJSONArchive(objects, "logs")
	records := []edgeprotocol.LogRecord{
		{ID: 7, SiteID: "site-1", CreatedAt: time.Date(2026, 7, 28, 3, 0, 0, 0, time.UTC), Payload: json.RawMessage(`{"ok":true}`)},
		{ID: 9, SiteID: "site-1", Payload: json.RawMessage(`{"ok":false}`)},
	}
	if err := archive.Write(context.Background(), "cluster-1", "node-1", records); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(objects.key, "cluster-1/2026/07/28/03/node-1-00000000000000000007-00000000000000000009.ndjson.gz") {
		t.Fatalf("unexpected archive key: %s", objects.key)
	}
	reader, err := gzip.NewReader(bytes.NewReader(objects.object.Data))
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Count(decoded, []byte{'\n'}) != 2 || !bytes.Contains(decoded, []byte(`"site_id":"site-1"`)) {
		t.Fatalf("unexpected archive payload: %s", decoded)
	}
}
