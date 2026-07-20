// Package simplefs provides Souin with a disk-backed cache whose byte budget
// follows the node cache policy instead of a fixed, externally reported limit.
package simplefs

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"github.com/darkweak/storages/core"
	"github.com/pierrec/lz4/v4"
	"github.com/shirou/gopsutil/v4/disk"
)

const moduleName = "simplefs"

type module struct{ core.Configuration }

func init() { caddy.RegisterModule(module{}) }

func (module) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{
		ID:  "storages.cache." + moduleName,
		New: func() caddy.Module { return new(module) },
	}
}

func (m *module) Provision(ctx caddy.Context) error {
	candidate, err := newProvider(m.Provider, ctx.Logger(m).Sugar(), m.Stale)
	if err != nil {
		return err
	}
	registeredName := candidate.Name() + "-" + candidate.Uuid()
	if existing, ok := core.GetRegisteredStorer(registeredName).(*provider); ok {
		existing.capacityMu.Lock()
		existing.limits = candidate.limits
		existing.stale = candidate.stale
		existing.capacityMu.Unlock()
		providers.Store(existing.path, existing)
		return nil
	}
	core.RegisterStorage(candidate)
	providers.Store(candidate.path, candidate)
	return nil
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

type limits struct {
	auto                bool
	maxBytes            uint64
	maxDiskUsagePercent int
}

type cacheItem struct {
	value      []byte
	expiresAt  time.Time
	lastAccess time.Time
	file       bool
}

type provider struct {
	mu         sync.Mutex
	capacityMu sync.Mutex
	items      map[string]cacheItem
	groups     map[string]map[string]struct{}
	path       string
	size       int
	stale      time.Duration
	limits     limits
	logger     core.Logger
}

func newProvider(config core.CacheProvider, logger core.Logger, stale time.Duration) (*provider, error) {
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
	return &provider{
		items:  map[string]cacheItem{},
		groups: map[string]map[string]struct{}{},
		path:   path,
		size:   size,
		stale:  stale,
		limits: configured,
		logger: logger,
	}, nil
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
	value, err := os.ReadFile(string(item.value))
	if err != nil {
		p.Delete(key)
		return nil
	}
	return value
}

func (p *provider) GetMultiLevel(key string, request *http.Request, validator *core.Revalidator) (*http.Response, *http.Response) {
	mapping := p.Get(core.MappingKeyPrefix + key)
	if mapping == nil {
		return nil, nil
	}
	fresh, stale, _ := core.MappingElection(p, mapping, request, validator, p.logger)
	return fresh, stale
}

func (p *provider) SetMultiLevel(baseKey, variedKey string, value []byte, variedHeaders http.Header, etag string, duration time.Duration, realKey string) error {
	compressed := new(bytes.Buffer)
	writer := lz4.NewWriter(compressed)
	if _, err := writer.Write(value); err != nil {
		_ = writer.Close()
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}
	path := filepath.Join(p.path, url.PathEscape(variedKey))
	p.capacityMu.Lock()
	defer p.capacityMu.Unlock()
	incoming := uint64(compressed.Len())
	if info, err := os.Stat(path); err == nil && uint64(info.Size()) < incoming {
		incoming -= uint64(info.Size())
	} else if err == nil {
		incoming = 0
	}
	if err := p.ensureSpaceLocked(incoming, p.limits); err != nil {
		return err
	}
	if err := os.WriteFile(path, compressed.Bytes(), 0o640); err != nil {
		return err
	}

	now := time.Now()
	p.mu.Lock()
	p.setLocked(variedKey, []byte(path), duration+p.stale, true, now)
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
		}
	}
	p.mu.Unlock()
	return err
}

func (p *provider) Set(key string, value []byte, duration time.Duration) error {
	p.mu.Lock()
	p.setLocked(key, value, duration, false, time.Now())
	p.mu.Unlock()
	return nil
}

func (p *provider) Delete(key string) {
	p.mu.Lock()
	p.deleteLocked(key)
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
	p.mu.Unlock()
}

func (p *provider) Reset() error {
	p.mu.Lock()
	for key := range p.items {
		p.deleteLocked(key)
	}
	p.mu.Unlock()
	providers.Delete(p.path)
	return nil
}

// Purge removes cached objects from one site-scoped provider.
func Purge(path, purgeType string, hosts, values []string) (int, error) {
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
		count := len(provider.items)
		for key := range provider.items {
			provider.deleteLocked(key)
		}
		return count, nil
	}
	if purgeType == "TAG" {
		count := 0
		for _, group := range values {
			for key := range provider.groups[group] {
				if _, exists := provider.items[key]; exists {
					provider.deleteLocked(key)
					count++
				}
			}
			delete(provider.groups, group)
		}
		return count, nil
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
			if index < 0 {
				continue
			}
			if purgeType == "PREFIX" {
				matched = true
				break
			}
			remainder := key[index+len(marker):]
			if remainder == "" || strings.HasPrefix(remainder, "?") || strings.HasPrefix(remainder, "-") {
				matched = true
				break
			}
		}
		if matched {
			provider.deleteLocked(key)
			count++
		}
	}
	return count, nil
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
		if err != nil || parsed.Host == "" || parsed.Path == "" {
			return nil, fmt.Errorf("invalid purge URL %q", value)
		}
		markers = append(markers, parsed.Host+"-"+parsed.EscapedPath())
	}
	return markers, nil
}

func (p *provider) ensureSpace(incoming uint64, configured limits) error {
	p.capacityMu.Lock()
	defer p.capacityMu.Unlock()
	return p.ensureSpaceLocked(incoming, configured)
}

func (p *provider) ensureSpaceLocked(incoming uint64, configured limits) error {
	p.mu.Lock()
	p.pruneExpiredLocked(time.Now())
	p.mu.Unlock()
	for {
		fits, err := p.fits(incoming, configured)
		if err != nil {
			return err
		}
		if fits {
			return nil
		}
		if !p.evictOldest() {
			return errors.New("cache storage limit leaves no room for this response")
		}
	}
}

func (p *provider) fits(incoming uint64, configured limits) (bool, error) {
	if configured.auto {
		usage, err := disk.Usage(p.path)
		if err != nil {
			return false, err
		}
		return withinAutoLimit(
			usage.Total,
			usage.Used,
			incoming,
			configured.maxDiskUsagePercent,
		), nil
	}
	if configured.maxBytes == 0 {
		return true, nil
	}
	used, err := directorySize(p.path)
	if err != nil {
		return false, err
	}
	return used <= configured.maxBytes && incoming <= configured.maxBytes-used, nil
}

func withinAutoLimit(total, used, incoming uint64, percent int) bool {
	target := total * uint64(normalizePercent(percent)) / 100
	return used <= target && incoming <= target-used
}

func (p *provider) evictOldest() bool {
	p.mu.Lock()
	var victim string
	var oldest time.Time
	for key, item := range p.items {
		if !item.file {
			continue
		}
		if victim == "" || item.lastAccess.Before(oldest) {
			victim, oldest = key, item.lastAccess
		}
	}
	if victim != "" {
		p.deleteLocked(victim)
		p.mu.Unlock()
		return true
	}
	p.mu.Unlock()

	entries, err := os.ReadDir(p.path)
	if err != nil {
		return false
	}
	var orphan os.DirEntry
	var orphanTime time.Time
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if orphan == nil || info.ModTime().Before(orphanTime) {
			orphan, orphanTime = entry, info.ModTime()
		}
	}
	if orphan == nil {
		return false
	}
	return os.Remove(filepath.Join(p.path, orphan.Name())) == nil
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
	p.items[key] = item
	return item, true
}

func (p *provider) setLocked(key string, value []byte, duration time.Duration, file bool, now time.Time) {
	if old, ok := p.items[key]; ok && old.file && string(old.value) != string(value) {
		_ = os.Remove(string(old.value))
	}
	expiresAt := time.Time{}
	if duration > 0 {
		expiresAt = now.Add(duration)
	}
	p.items[key] = cacheItem{value: value, expiresAt: expiresAt, lastAccess: now, file: file}
}

func (p *provider) deleteLocked(key string) {
	item, ok := p.items[key]
	if !ok {
		return
	}
	delete(p.items, key)
	for group, keys := range p.groups {
		delete(keys, key)
		if len(keys) == 0 {
			delete(p.groups, group)
		}
	}
	if item.file {
		_ = os.Remove(string(item.value))
	}
}

func (p *provider) pruneExpiredLocked(now time.Time) {
	for key, item := range p.items {
		if !item.expiresAt.IsZero() && !item.expiresAt.After(now) {
			p.deleteLocked(key)
		}
	}
}

func directorySize(path string) (uint64, error) {
	var total uint64
	err := filepath.Walk(path, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info != nil && info.Mode().IsRegular() {
			total += uint64(info.Size())
		}
		return nil
	})
	return total, err
}

func normalizePercent(value int) int {
	if value < 1 || value > 95 {
		return 80
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
