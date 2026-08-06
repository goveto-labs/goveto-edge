package simplefs

import (
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"sync"
	"time"

	"go.uber.org/zap"
	core "goveto-edge/internal/cachecore"
)

type Config struct {
	Path                string
	AutoMaxSize         bool
	MaxSizeBytes        uint64
	MaxDiskUsagePercent int
	Stale               time.Duration
}

type Storage struct {
	provider *provider
	once     sync.Once
}

type LookupMetadata struct {
	StoredAt   time.Time
	FreshUntil time.Time
	StaleUntil time.Time
	Headers    http.Header
	StorageKey string
}

var providerLifecycle sync.Mutex

func Acquire(config Config, logger *zap.SugaredLogger) (*Storage, error) {
	path, err := filepath.Abs(filepath.Clean(config.Path))
	if err != nil {
		return nil, err
	}
	providerLifecycle.Lock()
	defer providerLifecycle.Unlock()
	if value, ok := providers.Load(path); ok {
		p := value.(*provider)
		p.capacityMu.Lock()
		p.limits = limits{
			auto:                config.AutoMaxSize,
			maxBytes:            config.MaxSizeBytes,
			maxDiskUsagePercent: normalizePercent(config.MaxDiskUsagePercent),
		}
		if config.MaxDiskUsagePercent == 0 {
			p.limits.maxDiskUsagePercent = 80
		}
		p.stale = config.Stale
		p.refs++
		p.capacityMu.Unlock()
		return &Storage{provider: p}, nil
	}
	p, err := newProvider(core.Config{
		Path: path, AutoMaxSize: config.AutoMaxSize, MaxSizeBytes: config.MaxSizeBytes,
		MaxDiskUsagePercent: config.MaxDiskUsagePercent,
	}, logger, config.Stale)
	if err != nil {
		return nil, err
	}
	p.refs = 1
	providers.Store(path, p)
	return &Storage{provider: p}, nil
}

func (s *Storage) Close() error {
	if s == nil || s.provider == nil {
		return nil
	}
	var result error
	s.once.Do(func() {
		providerLifecycle.Lock()
		defer providerLifecycle.Unlock()
		p := s.provider
		p.capacityMu.Lock()
		p.refs--
		last := p.refs == 0
		p.capacityMu.Unlock()
		if !last {
			return
		}
		p.operationMu.Lock()
		p.drain()
		p.closeIndex()
		p.operationMu.Unlock()
		providers.Delete(p.path)
	})
	return result
}

func (s *Storage) Lookup(key string, request *http.Request) (fresh, stale *http.Response) {
	validator := &core.Revalidator{Matched: true}
	return s.provider.GetMultiLevel(key, request, validator)
}

func (s *Storage) LookupEntry(key string, request *http.Request) (fresh, stale *http.Response, metadata LookupMetadata) {
	validator := &core.Revalidator{Matched: true}
	fresh, stale, index, storageKey := s.provider.getMultiLevel(key, request, validator)
	if index != nil {
		metadata.StoredAt = index.StoredAt
		metadata.FreshUntil = index.GetFreshTime()
		metadata.StaleUntil = index.GetStaleTime()
		metadata.Headers = index.Revalidated.Clone()
		metadata.StorageKey = storageKey
	}
	return fresh, stale, metadata
}

func (s *Storage) Put(baseKey, variedKey string, response []byte, varied http.Header, etag string, ttl time.Duration, realKey string) error {
	if s == nil || s.provider == nil {
		return errors.New("cache storage is closed")
	}
	return s.provider.SetMultiLevel(baseKey, variedKey, response, varied, etag, ttl, realKey)
}

func (s *Storage) PutReader(baseKey, variedKey string, source io.Reader, size uint64, groups []string, varied http.Header, etag string, ttl time.Duration, realKey string) error {
	if s == nil || s.provider == nil {
		return errors.New("cache storage is closed")
	}
	return s.provider.SetMultiLevelStream(baseKey, variedKey, source, size, groups, varied, etag, ttl, realKey)
}

func (s *Storage) Refresh(baseKey string, request *http.Request, ttl time.Duration, update http.Header) bool {
	return s != nil && s.provider != nil && s.provider.Refresh(baseKey, request, ttl, update)
}

func (s *Storage) Delete(key string) { s.provider.Delete(key) }
