package edgeagent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	"goveto-edge/internal/edgeprotocol"
	"goveto-edge/internal/geoipdb"
)

type fakeGeoIPClient struct {
	chunks   []*edgeprotocol.GeoIPChunk
	finalErr error
	calls    int
}

func (f fakeGeoIPClient) Connect(context.Context, ...grpc.CallOption) (edgeprotocol.ManagementConnectClient, error) {
	return nil, nil
}

func (f *fakeGeoIPClient) DownloadGeoIP(context.Context, *edgeprotocol.GeoIPDownloadRequest, ...grpc.CallOption) (edgeprotocol.ManagementDownloadGeoIPClient, error) {
	f.calls++
	return &fakeGeoIPStream{chunks: f.chunks, finalErr: f.finalErr}, nil
}

type fakeGeoIPStream struct {
	grpc.ClientStream
	chunks   []*edgeprotocol.GeoIPChunk
	finalErr error
	index    int
}

func (f *fakeGeoIPStream) Header() (metadata.MD, error) { return nil, nil }
func (f *fakeGeoIPStream) Trailer() metadata.MD         { return nil }
func (f *fakeGeoIPStream) CloseSend() error             { return nil }
func (f *fakeGeoIPStream) Context() context.Context     { return context.Background() }
func (f *fakeGeoIPStream) SendMsg(any) error            { return nil }
func (f *fakeGeoIPStream) RecvMsg(any) error            { return nil }
func (f *fakeGeoIPStream) Recv() (*edgeprotocol.GeoIPChunk, error) {
	if f.index == len(f.chunks) {
		if f.finalErr != nil {
			return nil, f.finalErr
		}
		return nil, io.EOF
	}
	chunk := f.chunks[f.index]
	f.index++
	return chunk, nil
}

func TestGeoIPInstallRejectsInvalidChunkSequence(t *testing.T) {
	store := NewGeoIPStore(t.TempDir(), nil)
	err := store.Install(context.Background(), &fakeGeoIPClient{chunks: []*edgeprotocol.GeoIPChunk{{Offset: 1, Data: []byte("x")}}}, "node-1", edgeprotocol.GeoIPSyncPayload{SHA256: strings.Repeat("0", 64), Size: 1})
	if err == nil || !strings.Contains(err.Error(), "chunk sequence") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestGeoIPInstallRejectsChecksumMismatch(t *testing.T) {
	store := NewGeoIPStore(t.TempDir(), nil)
	err := store.Install(context.Background(), &fakeGeoIPClient{chunks: []*edgeprotocol.GeoIPChunk{{Offset: 0, Data: []byte("x")}}}, "node-1", edgeprotocol.GeoIPSyncPayload{SHA256: strings.Repeat("0", 64), Size: 1})
	if err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestGeoIPInstallRejectsOversizedMetadata(t *testing.T) {
	store := NewGeoIPStore(t.TempDir(), nil)
	err := store.Install(context.Background(), &fakeGeoIPClient{}, "node-1", edgeprotocol.GeoIPSyncPayload{SHA256: strings.Repeat("0", 64), Size: maxGeoIPDatabaseSize + 1})
	if err == nil || !strings.Contains(err.Error(), "metadata") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestGeoIPInstallRejectsTruncatedAndInterruptedDownloads(t *testing.T) {
	store := NewGeoIPStore(t.TempDir(), nil)
	err := store.Install(context.Background(), &fakeGeoIPClient{chunks: []*edgeprotocol.GeoIPChunk{{Offset: 0, Data: []byte("x")}}}, "node-1", edgeprotocol.GeoIPSyncPayload{SHA256: strings.Repeat("0", 64), Size: 2})
	if err == nil || !strings.Contains(err.Error(), "size mismatch") {
		t.Fatalf("unexpected truncated download error: %v", err)
	}
	interrupted := errors.New("stream interrupted")
	err = store.Install(context.Background(), &fakeGeoIPClient{finalErr: interrupted}, "node-1", edgeprotocol.GeoIPSyncPayload{SHA256: strings.Repeat("0", 64), Size: 1})
	if !errors.Is(err, interrupted) {
		t.Fatalf("unexpected interrupted download error: %v", err)
	}
}

func TestGeoIPInstallAndRepeatedVersionSkip(t *testing.T) {
	fixture := filepath.Join("..", "testdata", "GeoIP2-City-Test.mmdb")
	metadata, data, err := geoipdb.Inspect(fixture)
	if err != nil {
		t.Fatal(err)
	}
	payload := edgeprotocol.GeoIPSyncPayload{SHA256: metadata.SHA256, Size: metadata.Size, BuildEpoch: metadata.BuildEpoch}
	client := &fakeGeoIPClient{chunks: chunksFor(data, 4096)}
	store := NewGeoIPStore(t.TempDir(), nil)
	reloads := 0
	store.reload = func() error { reloads++; return nil }
	if err = store.Install(context.Background(), client, "node-1", payload); err != nil {
		t.Fatal(err)
	}
	installed, err := os.ReadFile(store.path)
	if err != nil || !bytes.Equal(installed, data) {
		t.Fatalf("installed database mismatch: err=%v", err)
	}
	if status := store.Status(); status != payload {
		t.Fatalf("unexpected installed status: %#v", status)
	}
	if err = store.Install(context.Background(), client, "node-1", payload); err != nil {
		t.Fatal(err)
	}
	if client.calls != 1 || reloads != 1 {
		t.Fatalf("repeated version was not skipped: downloads=%d reloads=%d", client.calls, reloads)
	}
}

func TestGeoIPInstallReloadFailureRestoresPreviousFiles(t *testing.T) {
	fixture := filepath.Join("..", "testdata", "GeoIP2-City-Test.mmdb")
	metadata, data, err := geoipdb.Inspect(fixture)
	if err != nil {
		t.Fatal(err)
	}
	store := NewGeoIPStore(t.TempDir(), nil)
	if err = os.MkdirAll(store.dir, 0700); err != nil {
		t.Fatal(err)
	}
	oldDatabase := bytes.Repeat([]byte("o"), len(data))
	oldStatus := edgeprotocol.GeoIPStatus{SHA256: strings.Repeat("1", 64), Size: int64(len(oldDatabase)), BuildEpoch: 1}
	oldMetadata, _ := json.Marshal(oldStatus)
	if err = os.WriteFile(store.path, oldDatabase, 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(store.metaPath, oldMetadata, 0600); err != nil {
		t.Fatal(err)
	}
	reloads := 0
	store.reload = func() error {
		reloads++
		if reloads == 1 {
			return errors.New("reload failed")
		}
		return nil
	}
	payload := edgeprotocol.GeoIPSyncPayload{SHA256: metadata.SHA256, Size: metadata.Size, BuildEpoch: metadata.BuildEpoch}
	err = store.Install(context.Background(), &fakeGeoIPClient{chunks: chunksFor(data, 8192)}, "node-1", payload)
	if err == nil || !strings.Contains(err.Error(), "reload failed") {
		t.Fatalf("unexpected reload result: %v", err)
	}
	restoredDatabase, _ := os.ReadFile(store.path)
	restoredMetadata, _ := os.ReadFile(store.metaPath)
	if !bytes.Equal(restoredDatabase, oldDatabase) || !bytes.Equal(restoredMetadata, oldMetadata) {
		t.Fatal("reload failure did not restore the previous database and metadata")
	}
	if reloads != 2 {
		t.Fatalf("previous Caddy configuration was not reloaded: %d", reloads)
	}
}

func TestGeoIPStatusRejectsSameSizeCorruption(t *testing.T) {
	fixture := filepath.Join("..", "testdata", "GeoIP2-City-Test.mmdb")
	metadata, data, err := geoipdb.Inspect(fixture)
	if err != nil {
		t.Fatal(err)
	}
	store := NewGeoIPStore(t.TempDir(), nil)
	if err = os.MkdirAll(store.dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(store.path, data, 0600); err != nil {
		t.Fatal(err)
	}
	recorded, _ := json.Marshal(edgeprotocol.GeoIPStatus{SHA256: metadata.SHA256, Size: metadata.Size, BuildEpoch: metadata.BuildEpoch})
	if err = os.WriteFile(store.metaPath, recorded, 0600); err != nil {
		t.Fatal(err)
	}
	if status := store.Status(); status.SHA256 != metadata.SHA256 {
		t.Fatalf("valid status rejected: %#v", status)
	}
	data[0] ^= 0xff
	if err = os.WriteFile(store.path, data, 0600); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(2 * time.Second)
	if err = os.Chtimes(store.path, future, future); err != nil {
		t.Fatal(err)
	}
	if status := store.Status(); status.SHA256 != "" {
		t.Fatalf("same-size corruption was accepted: %#v", status)
	}
}

func chunksFor(data []byte, size int) []*edgeprotocol.GeoIPChunk {
	chunks := make([]*edgeprotocol.GeoIPChunk, 0, (len(data)+size-1)/size)
	for offset := 0; offset < len(data); offset += size {
		end := min(offset+size, len(data))
		chunks = append(chunks, &edgeprotocol.GeoIPChunk{Offset: int64(offset), Data: append([]byte(nil), data[offset:end]...)})
	}
	return chunks
}
