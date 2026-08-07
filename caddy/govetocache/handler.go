package govetocache

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"go.uber.org/zap"

	"goveto-edge/caddy/simplefs"
	"goveto-edge/internal/cacherange"
	"goveto-edge/internal/policy"
)

type Handler struct {
	SiteID                  string         `json:"site_id"`
	Path                    string         `json:"path"`
	AutoMaxSize             bool           `json:"auto_max_size"`
	MaxSizeBytes            uint64         `json:"max_size_bytes"`
	MaxDiskUsagePercent     int            `json:"max_disk_usage_percent"`
	KeyParts                []string       `json:"key_parts"`
	KeyHeaders              []string       `json:"key_headers,omitempty"`
	KeyQueryNormalize       bool           `json:"key_query_normalize,omitempty"`
	KeyQueryInclude         []string       `json:"key_query_include,omitempty"`
	KeyQueryExclude         []string       `json:"key_query_exclude,omitempty"`
	KeyCookies              []string       `json:"key_cookies,omitempty"`
	HashKey                 bool           `json:"hash_key,omitempty"`
	HideKey                 bool           `json:"hide_key,omitempty"`
	DefaultTTL              int            `json:"default_ttl"`
	StatusTTL               map[string]int `json:"status_ttl,omitempty"`
	StaleIfErrorTTL         int            `json:"stale_if_error_ttl,omitempty"`
	StaleWhileRevalidateTTL int            `json:"stale_while_revalidate_ttl,omitempty"`
	MaxBodyBytes            uint64         `json:"max_body_bytes"`
	// Coalesce collapses concurrent misses on the same key into one origin fetch.
	// Only the leader observes origin 1xx responses (e.g. 103 Early Hints);
	// waiters block until the final response is cached and then see a HIT.
	Coalesce           bool     `json:"coalesce,omitempty"`
	XCache             bool     `json:"x_cache"`
	Age                bool     `json:"age"`
	Debug              bool     `json:"debug,omitempty"`
	OverrideClientTTL  bool     `json:"override_client_ttl,omitempty"`
	ClientTTL          int      `json:"client_ttl,omitempty"`
	BypassCacheControl []string `json:"bypass_cache_control,omitempty"`
	SurrogateKeyHeader string   `json:"surrogate_key_header,omitempty"`
	storage            *simplefs.Storage
	flightsMu          sync.Mutex
	flights            map[string]*flight
	logger             *zap.Logger
}

type flight struct {
	done chan struct{}
}

var cacheableStatus = map[int]struct{}{
	200: {}, 203: {}, 204: {}, 206: {}, 300: {}, 301: {}, 404: {}, 405: {}, 410: {}, 414: {}, 501: {},
}

func init() { caddy.RegisterModule(new(Handler)) }

func (*Handler) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{ID: "http.handlers.goveto_cache", New: func() caddy.Module { return new(Handler) }}
}

func (h *Handler) Provision(ctx caddy.Context) error {
	h.logger = ctx.Logger(h)
	h.flights = map[string]*flight{}
	maxStale := max(h.StaleIfErrorTTL, h.StaleWhileRevalidateTTL)
	storage, err := simplefs.Acquire(simplefs.Config{
		Path: h.Path, AutoMaxSize: h.AutoMaxSize, MaxSizeBytes: h.MaxSizeBytes,
		MaxDiskUsagePercent: h.MaxDiskUsagePercent, Stale: time.Duration(maxStale) * time.Second,
	}, h.logger.Sugar())
	if err != nil {
		return fmt.Errorf("open cache storage for site %s: %w", h.SiteID, err)
	}
	h.storage = storage
	return nil
}

func (h *Handler) Cleanup() error {
	if h.storage == nil {
		return nil
	}
	return h.storage.Close()
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request, next caddyhttp.Handler) error {
	if r.Header.Get("Authorization") != "" {
		return next.ServeHTTP(w, r)
	}
	baseRaw := h.cacheKey(r, nil)
	baseKey := h.storageKey(baseRaw)
	fresh, stale, metadata := h.storage.LookupEntry(baseKey, r)
	applyRevalidatedHeaders(fresh, metadata.Headers)
	applyRevalidatedHeaders(stale, metadata.Headers)
	if fresh != nil {
		if stale != nil {
			_ = stale.Body.Close()
		}
		return h.serveCached(w, fresh, "HIT", baseRaw, metadata)
	}
	if stale != nil && withinStaleWindow(metadata.FreshUntil, h.StaleWhileRevalidateTTL) {
		flightKey := baseKey + "\x00" + metadata.StorageKey
		if h.beginFlight(flightKey) != nil {
			request := detachedRequest(r)
			go func() {
				defer h.endFlight(flightKey)
				h.refresh(request, next, baseRaw, baseKey)
			}()
		}
		return h.serveCached(w, stale, "STALE", baseRaw, metadata)
	}

	if h.Coalesce {
		if waiting := h.beginFlight(baseKey); waiting == nil {
			select {
			case <-r.Context().Done():
				if stale != nil {
					_ = stale.Body.Close()
				}
				return r.Context().Err()
			case <-h.flightDone(baseKey):
			}
			fresh, _, metadata = h.storage.LookupEntry(baseKey, r)
			applyRevalidatedHeaders(fresh, metadata.Headers)
			if fresh != nil {
				if stale != nil {
					_ = stale.Body.Close()
				}
				return h.serveCached(w, fresh, "HIT", baseRaw, metadata)
			}
		} else {
			defer h.endFlight(baseKey)
		}
	}
	return h.fetchAndServe(w, r, next, baseRaw, baseKey, stale, metadata)
}

func (h *Handler) beginFlight(key string) *flight {
	h.flightsMu.Lock()
	defer h.flightsMu.Unlock()
	if existing := h.flights[key]; existing != nil {
		return nil
	}
	f := &flight{done: make(chan struct{})}
	h.flights[key] = f
	return f
}

func (h *Handler) flightDone(key string) <-chan struct{} {
	h.flightsMu.Lock()
	defer h.flightsMu.Unlock()
	if current := h.flights[key]; current != nil {
		return current.done
	}
	done := make(chan struct{})
	close(done)
	return done
}

func (h *Handler) endFlight(key string) {
	h.flightsMu.Lock()
	if current := h.flights[key]; current != nil {
		delete(h.flights, key)
		close(current.done)
	}
	h.flightsMu.Unlock()
}

func (h *Handler) fetchAndServe(w http.ResponseWriter, request *http.Request, next caddyhttp.Handler, baseRaw, baseKey string, stale *http.Response, metadata simplefs.LookupMetadata) error {
	originRequest := request
	if _, ranged := cacherange.FromContext(request.Context()); ranged {
		originRequest = request.Clone(request.Context())
		originRequest.Header = request.Header.Clone()
		originRequest.Header.Del("Range")
		originRequest.Header.Del("If-Range")
	}
	if stale != nil {
		originRequest = originRequest.Clone(originRequest.Context())
		originRequest.Header = originRequest.Header.Clone()
		if etag := stale.Header.Get("ETag"); etag != "" {
			originRequest.Header.Set("If-None-Match", etag)
		} else if modified := stale.Header.Get("Last-Modified"); modified != "" {
			originRequest.Header.Set("If-Modified-Since", modified)
		}
	}
	captured, captureErr := newCapturedResponse(h.Path, w)
	if captureErr != nil {
		if stale != nil {
			_ = stale.Body.Close()
		}
		return next.ServeHTTP(w, request)
	}
	defer captured.Close()
	err := callNext(captured, originRequest, next)
	status := captured.Status()
	incomplete := responseIncomplete(originRequest.Method, status, captured.Header(), captured.Size())
	if err != nil || incomplete || staleEligibleStatus(status) {
		if stale != nil && withinStaleWindow(metadata.FreshUntil, h.StaleIfErrorTTL) {
			return h.serveCached(w, stale, "STALE", baseRaw, metadata)
		}
		if stale != nil {
			_ = stale.Body.Close()
		}
		// After any 1xx was already flushed to the client, do not retry the
		// full upstream chain — that can emit another 1xx/final response pair.
		if captured.writeErr != nil && !captured.wrote1xx {
			return next.ServeHTTP(w, request)
		}
		if err != nil || incomplete || captured.writeErr != nil {
			writeBadGateway(w)
			return nil
		}
		return captured.WriteResponse(w)
	}
	if status == http.StatusNotModified && stale != nil {
		return h.handleNotModified(w, request, stale, captured.Header(), baseRaw, baseKey)
	}
	if stale != nil {
		_ = stale.Body.Close()
	}
	h.prepareResponse(captured.Header(), status)
	varied, varyOK := h.variedHeaders(request, captured.Header())
	if !h.cacheable(request, status, captured.Header(), uint64(captured.Size())) || !varyOK {
		h.setResultHeaders(captured.Header(), "BYPASS", baseRaw, 0)
		return captured.WriteResponse(w)
	}
	headerBytes, err := serializedHeader(status, h.cacheStorageHeader(captured.Header()), captured.Size(), request.Method)
	if err == nil {
		variedRaw := h.cacheKey(request, varied)
		variedKey := h.storageKey(variedRaw)
		ttl := h.ttl(status)
		if _, err = captured.Seek(0, io.SeekStart); err == nil {
			groups := surrogateGroups(captured.Header(), h.SurrogateKeyHeader)
			err = h.storage.PutReader(baseKey, variedKey, io.MultiReader(bytes.NewReader(headerBytes), captured), uint64(len(headerBytes))+uint64(captured.Size()), groups, varied, captured.Header().Get("ETag"), time.Duration(ttl)*time.Second, purgeKey(request))
		}
	}
	h.setResultHeaders(captured.Header(), "MISS", baseRaw, h.ttl(status))
	if _, ranged := cacherange.FromContext(request.Context()); ranged && err == nil {
		fresh, _, storedMetadata := h.storage.LookupEntry(baseKey, request)
		if fresh != nil {
			return h.serveCached(w, fresh, "MISS", baseRaw, storedMetadata)
		}
	}
	return captured.WriteResponse(w)
}

func (h *Handler) refresh(request *http.Request, next caddyhttp.Handler, baseRaw, baseKey string) {
	lookupRequest := request.Clone(context.Background())
	lookupRequest.Header = request.Header.Clone()
	_, stale, metadata := h.storage.LookupEntry(baseKey, lookupRequest)
	applyRevalidatedHeaders(stale, metadata.Headers)
	if stale == nil {
		return
	}
	defer stale.Body.Close()
	originRequest := request.Clone(request.Context())
	originRequest.Header = request.Header.Clone()
	if etag := stale.Header.Get("ETag"); etag != "" {
		originRequest.Header.Set("If-None-Match", etag)
	} else if modified := stale.Header.Get("Last-Modified"); modified != "" {
		originRequest.Header.Set("If-Modified-Since", modified)
	}
	captured, err := newCapturedResponse(h.Path, nil)
	if err != nil {
		return
	}
	defer captured.Close()
	if err := callNext(captured, originRequest, next); err != nil || responseIncomplete(originRequest.Method, captured.Status(), captured.Header(), captured.Size()) || staleEligibleStatus(captured.Status()) {
		return
	}
	if captured.Status() == http.StatusNotModified {
		_ = h.storage.Refresh(baseKey, lookupRequest, time.Duration(h.ttl(stale.StatusCode))*time.Second, captured.Header())
		return
	}
	h.prepareResponse(captured.Header(), captured.Status())
	if !h.cacheable(request, captured.Status(), captured.Header(), uint64(captured.Size())) {
		return
	}
	headerBytes, err := serializedHeader(captured.Status(), h.cacheStorageHeader(captured.Header()), captured.Size(), request.Method)
	varied, ok := h.variedHeaders(request, captured.Header())
	if err == nil && ok {
		if _, err = captured.Seek(0, io.SeekStart); err == nil {
			_ = h.storage.PutReader(baseKey, h.storageKey(h.cacheKey(request, varied)), io.MultiReader(bytes.NewReader(headerBytes), captured), uint64(len(headerBytes))+uint64(captured.Size()), surrogateGroups(captured.Header(), h.SurrogateKeyHeader), varied, captured.Header().Get("ETag"), time.Duration(h.ttl(captured.Status()))*time.Second, purgeKey(request))
		}
	}
}

func (h *Handler) handleNotModified(w http.ResponseWriter, request *http.Request, stale *http.Response, update http.Header, baseRaw, baseKey string) error {
	merge304Headers(stale.Header, update)
	h.prepareResponse(stale.Header, stale.StatusCode)
	ttl := h.ttl(stale.StatusCode)
	_ = h.storage.Refresh(baseKey, request, time.Duration(ttl)*time.Second, update)
	now := time.Now()
	return h.serveCached(w, stale, "HIT", baseRaw, simplefs.LookupMetadata{StoredAt: now, FreshUntil: now.Add(time.Duration(ttl) * time.Second)})
}

func (h *Handler) serveCached(w http.ResponseWriter, response *http.Response, result, key string, metadata ...simplefs.LookupMetadata) error {
	defer response.Body.Close()
	header := response.Header
	if h.SurrogateKeyHeader != "" && !strings.EqualFold(h.SurrogateKeyHeader, "Surrogate-Key") {
		header.Del("Surrogate-Key")
	}
	ttl := cacheStatusTTL(response.Header.Get("Cache-Control"))
	if len(metadata) > 0 {
		if !metadata[0].FreshUntil.IsZero() {
			ttl = max(0, int(time.Until(metadata[0].FreshUntil).Seconds()))
		}
		if h.Age && !metadata[0].StoredAt.IsZero() {
			header.Set("Age", strconv.FormatInt(max(0, int64(time.Since(metadata[0].StoredAt)/time.Second)), 10))
		}
	}
	h.setResultHeaders(header, result, key, ttl)
	transferHeader(w.Header(), header)
	w.WriteHeader(response.StatusCode)
	if response.Body != http.NoBody {
		_, err := io.Copy(w, response.Body)
		return err
	}
	return nil
}

func (h *Handler) cacheable(request *http.Request, status int, header http.Header, size uint64) bool {
	if _, ok := cacheableStatus[status]; !ok || status == http.StatusNotModified {
		return false
	}
	if h.ttl(status) <= 0 || (h.MaxBodyBytes > 0 && size > h.MaxBodyBytes) || header.Get("Set-Cookie") != "" {
		return false
	}
	control := strings.Join(header.Values("Cache-Control"), ",")
	return !hasDirective(control, "private") && !hasDirective(control, "no-store") && !hasDirective(control, "no-cache") &&
		!hasDirective(control, "must-revalidate") && !hasDirective(control, "proxy-revalidate")
}

func (h *Handler) prepareResponse(header http.Header, status int) {
	header.Del("Age")
	header.Del("Date")
	header.Del("Cache-Status")
	header.Del("X-Cache")
	header.Del("X-Goveto-Origin-Content-Length")
	header.Del("X-Goveto-Origin-Method")
	header.Del("X-Goveto-Cache-Method")
	if header.Get("Set-Cookie") != "" || matchesConfigured(strings.Join(header.Values("Cache-Control"), ","), h.BypassCacheControl) {
		header.Set("Cache-Control", "private, no-store")
		return
	}
	ttl := h.ttl(status)
	control := strings.Join(header.Values("Cache-Control"), ",")
	if ttl <= 0 || hasDirective(control, "private") || hasDirective(control, "no-store") || hasDirective(control, "no-cache") || hasDirective(control, "must-revalidate") || hasDirective(control, "proxy-revalidate") {
		return
	}
	if strings.TrimSpace(control) == "" {
		control = "public"
	}
	control = replaceDirective(control, "s-maxage", strconv.Itoa(ttl))
	if h.OverrideClientTTL {
		control = replaceDirective(control, "max-age", strconv.Itoa(h.ClientTTL))
	} else if !hasDirective(control, "max-age") {
		control = replaceDirective(control, "max-age", strconv.Itoa(ttl))
	}
	if h.StaleWhileRevalidateTTL > 0 && !hasDirective(control, "stale-while-revalidate") {
		control += ", stale-while-revalidate=" + strconv.Itoa(h.StaleWhileRevalidateTTL)
	}
	if h.StaleIfErrorTTL > 0 && !hasDirective(control, "stale-if-error") {
		control += ", stale-if-error=" + strconv.Itoa(h.StaleIfErrorTTL)
	}
	header.Set("Cache-Control", control)
}

func (h *Handler) cacheStorageHeader(header http.Header) http.Header {
	if h.SurrogateKeyHeader == "" || strings.EqualFold(h.SurrogateKeyHeader, "Surrogate-Key") {
		return header
	}
	stored := header.Clone()
	if values := header.Values(h.SurrogateKeyHeader); len(values) > 0 {
		stored["Surrogate-Key"] = append([]string(nil), values...)
	} else {
		stored.Del("Surrogate-Key")
	}
	return stored
}

func (h *Handler) ttl(status int) int {
	if configured, ok := h.StatusTTL[strconv.Itoa(status)]; ok {
		return configured
	}
	return h.DefaultTTL
}

func (h *Handler) variedHeaders(request *http.Request, response http.Header) (http.Header, bool) {
	names := make(map[string]struct{}, len(h.KeyHeaders)+2)
	for _, name := range h.KeyHeaders {
		name = http.CanonicalHeaderKey(strings.TrimSpace(name))
		if name != "" && !strings.EqualFold(name, "Accept-Encoding") {
			names[name] = struct{}{}
		}
	}
	for _, value := range response.Values("Vary") {
		for _, name := range strings.Split(value, ",") {
			name = http.CanonicalHeaderKey(strings.TrimSpace(name))
			if name == "*" {
				return nil, false
			}
			if name != "" && !strings.EqualFold(name, "Accept-Encoding") {
				names[name] = struct{}{}
			}
		}
	}
	result := make(http.Header, len(names))
	for name := range names {
		result[name] = append([]string(nil), request.Header.Values(name)...)
	}
	return result, true
}

func (h *Handler) cacheKey(request *http.Request, varied http.Header) string {
	var key strings.Builder
	appendField(&key, "site", h.SiteID)
	for _, part := range h.KeyParts {
		switch part {
		case policy.CacheKeyPartMethod:
			appendField(&key, "method", request.Method)
		case policy.CacheKeyPartScheme:
			scheme := request.URL.Scheme
			if scheme == "" {
				if request.TLS != nil {
					scheme = "https"
				} else {
					scheme = "http"
				}
			}
			appendField(&key, "scheme", scheme)
		case policy.CacheKeyPartHost:
			appendField(&key, "host", strings.ToLower(request.Host))
		case policy.CacheKeyPartPath:
			appendField(&key, "path", request.URL.EscapedPath())
		case policy.CacheKeyPartQuery:
			appendField(&key, "query", normalizeQuery(request.URL.RawQuery, h.KeyQueryNormalize, h.KeyQueryInclude, h.KeyQueryExclude))
		}
	}
	if varied == nil {
		for _, name := range h.KeyHeaders {
			if !strings.EqualFold(name, "Accept-Encoding") {
				appendHeaderField(&key, name, request.Header.Values(name))
			}
		}
	} else {
		names := make([]string, 0, len(varied))
		for name := range varied {
			names = append(names, name)
		}
		sortStrings(names)
		for _, name := range names {
			appendHeaderField(&key, name, varied.Values(name))
		}
	}
	if len(h.KeyCookies) > 0 {
		cookies := request.Cookies()
		for _, name := range h.KeyCookies {
			var values []string
			for _, cookie := range cookies {
				if cookie.Name == name {
					values = append(values, cookie.Value)
				}
			}
			appendField(&key, "cookie", name)
			for _, value := range values {
				appendField(&key, "value", value)
			}
			appendField(&key, "count", strconv.Itoa(len(values)))
		}
	}
	return key.String()
}

func appendField(target *strings.Builder, name, value string) {
	target.WriteString(strconv.Itoa(len(name)))
	target.WriteByte(':')
	target.WriteString(name)
	target.WriteString(strconv.Itoa(len(value)))
	target.WriteByte(':')
	target.WriteString(value)
}

func appendHeaderField(target *strings.Builder, name string, values []string) {
	appendField(target, "header", http.CanonicalHeaderKey(name))
	for _, value := range values {
		appendField(target, "value", value)
	}
	appendField(target, "count", strconv.Itoa(len(values)))
}

// normalizeQuery canonicalizes the query component of a cache key. Sorting
// collapses reordered parameters (?b=2&a=1 ?a=1&b=2) into one entry, and
// include/exclude lists drop tracking parameters (utm_*) and similar noise.
// The include/exclude match is case-insensitive and honors a trailing "*"
// prefix wildcard.
func normalizeQuery(raw string, sorted bool, include, exclude []string) string {
	if !sorted && len(include) == 0 && len(exclude) == 0 {
		return raw
	}
	pairs := parseQueryPairs(raw)
	filter := len(include) > 0 || len(exclude) > 0
	if filter {
		var includePatterns, excludePatterns []string
		for _, name := range include {
			includePatterns = append(includePatterns, strings.ToLower(name))
		}
		for _, name := range exclude {
			excludePatterns = append(excludePatterns, strings.ToLower(name))
		}
		kept := pairs[:0]
		for _, pair := range pairs {
			lower := strings.ToLower(pair.key)
			if len(includePatterns) > 0 {
				if !matchAnyParam(lower, includePatterns) {
					continue
				}
			} else if matchAnyParam(lower, excludePatterns) {
				continue
			}
			kept = append(kept, pair)
		}
		pairs = kept
	}
	if !sorted {
		var builder strings.Builder
		for i, pair := range pairs {
			if i > 0 {
				builder.WriteByte('&')
			}
			builder.WriteString(url.QueryEscape(pair.key))
			builder.WriteByte('=')
			builder.WriteString(pair.value)
		}
		return builder.String()
	}
	sort.SliceStable(pairs, func(i, j int) bool {
		return pairs[i].key < pairs[j].key
	})
	values := make(url.Values, len(pairs))
	for _, pair := range pairs {
		values[pair.key] = append(values[pair.key], pair.value)
	}
	return values.Encode()
}

type queryPair struct {
	key   string
	value string
}

func parseQueryPairs(raw string) []queryPair {
	if raw == "" {
		return nil
	}
	pairs := make([]queryPair, 0, strings.Count(raw, "&")+1)
	for raw != "" {
		element := raw
		if i := strings.IndexByte(raw, '&'); i >= 0 {
			element = raw[:i]
			raw = raw[i+1:]
		} else {
			raw = ""
		}
		if element == "" {
			continue
		}
		key := element
		value := ""
		if i := strings.IndexByte(element, '='); i >= 0 {
			key = element[:i]
			value = element[i+1:]
		}
		if decoded, err := url.QueryUnescape(key); err == nil {
			key = decoded
		}
		if decoded, err := url.QueryUnescape(value); err == nil {
			value = decoded
		}
		pairs = append(pairs, queryPair{key: key, value: value})
	}
	return pairs
}

func matchAnyParam(name string, patterns []string) bool {
	for _, pattern := range patterns {
		if strings.HasSuffix(pattern, "*") {
			if strings.HasPrefix(name, strings.TrimSuffix(pattern, "*")) {
				return true
			}
		} else if name == pattern {
			return true
		}
	}
	return false
}

func purgeKey(request *http.Request) string {
	scheme := request.URL.Scheme
	if scheme == "" {
		if request.TLS != nil {
			scheme = "https"
		} else {
			scheme = "http"
		}
	}
	return request.Method + "-" + scheme + "-" + strings.ToLower(request.Host) + "-" + request.URL.RequestURI()
}

func (h *Handler) storageKey(raw string) string {
	if !h.HashKey {
		return raw
	}
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func (h *Handler) setResultHeaders(header http.Header, result, key string, ttl int) {
	if h.Debug {
		status := "Goveto; " + strings.ToLower(result)
		if ttl >= 0 {
			status += "; ttl=" + strconv.Itoa(ttl)
		}
		if !h.HideKey {
			exposed := key
			if h.HashKey {
				exposed = h.storageKey(key)
			}
			status += "; key=\"" + strings.ReplaceAll(exposed, "\"", "\\\"") + "\""
		}
		header.Set("Cache-Status", status)
	} else {
		header.Del("Cache-Status")
	}
	if h.XCache {
		header.Set("X-Cache", result)
	} else {
		header.Del("X-Cache")
	}
	if h.Age && (result == "HIT" || result == "STALE") {
		if header.Get("Age") == "" {
			header.Set("Age", "0")
		}
	} else {
		header.Del("Age")
	}
}

func serializedHeader(status int, header http.Header, bodySize int64, method string) ([]byte, error) {
	header = header.Clone()
	header.Del("Transfer-Encoding")
	contentLength := bodySize
	if method == http.MethodHead {
		header.Set("X-Goveto-Cache-Method", http.MethodHead)
		if declared, err := strconv.ParseInt(header.Get("Content-Length"), 10, 64); err == nil {
			contentLength = declared
		}
	}
	if method != http.MethodHead && status != http.StatusNoContent && status != http.StatusNotModified {
		header.Set("Content-Length", strconv.FormatInt(bodySize, 10))
	}
	response := &http.Response{
		StatusCode: status, Status: strconv.Itoa(status) + " " + http.StatusText(status), ProtoMajor: 1, ProtoMinor: 1,
		Header: header, Body: http.NoBody, ContentLength: contentLength,
	}
	return httputil.DumpResponse(response, false)
}

func surrogateGroups(header http.Header, configured string) []string {
	if configured == "" {
		configured = "Surrogate-Key"
	}
	return strings.FieldsFunc(strings.Join(header.Values(configured), " "), func(value rune) bool {
		return value == ' ' || value == ',' || value == '\t'
	})
}

func responseIncomplete(method string, status int, header http.Header, actual int64) bool {
	if method == http.MethodHead || status < 200 || status == http.StatusNoContent || status == http.StatusNotModified {
		return false
	}
	expected, err := strconv.ParseInt(header.Get("Content-Length"), 10, 64)
	return err == nil && expected >= 0 && actual != expected
}

func callNext(w http.ResponseWriter, r *http.Request, next caddyhttp.Handler) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			if recoveredErr, ok := recovered.(error); ok && errors.Is(recoveredErr, http.ErrAbortHandler) {
				err = io.ErrUnexpectedEOF
				return
			}
			panic(recovered)
		}
	}()
	return next.ServeHTTP(w, r)
}

func staleEligibleStatus(status int) bool {
	return status == 500 || status == 502 || status == 503 || status == 504
}

func withinStaleWindow(freshUntil time.Time, seconds int) bool {
	return seconds > 0 && !freshUntil.IsZero() && time.Now().Before(freshUntil.Add(time.Duration(seconds)*time.Second))
}

func writeBadGateway(w http.ResponseWriter) {
	w.Header().Del("Content-Length")
	w.Header().Del("Content-Range")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusBadGateway)
}

// transferHeader moves ownership of source's value slices to the response writer.
func transferHeader(target, source http.Header) {
	clear(target)
	for name, values := range source {
		target[name] = values
	}
}

func merge304Headers(target, update http.Header) {
	for _, name := range []string{"Cache-Control", "Content-Location", "Date", "ETag", "Expires", "Last-Modified", "Vary"} {
		if values, ok := update[name]; ok {
			target[name] = append([]string(nil), values...)
		}
	}
}

func applyRevalidatedHeaders(response *http.Response, update http.Header) {
	if response == nil {
		return
	}
	merge304Headers(response.Header, update)
}

func hasDirective(value, directive string) bool {
	for _, part := range splitCacheControl(value) {
		name := strings.TrimSpace(strings.SplitN(part, "=", 2)[0])
		if strings.EqualFold(name, directive) {
			return true
		}
	}
	return false
}

func matchesConfigured(value string, configured []string) bool {
	for _, part := range splitCacheControl(value) {
		part = strings.ToLower(strings.ReplaceAll(strings.TrimSpace(part), " ", ""))
		name := strings.SplitN(part, "=", 2)[0]
		for _, candidate := range configured {
			candidate = strings.ToLower(strings.ReplaceAll(strings.TrimSpace(candidate), " ", ""))
			if part == candidate || (!strings.Contains(candidate, "=") && name == candidate) {
				return true
			}
		}
	}
	return false
}

func replaceDirective(value, directive, replacement string) string {
	parts := splitCacheControl(value)
	replaced := false
	kept := parts[:0]
	for _, part := range parts {
		name := strings.TrimSpace(strings.SplitN(part, "=", 2)[0])
		if strings.EqualFold(name, directive) {
			if !replaced {
				kept = append(kept, directive+"="+replacement)
				replaced = true
			}
			continue
		}
		if strings.TrimSpace(part) != "" {
			kept = append(kept, strings.TrimSpace(part))
		}
	}
	if !replaced {
		kept = append(kept, directive+"="+replacement)
	}
	return strings.Join(kept, ", ")
}

func splitCacheControl(value string) []string {
	var result []string
	start := 0
	quoted, escaped := false, false
	for index, current := range value {
		if escaped {
			escaped = false
			continue
		}
		if current == '\\' && quoted {
			escaped = true
			continue
		}
		if current == '"' {
			quoted = !quoted
			continue
		}
		if current == ',' && !quoted {
			result = append(result, value[start:index])
			start = index + 1
		}
	}
	return append(result, value[start:])
}

func cacheStatusTTL(control string) int {
	for _, directive := range []string{"s-maxage", "max-age"} {
		for _, part := range splitCacheControl(control) {
			name, value, ok := strings.Cut(strings.TrimSpace(part), "=")
			if ok && strings.EqualFold(name, directive) {
				ttl, _ := strconv.Atoi(strings.TrimSpace(value))
				return ttl
			}
		}
	}
	return 0
}

func detachedRequest(request *http.Request) *http.Request {
	ctx := context.WithoutCancel(request.Context())
	if variables, ok := ctx.Value(caddyhttp.VarsCtxKey).(map[string]any); ok {
		isolated := make(map[string]any, len(variables))
		for key, value := range variables {
			isolated[key] = value
		}
		ctx = context.WithValue(ctx, caddyhttp.VarsCtxKey, isolated)
	}
	ctx = context.WithValue(ctx, caddyhttp.ExtraLogFieldsCtxKey, new(caddyhttp.ExtraLogFields))
	clone := request.Clone(ctx)
	clone.Header = request.Header.Clone()
	return clone
}

func sortStrings(values []string) {
	for index := 1; index < len(values); index++ {
		for current := index; current > 0 && values[current] < values[current-1]; current-- {
			values[current], values[current-1] = values[current-1], values[current]
		}
	}
}

type capturedResponse struct {
	header      http.Header
	status      int
	size        int64
	file        *os.File
	path        string
	downstream  http.ResponseWriter
	wroteHeader bool
	wrote1xx    bool
	writeErr    error
}

func newCapturedResponse(directory string, downstream http.ResponseWriter) (*capturedResponse, error) {
	file, err := os.CreateTemp(directory, ".goveto-origin-*")
	if err != nil {
		return nil, err
	}
	if err = file.Chmod(0o640); err != nil {
		_ = file.Close()
		_ = os.Remove(file.Name())
		return nil, err
	}
	return &capturedResponse{header: http.Header{}, file: file, path: file.Name(), downstream: downstream}, nil
}

func (w *capturedResponse) Header() http.Header { return w.header }

func (w *capturedResponse) WriteHeader(status int) {
	if w.wroteHeader {
		return
	}
	if status >= 100 && status < 200 && status != http.StatusSwitchingProtocols {
		w.forwardInformational(status)
		return
	}
	w.wroteHeader = true
	w.status = status
}

// forwardInformational passes an interim 1xx response through to the real
// client without latching it as the final status. The caller (Caddy's
// reverseproxy Got1xxResponse hook) has already copied the 1xx headers into
// w.header and clears them after WriteHeader returns; the downstream header
// map is cleared here because Go's server does not do that automatically
// after a 1xx WriteHeader.
func (w *capturedResponse) forwardInformational(status int) {
	if w.downstream == nil {
		return
	}
	header := w.downstream.Header()
	for name, values := range w.header {
		header[name] = append([]string(nil), values...)
	}
	w.downstream.WriteHeader(status)
	clear(header)
	w.wrote1xx = true
}

func (w *capturedResponse) Write(value []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	count, err := w.file.Write(value)
	w.size += int64(count)
	if err != nil {
		w.writeErr = err
	}
	return count, err
}

func (w *capturedResponse) Read(value []byte) (int, error) { return w.file.Read(value) }
func (w *capturedResponse) Seek(offset int64, whence int) (int64, error) {
	return w.file.Seek(offset, whence)
}
func (w *capturedResponse) Flush() {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
}
func (w *capturedResponse) Status() int {
	if w.status == 0 {
		return http.StatusOK
	}
	return w.status
}
func (w *capturedResponse) Size() int64 { return w.size }

func (w *capturedResponse) WriteResponse(target http.ResponseWriter) error {
	if _, err := w.Seek(0, io.SeekStart); err != nil {
		return err
	}
	transferHeader(target.Header(), w.header)
	target.WriteHeader(w.Status())
	_, err := io.Copy(target, w)
	return err
}

func (w *capturedResponse) Close() error {
	err := w.file.Close()
	return errors.Join(err, os.Remove(w.path))
}

var (
	_ caddy.Provisioner           = (*Handler)(nil)
	_ caddy.CleanerUpper          = (*Handler)(nil)
	_ caddyhttp.MiddlewareHandler = (*Handler)(nil)
)
