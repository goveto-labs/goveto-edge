package analytics

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"goveto-edge/internal/edgeprotocol"
)

type ArchiveObject struct {
	ContentType     string
	ContentEncoding string
	Data            []byte
}

type ObjectStore interface {
	Put(context.Context, string, ArchiveObject) error
}

type LogArchive interface {
	Write(context.Context, string, string, []edgeprotocol.LogRecord) error
}

type GzipNDJSONArchive struct {
	store  ObjectStore
	prefix string
}

func NewGzipNDJSONArchive(store ObjectStore, prefix string) *GzipNDJSONArchive {
	return &GzipNDJSONArchive{store: store, prefix: prefix}
}

func (a *GzipNDJSONArchive) Write(
	ctx context.Context,
	clusterID string,
	nodeID string,
	records []edgeprotocol.LogRecord,
) error {
	if len(records) == 0 {
		return nil
	}
	var compressed bytes.Buffer
	zipper := gzip.NewWriter(&compressed)
	encoder := json.NewEncoder(zipper)
	for _, record := range records {
		if err := encoder.Encode(record); err != nil {
			_ = zipper.Close()
			return err
		}
	}
	if err := zipper.Close(); err != nil {
		return err
	}
	stamp := records[0].CreatedAt
	if stamp.IsZero() {
		stamp = time.Now().UTC()
	}
	key := filepath.ToSlash(filepath.Join(
		a.prefix, clusterID, stamp.UTC().Format("2006/01/02/15"),
		fmt.Sprintf("%s-%020d-%020d.ndjson.gz", nodeID, records[0].ID, records[len(records)-1].ID),
	))
	return a.store.Put(ctx, key, ArchiveObject{
		ContentType: "application/x-ndjson", ContentEncoding: "gzip", Data: compressed.Bytes(),
	})
}

type FileObjectStore struct{ root string }

func NewFileObjectStore(root string) *FileObjectStore { return &FileObjectStore{root: root} }

func (s *FileObjectStore) Put(ctx context.Context, key string, object ArchiveObject) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	path := filepath.Join(s.root, filepath.FromSlash(key))
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".archive-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := io.Copy(temporary, bytes.NewReader(object.Data)); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}
