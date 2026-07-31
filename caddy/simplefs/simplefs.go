// Package simplefs provides Souin with a disk-backed cache whose byte budget
// follows the node cache policy instead of a fixed, externally reported limit.
package simplefs

import (
	"bufio"
	"bytes"
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

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"github.com/darkweak/storages/core"
	"github.com/pierrec/lz4/v4"
	"github.com/shirou/gopsutil/v4/disk"
	bolt "go.etcd.io/bbolt"
	"google.golang.org/protobuf/proto"
)

const (
	moduleName   = "simplefs"
	indexName    = ".goveto-cache-index.db"
	oldIndexName = ".goveto-cache-index.json"
	bodyPrefix   = "body-"
	indexVersion = 2
)

var (
	indexItemsBucket  = []byte("items")
	indexGroupsBucket = []byte("groups")
	indexMetaBucket   = []byte("meta")
	indexVersionKey   = []byte("version")
	indexUsedBytesKey = []byte("used_bytes")
)

var ErrCapacity = errors.New("cache storage limit leaves no room for this response")
var ErrUncacheable = errors.New("response cache directives prohibit shared storage")

type module struct{ core.Configuration }

func init() { caddy.RegisterModule(module{}) }

func (module) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{
		ID:  "storages.cache." + moduleName,
		New: func() caddy.Module { return new(module) },
	}
}

func (m *module) Provision(ctx caddy.Context) error {
	candidate, err := buildProvider(m.Provider, ctx.Logger(m).Sugar(), m.Stale)
	if err != nil {
		return err
	}
	if value, ok := providers.Load(candidate.path); ok {
		if existing, valid := value.(*provider); valid {
			updateProvider(existing, candidate)
			core.RegisterStorage(existing)
			return nil
		}
	}
	registeredName := candidate.Name() + "-" + candidate.Uuid()
	if existing, ok := core.GetRegisteredStorer(registeredName).(*provider); ok {
		updateProvider(existing, candidate)
		return nil
	}
	if err = candidate.loadIndex(); err != nil {
		return err
	}
	core.RegisterStorage(candidate)
	providers.Store(candidate.path, candidate)
	return nil
}

func updateProvider(existing, candidate *provider) {
	existing.capacityMu.Lock()
	existing.limits = candidate.limits
	existing.stale = candidate.stale
	existing.diskUsage = candidate.diskUsage
	existing.capacityMu.Unlock()
	providers.Store(existing.path, existing)
}

func (*module) ServeHTTP(rw http.ResponseWriter, request *http.Request, next caddyhttp.Handler) error {
	return next.ServeHTTP(rw, request)
}

var (
	_         caddy.Provisioner           = (*module)(nil)
	_         caddyhttp.MiddlewareHandler = (*module)(nil)
	providers sync.Map
)

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
		provider.mu.Lock()
		for _, item := range provider.items {
			if item.file {
				result.Entries++
			}
		}
		provider.mu.Unlock()
		result.Hits += provider.hits.Load()
		result.Misses += provider.misses.Load()
		result.StaleHits += provider.staleHits.Load()
		result.Evictions += provider.evictions.Load()
		result.RejectedWrites += provider.rejections.Load()
		result.Corruptions += provider.corruptions.Load()
		return true
	})
	if total := result.Hits + result.Misses; total > 0 {
		result.HitRate = float64(result.Hits) / float64(total)
	}
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
	expiresAt      time.Time
	lastAccess     time.Time
	file           bool
	generation     uint64
	compressedSize uint64
	originalSize   uint64
	checksum       [sha256.Size]byte
	lru            *list.Element
}

type diskItem struct {
	Value          []byte    `json:"value,omitempty"`
	File           string    `json:"file,omitempty"`
	ExpiresAt      time.Time `json:"expires_at,omitempty"`
	LastAccess     time.Time `json:"last_access"`
	CompressedSize uint64    `json:"compressed_size,omitempty"`
	OriginalSize   uint64    `json:"original_size,omitempty"`
	Checksum       []byte    `json:"checksum,omitempty"`
}

type Statistics struct {
	Entries        uint64  `json:"entries"`
	Hits           uint64  `json:"hits"`
	Misses         uint64  `json:"misses"`
	StaleHits      uint64  `json:"stale_hits"`
	Evictions      uint64  `json:"evictions"`
	RejectedWrites uint64  `json:"rejected_writes"`
	Corruptions    uint64  `json:"corruptions"`
	HitRate        float64 `json:"hit_rate"`
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
	mu          sync.Mutex
	capacityMu  sync.Mutex
	items       map[string]cacheItem
	groups      map[string]map[string]struct{}
	lru         list.List
	index       *bolt.DB
	dirtyItems  map[string]struct{}
	dirtyGroups map[string]struct{}
	path        string
	size        int
	stale       time.Duration
	limits      limits
	logger      core.Logger
	diskUsage   diskUsageFunc
	hits        atomic.Uint64
	misses      atomic.Uint64
	staleHits   atomic.Uint64
	evictions   atomic.Uint64
	rejections  atomic.Uint64
	corruptions atomic.Uint64
	indexWrites atomic.Uint64
	cacheUsed   atomic.Uint64
	nextVersion uint64
}

func newProvider(config core.CacheProvider, logger core.Logger, stale time.Duration) (*provider, error) {
	provider, err := buildProvider(config, logger, stale)
	if err != nil {
		return nil, err
	}
	if err = provider.loadIndex(); err != nil {
		return nil, err
	}
	return provider, nil
}

func buildProvider(config core.CacheProvider, logger core.Logger, stale time.Duration) (*provider, error) {
	path := config.Path
	configured := limits{auto: true, maxDiskUsagePercent: 80}
	size := 0
	if raw, ok := config.Configuration.(map[string]any); ok {
		path = stringValue(raw["path"], path)
		size = intValue(raw["size"], 0)
		configured.auto = boolValue(raw["auto_max_size"], true)
		configured.maxBytes = uint64Value(raw["max_size_bytes"], 0)
		configured.maxDiskUsagePercent = normalizePercent(
			intValue(raw["max_disk_usage_percent"], 80),
		)
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
		items:       map[string]cacheItem{},
		groups:      map[string]map[string]struct{}{},
		dirtyItems:  map[string]struct{}{},
		dirtyGroups: map[string]struct{}{},
		path:        path,
		size:        size,
		stale:       stale,
		limits:      configured,
		logger:      logger,
		diskUsage:   diskUsageForProvider(path),
	}
	return provider, nil
}

func (p *provider) Name() string { return "SIMPLEFS" }
func (p *provider) Uuid() string { return fmt.Sprintf("%s-%d", p.path, p.size) }
func (p *provider) Init() error  { return nil }

func (p *provider) MapKeys(prefix string) map[string]string {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.pruneExpiredLocked(time.Now())
	result := map[string]string{}
	for key, item := range p.items {
		if strings.HasPrefix(key, prefix) {
			result[strings.TrimPrefix(key, prefix)] = string(item.value)
		}
	}
	return result
}

func (p *provider) ListKeys() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.pruneExpiredLocked(time.Now())
	result := make([]string, 0, len(p.items))
	for key := range p.items {
		result = append(result, key)
	}
	sort.Strings(result)
	return result
}

func (p *provider) Get(key string) []byte {
	p.mu.Lock()
	item, ok := p.itemLocked(key, time.Now())
	p.mu.Unlock()
	if !ok {
		return nil
	}
	if !item.file {
		return item.value
	}
	value, err := readCachedObjectFile(string(item.value), item.compressedSize, item.checksum)
	if err != nil {
		p.mu.Lock()
		current, exists := p.items[key]
		if exists && current.generation == item.generation {
			p.deleteLocked(key)
			_ = p.persistIndexLocked()
			p.corruptions.Add(1)
		}
		p.mu.Unlock()
		return nil
	}
	return value
}

func (p *provider) GetMultiLevel(key string, request *http.Request, validator *core.Revalidator) (*http.Response, *http.Response) {
	mappingKey := core.MappingKeyPrefix + key
	mapping := p.Get(mappingKey)
	if mapping == nil {
		p.misses.Add(1)
		return nil, nil
	}
	if _, err := core.DecodeMapping(mapping); err != nil {
		p.mu.Lock()
		p.deleteLocked(mappingKey)
		_ = p.persistIndexLocked()
		p.mu.Unlock()
		p.corruptions.Add(1)
		p.misses.Add(1)
		return nil, nil
	}
	fresh, stale, _ := core.MappingElection(p, mapping, request, validator, p.logger)
	if fresh == nil && stale == nil {
		p.misses.Add(1)
	} else {
		p.hits.Add(1)
		if fresh == nil {
			p.staleHits.Add(1)
		}
	}
	return fresh, stale
}

func (p *provider) SetMultiLevel(baseKey, variedKey string, value []byte, variedHeaders http.Header, etag string, duration time.Duration, realKey string) error {
	if err := validateCachedResponse(value); err != nil {
		p.corruptions.Add(1)
		return fmt.Errorf("reject incomplete cache response: %w", err)
	}
	if cacheResponseHasDirective(value, "private", "no-store", "no-cache", "must-revalidate") {
		return ErrUncacheable
	}
	path := filepath.Join(p.path, bodyFileName(variedKey))
	temporaryPath, compressedSize, checksum, err := writeCompressedTemporary(path, value)
	if err != nil {
		return err
	}
	defer os.Remove(temporaryPath)
	p.capacityMu.Lock()
	defer p.capacityMu.Unlock()
	incoming := compressedSize
	if info, err := os.Stat(path); err == nil && uint64(info.Size()) < incoming {
		incoming -= uint64(info.Size())
	} else if err == nil {
		incoming = 0
	}
	if err := p.ensureSpaceLockedWithTransient(incoming, p.limits, compressedSize); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		if errors.Is(err, syscall.ENOSPC) {
			p.rejections.Add(1)
		}
		return err
	}
	if directory, openErr := os.Open(p.path); openErr == nil {
		err = directory.Sync()
		_ = directory.Close()
		if err != nil {
			return err
		}
	}

	now := time.Now()
	p.mu.Lock()
	p.setFileLocked(variedKey, path, duration+p.stale, now, compressedSize, uint64(len(value)), checksum)
	mappingKey := core.MappingKeyPrefix + baseKey
	previous, _ := p.itemLocked(mappingKey, now)
	mapping, err := core.MappingUpdater(
		variedKey,
		previous.value,
		p.logger,
		now,
		now.Add(duration),
		now.Add(duration+p.stale),
		variedHeaders,
		etag,
		realKey,
	)
	if err == nil {
		p.setLocked(mappingKey, mapping, duration+p.stale, false, now)
		for _, group := range surrogateGroups(value) {
			if p.groups[group] == nil {
				p.groups[group] = map[string]struct{}{}
			}
			p.groups[group][variedKey] = struct{}{}
			p.groups[group][mappingKey] = struct{}{}
			p.dirtyGroups[group] = struct{}{}
		}
		err = p.persistIndexLocked()
		if err != nil {
			p.recordRejectedWrite(err)
			p.deleteLocked(variedKey)
			_ = p.persistIndexLocked()
		}
	} else {
		p.deleteLocked(variedKey)
		p.deleteLocked(mappingKey)
		if persistErr := p.persistIndexLocked(); persistErr != nil {
			p.recordRejectedWrite(persistErr)
			err = errors.Join(err, persistErr)
		}
	}
	p.mu.Unlock()
	return err
}

func (p *provider) Set(key string, value []byte, duration time.Duration) error {
	p.mu.Lock()
	p.setLocked(key, value, duration, false, time.Now())
	err := p.persistIndexLocked()
	p.mu.Unlock()
	return err
}

func (p *provider) Delete(key string) {
	p.mu.Lock()
	p.deleteLocked(key)
	_ = p.persistIndexLocked()
	p.mu.Unlock()
}

func (p *provider) DeleteMany(expression string) {
	matcher, err := regexp.Compile(expression)
	if err != nil {
		return
	}
	p.mu.Lock()
	for key := range p.items {
		if matcher.MatchString(key) || strings.HasPrefix(key, expression) {
			p.deleteLocked(key)
		}
	}
	_ = p.persistIndexLocked()
	p.mu.Unlock()
}

func (p *provider) Reset() error {
	p.mu.Lock()
	for key := range p.items {
		p.deleteLocked(key)
	}
	err := p.persistIndexLocked()
	p.mu.Unlock()
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
		for _, storer := range core.GetRegisteredStorers() {
			candidate, candidateOK := storer.(*provider)
			if candidateOK && samePath(candidate.path, path) {
				value, ok = candidate, true
				providers.Store(path, candidate)
				break
			}
		}
	}
	if !ok {
		return 0, fmt.Errorf("cache provider %q is not active", path)
	}
	provider := value.(*provider)
	provider.mu.Lock()
	defer provider.mu.Unlock()

	if purgeType == "ALL" {
		count := provider.objectCountLocked()
		for key := range provider.items {
			provider.deleteLocked(key)
		}
		return count, provider.persistIndexLocked()
	}
	if purgeType == "TAG" {
		count := 0
		for _, group := range values {
			for key := range provider.groups[group] {
				if item, exists := provider.items[key]; exists {
					provider.deleteLocked(key)
					if item.file {
						count++
					}
				}
			}
			delete(provider.groups, group)
		}
		return count, provider.persistIndexLocked()
	}

	markers, err := purgeMarkers(hosts, values)
	if err != nil {
		return 0, err
	}
	count := 0
	for key := range provider.items {
		matched := false
		for _, marker := range markers {
			index := strings.Index(key, marker)
			if index < 0 || (index > 0 && key[index-1] != '-') {
				continue
			}
			if purgeType == "PREFIX" {
				matched = true
				break
			}
			remainder := key[index+len(marker):]
			if remainder == "" || strings.HasPrefix(remainder, "-") {
				matched = true
				break
			}
		}
		if matched {
			item := provider.items[key]
			provider.deleteLocked(key)
			if item.file {
				count++
			}
		}
	}
	return count, provider.persistIndexLocked()
}

func (p *provider) objectCountLocked() int {
	count := 0
	for _, item := range p.items {
		if item.file {
			count++
		}
	}
	return count
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
	pruned := p.pruneExpiredLocked(time.Now())
	var pruneErr error
	if pruned {
		pruneErr = p.persistIndexLocked()
		p.recordRejectedWrite(pruneErr)
	}
	p.mu.Unlock()
	if pruneErr != nil {
		return pruneErr
	}
	used, available, err := p.capacityAvailable(configured, transient)
	if err != nil {
		return err
	}
	if incoming > available {
		p.rejections.Add(1)
		return ErrCapacity
	}
	if used <= available-incoming {
		return nil
	}
	required := used - (available - incoming)
	freed, err := p.evictBytes(required)
	if err != nil {
		return err
	}
	if freed < required {
		p.rejections.Add(1)
		return ErrCapacity
	}
	return nil
}

func (p *provider) capacityAvailable(configured limits, transient uint64) (uint64, uint64, error) {
	usage, err := p.diskUsage(p.path)
	if err != nil {
		return 0, 0, err
	}
	cacheUsed := p.cacheUsed.Load()
	target := usage.Total * uint64(normalizePercent(configured.maxDiskUsagePercent)) / 100
	nonCacheUsed := uint64(0)
	trackedAndTransient := cacheUsed + transient
	if usage.Used > trackedAndTransient {
		nonCacheUsed = usage.Used - trackedAndTransient
	}
	if nonCacheUsed > target {
		return cacheUsed, 0, nil
	}
	available := target - nonCacheUsed
	if !configured.auto && configured.maxBytes > 0 {
		available = min(available, configured.maxBytes)
	}
	return cacheUsed, available, nil
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
	if required == 0 {
		return 0, nil
	}
	p.mu.Lock()
	var freed, evicted uint64
	for freed < required {
		oldest := p.lru.Front()
		if oldest == nil {
			break
		}
		key := oldest.Value.(string)
		item, ok := p.items[key]
		if !ok || !item.file {
			p.lru.Remove(oldest)
			continue
		}
		if err := os.Remove(string(item.value)); err != nil && !errors.Is(err, os.ErrNotExist) {
			break
		}
		freed += item.compressedSize
		evicted++
		p.forgetItemLocked(key, item)
	}
	var err error
	if evicted > 0 {
		p.repairMappingsLocked()
		err = p.persistIndexLocked()
	}
	p.mu.Unlock()
	p.evictions.Add(evicted)
	return freed, err
}

func (p *provider) itemLocked(key string, now time.Time) (cacheItem, bool) {
	item, ok := p.items[key]
	if !ok {
		return cacheItem{}, false
	}
	if !item.expiresAt.IsZero() && !item.expiresAt.After(now) {
		p.deleteLocked(key)
		return cacheItem{}, false
	}
	item.lastAccess = now
	if item.lru != nil {
		p.lru.MoveToBack(item.lru)
	}
	p.items[key] = item
	p.dirtyItems[key] = struct{}{}
	return item, true
}

func (p *provider) setLocked(key string, value []byte, duration time.Duration, file bool, now time.Time) {
	if file {
		panic("use setFileLocked for cache objects")
	}
	if old, ok := p.items[key]; ok {
		if old.file {
			_ = os.Remove(string(old.value))
			if old.compressedSize > 0 {
				p.cacheUsed.Add(^uint64(old.compressedSize - 1))
			}
		}
		p.removeItemAccountingLocked(old)
	}
	expiresAt := time.Time{}
	if duration > 0 {
		expiresAt = now.Add(duration)
	}
	p.nextVersion++
	p.items[key] = cacheItem{
		value: value, expiresAt: expiresAt, lastAccess: now, file: file, generation: p.nextVersion,
	}
	p.dirtyItems[key] = struct{}{}
}

func (p *provider) setFileLocked(key, path string, duration time.Duration, now time.Time, compressedSize, originalSize uint64, checksum [sha256.Size]byte) {
	if old, ok := p.items[key]; ok {
		if old.file && string(old.value) != path {
			_ = os.Remove(string(old.value))
		}
		p.removeItemAccountingLocked(old)
		if old.file && old.compressedSize > 0 {
			p.cacheUsed.Add(^uint64(old.compressedSize - 1))
		}
	}
	expiresAt := time.Time{}
	if duration > 0 {
		expiresAt = now.Add(duration)
	}
	p.nextVersion++
	item := cacheItem{
		value: []byte(path), expiresAt: expiresAt, lastAccess: now, file: true, generation: p.nextVersion,
		compressedSize: compressedSize, originalSize: originalSize, checksum: checksum,
	}
	item.lru = p.lru.PushBack(key)
	p.items[key] = item
	p.cacheUsed.Add(compressedSize)
	p.dirtyItems[key] = struct{}{}
}

func (p *provider) deleteLocked(key string) {
	item, ok := p.items[key]
	if !ok {
		return
	}
	p.deleteItemLocked(key, item)
	if item.file {
		p.removeVariantFromMappingsLocked(key)
	}
}

func (p *provider) deleteItemLocked(key string, item cacheItem) {
	p.forgetItemLocked(key, item)
	if item.file {
		_ = os.Remove(string(item.value))
	}
}

func (p *provider) forgetItemLocked(key string, item cacheItem) {
	delete(p.items, key)
	p.removeItemAccountingLocked(item)
	p.dirtyItems[key] = struct{}{}
	for group, keys := range p.groups {
		if _, ok := keys[key]; !ok {
			continue
		}
		delete(keys, key)
		p.dirtyGroups[group] = struct{}{}
		if len(keys) == 0 {
			delete(p.groups, group)
		}
	}
	if item.file && item.compressedSize > 0 {
		p.cacheUsed.Add(^uint64(item.compressedSize - 1))
	}
}

func (p *provider) removeItemAccountingLocked(item cacheItem) {
	if item.lru != nil {
		p.lru.Remove(item.lru)
	}
}

func (p *provider) removeVariantFromMappingsLocked(variedKey string) {
	for key, item := range p.items {
		if item.file || !strings.HasPrefix(key, core.MappingKeyPrefix) {
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
		value, marshalErr := proto.Marshal(mapping)
		if marshalErr != nil {
			p.deleteItemLocked(key, item)
			continue
		}
		item.value = value
		p.nextVersion++
		item.generation = p.nextVersion
		p.items[key] = item
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
		value, marshalErr := proto.Marshal(mapping)
		if marshalErr != nil {
			p.corruptions.Add(1)
			p.deleteItemLocked(key, item)
			continue
		}
		item.value = value
		p.nextVersion++
		item.generation = p.nextVersion
		p.items[key] = item
		p.dirtyItems[key] = struct{}{}
	}
	return repaired
}

func (p *provider) pruneExpiredLocked(now time.Time) bool {
	pruned := false
	for key, item := range p.items {
		if !item.expiresAt.IsZero() && !item.expiresAt.After(now) {
			p.deleteLocked(key)
			pruned = true
		}
	}
	return pruned
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
				if strings.HasPrefix(key, core.MappingKeyPrefix) {
					if _, decodeErr := core.DecodeMapping(item.Value); decodeErr != nil {
						p.corruptions.Add(1)
						repaired = true
						p.dirtyItems[key] = struct{}{}
						return nil
					}
				}
				p.nextVersion++
				p.items[key] = cacheItem{
					value: item.Value, expiresAt: item.ExpiresAt, lastAccess: item.LastAccess,
					generation: p.nextVersion,
				}
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
			loaded := cacheItem{
				value: []byte(path), file: true, expiresAt: item.ExpiresAt, lastAccess: item.LastAccess,
				generation: p.nextVersion, compressedSize: uint64(len(compressed)), originalSize: originalSize, checksum: checksum,
			}
			p.items[key] = loaded
			p.cacheUsed.Add(loaded.compressedSize)
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
			}
			return nil
		})
	})
	if err != nil {
		return err
	}
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
			diskValue := diskItem{ExpiresAt: item.expiresAt, LastAccess: item.lastAccess}
			if item.file {
				diskValue.File = filepath.Base(string(item.value))
				diskValue.CompressedSize = item.compressedSize
				diskValue.OriginalSize = item.originalSize
				diskValue.Checksum = item.checksum[:]
			} else {
				diskValue.Value = item.value
			}
			encoded, err := json.Marshal(diskValue)
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
		if isTemporaryCacheFile(entry.Name()) {
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
	compressed, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if _, err = validateCompressedResponse(compressed); err != nil {
		return nil, err
	}
	return compressed, nil
}

func inspectCachedResponseFile(path string) ([]byte, uint64, error) {
	compressed, err := os.ReadFile(path)
	if err != nil {
		return nil, 0, err
	}
	originalSize, err := validateCompressedResponse(compressed)
	return compressed, originalSize, err
}

func validateCompressedResponse(compressed []byte) (uint64, error) {
	counted := &countingReader{Reader: lz4.NewReader(bytes.NewReader(compressed))}
	if err := validateCachedResponseReader(counted); err != nil {
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

func readCachedObjectFile(path string, expectedSize uint64, expectedChecksum [sha256.Size]byte) ([]byte, error) {
	compressed, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if uint64(len(compressed)) != expectedSize {
		return nil, errors.New("cache object size does not match metadata")
	}
	if checksum := sha256.Sum256(compressed); checksum != expectedChecksum {
		return nil, errors.New("cache object checksum does not match metadata")
	}
	return compressed, nil
}

func writeCompressedTemporary(target string, value []byte) (path string, size uint64, checksum [sha256.Size]byte, err error) {
	temporary, err := os.CreateTemp(filepath.Dir(target), "."+filepath.Base(target)+".tmp-")
	if err != nil {
		return "", 0, checksum, err
	}
	path = temporary.Name()
	cleanup := func() {
		_ = temporary.Close()
		_ = os.Remove(path)
	}
	if err = temporary.Chmod(0o640); err != nil {
		cleanup()
		return "", 0, checksum, err
	}
	hash := sha256.New()
	writer := lz4.NewWriter(io.MultiWriter(temporary, hash))
	if _, err = writer.Write(value); err != nil {
		_ = writer.Close()
		cleanup()
		return "", 0, checksum, err
	}
	if err = writer.Close(); err != nil {
		cleanup()
		return "", 0, checksum, err
	}
	if err = temporary.Sync(); err != nil {
		cleanup()
		return "", 0, checksum, err
	}
	info, err := temporary.Stat()
	if err != nil {
		cleanup()
		return "", 0, checksum, err
	}
	if err = temporary.Close(); err != nil {
		cleanup()
		return "", 0, checksum, err
	}
	copy(checksum[:], hash.Sum(nil))
	return path, uint64(info.Size()), checksum, nil
}

func validateCachedResponse(value []byte) error {
	return validateCachedResponseReader(bytes.NewReader(value))
}

func validateCachedResponseReader(source io.Reader) error {
	buffered := bufio.NewReader(source)
	response, err := http.ReadResponse(buffered, nil)
	if err != nil {
		return fmt.Errorf("read cached HTTP response: %w", err)
	}
	defer response.Body.Close()
	noBody := response.Header.Get("X-Goveto-Origin-Method") == http.MethodHead ||
		response.StatusCode == http.StatusNoContent || response.StatusCode == http.StatusNotModified
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
