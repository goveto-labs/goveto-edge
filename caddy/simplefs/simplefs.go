// Package simplefs provides Goveto's disk-backed HTTP cache storage.
package simplefs

import (
	"bufio"
	"bytes"
	"container/heap"
	"container/list"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/shirou/gopsutil/v4/disk"
	bolt "go.etcd.io/bbolt"
	core "goveto-edge/internal/cachecore"
	"goveto-edge/internal/cacherange"
)

const (
	indexName         = ".goveto-cache-index.db"
	oldIndexName      = ".goveto-cache-index.json"
	formatMarkerName  = ".goveto-cache-format"
	formatMarkerValue = "goveto-http-cache-v1\n"
	bodyPrefix        = "body-"
	indexVersion      = 3
	itemIndexOverhead = uint64(512)
)

var (
	indexItemsBucket  = []byte("items")
	indexGroupsBucket = []byte("groups")
	indexMetaBucket   = []byte("meta")
	indexVersionKey   = []byte("version")
	indexUsedBytesKey = []byte("used_bytes")
	responseDecoders  = sync.Pool{New: func() any { return newResponseDecoder() }}
	decodeMapping     = core.DecodeMapping
)

var ErrCapacity = errors.New("cache storage limit leaves no room for this response")
var ErrUncacheable = errors.New("response is not eligible for shared storage")
var ErrWriteQueueFull = errors.New("cache write queue is full")

var providers sync.Map

// Enforce applies the latest node policy even when no new cache object is
// being written. Cache writers also enforce the same policy before each write.
func Enforce(path string, auto bool, maxBytes uint64, maxDiskUsagePercent int) error {
	value, ok := providers.Load(path)
	if !ok {
		return nil
	}
	provider, ok := value.(*provider)
	if !ok {
		return nil
	}
	provider.operationMu.Lock()
	defer provider.operationMu.Unlock()
	return provider.ensureSpace(0, limits{
		auto:                auto,
		maxBytes:            maxBytes,
		maxDiskUsagePercent: normalizePercent(maxDiskUsagePercent),
	})
}

// Stats returns aggregate cache activity for providers below path.
func Stats(path string) Statistics {
	result := Statistics{}
	providers.Range(func(_, value any) bool {
		provider, ok := value.(*provider)
		if !ok || !pathContains(path, provider.path) {
			return true
		}
		result.BodyEntries += provider.bodyEntries.Load()
		result.MappingEntries += provider.mappingEntries.Load()
		result.ExpirationEntries += provider.expirationEntries.Load()
		result.Entries += provider.bodyEntries.Load()
		result.AccountedBytes += provider.cacheUsed.Load()
		result.PhysicalBytes += provider.physicalUsed.Load()
		provider.operationMu.RLock()
		if provider.index != nil {
			if info, err := os.Stat(provider.index.Path()); err == nil && info.Size() > 0 {
				result.IndexBytes += uint64(info.Size())
			}
			indexStats := provider.index.Stats()
			result.IndexFreePages += uint64(max(indexStats.FreePageN, 0))
			result.IndexPendingPages += uint64(max(indexStats.PendingPageN, 0))
		}
		provider.operationMu.RUnlock()
		result.Hits += provider.hits.Load()
		result.Misses += provider.misses.Load()
		result.StaleHits += provider.staleHits.Load()
		result.Evictions += provider.evictions.Load()
		result.RejectedWrites += provider.rejections.Load()
		result.Corruptions += provider.corruptions.Load()
		result.WriteQueueDepth += provider.queueDepth.Load()
		result.WriteQueueBytes += provider.queueBytes.Load()
		result.WriteQueueDepthMax = max(result.WriteQueueDepthMax, provider.queueDepthMax.Load())
		result.WriteQueueBytesMax = max(result.WriteQueueBytesMax, provider.queueBytesMax.Load())
		result.WriteQueueRejections += provider.queueRejections.Load()
		result.WriteBatches += provider.writeBatches.Load()
		result.WriteObjectsCommitted += provider.objectsCommitted.Load()
		result.WriteCommitLatencyMS += float64(provider.commitNanos.Load()) / float64(time.Millisecond)
		result.InflightWrites += provider.inflightWrites.Load()
		return true
	})
	if total := result.Hits + result.Misses; total > 0 {
		result.HitRate = float64(result.Hits) / float64(total)
	}
	if result.WriteBatches > 0 {
		result.AverageWriteBatchSize = float64(result.WriteObjectsCommitted) / float64(result.WriteBatches)
		result.WriteCommitLatencyMS /= float64(result.WriteBatches)
	}
	return result
}

// Drain waits until every cache write below path has committed or failed.
func Drain(path string) {
	active := providersForPath(path)
	for _, provider := range active {
		provider.operationMu.Lock()
	}
	defer func() {
		for index := len(active) - 1; index >= 0; index-- {
			active[index].operationMu.Unlock()
		}
	}()
	for _, provider := range active {
		provider.drain()
	}
}

// ResetPath clears active cache providers below path without unregistering them.
func ResetPath(path string) error {
	active := providersForPath(path)
	for _, provider := range active {
		provider.operationMu.Lock()
	}
	defer func() {
		for index := len(active) - 1; index >= 0; index-- {
			active[index].operationMu.Unlock()
		}
	}()
	var result error
	for _, provider := range active {
		provider.drain()
		provider.capacityMu.Lock()
		provider.mu.Lock()
		obsolete, err := provider.resetLocked()
		provider.mu.Unlock()
		provider.capacityMu.Unlock()
		if err == nil {
			removeFiles(obsolete)
			provider.resetWriteStatistics()
		}
		result = errors.Join(result, err)
	}
	return result
}

func (p *provider) resetWriteStatistics() {
	p.queueDepth.Store(0)
	p.queueBytes.Store(0)
	p.queueDepthMax.Store(0)
	p.queueBytesMax.Store(0)
	p.queueRejections.Store(0)
	p.writeBatches.Store(0)
	p.objectsCommitted.Store(0)
	p.commitNanos.Store(0)
	p.inflightWrites.Store(0)
}

func providersForPath(path string) []*provider {
	result := make([]*provider, 0)
	providers.Range(func(_, value any) bool {
		provider, ok := value.(*provider)
		if ok && pathContains(path, provider.path) {
			result = append(result, provider)
		}
		return true
	})
	sort.Slice(result, func(i, j int) bool { return result[i].path < result[j].path })
	return result
}

func pathContains(parent, child string) bool {
	relative, err := filepath.Rel(filepath.Clean(parent), filepath.Clean(child))
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

type limits struct {
	auto                bool
	maxBytes            uint64
	maxDiskUsagePercent int
}

type cacheItem struct {
	value          []byte
	mapping        *core.StorageMapper
	object         *cachedObjectState
	expiresAt      time.Time
	lastAccess     time.Time
	file           bool
	generation     uint64
	compressedSize uint64
	physicalSize   uint64
	accountedSize  uint64
	originalSize   uint64
	checksum       [sha256.Size]byte
	modifiedAt     int64
	lru            *list.Element
	expiration     *expirationEntry
}

type responseDecoder struct {
	empty    bytes.Reader
	buffered *bufio.Reader
}

type cachedResponseTemplate struct {
	status        string
	statusCode    int
	proto         string
	protoMajor    int
	protoMinor    int
	header        http.Header
	contentLength int64
	bodyStart     int64
}

type cachedObjectState struct {
	once     sync.Once
	layout   *objectLayout
	response cachedResponseTemplate
	err      error
}

func newResponseDecoder() *responseDecoder {
	decoder := new(responseDecoder)
	decoder.buffered = bufio.NewReader(&decoder.empty)
	return decoder
}

func acquireResponseDecoder(source io.Reader) *responseDecoder {
	decoder := responseDecoders.Get().(*responseDecoder)
	decoder.buffered.Reset(source)
	return decoder
}

func releaseResponseDecoder(decoder *responseDecoder) {
	decoder.empty.Reset(nil)
	decoder.buffered.Reset(&decoder.empty)
	responseDecoders.Put(decoder)
}

type pooledResponseBody struct {
	body      io.Reader
	remaining int64
	file      *os.File
	object    *objectReader
	bodyStart int64
	decoder   *responseDecoder
	onError   func()
	once      sync.Once
}

func (b *pooledResponseBody) seekBody(offset uint64) error {
	if b.object == nil || offset > uint64(b.remaining) {
		return errors.New("cached body seek is unavailable")
	}
	if _, err := b.object.Seek(b.bodyStart+int64(offset), io.SeekStart); err != nil {
		return err
	}
	if b.decoder != nil {
		b.decoder.buffered.Reset(b.object)
		b.body = b.decoder.buffered
	} else {
		b.body = b.object
	}
	b.remaining -= int64(offset)
	return nil
}

type rangedResponseBody struct {
	body      io.ReadCloser
	skip      uint64
	remaining uint64
	closed    bool
}

func (b *rangedResponseBody) Read(target []byte) (int, error) {
	if b.closed {
		return 0, io.EOF
	}
	if b.skip > 0 {
		if _, err := io.CopyN(io.Discard, b.body, int64(b.skip)); err != nil {
			_ = b.Close()
			return 0, err
		}
		b.skip = 0
	}
	if b.remaining == 0 {
		_ = b.Close()
		return 0, io.EOF
	}
	if uint64(len(target)) > b.remaining {
		target = target[:b.remaining]
	}
	count, err := b.body.Read(target)
	b.remaining -= uint64(count)
	if err != nil || b.remaining == 0 {
		closeErr := b.Close()
		if err == nil && closeErr != nil {
			err = closeErr
		}
	}
	return count, err
}

func (b *rangedResponseBody) Close() error {
	if b.closed {
		return nil
	}
	b.closed = true
	return b.body.Close()
}

func (b *pooledResponseBody) Read(target []byte) (int, error) {
	if b.remaining == 0 {
		_ = b.release()
		return 0, io.EOF
	}
	if int64(len(target)) > b.remaining {
		target = target[:b.remaining]
	}
	count, err := b.body.Read(target)
	b.remaining -= int64(count)
	if errors.Is(err, io.EOF) && b.remaining > 0 {
		err = io.ErrUnexpectedEOF
	}
	if b.remaining == 0 {
		if errors.Is(err, io.EOF) {
			err = nil
		}
		_ = b.release()
	} else if err != nil {
		if b.onError != nil {
			b.onError()
			b.onError = nil
		}
		_ = b.release()
	}
	return count, err
}

func (b *pooledResponseBody) Close() error {
	return b.release()
}

func (b *pooledResponseBody) release() error {
	var err error
	b.once.Do(func() {
		err = b.file.Close()
		if b.object != nil {
			b.object.release()
		}
		if b.decoder != nil {
			releaseResponseDecoder(b.decoder)
		}
		b.file = nil
		b.object = nil
		b.decoder = nil
	})
	return err
}

type diskItem struct {
	Value          []byte    `json:"value,omitempty"`
	File           string    `json:"file,omitempty"`
	ExpiresAt      time.Time `json:"expires_at,omitempty"`
	LastAccess     time.Time `json:"last_access"`
	CompressedSize uint64    `json:"compressed_size,omitempty"`
	OriginalSize   uint64    `json:"original_size,omitempty"`
	Checksum       []byte    `json:"checksum,omitempty"`
	ModifiedAt     int64     `json:"modified_at,omitempty"`
}

type Statistics struct {
	Entries               uint64  `json:"entries"`
	BodyEntries           uint64  `json:"body_entries"`
	MappingEntries        uint64  `json:"mapping_entries"`
	ExpirationEntries     uint64  `json:"expiration_entries"`
	AccountedBytes        uint64  `json:"accounted_bytes"`
	PhysicalBytes         uint64  `json:"physical_bytes"`
	IndexBytes            uint64  `json:"index_bytes"`
	IndexFreePages        uint64  `json:"index_free_pages"`
	IndexPendingPages     uint64  `json:"index_pending_pages"`
	Hits                  uint64  `json:"hits"`
	Misses                uint64  `json:"misses"`
	StaleHits             uint64  `json:"stale_hits"`
	Evictions             uint64  `json:"evictions"`
	RejectedWrites        uint64  `json:"rejected_writes"`
	Corruptions           uint64  `json:"corruptions"`
	HitRate               float64 `json:"hit_rate"`
	WriteQueueDepth       uint64  `json:"write_queue_depth"`
	WriteQueueBytes       uint64  `json:"write_queue_bytes"`
	WriteQueueDepthMax    uint64  `json:"write_queue_depth_max"`
	WriteQueueBytesMax    uint64  `json:"write_queue_bytes_max"`
	WriteQueueRejections  uint64  `json:"write_queue_rejections"`
	WriteBatches          uint64  `json:"write_batches"`
	WriteObjectsCommitted uint64  `json:"write_objects_committed"`
	AverageWriteBatchSize float64 `json:"average_write_batch_size"`
	WriteCommitLatencyMS  float64 `json:"write_commit_latency_ms"`
	InflightWrites        uint64  `json:"inflight_writes"`
}

type diskUsageFunc func(string) (*disk.UsageStat, error)

type diskUsageSnapshot struct {
	total uint64
	used  uint64
}

var diskUsageTestOverrides sync.Map

// OverrideDiskUsageForTesting supplies a deterministic filesystem snapshot for providers below path.
func OverrideDiskUsageForTesting(path string, total, used uint64) func() {
	path = filepath.Clean(path)
	diskUsageTestOverrides.Store(path, diskUsageSnapshot{total: total, used: used})
	return func() { diskUsageTestOverrides.Delete(path) }
}

func diskUsageForProvider(path string) diskUsageFunc {
	for current := filepath.Clean(path); ; current = filepath.Dir(current) {
		if value, ok := diskUsageTestOverrides.Load(current); ok {
			snapshot := value.(diskUsageSnapshot)
			return func(actualPath string) (*disk.UsageStat, error) {
				return &disk.UsageStat{Path: actualPath, Total: snapshot.total, Used: snapshot.used}, nil
			}
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
	}
	return disk.Usage
}

type provider struct {
	mu                sync.RWMutex
	capacityMu        sync.Mutex
	operationMu       sync.RWMutex
	batchMu           sync.Mutex
	batchCond         *sync.Cond
	batchWake         chan struct{}
	pending           []*pendingWrite
	pendingBytes      uint64
	flushing          bool
	items             map[string]cacheItem
	groups            map[string]map[string]struct{}
	variantMappings   map[string]map[string]struct{}
	itemGroups        map[string]map[string]struct{}
	expirations       expirationHeap
	lru               list.List
	index             *bolt.DB
	dirtyItems        map[string]struct{}
	dirtyGroups       map[string]struct{}
	path              string
	size              int
	stale             time.Duration
	limits            limits
	logger            core.Logger
	diskUsage         diskUsageFunc
	hits              atomic.Uint64
	misses            atomic.Uint64
	staleHits         atomic.Uint64
	evictions         atomic.Uint64
	rejections        atomic.Uint64
	corruptions       atomic.Uint64
	indexWrites       atomic.Uint64
	cacheUsed         atomic.Uint64
	physicalUsed      atomic.Uint64
	bodyEntries       atomic.Uint64
	mappingEntries    atomic.Uint64
	expirationEntries atomic.Uint64
	queueDepth        atomic.Uint64
	queueBytes        atomic.Uint64
	queueDepthMax     atomic.Uint64
	queueBytesMax     atomic.Uint64
	queueRejections   atomic.Uint64
	writeBatches      atomic.Uint64
	objectsCommitted  atomic.Uint64
	commitNanos       atomic.Uint64
	inflightWrites    atomic.Uint64
	nextVersion       uint64
	refs              int
}

func newProvider(config core.Config, logger core.Logger, stale time.Duration) (*provider, error) {
	provider, err := buildProvider(config, logger, stale)
	if err != nil {
		return nil, err
	}
	if err = prepareCacheDirectory(provider.path); err != nil {
		return nil, err
	}
	if err = provider.loadIndex(); err != nil {
		return nil, err
	}
	return provider, nil
}

func prepareCacheDirectory(path string) error {
	marker := filepath.Join(path, formatMarkerName)
	if value, err := os.ReadFile(marker); err == nil {
		if string(value) == formatMarkerValue {
			return nil
		}
		return fmt.Errorf("unsupported cache directory format marker %q", strings.TrimSpace(string(value)))
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		return atomicWriteFile(marker, []byte(formatMarkerValue), 0o640)
	}
	discarded := filepath.Join(filepath.Dir(path), ".goveto-cache-discarded-"+filepath.Base(path)+"-"+strconv.FormatInt(time.Now().UnixNano(), 10))
	if err = os.Rename(path, discarded); err != nil {
		return fmt.Errorf("isolate legacy cache directory: %w", err)
	}
	if err = os.Mkdir(path, 0o750); err != nil {
		_ = os.Rename(discarded, path)
		return err
	}
	if err = atomicWriteFile(marker, []byte(formatMarkerValue), 0o640); err != nil {
		_ = os.Remove(path)
		_ = os.Rename(discarded, path)
		return err
	}
	if err = os.RemoveAll(discarded); err != nil {
		return fmt.Errorf("remove isolated legacy cache directory: %w", err)
	}
	return nil
}

func buildProvider(config core.Config, logger core.Logger, stale time.Duration) (*provider, error) {
	path := config.Path
	configured := limits{
		auto:                config.AutoMaxSize,
		maxBytes:            config.MaxSizeBytes,
		maxDiskUsagePercent: normalizePercent(config.MaxDiskUsagePercent),
	}
	if config.MaxDiskUsagePercent == 0 {
		configured.maxDiskUsagePercent = 80
	}
	size := 0
	if raw, ok := config.Configuration.(map[string]any); ok {
		path = stringValue(raw["path"], path)
		size = intValue(raw["size"], 0)
		configured.auto = boolValue(raw["auto_max_size"], true)
		configured.maxBytes = uint64Value(raw["max_size_bytes"], 0)
		configured.maxDiskUsagePercent = normalizePercent(intValue(raw["max_disk_usage_percent"], 80))
	}
	if path == "" {
		var err error
		path, err = os.Getwd()
		if err != nil {
			return nil, err
		}
	}
	if err := os.MkdirAll(path, 0o750); err != nil {
		return nil, err
	}
	provider := &provider{
		items:           map[string]cacheItem{},
		groups:          map[string]map[string]struct{}{},
		variantMappings: map[string]map[string]struct{}{},
		itemGroups:      map[string]map[string]struct{}{},
		batchWake:       make(chan struct{}, 1),
		dirtyItems:      map[string]struct{}{},
		dirtyGroups:     map[string]struct{}{},
		path:            path,
		size:            size,
		stale:           stale,
		limits:          configured,
		logger:          logger,
		diskUsage:       diskUsageForProvider(path),
	}
	provider.batchCond = sync.NewCond(&provider.batchMu)
	heap.Init(&provider.expirations)
	return provider, nil
}

func (p *provider) Name() string { return "goveto-disk" }
func (p *provider) Uuid() string { return fmt.Sprintf("%s-%d", p.path, p.size) }
func (p *provider) Init() error  { return nil }

func (p *provider) MapKeys(prefix string) map[string]string {
	now := time.Now()
	p.pruneExpired(now)
	p.mu.RLock()
	defer p.mu.RUnlock()
	result := map[string]string{}
	for key, item := range p.items {
		if (item.expiresAt.IsZero() || item.expiresAt.After(now)) && strings.HasPrefix(key, prefix) {
			result[strings.TrimPrefix(key, prefix)] = string(item.value)
		}
	}
	return result
}

func (p *provider) ListKeys() []string {
	now := time.Now()
	p.pruneExpired(now)
	p.mu.RLock()
	defer p.mu.RUnlock()
	result := make([]string, 0, len(p.items))
	for key, item := range p.items {
		if item.expiresAt.IsZero() || item.expiresAt.After(now) {
			result = append(result, key)
		}
	}
	sort.Strings(result)
	return result
}

func (p *provider) Get(key string) []byte {
	value, _ := p.get(key)
	return value
}

func (p *provider) get(key string) ([]byte, uint64) {
	now := time.Now()
	p.mu.RLock()
	item, ok := p.items[key]
	if !ok {
		p.mu.RUnlock()
		return nil, 0
	}
	if !item.expiresAt.IsZero() && !item.expiresAt.After(now) {
		p.mu.RUnlock()
		p.removeExpired(key, item.generation, now)
		return nil, 0
	}
	if !item.file {
		p.mu.RUnlock()
		p.touchItem(key, item.generation, now, 0)
		return item.value, 0
	}
	file, err := os.Open(string(item.value))
	p.mu.RUnlock()
	if err != nil {
		p.removeCorrupt(key, item.generation)
		return nil, 0
	}
	value, modifiedAt, err := readCachedObjectFile(file, item.compressedSize, item.checksum, item.modifiedAt)
	_ = file.Close()
	if err != nil {
		p.removeCorrupt(key, item.generation)
		return nil, 0
	}
	p.touchItem(key, item.generation, now, modifiedAt)
	return value, item.originalSize
}

func (p *provider) touchItem(key string, generation uint64, now time.Time, modifiedAt int64) {
	p.mu.RLock()
	item, ok := p.items[key]
	needsUpdate := ok && item.generation == generation && (now.Sub(item.lastAccess) >= time.Second || (modifiedAt != 0 && modifiedAt != item.modifiedAt))
	p.mu.RUnlock()
	if !needsUpdate || !p.mu.TryLock() {
		return
	}
	defer p.mu.Unlock()
	item, ok = p.items[key]
	if !ok || item.generation != generation {
		return
	}
	if now.Sub(item.lastAccess) >= time.Second {
		item.lastAccess = now
		if item.lru != nil {
			p.lru.MoveToBack(item.lru)
		}
	}
	if modifiedAt != 0 {
		item.modifiedAt = modifiedAt
	}
	p.items[key] = item
	p.dirtyItems[key] = struct{}{}
}

func (p *provider) removeExpired(key string, generation uint64, now time.Time) {
	p.deleteItemIf(key, func(item cacheItem) bool {
		return item.generation == generation && !item.expiresAt.IsZero() && !item.expiresAt.After(now)
	})
}

func (p *provider) removeCorrupt(key string, generation uint64) {
	p.deleteItemIf(key, func(item cacheItem) bool { return item.generation == generation })
	p.corruptions.Add(1)
}

func (p *provider) removeInvalidMapping(key string, value []byte) {
	p.deleteItemIf(key, func(item cacheItem) bool { return !item.file && bytes.Equal(item.value, value) })
}

func (p *provider) deleteItemIf(key string, match func(cacheItem) bool) {
	p.operationMu.RLock()
	defer p.operationMu.RUnlock()
	p.capacityMu.Lock()
	defer p.capacityMu.Unlock()
	p.mu.Lock()
	item, ok := p.items[key]
	if !ok || !match(item) {
		p.mu.Unlock()
		return
	}
	state := newBatchState(p)
	state.deleteItem(p, key)
	err := p.persistBatchLocked(state)
	if err == nil {
		p.applyBatchLocked(state)
	}
	p.mu.Unlock()
	if err == nil {
		removeFiles(state.obsoleteFiles)
	}
}

func (p *provider) pruneExpired(now time.Time) {
	p.operationMu.RLock()
	defer p.operationMu.RUnlock()
	p.capacityMu.Lock()
	defer p.capacityMu.Unlock()
	p.mu.Lock()
	state := newBatchState(p)
	state.stageExpired(p, now)
	var err error
	if len(state.items) > 0 {
		err = p.persistBatchLocked(state)
		if err == nil {
			p.applyBatchLocked(state)
		} else {
			state.restoreExpiry(p)
		}
	}
	p.mu.Unlock()
	if err == nil {
		removeFiles(state.obsoleteFiles)
	}
}

func (p *provider) GetMultiLevel(key string, request *http.Request, validator *core.Revalidator) (*http.Response, *http.Response) {
	fresh, stale, _, _ := p.getMultiLevel(key, request, validator)
	return fresh, stale
}

func (p *provider) getMultiLevel(key string, request *http.Request, validator *core.Revalidator) (*http.Response, *http.Response, *core.KeyIndex, string) {
	mappingKey := core.MappingKeyPrefix + key
	decoded, err := p.getDecodedMapping(mappingKey)
	if err != nil || decoded == nil || decoded.GetMapping() == nil {
		if err != nil {
			p.corruptions.Add(1)
		}
		p.misses.Add(1)
		return nil, nil, nil, ""
	}
	fresh, stale, index, storageKey, _ := p.mappingElection(decoded, request, validator)
	if fresh == nil && stale == nil {
		p.misses.Add(1)
	} else {
		p.hits.Add(1)
		if fresh == nil {
			p.staleHits.Add(1)
		}
	}
	return fresh, stale, index, storageKey
}

func (p *provider) getDecodedMapping(key string) (*core.StorageMapper, error) {
	now := time.Now()
	p.mu.RLock()
	item, ok := p.items[key]
	if !ok || item.file {
		p.mu.RUnlock()
		return nil, nil
	}
	if !item.expiresAt.IsZero() && !item.expiresAt.After(now) {
		p.mu.RUnlock()
		p.removeExpired(key, item.generation, now)
		return nil, nil
	}
	mapping := item.mapping
	value := item.value
	generation := item.generation
	p.mu.RUnlock()
	if mapping == nil {
		decoded, err := decodeMapping(value)
		if err != nil {
			p.removeInvalidMapping(key, value)
			return nil, err
		}
		mapping = decoded
		if p.mu.TryLock() {
			current, exists := p.items[key]
			if exists && current.generation == generation && current.mapping == nil {
				current.mapping = mapping
				p.items[key] = current
			}
			p.mu.Unlock()
		}
	}
	p.touchItem(key, generation, now, 0)
	return mapping, nil
}

func (p *provider) mappingElection(mapping *core.StorageMapper, request *http.Request, validator *core.Revalidator) (fresh, stale *http.Response, selected *core.KeyIndex, storageKey string, err error) {
	for key, index := range mapping.GetMapping() {
		matched := true
		for name, values := range index.GetVariedHeaders() {
			if request.Header.Get(name) != strings.Join(values.GetHeaderValue(), ", ") {
				matched = false
				break
			}
		}
		if !matched {
			continue
		}

		core.ValidateETagFromHeader(index.GetEtag(), validator)
		if !validator.Matched {
			continue
		}
		if time.Until(index.GetFreshTime()) > 0 {
			fresh, err = p.readHTTPResponse(key, request)
			if fresh != nil || err != nil {
				return fresh, nil, index, key, err
			}
		}
		if time.Until(index.GetStaleTime()) > 0 {
			stale, err = p.readHTTPResponse(key, request)
			if stale != nil || err != nil {
				return nil, stale, index, key, err
			}
		}
	}
	return nil, nil, nil, "", nil
}

func (p *provider) readHTTPResponse(key string, request *http.Request) (*http.Response, error) {
	file, item, err := p.openCachedObject(key)
	if err != nil {
		p.removeCorrupt(key, item.generation)
		return nil, nil
	}
	if file == nil {
		return nil, nil
	}
	state := item.object
	if state == nil {
		state = new(cachedObjectState)
		p.mu.Lock()
		current, exists := p.items[key]
		if exists && current.generation == item.generation {
			if current.object == nil {
				current.object = state
				p.items[key] = current
			} else {
				state = current.object
			}
		}
		p.mu.Unlock()
	}
	state.once.Do(func() { state.load(file, item.compressedSize) })
	if state.err != nil {
		_ = file.Close()
		p.removeCorrupt(key, item.generation)
		return nil, state.err
	}
	template := &state.response
	response := &http.Response{
		Status: template.status, StatusCode: template.statusCode, Proto: template.proto,
		ProtoMajor: template.protoMajor, ProtoMinor: template.protoMinor,
		Header: template.header.Clone(), ContentLength: template.contentLength, Request: request,
	}
	if response.ContentLength == 0 || request.Method == http.MethodHead {
		_ = file.Close()
		response.Body = http.NoBody
		response.Header.Del("X-Goveto-Cache-Method")
		return response, nil
	}
	object := newObjectReaderFromLayout(file, state.layout)
	if _, err = object.Seek(template.bodyStart, io.SeekStart); err != nil {
		_ = file.Close()
		return nil, err
	}
	response.Body = &pooledResponseBody{
		body: object, remaining: response.ContentLength, file: file, object: object,
		bodyStart: template.bodyStart,
		onError:   func() { p.removeCorrupt(key, item.generation) },
	}
	if requested, ok := cacherange.FromContext(request.Context()); ok {
		if err = applyCachedRange(response, requested); err != nil {
			_ = response.Body.Close()
			return nil, err
		}
	}
	return response, nil
}

func (state *cachedObjectState) load(file *os.File, encodedSize uint64) {
	layout, err := readObjectLayout(file, encodedSize)
	if err != nil {
		state.err = err
		return
	}
	object := newObjectReaderFromLayout(file, layout)
	defer object.release()
	decoder := acquireResponseDecoder(object)
	defer releaseResponseDecoder(decoder)
	response, err := http.ReadResponse(decoder.buffered, &http.Request{Method: http.MethodGet})
	if err != nil {
		state.err = err
		return
	}
	if response.ContentLength < 0 {
		state.err = errors.New("cached response has no fixed content length")
		return
	}
	state.layout = layout
	state.response = cachedResponseTemplate{
		status: response.Status, statusCode: response.StatusCode, proto: response.Proto,
		protoMajor: response.ProtoMajor, protoMinor: response.ProtoMinor,
		header: response.Header.Clone(), contentLength: response.ContentLength,
		bodyStart: object.pos - int64(decoder.buffered.Buffered()),
	}
}

func (p *provider) openCachedObject(key string) (*os.File, cacheItem, error) {
	now := time.Now()
	p.mu.RLock()
	item, ok := p.items[key]
	if !ok || !item.file {
		p.mu.RUnlock()
		return nil, cacheItem{}, nil
	}
	if !item.expiresAt.IsZero() && !item.expiresAt.After(now) {
		p.mu.RUnlock()
		p.removeExpired(key, item.generation, now)
		return nil, item, nil
	}
	file, err := os.Open(string(item.value))
	p.mu.RUnlock()
	if err != nil {
		return nil, item, err
	}
	p.touchItem(key, item.generation, now, item.modifiedAt)
	return file, item, nil
}

func applyCachedRange(response *http.Response, requested cacherange.Spec) error {
	bodyStart, bodyEnd, total, ok := cachedBodyRange(response)
	if !ok {
		return nil
	}
	start := requested.Start
	end := requested.End
	if requested.SuffixLength > 0 {
		// Suffix range (bytes=-N) resolves against the total length: the
		// final SuffixLength bytes, or the entire representation when it is
		// smaller than the requested suffix.
		if requested.SuffixLength >= total {
			start = 0
		} else {
			start = total - requested.SuffixLength
		}
		end = total - 1
	}
	if start >= total {
		_ = response.Body.Close()
		response.Body = http.NoBody
		response.StatusCode = http.StatusRequestedRangeNotSatisfiable
		response.Status = "416 Requested Range Not Satisfiable"
		response.ContentLength = 0
		response.Header.Set("Accept-Ranges", "bytes")
		response.Header.Set("Content-Range", "bytes */"+strconv.FormatUint(total, 10))
		response.Header.Set("Content-Length", "0")
		return nil
	}
	end = min(end, total-1)
	if start < bodyStart || start > bodyEnd || end > bodyEnd {
		return nil
	}
	length := end - start + 1
	skip := start - bodyStart
	if pooled, ok := response.Body.(*pooledResponseBody); ok && pooled.object != nil {
		if err := pooled.seekBody(skip); err != nil {
			return err
		}
		skip = 0
	}
	response.Body = &rangedResponseBody{body: response.Body, skip: skip, remaining: length}
	response.StatusCode = http.StatusPartialContent
	response.Status = "206 Partial Content"
	response.ContentLength = int64(length)
	response.Header.Set("Accept-Ranges", "bytes")
	response.Header.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, total))
	response.Header.Set("Content-Length", strconv.FormatUint(length, 10))
	return nil
}

func cachedBodyRange(response *http.Response) (start, end, total uint64, ok bool) {
	if response.StatusCode == http.StatusPartialContent {
		value := strings.TrimSpace(response.Header.Get("Content-Range"))
		unit, interval, found := strings.Cut(value, " ")
		bounds, totalText, foundTotal := strings.Cut(interval, "/")
		startText, endText, foundBounds := strings.Cut(bounds, "-")
		if !found || !foundTotal || !foundBounds || !strings.EqualFold(unit, "bytes") || totalText == "*" {
			return 0, 0, 0, false
		}
		start, _ = strconv.ParseUint(startText, 10, 64)
		end, _ = strconv.ParseUint(endText, 10, 64)
		total, _ = strconv.ParseUint(totalText, 10, 64)
		return start, end, total, total > 0 && start <= end && end < total
	}
	length := response.Header.Get("Content-Length")
	total, err := strconv.ParseUint(length, 10, 64)
	if err != nil || total == 0 {
		return 0, 0, 0, false
	}
	return 0, total - 1, total, true
}

func (p *provider) SetMultiLevel(baseKey, variedKey string, value []byte, variedHeaders http.Header, etag string, duration time.Duration, realKey string) error {
	p.operationMu.RLock()
	defer p.operationMu.RUnlock()
	status, err := cachedResponseStatus(value)
	if err != nil {
		p.corruptions.Add(1)
		return fmt.Errorf("reject malformed cache response: %w", err)
	}
	if status == http.StatusNotModified {
		return ErrUncacheable
	}
	if err := validateCachedResponse(value); err != nil {
		p.corruptions.Add(1)
		return fmt.Errorf("reject incomplete cache response: %w", err)
	}
	if cacheResponseHasDirective(value, "private", "no-store", "no-cache", "must-revalidate") {
		return ErrUncacheable
	}
	temporaryTarget := filepath.Join(p.path, bodyFileName(variedKey))
	temporaryPath, compressedSize, checksum, err := writeCompressedTemporary(temporaryTarget, value)
	if err != nil {
		return err
	}
	defer os.Remove(temporaryPath)
	physicalSize := physicalFileSize(temporaryPath, compressedSize)
	write := &pendingWrite{
		baseKey: baseKey, variedKey: variedKey, temporaryPath: temporaryPath,
		finalPath:      filepath.Join(p.path, contentBodyFileName(variedKey, checksum)),
		compressedSize: compressedSize, physicalSize: physicalSize, originalSize: uint64(len(value)), checksum: checksum,
		groups: surrogateGroups(value), variedHeaders: variedHeaders.Clone(), etag: etag,
		duration: duration, realKey: realKey, done: make(chan error, 1),
	}
	return p.enqueueWrite(write)
}

func (p *provider) SetMultiLevelStream(baseKey, variedKey string, source io.Reader, originalSize uint64, groups []string, variedHeaders http.Header, etag string, duration time.Duration, realKey string) error {
	p.operationMu.RLock()
	defer p.operationMu.RUnlock()
	temporaryTarget := filepath.Join(p.path, bodyFileName(variedKey))
	temporaryPath, encodedSize, err := writeObjectReader(temporaryTarget, source, originalSize)
	if err != nil {
		return err
	}
	defer os.Remove(temporaryPath)
	checksum, err := checksumFile(temporaryPath)
	if err != nil {
		return err
	}
	write := &pendingWrite{
		baseKey: baseKey, variedKey: variedKey, temporaryPath: temporaryPath,
		finalPath:      filepath.Join(p.path, contentBodyFileName(variedKey, checksum)),
		compressedSize: encodedSize, physicalSize: physicalFileSize(temporaryPath, encodedSize),
		originalSize: originalSize, checksum: checksum, groups: append([]string(nil), groups...),
		variedHeaders: variedHeaders.Clone(), etag: etag, duration: duration, realKey: realKey, done: make(chan error, 1),
	}
	return p.enqueueWrite(write)
}

func (p *provider) Refresh(baseKey string, request *http.Request, duration time.Duration, update http.Header) bool {
	p.operationMu.RLock()
	defer p.operationMu.RUnlock()
	p.mu.Lock()
	defer p.mu.Unlock()
	mappingKey := core.MappingKeyPrefix + baseKey
	mappingItem, ok := p.items[mappingKey]
	if !ok || mappingItem.file {
		return false
	}
	mapping, err := core.DecodeMapping(mappingItem.value)
	if err != nil {
		return false
	}
	var variedKey string
	for key, index := range mapping.GetMapping() {
		matched := true
		for name, values := range index.GetVariedHeaders() {
			if request.Header.Get(name) != strings.Join(values.GetHeaderValue(), ", ") {
				matched = false
				break
			}
		}
		if matched {
			variedKey = key
			now := time.Now()
			index.StoredAt = now
			index.FreshTime = now.Add(duration)
			index.StaleTime = now.Add(duration + p.stale)
			if index.Revalidated == nil {
				index.Revalidated = http.Header{}
			}
			for _, name := range []string{"Cache-Control", "Content-Location", "Date", "ETag", "Expires", "Last-Modified", "Vary"} {
				if values, exists := update[name]; exists {
					index.Revalidated[name] = append([]string(nil), values...)
				}
			}
			if etag := index.Revalidated.Get("ETag"); etag != "" {
				index.ETag = etag
			}
			break
		}
	}
	if variedKey == "" {
		return false
	}
	body, ok := p.items[variedKey]
	if !ok || !body.file {
		return false
	}
	encoded, err := core.EncodeMapping(mapping)
	if err != nil {
		return false
	}
	now := time.Now()
	expiresAt := now.Add(duration + p.stale)
	state := newBatchState(p)
	p.nextVersion++
	mappingItem.value = encoded
	mappingItem.mapping = mapping
	mappingItem.expiresAt = expiresAt
	mappingItem.lastAccess = now
	mappingItem.generation = p.nextVersion
	state.stageMapping(p, mappingKey, &mappingItem)
	p.nextVersion++
	body.expiresAt = expiresAt
	body.lastAccess = now
	body.generation = p.nextVersion
	state.items[variedKey] = stagedItem{item: body, present: true}
	if err = p.persistBatchLocked(state); err != nil {
		return false
	}
	p.applyBatchLocked(state)
	return true
}

func (p *provider) Set(key string, value []byte, duration time.Duration) error {
	p.operationMu.RLock()
	defer p.operationMu.RUnlock()
	p.mu.Lock()
	state := newBatchState(p)
	now := time.Now()
	if old, ok := state.getItem(p, key); ok && old.file {
		state.used -= min(state.used, old.accountedSize)
		state.physical -= min(state.physical, old.physicalSize)
		state.obsoleteFiles[string(old.value)] = struct{}{}
	}
	expiresAt := time.Time{}
	if duration > 0 {
		expiresAt = now.Add(duration)
	}
	p.nextVersion++
	item := cacheItem{value: value, expiresAt: expiresAt, lastAccess: now, generation: p.nextVersion}
	if strings.HasPrefix(key, core.MappingKeyPrefix) {
		state.stageMapping(p, key, &item)
	} else {
		if old, ok := state.getItem(p, key); ok {
			state.used -= min(state.used, old.accountedSize)
			state.physical -= min(state.physical, old.physicalSize)
		}
		item.accountedSize = accountedItemSize(key, item)
		state.used += item.accountedSize
		state.items[key] = stagedItem{item: item, present: true}
	}
	err := p.persistBatchLocked(state)
	if err == nil {
		p.applyBatchLocked(state)
	}
	p.mu.Unlock()
	if err == nil {
		for path := range state.obsoleteFiles {
			_ = os.Remove(path)
		}
	}
	return err
}

func (p *provider) Delete(key string) {
	p.operationMu.RLock()
	defer p.operationMu.RUnlock()
	p.mu.Lock()
	state := newBatchState(p)
	state.deleteItem(p, key)
	committed := len(state.items) == 0
	if len(state.items) > 0 && p.persistBatchLocked(state) == nil {
		p.applyBatchLocked(state)
		committed = true
	}
	p.mu.Unlock()
	if committed {
		for path := range state.obsoleteFiles {
			_ = os.Remove(path)
		}
	}
}

func (p *provider) DeleteMany(expression string) {
	matcher, err := regexp.Compile(expression)
	if err != nil {
		return
	}
	p.operationMu.RLock()
	defer p.operationMu.RUnlock()
	p.mu.Lock()
	state := newBatchState(p)
	for key := range p.items {
		if matcher.MatchString(key) || strings.HasPrefix(key, expression) {
			state.deleteItem(p, key)
		}
	}
	committed := len(state.items) == 0
	if len(state.items) > 0 && p.persistBatchLocked(state) == nil {
		p.applyBatchLocked(state)
		committed = true
	}
	p.mu.Unlock()
	if committed {
		for path := range state.obsoleteFiles {
			_ = os.Remove(path)
		}
	}
}

func (p *provider) Reset() error {
	p.operationMu.Lock()
	defer p.operationMu.Unlock()
	p.drain()
	p.capacityMu.Lock()
	defer p.capacityMu.Unlock()
	p.mu.Lock()
	obsolete, err := p.resetLocked()
	p.mu.Unlock()
	if err == nil {
		removeFiles(obsolete)
	}
	providers.Delete(p.path)
	return err
}

// Purge removes cached objects from one site-scoped provider.
func Purge(path, purgeType string, hosts, values []string) (int, error) {
	switch purgeType {
	case "ALL":
		if len(values) != 0 {
			return 0, errors.New("ALL purge does not accept values")
		}
	case "URL", "PREFIX", "TAG":
		if len(values) == 0 {
			return 0, errors.New("purge values are required")
		}
	default:
		return 0, fmt.Errorf("unsupported purge type %q", purgeType)
	}
	value, ok := providers.Load(path)
	if !ok {
		providers.Range(func(_, candidateValue any) bool {
			candidate := candidateValue.(*provider)
			if samePath(candidate.path, path) {
				value, ok = candidate, true
				return false
			}
			return true
		})
	}
	if !ok {
		return 0, fmt.Errorf("cache provider %q is not active", path)
	}
	provider := value.(*provider)
	provider.operationMu.Lock()
	defer provider.operationMu.Unlock()
	provider.drain()
	provider.capacityMu.Lock()
	defer provider.capacityMu.Unlock()
	provider.mu.Lock()
	if purgeType == "ALL" {
		count := int(provider.bodyEntries.Load())
		obsolete, resetErr := provider.resetLocked()
		provider.mu.Unlock()
		if resetErr == nil {
			removeFiles(obsolete)
		}
		return count, resetErr
	}
	var keys []string
	if purgeType == "TAG" {
		seen := map[string]struct{}{}
		for _, group := range values {
			for key := range provider.groups[group] {
				if _, ok := seen[key]; !ok {
					seen[key] = struct{}{}
					keys = append(keys, key)
				}
			}
		}
	} else {
		markers, markerErr := purgeMarkers(hosts, values)
		if markerErr != nil {
			provider.mu.Unlock()
			return 0, markerErr
		}
		for key := range provider.items {
			for _, marker := range markers {
				index := strings.Index(key, marker)
				if index < 0 || (index > 0 && key[index-1] != '-') {
					continue
				}
				remainder := key[index+len(marker):]
				if purgeType == "PREFIX" || remainder == "" || strings.HasPrefix(remainder, "-") {
					keys = append(keys, key)
					break
				}
			}
		}
		for mappingKey, item := range provider.items {
			if item.file || !strings.HasPrefix(mappingKey, core.MappingKeyPrefix) {
				continue
			}
			mapping, decodeErr := core.DecodeMapping(item.value)
			if decodeErr != nil {
				continue
			}
			for variedKey, index := range mapping.GetMapping() {
				for _, marker := range markers {
					if purgeKeyMatches(index.GetRealKey(), marker, purgeType) {
						keys = append(keys, variedKey)
						break
					}
				}
			}
		}
	}
	count, obsolete, err := provider.deleteKeysLocked(keys)
	provider.mu.Unlock()
	if err == nil {
		removeFiles(obsolete)
	}
	return count, err
}

func purgeKeyMatches(key, marker, purgeType string) bool {
	index := strings.Index(key, marker)
	if index < 0 || (index > 0 && key[index-1] != '-') {
		return false
	}
	remainder := key[index+len(marker):]
	return purgeType == "PREFIX" || remainder == "" || strings.HasPrefix(remainder, "-")
}

func (p *provider) deleteKeysLocked(keys []string) (int, map[string]struct{}, error) {
	state := newBatchState(p)
	count := 0
	for _, key := range keys {
		if item, ok := state.getItem(p, key); ok {
			if item.file {
				count++
			}
			state.deleteItem(p, key)
		}
	}
	if len(state.items) == 0 {
		return count, nil, nil
	}
	if err := p.persistBatchLocked(state); err != nil {
		return 0, nil, err
	}
	p.applyBatchLocked(state)
	return count, state.obsoleteFiles, nil
}

func (p *provider) resetLocked() (map[string]struct{}, error) {
	obsolete := make(map[string]struct{}, int(p.bodyEntries.Load()))
	for _, item := range p.items {
		if item.file {
			obsolete[string(item.value)] = struct{}{}
		}
	}
	if err := p.replaceWithEmptyIndexLocked(); err != nil {
		return nil, err
	}
	p.items = map[string]cacheItem{}
	p.groups = map[string]map[string]struct{}{}
	p.variantMappings = map[string]map[string]struct{}{}
	p.itemGroups = map[string]map[string]struct{}{}
	p.expirations = nil
	heap.Init(&p.expirations)
	p.lru.Init()
	p.dirtyItems = map[string]struct{}{}
	p.dirtyGroups = map[string]struct{}{}
	p.cacheUsed.Store(0)
	p.physicalUsed.Store(0)
	p.bodyEntries.Store(0)
	p.mappingEntries.Store(0)
	p.expirationEntries.Store(0)
	p.batchMu.Lock()
	p.pending = nil
	p.pendingBytes = 0
	p.batchMu.Unlock()
	return obsolete, nil
}

func removeFiles(paths map[string]struct{}) {
	for path := range paths {
		_ = os.Remove(path)
	}
}

func samePath(left, right string) bool {
	if filepath.Clean(left) == filepath.Clean(right) {
		return true
	}
	leftResolved, leftErr := filepath.EvalSymlinks(left)
	rightResolved, rightErr := filepath.EvalSymlinks(right)
	return leftErr == nil && rightErr == nil && leftResolved == rightResolved
}

func surrogateGroups(value []byte) []string {
	response, err := http.ReadResponse(bufio.NewReader(bytes.NewReader(value)), nil)
	if err != nil {
		return nil
	}
	if response.Body != nil {
		_ = response.Body.Close()
	}
	return strings.FieldsFunc(response.Header.Get("Surrogate-Key"), func(value rune) bool {
		return value == ' ' || value == ',' || value == '\t'
	})
}

func purgeMarkers(hosts, values []string) ([]string, error) {
	markers := make([]string, 0, len(hosts)*len(values))
	for _, value := range values {
		if strings.HasPrefix(value, "/") {
			for _, host := range hosts {
				markers = append(markers, host+"-"+value)
			}
			continue
		}
		candidate := value
		if !strings.Contains(candidate, "://") {
			candidate = "//" + candidate
		}
		parsed, err := url.Parse(candidate)
		if err != nil || parsed.Host == "" {
			return nil, fmt.Errorf("invalid purge URL %q", value)
		}
		requestURI := parsed.EscapedPath()
		if requestURI == "" {
			requestURI = "/"
		}
		if parsed.ForceQuery || parsed.RawQuery != "" {
			requestURI += "?" + parsed.RawQuery
		}
		markers = append(markers, strings.ToLower(parsed.Host)+"-"+requestURI)
	}
	return markers, nil
}

func (p *provider) ensureSpace(incoming uint64, configured limits) error {
	p.capacityMu.Lock()
	defer p.capacityMu.Unlock()
	return p.ensureSpaceLocked(incoming, configured)
}

func (p *provider) ensureSpaceLocked(incoming uint64, configured limits) error {
	return p.ensureSpaceLockedWithTransient(incoming, configured, 0)
}

func (p *provider) ensureSpaceLockedWithTransient(incoming uint64, configured limits, transient uint64) error {
	p.mu.Lock()
	state := newBatchState(p)
	state.stageExpired(p, time.Now())
	var pruneErr error
	if len(state.items) > 0 {
		pruneErr = p.persistBatchLocked(state)
		if pruneErr == nil {
			p.applyBatchLocked(state)
		} else {
			state.restoreExpiry(p)
		}
		p.recordRejectedWrite(pruneErr)
	}
	p.mu.Unlock()
	if pruneErr == nil {
		for path := range state.obsoleteFiles {
			_ = os.Remove(path)
		}
	}
	if pruneErr != nil {
		return pruneErr
	}
	budget, err := p.capacityAvailable(configured, transient)
	if err != nil {
		return err
	}
	if incoming > budget.accountedAvailable || incoming > budget.physicalAvailable {
		p.rejections.Add(1)
		return ErrCapacity
	}
	accountedRequired := uint64(0)
	physicalRequired := uint64(0)
	if budget.accountedUsed > budget.accountedAvailable-incoming {
		accountedRequired = budget.accountedUsed - (budget.accountedAvailable - incoming)
	}
	if budget.physicalUsed > budget.physicalAvailable-incoming {
		physicalRequired = budget.physicalUsed - (budget.physicalAvailable - incoming)
	}
	if accountedRequired == 0 && physicalRequired == 0 {
		return nil
	}
	freedAccounted, freedPhysical, err := p.evictForBudget(accountedRequired, physicalRequired)
	if err != nil {
		return err
	}
	if freedAccounted < accountedRequired || freedPhysical < physicalRequired {
		p.rejections.Add(1)
		return ErrCapacity
	}
	return nil
}

type capacityBudget struct {
	accountedUsed      uint64
	physicalUsed       uint64
	accountedAvailable uint64
	physicalAvailable  uint64
}

func (p *provider) capacityAvailable(configured limits, transient uint64) (capacityBudget, error) {
	usage, err := p.diskUsage(p.path)
	if err != nil {
		return capacityBudget{}, err
	}
	budget := capacityBudget{
		accountedUsed: p.cacheUsed.Load(), physicalUsed: p.physicalUsed.Load(),
		accountedAvailable: ^uint64(0),
	}
	target := usage.Total * uint64(normalizePercent(configured.maxDiskUsagePercent)) / 100
	nonCacheUsed := uint64(0)
	trackedAndTransient := budget.physicalUsed + transient
	if usage.Used > trackedAndTransient {
		nonCacheUsed = usage.Used - trackedAndTransient
	}
	if nonCacheUsed < target {
		budget.physicalAvailable = target - nonCacheUsed
	}
	if !configured.auto && configured.maxBytes > 0 {
		budget.accountedAvailable = configured.maxBytes
	}
	return budget, nil
}

func withinAutoLimit(total, used, incoming uint64, percent int) bool {
	target := total * uint64(normalizePercent(percent)) / 100
	return used <= target && incoming <= target-used
}

func (p *provider) evictOldest() bool {
	freed, err := p.evictBytes(1)
	return err == nil && freed > 0
}

func (p *provider) evictBytes(required uint64) (uint64, error) {
	freed, _, err := p.evictForBudget(required, 0)
	return freed, err
}

func (p *provider) evictForBudget(requiredAccounted, requiredPhysical uint64) (uint64, uint64, error) {
	if requiredAccounted == 0 && requiredPhysical == 0 {
		return 0, 0, nil
	}
	p.mu.Lock()
	state := newBatchState(p)
	var freedAccounted uint64
	var freedPhysical uint64
	for element := p.lru.Front(); element != nil && (freedAccounted < requiredAccounted || freedPhysical < requiredPhysical); element = element.Next() {
		key := element.Value.(string)
		item, ok := state.getItem(p, key)
		if !ok || !item.file {
			continue
		}
		freedAccounted += item.accountedSize
		freedPhysical += item.physicalSize
		state.deleteItem(p, key)
		state.evicted++
	}
	var err error
	if state.evicted > 0 {
		err = p.persistBatchLocked(state)
		if err == nil {
			p.applyBatchLocked(state)
		}
	}
	p.mu.Unlock()
	if err != nil {
		return 0, 0, err
	}
	for path := range state.obsoleteFiles {
		if removeErr := os.Remove(path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			return freedAccounted, freedPhysical, removeErr
		}
	}
	p.evictions.Add(state.evicted)
	return freedAccounted, freedPhysical, err
}

func (p *provider) deleteItemLocked(key string, item cacheItem) {
	p.forgetItemLocked(key, item)
	if item.file {
		_ = os.Remove(string(item.value))
	}
}

func (p *provider) forgetItemLocked(key string, item cacheItem) {
	delete(p.items, key)
	p.removeReverseMappingLocked(key, item)
	p.removeItemAccountingLocked(item)
	p.dirtyItems[key] = struct{}{}
	for group := range p.itemGroups[key] {
		keys := p.groups[group]
		delete(keys, key)
		p.dirtyGroups[group] = struct{}{}
		if len(keys) == 0 {
			delete(p.groups, group)
		}
	}
	delete(p.itemGroups, key)
	if item.accountedSize > 0 {
		p.cacheUsed.Add(^(item.accountedSize - 1))
	}
	if item.physicalSize > 0 {
		p.physicalUsed.Add(^(item.physicalSize - 1))
	}
}

func (p *provider) removeItemAccountingLocked(item cacheItem) {
	if item.lru != nil {
		p.lru.Remove(item.lru)
	}
	if item.expiration != nil && item.expiration.index >= 0 {
		heap.Remove(&p.expirations, item.expiration.index)
	}
	if item.file {
		atomicDecrement(&p.bodyEntries)
	} else {
		atomicDecrement(&p.mappingEntries)
	}
	p.expirationEntries.Store(uint64(len(p.expirations)))
}

func (p *provider) addItemAccountingLocked(item cacheItem) {
	if item.file {
		p.bodyEntries.Add(1)
	} else {
		p.mappingEntries.Add(1)
	}
}

func atomicDecrement(value *atomic.Uint64) {
	for current := value.Load(); current > 0 && !value.CompareAndSwap(current, current-1); current = value.Load() {
	}
}

func (p *provider) removeVariantFromMappingsLocked(variedKey string) {
	for key := range cloneSet(p.variantMappings[variedKey]) {
		item, ok := p.items[key]
		if !ok {
			continue
		}
		mapping, err := core.DecodeMapping(item.value)
		if err != nil || mapping.GetMapping() == nil {
			continue
		}
		if _, ok := mapping.Mapping[variedKey]; !ok {
			continue
		}
		delete(mapping.Mapping, variedKey)
		if len(mapping.Mapping) == 0 {
			p.deleteItemLocked(key, item)
			continue
		}
		value, marshalErr := core.EncodeMapping(mapping)
		if marshalErr != nil {
			p.deleteItemLocked(key, item)
			continue
		}
		p.removeReverseMappingLocked(key, item)
		item.value = value
		item.mapping = mapping
		p.nextVersion++
		item.generation = p.nextVersion
		p.items[key] = item
		p.addReverseMappingLocked(key, item)
		p.dirtyItems[key] = struct{}{}
	}
}

func (p *provider) repairMappingsLocked() bool {
	repaired := false
	for key, item := range p.items {
		if item.file || !strings.HasPrefix(key, core.MappingKeyPrefix) {
			continue
		}
		mapping, err := core.DecodeMapping(item.value)
		if err != nil || mapping.GetMapping() == nil {
			continue
		}
		changed := false
		for variedKey := range mapping.Mapping {
			body, ok := p.items[variedKey]
			if !ok || !body.file {
				delete(mapping.Mapping, variedKey)
				changed = true
			}
		}
		if !changed {
			continue
		}
		repaired = true
		if len(mapping.Mapping) == 0 {
			p.deleteItemLocked(key, item)
			continue
		}
		value, marshalErr := core.EncodeMapping(mapping)
		if marshalErr != nil {
			p.corruptions.Add(1)
			p.deleteItemLocked(key, item)
			continue
		}
		p.removeReverseMappingLocked(key, item)
		item.value = value
		item.mapping = mapping
		p.nextVersion++
		item.generation = p.nextVersion
		p.items[key] = item
		p.addReverseMappingLocked(key, item)
		p.dirtyItems[key] = struct{}{}
	}
	return repaired
}

func (p *provider) loadIndex() error {
	indexPath := filepath.Join(p.path, indexName)
	_, statErr := os.Stat(indexPath)
	newIndex := errors.Is(statErr, os.ErrNotExist)
	if statErr != nil && !newIndex {
		return statErr
	}
	if newIndex {
		_ = os.Remove(filepath.Join(p.path, oldIndexName))
		if err := p.removeOrphanFiles(nil); err != nil {
			return err
		}
	}
	if err := p.openIndex(indexPath); err != nil {
		p.corruptions.Add(1)
		if rebuildErr := p.rebuildIndex(indexPath); rebuildErr != nil {
			return errors.Join(err, rebuildErr)
		}
		return nil
	}

	now := time.Now()
	validFiles := map[string]struct{}{}
	repaired := false
	err := p.index.View(func(tx *bolt.Tx) error {
		items := tx.Bucket(indexItemsBucket)
		groups := tx.Bucket(indexGroupsBucket)
		if items == nil || groups == nil {
			return errors.New("cache metadata buckets are missing")
		}
		if err := items.ForEach(func(rawKey, rawValue []byte) error {
			key := string(rawKey)
			var item diskItem
			if json.Unmarshal(rawValue, &item) != nil {
				p.corruptions.Add(1)
				repaired = true
				p.dirtyItems[key] = struct{}{}
				return nil
			}
			if !item.ExpiresAt.IsZero() && !item.ExpiresAt.After(now) {
				repaired = true
				p.dirtyItems[key] = struct{}{}
				return nil
			}
			if item.File == "" {
				var decodedMapping *core.StorageMapper
				if strings.HasPrefix(key, core.MappingKeyPrefix) {
					var decodeErr error
					decodedMapping, decodeErr = core.DecodeMapping(item.Value)
					if decodeErr != nil {
						p.corruptions.Add(1)
						repaired = true
						p.dirtyItems[key] = struct{}{}
						return nil
					}
				}
				p.nextVersion++
				loaded := cacheItem{
					value: item.Value, expiresAt: item.ExpiresAt, lastAccess: item.LastAccess,
					generation: p.nextVersion, mapping: decodedMapping,
				}
				loaded.accountedSize = accountedItemSize(key, loaded)
				if !item.ExpiresAt.IsZero() {
					loaded.expiration = &expirationEntry{at: item.ExpiresAt, key: key, generation: p.nextVersion, index: -1}
					heap.Push(&p.expirations, loaded.expiration)
				}
				p.items[key] = loaded
				p.mappingEntries.Add(1)
				p.cacheUsed.Add(loaded.accountedSize)
				return nil
			}
			if filepath.Base(item.File) != item.File || item.File == indexName {
				p.corruptions.Add(1)
				repaired = true
				p.dirtyItems[key] = struct{}{}
				return nil
			}
			path := filepath.Join(p.path, item.File)
			compressed, originalSize, readErr := inspectCachedResponseFile(path)
			if readErr != nil {
				p.corruptions.Add(1)
				repaired = true
				p.dirtyItems[key] = struct{}{}
				_ = os.Remove(path)
				return nil
			}
			checksum := sha256.Sum256(compressed)
			if item.CompressedSize != uint64(len(compressed)) || item.OriginalSize != originalSize || !bytes.Equal(item.Checksum, checksum[:]) {
				repaired = true
				p.dirtyItems[key] = struct{}{}
			}
			validFiles[item.File] = struct{}{}
			p.nextVersion++
			modifiedAt := item.ModifiedAt
			physicalSize := roundUp4K(uint64(len(compressed)))
			if info, statErr := os.Stat(path); statErr == nil {
				modifiedAt = info.ModTime().UnixNano()
				physicalSize = physicalSizeFromInfo(info, uint64(len(compressed)))
			}
			loaded := cacheItem{
				value: []byte(path), file: true, object: new(cachedObjectState), expiresAt: item.ExpiresAt, lastAccess: item.LastAccess,
				generation: p.nextVersion, compressedSize: uint64(len(compressed)), physicalSize: physicalSize, originalSize: originalSize, checksum: checksum, modifiedAt: modifiedAt,
			}
			loaded.accountedSize = accountedItemSize(key, loaded)
			p.items[key] = loaded
			if !item.ExpiresAt.IsZero() {
				loaded.expiration = &expirationEntry{at: item.ExpiresAt, key: key, generation: p.nextVersion, index: -1}
				heap.Push(&p.expirations, loaded.expiration)
				p.items[key] = loaded
			}
			p.cacheUsed.Add(loaded.accountedSize)
			p.physicalUsed.Add(loaded.physicalSize)
			p.bodyEntries.Add(1)
			return nil
		}); err != nil {
			return err
		}
		return groups.ForEach(func(rawGroup, rawKeys []byte) error {
			group := string(rawGroup)
			var keys []string
			if json.Unmarshal(rawKeys, &keys) != nil {
				repaired = true
				p.dirtyGroups[group] = struct{}{}
				return nil
			}
			for _, key := range keys {
				if _, ok := p.items[key]; !ok {
					repaired = true
					p.dirtyGroups[group] = struct{}{}
					continue
				}
				if p.groups[group] == nil {
					p.groups[group] = map[string]struct{}{}
				}
				p.groups[group][key] = struct{}{}
				if p.itemGroups[key] == nil {
					p.itemGroups[key] = map[string]struct{}{}
				}
				p.itemGroups[key][group] = struct{}{}
			}
			return nil
		})
	})
	if err != nil {
		return err
	}
	p.expirationEntries.Store(uint64(len(p.expirations)))
	keysByAccess := make([]string, 0, len(p.items))
	for key, item := range p.items {
		if item.file {
			keysByAccess = append(keysByAccess, key)
		}
	}
	sort.Slice(keysByAccess, func(i, j int) bool {
		return p.items[keysByAccess[i]].lastAccess.Before(p.items[keysByAccess[j]].lastAccess)
	})
	for _, key := range keysByAccess {
		item := p.items[key]
		item.lru = p.lru.PushBack(key)
		p.items[key] = item
	}
	for key, item := range p.items {
		p.addReverseMappingLocked(key, item)
	}
	if p.repairMappingsLocked() {
		repaired = true
	}
	if err = p.removeOrphanFiles(validFiles); err != nil {
		return err
	}
	if repaired {
		return p.persistIndexLocked()
	}
	return nil
}

func (p *provider) persistIndexLocked() error {
	if p.index == nil {
		return errors.New("cache metadata database is not open")
	}
	p.indexWrites.Add(1)
	err := p.index.Update(func(tx *bolt.Tx) error {
		items := tx.Bucket(indexItemsBucket)
		groups := tx.Bucket(indexGroupsBucket)
		meta := tx.Bucket(indexMetaBucket)
		for key := range p.dirtyItems {
			item, ok := p.items[key]
			if !ok {
				if err := items.Delete([]byte(key)); err != nil {
					return err
				}
				continue
			}
			encoded, err := encodeDiskItem(item)
			if err != nil {
				return err
			}
			if err = items.Put([]byte(key), encoded); err != nil {
				return err
			}
		}
		for group := range p.dirtyGroups {
			keys, ok := p.groups[group]
			if !ok || len(keys) == 0 {
				if err := groups.Delete([]byte(group)); err != nil {
					return err
				}
				continue
			}
			values := make([]string, 0, len(keys))
			for key := range keys {
				values = append(values, key)
			}
			sort.Strings(values)
			encoded, err := json.Marshal(values)
			if err != nil {
				return err
			}
			if err = groups.Put([]byte(group), encoded); err != nil {
				return err
			}
		}
		var used [8]byte
		binary.BigEndian.PutUint64(used[:], p.cacheUsed.Load())
		return meta.Put(indexUsedBytesKey, used[:])
	})
	if err == nil {
		clear(p.dirtyItems)
		clear(p.dirtyGroups)
	}
	return err
}

func (p *provider) openIndex(path string) error {
	db, err := bolt.Open(path, 0o640, &bolt.Options{Timeout: time.Second, NoFreelistSync: true})
	if err != nil {
		return err
	}
	p.index = db
	if err = initializeIndexDB(db); err != nil {
		_ = db.Close()
		p.index = nil
	}
	return err
}

func initializeIndexDB(db *bolt.DB) error {
	return db.Update(func(tx *bolt.Tx) error {
		meta, err := tx.CreateBucketIfNotExists(indexMetaBucket)
		if err != nil {
			return err
		}
		version := meta.Get(indexVersionKey)
		if len(version) > 0 && (len(version) != 1 || version[0] != indexVersion) {
			return fmt.Errorf("unsupported cache metadata version %v", version)
		}
		if err = meta.Put(indexVersionKey, []byte{indexVersion}); err != nil {
			return err
		}
		if _, err = tx.CreateBucketIfNotExists(indexItemsBucket); err != nil {
			return err
		}
		_, err = tx.CreateBucketIfNotExists(indexGroupsBucket)
		return err
	})
}

func (p *provider) replaceWithEmptyIndexLocked() error {
	indexPath := filepath.Join(p.path, indexName)
	temporary, err := os.CreateTemp(p.path, ".goveto-cache-index-reset-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	if err = temporary.Close(); err != nil {
		_ = os.Remove(temporaryPath)
		return err
	}
	defer os.Remove(temporaryPath)
	empty, err := bolt.Open(temporaryPath, 0o640, &bolt.Options{Timeout: time.Second, NoFreelistSync: true})
	if err != nil {
		return err
	}
	if err = initializeIndexDB(empty); err == nil {
		err = empty.Sync()
	}
	if closeErr := empty.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}

	backupPath := indexPath + ".reset-backup"
	_ = os.Remove(backupPath)
	if p.index != nil {
		if err = p.index.Sync(); err != nil {
			return err
		}
		if err = p.index.Close(); err != nil {
			return err
		}
		p.index = nil
	}
	if err = os.Rename(indexPath, backupPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		_ = p.openIndex(indexPath)
		return err
	}
	restore := func(cause error) error {
		_ = os.Remove(indexPath)
		if restoreErr := os.Rename(backupPath, indexPath); restoreErr != nil && !errors.Is(restoreErr, os.ErrNotExist) {
			return errors.Join(cause, restoreErr)
		}
		return errors.Join(cause, p.openIndex(indexPath))
	}
	if err = os.Rename(temporaryPath, indexPath); err != nil {
		return restore(err)
	}
	if err = p.openIndex(indexPath); err != nil {
		return restore(err)
	}
	directory, openErr := os.Open(p.path)
	if openErr == nil {
		openErr = errors.Join(directory.Sync(), directory.Close())
	}
	if openErr != nil {
		p.closeIndex()
		return restore(openErr)
	}
	_ = os.Remove(backupPath)
	return nil
}

func (p *provider) rebuildIndex(path string) error {
	if p.index != nil {
		_ = p.index.Close()
		p.index = nil
	}
	if _, err := os.Stat(path); err == nil {
		backup := path + ".corrupt-" + strconv.FormatInt(time.Now().UnixNano(), 10)
		if err = os.Rename(path, backup); err != nil {
			return err
		}
	}
	if err := p.removeOrphanFiles(nil); err != nil {
		return err
	}
	return p.openIndex(path)
}

func (p *provider) closeIndex() {
	if p.index != nil {
		_ = p.index.Close()
		p.index = nil
	}
}

func (p *provider) removeOrphanFiles(valid map[string]struct{}) error {
	entries, err := os.ReadDir(p.path)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if isTemporaryCacheFile(entry.Name()) || strings.HasPrefix(entry.Name(), ".goveto-origin-") {
			if err = os.Remove(filepath.Join(p.path, entry.Name())); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			continue
		}
		if !isCacheData(entry.Name()) {
			continue
		}
		if _, ok := valid[entry.Name()]; ok {
			continue
		}
		if err = os.Remove(filepath.Join(p.path, entry.Name())); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func readCachedResponseFile(path string) ([]byte, error) {
	encoded, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if _, err = validateObjectResponse(bytes.NewReader(encoded), uint64(len(encoded))); err != nil {
		return nil, err
	}
	return encoded, nil
}

func inspectCachedResponseFile(path string) ([]byte, uint64, error) {
	encoded, err := os.ReadFile(path)
	if err != nil {
		return nil, 0, err
	}
	originalSize, err := validateObjectResponse(bytes.NewReader(encoded), uint64(len(encoded)))
	return encoded, originalSize, err
}

func validateObjectResponse(source io.ReaderAt, encodedSize uint64) (uint64, error) {
	reader, err := newObjectReader(source, encodedSize)
	if err != nil {
		return 0, err
	}
	counted := &countingReader{Reader: reader}
	if err = validateCachedResponseReader(counted); err != nil {
		return 0, err
	}
	return counted.Bytes, nil
}

type countingReader struct {
	io.Reader
	Bytes uint64
}

func (r *countingReader) Read(target []byte) (int, error) {
	count, err := r.Reader.Read(target)
	r.Bytes += uint64(count)
	return count, err
}

func readCachedObjectFile(file *os.File, expectedSize uint64, expectedChecksum [sha256.Size]byte, expectedModifiedAt int64) ([]byte, int64, error) {
	info, err := file.Stat()
	if err != nil {
		return nil, 0, err
	}
	if uint64(info.Size()) != expectedSize || expectedSize > uint64(^uint(0)>>1) {
		return nil, 0, errors.New("cache object size does not match metadata")
	}
	compressed := make([]byte, int(expectedSize))
	if _, err = io.ReadFull(file, compressed); err != nil {
		return nil, 0, err
	}
	var extra [1]byte
	if count, readErr := file.Read(extra[:]); count != 0 || (readErr != nil && !errors.Is(readErr, io.EOF)) {
		return nil, 0, errors.New("cache object contains unexpected trailing data")
	}
	modifiedAt := info.ModTime().UnixNano()
	if modifiedAt != expectedModifiedAt {
		if checksum := sha256.Sum256(compressed); checksum != expectedChecksum {
			return nil, 0, errors.New("cache object checksum does not match metadata")
		}
	}
	return compressed, modifiedAt, nil
}

func writeCompressedTemporary(target string, value []byte) (path string, size uint64, checksum [sha256.Size]byte, err error) {
	path, size, err = writeObject(target, value)
	if err != nil {
		return "", 0, checksum, err
	}
	checksum, err = checksumFile(path)
	if err != nil {
		_ = os.Remove(path)
		return "", 0, checksum, err
	}
	return path, size, checksum, nil
}

func validateCachedResponse(value []byte) error {
	return validateCachedResponseReader(bytes.NewReader(value))
}

func cachedResponseStatus(value []byte) (int, error) {
	response, err := http.ReadResponse(bufio.NewReader(bytes.NewReader(value)), nil)
	if err != nil {
		return 0, err
	}
	if response.Body != nil {
		_ = response.Body.Close()
	}
	return response.StatusCode, nil
}

func validateCachedResponseReader(source io.Reader) error {
	buffered := bufio.NewReader(source)
	response, err := http.ReadResponse(buffered, nil)
	if err != nil {
		return fmt.Errorf("read cached HTTP response: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotModified {
		return ErrUncacheable
	}
	noBody := response.Header.Get("X-Goveto-Cache-Method") == http.MethodHead ||
		response.StatusCode == http.StatusNoContent
	var bodyBytes int64
	if response.Body != nil && !noBody {
		bodyBytes, err = io.Copy(io.Discard, response.Body)
		if err != nil {
			return fmt.Errorf("read cached HTTP body: %w", err)
		}
	}
	expected := response.ContentLength
	if internal, parseErr := strconv.ParseInt(response.Header.Get("X-Goveto-Origin-Content-Length"), 10, 64); parseErr == nil {
		expected = internal
	}
	if !noBody && expected >= 0 && bodyBytes != expected {
		return io.ErrUnexpectedEOF
	}
	trailing, trailingErr := io.Copy(io.Discard, buffered)
	if trailingErr != nil && !errors.Is(trailingErr, io.EOF) {
		return fmt.Errorf("finish cached response stream: %w", trailingErr)
	}
	if trailing != 0 {
		return errors.New("cache response contains bytes beyond its declared body")
	}
	return nil
}

func cacheResponseHasDirective(value []byte, directives ...string) bool {
	response, err := http.ReadResponse(bufio.NewReader(bytes.NewReader(value)), nil)
	if err != nil {
		return true
	}
	if response.Body != nil {
		_ = response.Body.Close()
	}
	wanted := make(map[string]struct{}, len(directives))
	for _, directive := range directives {
		wanted[strings.ToLower(directive)] = struct{}{}
	}
	for _, part := range strings.Split(strings.Join(response.Header.Values("Cache-Control"), ","), ",") {
		name := strings.ToLower(strings.TrimSpace(strings.SplitN(part, "=", 2)[0]))
		if _, ok := wanted[name]; ok {
			return true
		}
	}
	return false
}

func atomicWriteFile(path string, data []byte, mode os.FileMode) (err error) {
	temporary, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer func() {
		_ = temporary.Close()
		_ = os.Remove(temporaryPath)
	}()
	if err = temporary.Chmod(mode); err != nil {
		return err
	}
	if _, err = temporary.Write(data); err != nil {
		return err
	}
	if err = temporary.Sync(); err != nil {
		return err
	}
	if err = temporary.Close(); err != nil {
		return err
	}
	if err = os.Rename(temporaryPath, path); err != nil {
		return err
	}
	if directory, openErr := os.Open(filepath.Dir(path)); openErr == nil {
		err = directory.Sync()
		_ = directory.Close()
	}
	return err
}

func bodyFileName(key string) string {
	return bodyPrefix + fmt.Sprintf("%x", sha256.Sum256([]byte(key)))
}

func (p *provider) recordRejectedWrite(err error) {
	if errors.Is(err, syscall.ENOSPC) {
		p.rejections.Add(1)
	}
}

func isCacheData(name string) bool {
	return strings.HasPrefix(name, bodyPrefix) && !isTemporaryCacheFile(name)
}

func isTemporaryCacheFile(name string) bool {
	return strings.Contains(name, ".tmp-")
}

func normalizePercent(value int) int {
	if value < 1 {
		return 80
	}
	if value > 90 {
		return 90
	}
	return value
}

func stringValue(value any, fallback string) string {
	if result, ok := value.(string); ok && result != "" {
		return result
	}
	return fallback
}

func intValue(value any, fallback int) int {
	switch typed := value.(type) {
	case int:
		return typed
	case float64:
		return int(typed)
	case string:
		parsed, err := strconv.Atoi(typed)
		if err == nil {
			return parsed
		}
	}
	return fallback
}

func uint64Value(value any, fallback uint64) uint64 {
	switch typed := value.(type) {
	case uint64:
		return typed
	case int:
		if typed >= 0 {
			return uint64(typed)
		}
	case float64:
		if typed >= 0 {
			return uint64(typed)
		}
	case string:
		parsed, err := strconv.ParseUint(typed, 10, 64)
		if err == nil {
			return parsed
		}
	}
	return fallback
}

func boolValue(value any, fallback bool) bool {
	if result, ok := value.(bool); ok {
		return result
	}
	return fallback
}
