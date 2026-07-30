package simplefs

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/darkweak/storages/core"
	"github.com/pierrec/lz4/v4"
	"go.uber.org/zap"
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
	return provider
}

func cachedResponse(body string) []byte {
	return []byte("HTTP/1.1 200 OK\r\nCache-Control: public, max-age=60\r\nContent-Length: " +
		strconv.Itoa(len(body)) + "\r\n\r\n" + body)
}

func TestCompressedResponseValidationPreservesStorageEncoding(t *testing.T) {
	want := cachedResponse("body")
	compressed := new(bytes.Buffer)
	writer := lz4.NewWriter(compressed)
	if _, err := writer.Write(want); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "response.lz4")
	if err := os.WriteFile(path, compressed.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := readCachedResponseFile(path)
	if err != nil {
		t.Fatalf("valid compressed response rejected: %v", err)
	}
	if !bytes.Equal(got, compressed.Bytes()) {
		t.Fatal("readCachedResponseFile() altered the LZ4 storage representation")
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

func TestFixedLimitEvictsOldestOrphan(t *testing.T) {
	directory := t.TempDir()
	provider := newTestProvider(t, directory, 0)
	old := filepath.Join(directory, "old-cache-entry")
	if err := os.WriteFile(old, []byte("12345678"), 0o600); err != nil {
		t.Fatal(err)
	}
	used, err := directorySize(directory)
	if err != nil {
		t.Fatal(err)
	}
	provider.limits.maxBytes = used - 8 + 10
	if err := provider.ensureSpace(5, provider.limits); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatalf("old cache entry was not evicted: %v", err)
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

	restarted := newTestProvider(t, directory, time.Minute)
	fresh, _ := restarted.GetMultiLevel("GET-http-example.test-/asset", &http.Request{Header: http.Header{}}, &core.Revalidator{})
	if fresh == nil {
		t.Fatalf("cache index did not restore the response: keys=%v corruptions=%d", restarted.ListKeys(), restarted.corruptions.Load())
	}
	defer fresh.Body.Close()
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
	firstInfo, err := os.Stat(string(provider.items[keys[0]].value))
	provider.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	writesBefore := provider.indexWrites.Load()
	freed, err := provider.evictBytes(uint64(firstInfo.Size()) + 1)
	if err != nil {
		t.Fatal(err)
	}
	if freed <= uint64(firstInfo.Size()) || provider.evictions.Load() != 2 {
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
	if len(filepath.Base(path)) != len(bodyPrefix)+sha256.Size*2 {
		t.Fatalf("cache body filename length=%d", len(filepath.Base(path)))
	}
	if provider.Get(key) == nil {
		t.Fatal("long-key response was not readable")
	}
}
