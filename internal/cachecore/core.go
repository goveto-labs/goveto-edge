package cachecore

import (
	"encoding/json"
	"net/http"
	"time"
)

const MappingKeyPrefix = "IDX_"

type Config struct {
	URL                 string
	Path                string
	Configuration       any
	AutoMaxSize         bool
	MaxSizeBytes        uint64
	MaxDiskUsagePercent int
}

type CacheProvider = Config

type Logger interface {
	Debug(args ...any)
	Info(args ...any)
	Warn(args ...any)
	Error(args ...any)
}

type Revalidator struct {
	Matched          bool
	RequestETags     []string
	ResponseETag     string
	IfNoneMatch      []string
	IfMatch          []string
	IfNoneMatchSet   bool
	IfMatchSet       bool
	NeedRevalidation bool
}

type StringList struct {
	Values []string `json:"values"`
}

func (s *StringList) GetHeaderValue() []string {
	if s == nil {
		return nil
	}
	return s.Values
}

type KeyIndex struct {
	StoredAt      time.Time              `json:"stored_at"`
	FreshTime     time.Time              `json:"fresh_time"`
	StaleTime     time.Time              `json:"stale_time"`
	VariedHeaders map[string]*StringList `json:"varied_headers,omitempty"`
	ETag          string                 `json:"etag,omitempty"`
	RealKey       string                 `json:"real_key,omitempty"`
	Revalidated   http.Header            `json:"revalidated_headers,omitempty"`
	StorageKey    string                 `json:"-"`
}

func (i *KeyIndex) GetFreshTime() time.Time { return i.FreshTime }
func (i *KeyIndex) GetStaleTime() time.Time { return i.StaleTime }
func (i *KeyIndex) GetVariedHeaders() map[string]*StringList {
	if i == nil {
		return nil
	}
	return i.VariedHeaders
}
func (i *KeyIndex) GetEtag() string {
	if i == nil {
		return ""
	}
	return i.ETag
}
func (i *KeyIndex) GetRealKey() string {
	if i == nil {
		return ""
	}
	return i.RealKey
}

type StorageMapper struct {
	Mapping map[string]*KeyIndex `json:"mapping"`
}

func (m *StorageMapper) GetMapping() map[string]*KeyIndex {
	if m == nil {
		return nil
	}
	return m.Mapping
}

func DecodeMapping(value []byte) (*StorageMapper, error) {
	mapping := new(StorageMapper)
	if err := json.Unmarshal(value, mapping); err != nil {
		return nil, err
	}
	return mapping, nil
}

func EncodeMapping(mapping *StorageMapper) ([]byte, error) { return json.Marshal(mapping) }

func MappingUpdater(key string, value []byte, now, fresh, stale time.Time, varied http.Header, etag, realKey string) ([]byte, error) {
	mapping := &StorageMapper{Mapping: map[string]*KeyIndex{}}
	if len(value) > 0 {
		var err error
		mapping, err = DecodeMapping(value)
		if err != nil {
			return nil, err
		}
		if mapping.Mapping == nil {
			mapping.Mapping = map[string]*KeyIndex{}
		}
	}
	var headers map[string]*StringList
	if varied != nil {
		headers = make(map[string]*StringList, len(varied))
		for name, values := range varied {
			headers[http.CanonicalHeaderKey(name)] = &StringList{Values: append([]string(nil), values...)}
		}
	}
	mapping.Mapping[key] = &KeyIndex{
		StoredAt: now, FreshTime: fresh, StaleTime: stale,
		VariedHeaders: headers, ETag: etag, RealKey: realKey,
	}
	return EncodeMapping(mapping)
}

func ValidateETagFromHeader(etag string, validator *Revalidator) {
	validator.ResponseETag = etag
	validator.NeedRevalidation = validator.NeedRevalidation || (etag != "" && len(validator.RequestETags) > 0)
	validator.Matched = etag == "" || len(validator.RequestETags) == 0
	if validator.IfNoneMatchSet {
		validator.Matched = false
		for _, candidate := range validator.IfNoneMatch {
			if candidate == "*" || candidate == etag {
				validator.Matched = true
				return
			}
		}
	}
	if validator.IfMatchSet {
		validator.Matched = false
		for _, candidate := range validator.IfMatch {
			if candidate == "*" || candidate == etag {
				validator.Matched = true
				return
			}
		}
	}
}
