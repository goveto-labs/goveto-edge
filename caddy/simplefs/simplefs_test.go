package simplefs

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
	"go.uber.org/zap"
	core "goveto-edge/internal/cachecore"
	"goveto-edge/internal/cacherange"
)

func newTestProvider(t *testing.T, directory string, stale time.Duration) *provider {
	t.Helper()
	t.Cleanup(OverrideDiskUsageForTesting(directory, 1<<40, 0))
	provider, err := newProvider(core.CacheProvider{
		Path: directory,
		Configuration: map[string]any{
			"auto_max_size":  false,
			"max_size_bytes": 1 << 20,
		},
	}, zap.NewNop().Sugar(), stale)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { provider.closeIndex() })
	return provider
}

func cachedResponse(body string) []byte {
	return []byte("HTTP/1.1 200 OK\r\nCache-Control: public, max-age=60\r\nContent-Length: " +
		strconv.Itoa(len(body)) + "\r\n\r\n" + body)
}

func TestResponseValidationPreservesStorageEncoding(t *testing.T) {
	want := cachedResponse("body")
	target := filepath.Join(t.TempDir(), "response")
	path, _, _, err := writeCompressedTemporary(target, want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := readCachedResponseFile(path)
	if err != nil {
		t.Fatalf("valid compressed response rejected: %v", err)
	}
	onDisk, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, onDisk) {
		t.Fatal("readCachedResponseFile() altered the storage representation")
	}
}

func TestObjectFormatUsesRawAndLZ4Chunks(t *testing.T) {
	random := make([]byte, objectChunkSize)
	var state uint32 = 1
	for index := range random {
		state = state*1664525 + 1013904223
		random[index] = byte(state >> 24)
	}
	value := append(random, bytes.Repeat([]byte("x"), objectChunkSize)...)
	path, size, _, err := writeCompressedTemporary(filepath.Join(t.TempDir(), "mixed"), value)
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	reader, err := newObjectReader(file, size)
	if err != nil {
		t.Fatal(err)
	}
	if len(reader.chunks) != 2 || reader.chunks[0].codec != codecRaw || reader.chunks[1].codec != codecLZ4 {
		t.Fatalf("unexpected codecs: %#v", reader.chunks)
	}
	decoded, err := io.ReadAll(reader)
	if err != nil || !bytes.Equal(decoded, value) {
		t.Fatalf("mixed object decode bytes=%d err=%v", len(decoded), err)
	}
}

func TestObjectFormatRejectsTouchedChunkCRC(t *testing.T) {
	value := bytes.Repeat([]byte("raw-data-"), 1024)
	path, size, _, err := writeCompressedTemporary(filepath.Join(t.TempDir(), "crc"), value)
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := newObjectReader(file, size)
	if err != nil {
		t.Fatal(err)
	}
	chunk := reader.chunks[0]
	byteValue := []byte{0}
	if _, err = file.ReadAt(byteValue, int64(chunk.offset)); err != nil {
		t.Fatal(err)
	}
	byteValue[0] ^= 0xff
	if _, err = file.WriteAt(byteValue, int64(chunk.offset)); err != nil {
		t.Fatal(err)
	}
	reader, err = newObjectReader(file, size)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = io.ReadAll(reader); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("corrupt chunk error=%v", err)
	}
	_ = file.Close()
}

func FuzzObjectTableDecoder(f *testing.F) {
	f.Add([]byte(objectMagic + "\x00\x01"))
	f.Add(make([]byte, objectHeaderSize))
	f.Fuzz(func(t *testing.T, value []byte) {
		_, _ = newObjectReader(bytes.NewReader(value), uint64(len(value)))
	})
}

func TestLegacyCacheDirectoryIsDiscarded(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "site")
	if err := os.Mkdir(directory, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, indexName), []byte("legacy"), 0o640); err != nil {
		t.Fatal(err)
	}
	provider := newTestProvider(t, directory, 0)
	if provider.bodyEntries.Load() != 0 {
		t.Fatal("legacy entries were loaded")
	}
	marker, err := os.ReadFile(filepath.Join(directory, formatMarkerName))
	if err != nil || string(marker) != formatMarkerValue {
		t.Fatalf("format marker=%q err=%v", marker, err)
	}
	entries, err := filepath.Glob(filepath.Join(filepath.Dir(directory), ".goveto-cache-discarded-*"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("discarded directories=%v err=%v", entries, err)
	}
}

func TestStorageSharesProviderUntilLastRelease(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "shared")
	t.Cleanup(OverrideDiskUsageForTesting(directory, 1<<40, 0))
	config := Config{Path: directory, MaxSizeBytes: 1 << 20, MaxDiskUsagePercent: 90}
	first, err := Acquire(config, zap.NewNop().Sugar())
	if err != nil {
		t.Fatal(err)
	}
	second, err := Acquire(config, zap.NewNop().Sugar())
	if err != nil {
		t.Fatal(err)
	}
	if first.provider != second.provider || first.provider.refs != 2 {
		t.Fatal("same path did not share one provider")
	}
	if err = first.Close(); err != nil {
		t.Fatal(err)
	}
	if _, ok := providers.Load(first.provider.path); !ok || second.provider.index == nil {
		t.Fatal("first release closed a shared provider")
	}
	if err = second.Close(); err != nil {
		t.Fatal(err)
	}
	if _, ok := providers.Load(first.provider.path); ok || second.provider.index != nil {
		t.Fatal("last release did not close the provider")
	}
}

func TestStorageInitializationFailureDoesNotRegisterProvider(t *testing.T) {
	parent := t.TempDir()
	path := filepath.Join(parent, "not-a-directory")
	if err := os.WriteFile(path, []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Acquire(Config{Path: path}, zap.NewNop().Sugar()); err == nil {
		t.Fatal("storage initialized on a regular file")
	}
	if _, ok := providers.Load(path); ok {
		t.Fatal("failed storage was registered")
	}
}

func TestWithinAutoLimitUsesWholeFilesystemTarget(t *testing.T) {
	tests := []struct {
		name     string
		total    uint64
		used     uint64
		incoming uint64
		percent  int
		want     bool
	}{
		{name: "at target", total: 100, used: 79, incoming: 1, percent: 80, want: true},
		{name: "write crosses target", total: 100, used: 79, incoming: 2, percent: 80},
		{name: "already over target", total: 100, used: 81, percent: 80},
		{name: "invalid percent defaults to 80", total: 100, used: 80, percent: 0, want: true},
		{name: "percent above hard limit clamps to 90", total: 100, used: 89, incoming: 2, percent: 95},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := withinAutoLimit(test.total, test.used, test.incoming, test.percent); got != test.want {
				t.Fatalf("withinAutoLimit() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestFixedLimitStillRejectsWritesAboveHardDiskThreshold(t *testing.T) {
	directory := t.TempDir()
	t.Cleanup(OverrideDiskUsageForTesting(directory, 100, 91))
	provider, err := newProvider(core.CacheProvider{
		Path: directory,
		Configuration: map[string]any{
			"auto_max_size":          false,
			"max_size_bytes":         1 << 20,
			"max_disk_usage_percent": 95,
		},
	}, zap.NewNop().Sugar(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if provider.limits.maxDiskUsagePercent != 90 {
		t.Fatalf("disk threshold=%d, want hard maximum 90", provider.limits.maxDiskUsagePercent)
	}
	if err := provider.ensureSpace(1, provider.limits); !errors.Is(err, ErrCapacity) {
		t.Fatalf("fixed cache write above disk threshold error=%v, want ErrCapacity", err)
	}
}

func TestMultiLevelKeepsResponseBodyForStaleWindow(t *testing.T) {
	provider := newTestProvider(t, t.TempDir(), time.Second)
	response := []byte("HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nok")
	if err := provider.SetMultiLevel("key", "key", response, nil, "", 10*time.Millisecond, "key"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	fresh, stale := provider.GetMultiLevel("key", &http.Request{Header: http.Header{}}, &core.Revalidator{})
	if fresh != nil {
		t.Fatal("expired response should not remain fresh")
	}
	if stale == nil {
		t.Fatal("response body was removed before the stale window elapsed")
	}
	_ = stale.Body.Close()
}

func TestCachedRangeResponseReadsOnlyRequestedBytes(t *testing.T) {
	provider := newTestProvider(t, t.TempDir(), 0)
	body := "0123456789abcdefghijklmnopqrstuvwxyz"
	if err := provider.SetMultiLevel("key", "key", cachedResponse(body), nil, "", time.Minute, "key"); err != nil {
		t.Fatal(err)
	}
	request := &http.Request{Header: http.Header{}}
	request = request.WithContext(cacherange.WithContext(request.Context(), cacherange.Spec{Start: 10, End: 19}))
	fresh, _ := provider.GetMultiLevel("key", request, &core.Revalidator{})
	if fresh == nil {
		t.Fatal("range cache lookup missed")
	}
	got, err := io.ReadAll(fresh.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != body[10:20] {
		t.Fatalf("range body = %q, want %q", got, body[10:20])
	}
	if fresh.StatusCode != http.StatusPartialContent || fresh.ContentLength != 10 || fresh.Header.Get("Content-Range") != "bytes 10-19/36" || fresh.Header.Get("Accept-Ranges") != "bytes" {
		t.Fatalf("range response = status %d length %d headers %#v", fresh.StatusCode, fresh.ContentLength, fresh.Header)
	}
}

func TestCachedRangeClampsEndAndRejectsUnsatisfiableStart(t *testing.T) {
	provider := newTestProvider(t, t.TempDir(), 0)
	body := "0123456789"
	if err := provider.SetMultiLevel("key", "key", cachedResponse(body), nil, "", time.Minute, "key"); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name       string
		spec       cacherange.Spec
		wantStatus int
		wantBody   string
		wantRange  string
		wantLength int64
	}{
		{name: "end beyond object", spec: cacherange.Spec{Start: 7, End: 100}, wantStatus: http.StatusPartialContent, wantBody: "789", wantRange: "bytes 7-9/10", wantLength: 3},
		{name: "start beyond object", spec: cacherange.Spec{Start: 10, End: 20}, wantStatus: http.StatusRequestedRangeNotSatisfiable, wantRange: "bytes */10"},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := (&http.Request{Header: http.Header{}}).WithContext(cacherange.WithContext(t.Context(), test.spec))
			fresh, _ := provider.GetMultiLevel("key", request, &core.Revalidator{})
			if fresh == nil {
				t.Fatal("range cache lookup missed")
			}
			got, err := io.ReadAll(fresh.Body)
			if err != nil {
				t.Fatal(err)
			}
			if fresh.StatusCode != test.wantStatus || string(got) != test.wantBody || fresh.Header.Get("Content-Range") != test.wantRange || fresh.ContentLength != test.wantLength {
				t.Fatalf("response = status %d body %q range %q length %d", fresh.StatusCode, got, fresh.Header.Get("Content-Range"), fresh.ContentLength)
			}
		})
	}
}

func TestCachedRangeEarlyCloseReleasesDecoder(t *testing.T) {
	provider := newTestProvider(t, t.TempDir(), 0)
	if err := provider.SetMultiLevel("key", "key", cachedResponse(strings.Repeat("x", 128)), nil, "", time.Minute, "key"); err != nil {
		t.Fatal(err)
	}
	request := (&http.Request{Header: http.Header{}}).WithContext(cacherange.WithContext(t.Context(), cacherange.Spec{Start: 32, End: 63}))
	fresh, _ := provider.GetMultiLevel("key", request, &core.Revalidator{})
	ranged, ok := fresh.Body.(*rangedResponseBody)
	if !ok {
		t.Fatalf("range body type = %T", fresh.Body)
	}
	pooled, ok := ranged.body.(*pooledResponseBody)
	if !ok {
		t.Fatalf("underlying body type = %T", ranged.body)
	}
	buffer := make([]byte, 1)
	if _, err := fresh.Body.Read(buffer); err != nil {
		t.Fatal(err)
	}
	if err := fresh.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if pooled.decoder != nil || !ranged.closed {
		t.Fatal("early close did not release the pooled decoder")
	}
}

func TestCachedPartialResponseUsesAbsoluteRangeOffsets(t *testing.T) {
	provider := newTestProvider(t, t.TempDir(), 0)
	response := []byte("HTTP/1.1 206 Partial Content\r\nContent-Length: 10\r\nContent-Range: bytes 100-109/1000\r\n\r\nabcdefghij")
	if err := provider.SetMultiLevel("key", "key", response, nil, "", time.Minute, "key"); err != nil {
		t.Fatal(err)
	}
	request := (&http.Request{Header: http.Header{}}).WithContext(cacherange.WithContext(t.Context(), cacherange.Spec{Start: 103, End: 106}))
	fresh, _ := provider.GetMultiLevel("key", request, &core.Revalidator{})
	got, err := io.ReadAll(fresh.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "defg" || fresh.Header.Get("Content-Range") != "bytes 103-106/1000" {
		t.Fatalf("partial cached range = %q, %q", got, fresh.Header.Get("Content-Range"))
	}
}

func TestNonRangeCacheHitPreservesFullResponseAndDecodesMappingOnce(t *testing.T) {
	provider := newTestProvider(t, t.TempDir(), 0)
	if err := provider.SetMultiLevel("key", "key", cachedResponse("complete"), nil, "", time.Minute, "key"); err != nil {
		t.Fatal(err)
	}
	original := decodeMapping
	count := 0
	decodeMapping = func(value []byte) (*core.StorageMapper, error) {
		count++
		return original(value)
	}
	t.Cleanup(func() { decodeMapping = original })
	fresh, _ := provider.GetMultiLevel("key", &http.Request{Header: http.Header{}}, &core.Revalidator{})
	got, err := io.ReadAll(fresh.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "complete" || fresh.StatusCode != http.StatusOK || count != 1 {
		t.Fatalf("full hit = status %d body %q mapping decodes %d", fresh.StatusCode, got, count)
	}
}

func TestStartupRemovesOrphanWithoutScanningOnWrites(t *testing.T) {
	directory := t.TempDir()
	old := filepath.Join(directory, bodyPrefix+"orphan")
	if err := os.WriteFile(old, []byte("12345678"), 0o600); err != nil {
		t.Fatal(err)
	}
	provider := newTestProvider(t, directory, 0)
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatalf("orphan cache entry was not removed at startup: %v", err)
	}
	if err := provider.ensureSpace(provider.limits.maxBytes+1, provider.limits); err == nil {
		t.Fatal("oversized cache entry should be rejected")
	}
}

func TestIndexRestoresCacheAcrossProviderRestart(t *testing.T) {
	directory := t.TempDir()
	first := newTestProvider(t, directory, time.Minute)
	if err := first.SetMultiLevel("GET-http-example.test-/asset", "GET-http-example.test-/asset", cachedResponse("body"), nil, "", time.Minute, "GET-http-example.test-/asset"); err != nil {
		t.Fatal(err)
	}
	first.closeIndex()

	restarted := newTestProvider(t, directory, time.Minute)
	fresh, _ := restarted.GetMultiLevel("GET-http-example.test-/asset", &http.Request{Header: http.Header{}}, &core.Revalidator{})
	if fresh == nil {
		t.Fatalf("cache index did not restore the response: keys=%v corruptions=%d", restarted.ListKeys(), restarted.corruptions.Load())
	}
	defer fresh.Body.Close()
}

func TestTagIndexPersistsAcrossProviderRestart(t *testing.T) {
	directory := t.TempDir()
	key := "GET-http-example.test-/tagged"
	response := []byte("HTTP/1.1 200 OK\r\nCache-Control: public, max-age=60\r\nContent-Length: 4\r\nSurrogate-Key: group-a\r\n\r\nbody")
	first := newTestProvider(t, directory, time.Minute)
	if err := first.SetMultiLevel(key, key, response, nil, "", time.Minute, key); err != nil {
		t.Fatal(err)
	}
	first.closeIndex()

	restarted := newTestProvider(t, directory, time.Minute)
	providers.Store(directory, restarted)
	t.Cleanup(func() { providers.Delete(directory) })
	removed, err := Purge(directory, "TAG", nil, []string{"group-a"})
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 || restarted.Get(key) != nil || restarted.Get(core.MappingKeyPrefix+key) != nil {
		t.Fatalf("persisted TAG purge removed=%d body=%v mapping=%v", removed, restarted.Get(key) != nil, restarted.Get(core.MappingKeyPrefix+key) != nil)
	}
}

func TestOverwritingCacheObjectReplacesUsedBytes(t *testing.T) {
	provider := newTestProvider(t, t.TempDir(), time.Minute)
	key := "GET-http-example.test-/asset"
	if err := provider.SetMultiLevel(key, key, cachedResponse("first"), nil, "", time.Minute, key); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetMultiLevel(key, key, cachedResponse(strings.Repeat("second", 100)), nil, "", time.Minute, key); err != nil {
		t.Fatal(err)
	}

	provider.mu.Lock()
	want := provider.items[key].accountedSize + provider.items[core.MappingKeyPrefix+key].accountedSize
	provider.mu.Unlock()
	if got := provider.cacheUsed.Load(); got != want {
		t.Fatalf("cache used bytes after overwrite = %d, want current object size %d", got, want)
	}
	if err := provider.index.View(func(tx *bolt.Tx) error {
		got := binary.BigEndian.Uint64(tx.Bucket(indexMetaBucket).Get(indexUsedBytesKey))
		if got != want {
			t.Fatalf("persisted used bytes after overwrite = %d, want %d", got, want)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestCorruptIndexAndBodyRecoverWithoutServingPartialData(t *testing.T) {
	for _, test := range []struct {
		name    string
		corrupt func(t *testing.T, directory string, provider *provider)
	}{
		{
			name: "index",
			corrupt: func(t *testing.T, directory string, _ *provider) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(directory, indexName), []byte("{"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "body",
			corrupt: func(t *testing.T, _ string, provider *provider) {
				t.Helper()
				provider.mu.Lock()
				path := string(provider.items["GET-http-example.test-/asset"].value)
				provider.mu.Unlock()
				if err := os.WriteFile(path, []byte("not-lz4"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			first := newTestProvider(t, directory, time.Minute)
			if err := first.SetMultiLevel("GET-http-example.test-/asset", "GET-http-example.test-/asset", cachedResponse("body"), nil, "", time.Minute, "GET-http-example.test-/asset"); err != nil {
				t.Fatal(err)
			}
			first.closeIndex()
			test.corrupt(t, directory, first)
			restarted := newTestProvider(t, directory, time.Minute)
			fresh, stale := restarted.GetMultiLevel("GET-http-example.test-/asset", &http.Request{Header: http.Header{}}, &core.Revalidator{})
			if fresh != nil || stale != nil {
				t.Fatal("corrupt cache data was served")
			}
			if restarted.corruptions.Load() == 0 {
				t.Fatal("cache corruption was not recorded")
			}
			if restarted.Get(core.MappingKeyPrefix+"GET-http-example.test-/asset") != nil {
				t.Fatal("corruption recovery left a dangling cache mapping")
			}
		})
	}
}

func TestOversizedWriteDoesNotEvictExistingObject(t *testing.T) {
	provider := newTestProvider(t, t.TempDir(), 0)
	key := "GET-http-example.test-/kept"
	if err := provider.SetMultiLevel(key, key, cachedResponse("keep"), nil, "", time.Minute, key); err != nil {
		t.Fatal(err)
	}
	if err := provider.ensureSpace(1<<20+1, provider.limits); !errors.Is(err, ErrCapacity) {
		t.Fatalf("ensureSpace error = %v, want ErrCapacity", err)
	}
	if provider.Get(key) == nil {
		t.Fatal("oversized write evicted the existing object")
	}
	if provider.rejections.Load() != 1 {
		t.Fatalf("rejected writes = %d", provider.rejections.Load())
	}
}

func TestRejectsInterruptedResponseAndPurgeCountsObjects(t *testing.T) {
	provider := newTestProvider(t, t.TempDir(), 0)
	key := "GET-http-example.test-/asset"
	partial := []byte("HTTP/1.1 200 OK\r\nContent-Length: 10\r\n\r\nxx")
	if err := provider.SetMultiLevel(key, key, partial, nil, "", time.Minute, key); err == nil {
		t.Fatal("incomplete upstream response was cached")
	}
	if err := provider.SetMultiLevel(key, key, cachedResponse("complete"), nil, "", time.Minute, key); err != nil {
		t.Fatal(err)
	}
	providers.Store(provider.path, provider)
	t.Cleanup(func() { providers.Delete(provider.path) })
	count, err := Purge(provider.path, "URL", []string{"example.test"}, []string{"/asset"})
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("purged objects = %d, want 1", count)
	}
	if stats := Stats(provider.path); stats.Entries != 0 || stats.Corruptions != 1 {
		t.Fatalf("unexpected cache stats: %#v", stats)
	}
}

func TestURLPurgeRequiresHostBoundary(t *testing.T) {
	provider := newTestProvider(t, t.TempDir(), 0)
	key := "GET-http-notexample.test-/asset"
	if err := provider.SetMultiLevel(key, key, cachedResponse("keep"), nil, "", time.Minute, key); err != nil {
		t.Fatal(err)
	}
	providers.Store(provider.path, provider)
	t.Cleanup(func() { providers.Delete(provider.path) })
	count, err := Purge(provider.path, "URL", []string{"example.test"}, []string{"/asset"})
	if err != nil {
		t.Fatal(err)
	}
	if count != 0 || provider.Get(key) == nil {
		t.Fatalf("purge crossed host boundary: count=%d", count)
	}
}

func TestStrictResponseDirectivesAreNeverStored(t *testing.T) {
	provider := newTestProvider(t, t.TempDir(), 0)
	for _, directive := range []string{"private", "no-store", "no-cache", "must-revalidate"} {
		key := "GET-http-example.test-/" + directive
		response := []byte("HTTP/1.1 200 OK\r\nCache-Control: " + directive +
			"\r\nContent-Length: 2\r\n\r\nok")
		if err := provider.SetMultiLevel(key, key, response, nil, "", time.Minute, key); !errors.Is(err, ErrUncacheable) {
			t.Fatalf("directive %s: error=%v, want ErrUncacheable", directive, err)
		}
		if provider.Get(key) != nil {
			t.Fatalf("directive %s was stored", directive)
		}
	}
	key := "GET-http-example.test-/multiple-cache-control-lines"
	response := []byte("HTTP/1.1 200 OK\r\nCache-Control: public, max-age=60\r\n" +
		"Cache-Control: no-store\r\nContent-Length: 2\r\n\r\nok")
	if err := provider.SetMultiLevel(key, key, response, nil, "", time.Minute, key); !errors.Is(err, ErrUncacheable) {
		t.Fatalf("second Cache-Control field was ignored: %v", err)
	}
}

func TestNotModifiedResponseIsNeverStored(t *testing.T) {
	provider := newTestProvider(t, t.TempDir(), 0)
	key := "GET-http-example.test-/asset.js"
	response := []byte("HTTP/1.1 304 Not Modified\r\n" +
		"Cache-Control: public, max-age=300\r\nEtag: \"v1\"\r\n\r\n")
	if err := provider.SetMultiLevel(key, key, response, nil, `"v1"`, time.Minute, key); !errors.Is(err, ErrUncacheable) {
		t.Fatalf("304 response error=%v, want ErrUncacheable", err)
	}
	if provider.Get(key) != nil || provider.Get(core.MappingKeyPrefix+key) != nil {
		t.Fatal("304 response or its lookup mapping was stored")
	}
	if err := validateCachedResponse(response); !errors.Is(err, ErrUncacheable) {
		t.Fatalf("stored 304 response validation error=%v, want ErrUncacheable", err)
	}
}

func TestRuntimeCorruptionRemovesBodyAndMapping(t *testing.T) {
	provider := newTestProvider(t, t.TempDir(), 0)
	key := "GET-http-example.test-/asset"
	if err := provider.SetMultiLevel(key, key, cachedResponse("body"), nil, "", time.Minute, key); err != nil {
		t.Fatal(err)
	}
	provider.mu.Lock()
	bodyPath := string(provider.items[key].value)
	provider.mu.Unlock()
	if err := os.WriteFile(bodyPath, []byte("corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}

	fresh, stale := provider.GetMultiLevel(key, &http.Request{Header: http.Header{}}, &core.Revalidator{})
	if fresh != nil || stale != nil {
		t.Fatal("runtime-corrupt response was served")
	}
	provider.mu.Lock()
	_, bodyExists := provider.items[key]
	_, mappingExists := provider.items[core.MappingKeyPrefix+key]
	provider.mu.Unlock()
	if bodyExists || mappingExists {
		t.Fatalf("corrupt body or mapping remained: body=%v mapping=%v", bodyExists, mappingExists)
	}
	if provider.corruptions.Load() != 1 {
		t.Fatalf("corruptions=%d, want 1", provider.corruptions.Load())
	}
}

func TestEmptyMappingIsRemovedAsCorruption(t *testing.T) {
	provider := newTestProvider(t, t.TempDir(), 0)
	mappingKey := core.MappingKeyPrefix + "key"
	if err := provider.Set(mappingKey, []byte{}, time.Minute); err != nil {
		t.Fatal(err)
	}
	fresh, stale := provider.GetMultiLevel("key", &http.Request{Header: http.Header{}}, &core.Revalidator{})
	if fresh != nil || stale != nil {
		t.Fatal("empty mapping was served")
	}
	if provider.Get(mappingKey) != nil || provider.corruptions.Load() != 1 || provider.misses.Load() != 1 {
		t.Fatalf("empty mapping recovery: mapping=%v corruptions=%d misses=%d", provider.Get(mappingKey) != nil, provider.corruptions.Load(), provider.misses.Load())
	}
}

func TestEvictionRemovesVariantFromMapping(t *testing.T) {
	provider := newTestProvider(t, t.TempDir(), 0)
	key := "GET-http-example.test-/asset"
	if err := provider.SetMultiLevel(key, key, cachedResponse("body"), nil, "", time.Minute, key); err != nil {
		t.Fatal(err)
	}
	if !provider.evictOldest() {
		t.Fatal("expected one cache object to be evicted")
	}
	if provider.Get(key) != nil || provider.Get(core.MappingKeyPrefix+key) != nil {
		t.Fatal("eviction left a response body or lookup mapping behind")
	}
}

func TestBatchEvictionRemovesEnoughLRUObjectsWithOneIndexWrite(t *testing.T) {
	provider := newTestProvider(t, t.TempDir(), 0)
	keys := []string{
		"GET-http-example.test-/first",
		"GET-http-example.test-/second",
		"GET-http-example.test-/third",
	}
	for _, key := range keys {
		if err := provider.SetMultiLevel(key, key, cachedResponse(strings.Repeat(key, 64)), nil, "", time.Minute, key); err != nil {
			t.Fatal(err)
		}
	}
	provider.mu.Lock()
	for index, key := range keys {
		item := provider.items[key]
		item.lastAccess = time.Unix(int64(index+1), 0)
		provider.items[key] = item
	}
	firstSize := provider.items[keys[0]].accountedSize
	provider.mu.Unlock()
	writesBefore := provider.indexWrites.Load()
	freed, err := provider.evictBytes(firstSize + 1)
	if err != nil {
		t.Fatal(err)
	}
	if freed <= firstSize || provider.evictions.Load() != 2 {
		t.Fatalf("batch eviction freed=%d evictions=%d", freed, provider.evictions.Load())
	}
	if writes := provider.indexWrites.Load() - writesBefore; writes != 1 {
		t.Fatalf("batch eviction index writes=%d, want 1", writes)
	}
	if provider.Get(keys[0]) != nil || provider.Get(keys[1]) != nil || provider.Get(keys[2]) == nil {
		t.Fatal("batch eviction did not remove the two oldest objects")
	}
}

func TestFullURLPurgePreservesQueryAndSupportsRoot(t *testing.T) {
	provider := newTestProvider(t, t.TempDir(), 0)
	first := "GET-http-example.test-/asset?version=1"
	second := "GET-http-example.test-/asset?version=2"
	bare := "GET-http-example.test-/asset"
	root := "GET-http-example.test-/"
	for _, key := range []string{first, second, bare, root} {
		if err := provider.SetMultiLevel(key, key, cachedResponse(key), nil, "", time.Minute, key); err != nil {
			t.Fatal(err)
		}
	}
	providers.Store(provider.path, provider)
	t.Cleanup(func() { providers.Delete(provider.path) })

	count, err := Purge(provider.path, "URL", []string{"example.test"}, []string{"https://EXAMPLE.test/asset?version=1"})
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 || provider.Get(first) != nil || provider.Get(second) == nil {
		t.Fatalf("query-specific purge mismatch: count=%d first=%v second=%v", count, provider.Get(first), provider.Get(second))
	}
	count, err = Purge(provider.path, "URL", []string{"example.test"}, []string{"/asset"})
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 || provider.Get(bare) != nil || provider.Get(second) == nil {
		t.Fatalf("queryless purge mismatch: count=%d bare=%v query=%v", count, provider.Get(bare), provider.Get(second))
	}
	count, err = Purge(provider.path, "URL", []string{"example.test"}, []string{"https://example.test"})
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 || provider.Get(root) != nil {
		t.Fatalf("root purge mismatch: count=%d root=%v", count, provider.Get(root))
	}
}

func TestBuildProviderDoesNotScanActiveDirectory(t *testing.T) {
	directory := t.TempDir()
	pending := filepath.Join(directory, bodyPrefix+"pending")
	if err := os.WriteFile(pending, []byte("pending-write"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := buildProvider(core.CacheProvider{Path: directory}, zap.NewNop().Sugar(), 0); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(pending); err != nil {
		t.Fatalf("provider construction scanned or deleted active data: %v", err)
	}
}

func TestStartupRemovesInterruptedAtomicWrite(t *testing.T) {
	directory := t.TempDir()
	temporary := filepath.Join(directory, ".body-item.tmp-interrupted")
	if err := os.WriteFile(temporary, []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = newTestProvider(t, directory, 0)
	if _, err := os.Stat(temporary); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temporary file was not removed: %v", err)
	}
}

func TestNormalizePercentEnforcesNinetyPercentHardLimit(t *testing.T) {
	for input, want := range map[int]int{0: 80, 1: 1, 80: 80, 90: 90, 91: 90, 100: 90} {
		if got := normalizePercent(input); got != want {
			t.Fatalf("normalizePercent(%d)=%d, want %d", input, got, want)
		}
	}
}

func TestPurgeRejectsInvalidRequestsBeforeProviderLookup(t *testing.T) {
	for _, test := range []struct {
		purgeType string
		values    []string
	}{
		{purgeType: "UNKNOWN", values: []string{"/asset"}},
		{purgeType: "URL"},
		{purgeType: "ALL", values: []string{"/asset"}},
	} {
		if _, err := Purge(t.TempDir(), test.purgeType, nil, test.values); err == nil {
			t.Fatalf("Purge(%q, %v) accepted invalid input", test.purgeType, test.values)
		}
	}
}

func TestLongCacheKeyUsesBoundedBodyFileName(t *testing.T) {
	provider := newTestProvider(t, t.TempDir(), 0)
	key := "GET-http-example.test-/asset?token=" + string(bytes.Repeat([]byte("a"), 8<<10))
	if err := provider.SetMultiLevel(key, key, cachedResponse("body"), nil, "", time.Minute, key); err != nil {
		t.Fatal(err)
	}
	provider.mu.Lock()
	path := string(provider.items[key].value)
	provider.mu.Unlock()
	if len(filepath.Base(path)) != len(bodyPrefix)+sha256.Size*4+1 {
		t.Fatalf("cache body filename length=%d", len(filepath.Base(path)))
	}
	if provider.Get(key) == nil {
		t.Fatal("long-key response was not readable")
	}
}

func TestConcurrentPreparedWritesShareOneIndexCommit(t *testing.T) {
	provider := newTestProvider(t, t.TempDir(), 0)
	const count = 32
	writes := make([]*pendingWrite, count)
	for index := range count {
		key := fmt.Sprintf("GET-http-example.test-/batch/%d", index)
		value := cachedResponse(strings.Repeat("body", 32))
		temporary, size, checksum, err := writeCompressedTemporary(filepath.Join(provider.path, bodyFileName(key)), value)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Remove(temporary) })
		writes[index] = &pendingWrite{
			baseKey: key, variedKey: key, temporaryPath: temporary,
			finalPath:      filepath.Join(provider.path, contentBodyFileName(key, checksum)),
			compressedSize: size, originalSize: uint64(len(value)), checksum: checksum,
			duration: time.Minute, realKey: key, done: make(chan error, 1),
		}
	}

	writesBefore := provider.indexWrites.Load()
	start := make(chan struct{})
	errorsByIndex := make([]error, count)
	var group sync.WaitGroup
	for index := range count {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			errorsByIndex[index] = provider.enqueueWrite(writes[index])
		}()
	}
	close(start)
	group.Wait()
	for index, err := range errorsByIndex {
		if err != nil {
			t.Fatalf("write %d failed: %v", index, err)
		}
	}
	if writes := provider.indexWrites.Load() - writesBefore; writes != 1 {
		t.Fatalf("index writes=%d, want one batch commit", writes)
	}
	if provider.objectsCommitted.Load() != count || provider.writeBatches.Load() != 1 {
		t.Fatalf("objects=%d batches=%d", provider.objectsCommitted.Load(), provider.writeBatches.Load())
	}
}

func TestBatchCommitFailureDoesNotPublishObject(t *testing.T) {
	provider := newTestProvider(t, t.TempDir(), 0)
	key := "GET-http-example.test-/failed-batch"
	value := cachedResponse("body")
	temporary, size, checksum, err := writeCompressedTemporary(filepath.Join(provider.path, bodyFileName(key)), value)
	if err != nil {
		t.Fatal(err)
	}
	finalPath := filepath.Join(provider.path, contentBodyFileName(key, checksum))
	provider.closeIndex()
	err = provider.enqueueWrite(&pendingWrite{
		baseKey: key, variedKey: key, temporaryPath: temporary, finalPath: finalPath,
		compressedSize: size, originalSize: uint64(len(value)), checksum: checksum,
		groups: []string{"group-a"}, duration: time.Minute, realKey: key, done: make(chan error, 1),
	})
	if err == nil {
		t.Fatal("write with a closed index succeeded")
	}
	provider.mu.RLock()
	_, bodyExists := provider.items[key]
	_, mappingExists := provider.items[core.MappingKeyPrefix+key]
	provider.mu.RUnlock()
	if bodyExists || mappingExists {
		t.Fatalf("failed transaction published state: body=%v mapping=%v", bodyExists, mappingExists)
	}
	if len(provider.groups["group-a"]) != 0 || len(provider.itemGroups[key]) != 0 {
		t.Fatal("failed transaction published surrogate tag indexes")
	}
	if _, statErr := os.Stat(finalPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("failed transaction left final file: %v", statErr)
	}
}

func TestWriteQueueRejectsAtObjectLimit(t *testing.T) {
	provider := newTestProvider(t, t.TempDir(), 0)
	provider.batchMu.Lock()
	provider.pending = make([]*pendingWrite, writeQueueMaxObjects)
	provider.flushing = true
	provider.batchMu.Unlock()
	err := provider.enqueueWrite(&pendingWrite{compressedSize: 1, done: make(chan error, 1)})
	if !errors.Is(err, ErrWriteQueueFull) {
		t.Fatalf("queue error=%v, want ErrWriteQueueFull", err)
	}
	if provider.queueRejections.Load() != 1 || provider.rejections.Load() != 1 {
		t.Fatalf("queue rejections=%d total rejections=%d", provider.queueRejections.Load(), provider.rejections.Load())
	}
	provider.batchMu.Lock()
	provider.pending = nil
	provider.flushing = false
	provider.batchMu.Unlock()
}

func TestResetPathClearsWriteStatisticsAfterCommit(t *testing.T) {
	directory := t.TempDir()
	provider := newTestProvider(t, directory, 0)
	providers.Store(provider.path, provider)
	t.Cleanup(func() { providers.Delete(provider.path) })

	provider.queueDepth.Store(1)
	provider.queueBytes.Store(2)
	provider.queueDepthMax.Store(3)
	provider.queueBytesMax.Store(4)
	provider.queueRejections.Store(5)
	provider.writeBatches.Store(6)
	provider.objectsCommitted.Store(7)
	provider.commitNanos.Store(8)
	provider.inflightWrites.Store(9)

	if err := ResetPath(directory); err != nil {
		t.Fatal(err)
	}
	stats := Stats(directory)
	if stats.WriteQueueDepth != 0 || stats.WriteQueueBytes != 0 || stats.WriteQueueDepthMax != 0 || stats.WriteQueueBytesMax != 0 ||
		stats.WriteQueueRejections != 0 || stats.WriteBatches != 0 || stats.WriteObjectsCommitted != 0 ||
		stats.WriteCommitLatencyMS != 0 || stats.InflightWrites != 0 {
		t.Fatalf("write statistics were not reset: %+v", stats)
	}
}

func TestExpiredReadDoesNotPublishDeletionWhenIndexCommitFails(t *testing.T) {
	provider := newTestProvider(t, t.TempDir(), 0)
	key := "GET-http-example.test-/expired"
	value := cachedResponse("body")
	if err := provider.SetMultiLevel(key, key, value, nil, "", time.Millisecond, key); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Millisecond)

	provider.mu.RLock()
	item := provider.items[key]
	provider.mu.RUnlock()
	provider.closeIndex()
	if got := provider.Get(key); got != nil {
		t.Fatal("expired object remained readable")
	}
	provider.mu.RLock()
	retained, ok := provider.items[key]
	provider.mu.RUnlock()
	if !ok || retained.generation != item.generation {
		t.Fatal("failed index transaction published the deletion in memory")
	}
	if _, err := os.Stat(string(item.value)); err != nil {
		t.Fatalf("failed index transaction removed the cache file: %v", err)
	}
}

func TestConcurrentMixedKeyReadsRemainComplete(t *testing.T) {
	provider := newTestProvider(t, t.TempDir(), 0)
	provider.limits.maxBytes = 8 << 20
	const keyCount = 256
	body := strings.Repeat("x", 16<<10)
	response := cachedResponse(body)

	var writers sync.WaitGroup
	writeErrors := make(chan error, keyCount)
	for index := range keyCount {
		writers.Add(1)
		go func() {
			defer writers.Done()
			key := fmt.Sprintf("GET-http-example.test-/mixed/%d", index)
			writeErrors <- provider.SetMultiLevel(key, key, response, nil, "", time.Minute, key)
		}()
	}
	writers.Wait()
	close(writeErrors)
	for err := range writeErrors {
		if err != nil {
			t.Fatal(err)
		}
	}

	const readerCount = 128
	const readsPerReader = 80
	readErrors := make(chan error, readerCount)
	var readers sync.WaitGroup
	for reader := range readerCount {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for offset := range readsPerReader {
				key := fmt.Sprintf("GET-http-example.test-/mixed/%d", (reader+offset)%keyCount)
				fresh, _ := provider.GetMultiLevel(key, &http.Request{Header: http.Header{}}, &core.Revalidator{})
				if fresh == nil {
					readErrors <- fmt.Errorf("key %q missed", key)
					return
				}
				got, err := io.ReadAll(fresh.Body)
				_ = fresh.Body.Close()
				if err != nil || string(got) != body {
					readErrors <- fmt.Errorf("key %q bytes=%d err=%v", key, len(got), err)
					return
				}
			}
		}()
	}
	readers.Wait()
	close(readErrors)
	for err := range readErrors {
		t.Fatal(err)
	}
}

func TestCachedRangeStopsBeforeFollowingObjectChunk(t *testing.T) {
	payload := make([]byte, 256<<10)
	var value uint32 = 1
	for index := range payload {
		value = value*1664525 + 1013904223
		payload[index] = byte(value >> 24)
	}
	responseBytes := append([]byte("HTTP/1.1 200 OK\r\nContent-Length: "+strconv.Itoa(len(payload))+"\r\n\r\n"), payload...)
	target := filepath.Join(t.TempDir(), "range")
	path, size, _, err := writeCompressedTemporary(target, responseBytes)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	object, err := newObjectReader(bytes.NewReader(encoded), size)
	if err != nil {
		t.Fatal(err)
	}
	counted := &countingReader{Reader: object}
	decoder := acquireResponseDecoder(counted)
	parsed, err := http.ReadResponse(decoder.buffered, &http.Request{Method: http.MethodGet})
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(filepath.Join(t.TempDir(), "owner"))
	if err == nil {
		t.Fatal("owner file unexpectedly existed")
	}
	ownerPath := filepath.Join(t.TempDir(), "owner")
	if err = os.WriteFile(ownerPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	file, err = os.Open(ownerPath)
	if err != nil {
		t.Fatal(err)
	}
	parsed.Body = &pooledResponseBody{body: decoder.buffered, remaining: parsed.ContentLength, file: file, decoder: decoder}
	if err = applyCachedRange(parsed, cacherange.Spec{Start: 0, End: 1023}); err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(parsed.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload[:1024]) {
		t.Fatal("range body mismatch")
	}
	if counted.Bytes >= uint64(len(responseBytes)) || counted.Bytes > 80<<10 {
		t.Fatalf("range consumed %d of %d logical bytes", counted.Bytes, len(responseBytes))
	}
}

func TestExpirationHeapRemainsBoundedAcrossOverwrites(t *testing.T) {
	provider := newTestProvider(t, t.TempDir(), 0)
	for index := range 100 {
		if err := provider.SetMultiLevel("key", "key", cachedResponse(strconv.Itoa(index)), nil, "", time.Hour, "key"); err != nil {
			t.Fatal(err)
		}
		if len(provider.expirations) != 2 {
			t.Fatalf("overwrite %d left %d expiration entries", index, len(provider.expirations))
		}
	}
	stats := Statistics{BodyEntries: provider.bodyEntries.Load(), MappingEntries: provider.mappingEntries.Load(), ExpirationEntries: provider.expirationEntries.Load()}
	if stats.BodyEntries != 1 || stats.MappingEntries != 1 || stats.ExpirationEntries != 2 {
		t.Fatalf("unexpected O(1) counters: %#v", stats)
	}
	provider.Delete("key")
	if len(provider.expirations) != 0 || provider.expirationEntries.Load() != 0 {
		t.Fatal("deletion left expiration entries")
	}
}

func TestCapacityChurnReusesBoltPagesAndKeepsCountersBounded(t *testing.T) {
	provider := newTestProvider(t, t.TempDir(), 0)
	provider.limits.maxBytes = 128 << 10
	const writesPerRound = 240
	indexPath := filepath.Join(provider.path, indexName)

	indexSizes := make([]int64, 0, 2)
	for round := range 2 {
		for index := range writesPerRound {
			key := fmt.Sprintf("GET-http-example.test-/churn/%d/%d", round, index)
			if err := provider.SetMultiLevel(key, key, cachedResponse(strings.Repeat("x", 1024)), nil, "", time.Hour, key); err != nil {
				t.Fatal(err)
			}
		}
		info, err := os.Stat(indexPath)
		if err != nil {
			t.Fatal(err)
		}
		indexSizes = append(indexSizes, info.Size())
		bodyCount := provider.bodyEntries.Load()
		mappingCount := provider.mappingEntries.Load()
		if bodyCount == 0 || mappingCount == 0 || provider.cacheUsed.Load() > provider.limits.maxBytes {
			t.Fatalf("round %d counters exceed capacity: body=%d mapping=%d accounted=%d max=%d", round, bodyCount, mappingCount, provider.cacheUsed.Load(), provider.limits.maxBytes)
		}
		if got, want := provider.expirationEntries.Load(), bodyCount+mappingCount; got != want {
			t.Fatalf("round %d expiration entries=%d, want %d", round, got, want)
		}
	}
	if growth := indexSizes[1] - indexSizes[0]; growth > 64<<20 {
		t.Fatalf("second steady-state churn round grew Bolt by %d bytes; background compaction is required", growth)
	}
}

func TestResetReplacesAndShrinksBoltIndex(t *testing.T) {
	provider := newTestProvider(t, t.TempDir(), 0)
	provider.limits.maxBytes = 4 << 20
	for index := range 100 {
		key := fmt.Sprintf("GET-http-example.test-/reset/%d", index)
		if err := provider.SetMultiLevel(key, key, cachedResponse(strings.Repeat("x", 1024)), nil, "", time.Hour, key); err != nil {
			t.Fatal(err)
		}
	}
	before, err := os.Stat(filepath.Join(provider.path, indexName))
	if err != nil {
		t.Fatal(err)
	}
	if err = provider.Reset(); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(filepath.Join(provider.path, indexName))
	if err != nil {
		t.Fatal(err)
	}
	if after.Size() >= before.Size() {
		t.Fatalf("reset index size=%d, want less than %d", after.Size(), before.Size())
	}
	if len(provider.items) != 0 || len(provider.expirations) != 0 || provider.cacheUsed.Load() != 0 || provider.physicalUsed.Load() != 0 || provider.bodyEntries.Load() != 0 || provider.mappingEntries.Load() != 0 {
		t.Fatal("reset retained cache index state")
	}
}
