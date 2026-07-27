package cacheheaders

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
)

type Handler struct {
	XCache               bool           `json:"x_cache"`
	Age                  bool           `json:"age"`
	DefaultTTL           int            `json:"default_ttl"`
	StatusTTL            map[string]int `json:"status_ttl,omitempty"`
	StaleIfErrorTTL      int            `json:"stale_if_error_ttl,omitempty"`
	StaleWhileTTL        int            `json:"stale_while_revalidate_ttl,omitempty"`
	BackgroundRevalidate bool           `json:"background_revalidate,omitempty"`
	ValidateUpstream     bool           `json:"validate_upstream,omitempty"`
	SiteID               string         `json:"site_id,omitempty"`
	Coalesce             bool           `json:"coalesce,omitempty"`
	CoalesceHeaders      []string       `json:"coalesce_headers,omitempty"`
}

var ErrIncompleteResponse = errors.New("upstream response ended before its declared content length")
var errRangeProcessing = errors.New("cache range response processing failed")

var requestLocks = struct {
	sync.Mutex
	values       map[string]*requestLock
	revalidating map[string]struct{}
}{values: map[string]*requestLock{}, revalidating: map[string]struct{}{}}

type requestLock struct {
	mu   sync.Mutex
	refs int
}

func init() { caddy.RegisterModule(Handler{}) }

func (Handler) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{ID: "http.handlers.goveto_cache_headers", New: func() caddy.Module { return new(Handler) }}
}

func (h Handler) ServeHTTP(w http.ResponseWriter, r *http.Request, next caddyhttp.Handler) error {
	key := h.coalescingKey(r)
	var unlock func()
	if h.Coalesce {
		unlock = acquireRequestLock(key)
		defer func() {
			if unlock != nil {
				unlock()
			}
		}()
	}
	clientRequiresRevalidation := requestRequiresRevalidation(r)
	if clientRequiresRevalidation && requestCacheControl(r) == "" {
		r = r.Clone(r.Context())
		r.Header = r.Header.Clone()
		r.Header.Set("Cache-Control", "no-cache")
	}
	if h.StaleWhileTTL > 0 && !clientRequiresRevalidation &&
		!hasCacheDirective(requestCacheControl(r), "max-stale") {
		r = r.Clone(r.Context())
		r.Header = r.Header.Clone()
		value := strings.TrimSpace(requestCacheControl(r))
		if value != "" {
			value += ", "
		}
		r.Header.Set("Cache-Control", value+"max-stale="+strconv.Itoa(h.StaleWhileTTL))
	}
	wrapped := &responseWriter{
		ResponseWriterWrapper: &caddyhttp.ResponseWriterWrapper{ResponseWriter: w}, policy: h,
		expectedLength: -1, requestMethod: r.Method,
	}
	if h.ValidateUpstream {
		buffer := new(bytes.Buffer)
		recorder := caddyhttp.NewResponseRecorder(w, buffer, func(int, http.Header) bool { return true })
		wrapped.ResponseWriterWrapper = &caddyhttp.ResponseWriterWrapper{ResponseWriter: recorder}
		err := serveValidated(wrapped, r, next)
		if err != nil || wrapped.incomplete(r.Method) {
			writeUpstreamFailure(w, buffer)
			return nil
		}
		return recorder.WriteResponse()
	}
	err := serveDownstream(wrapped, r, next)
	if err != nil {
		if rangeWasNotSatisfiable(r, wrapped.Header()) {
			writeRangeNotSatisfiable(w)
			return nil
		}
		wrapped.Header().Set("Cache-Control", "no-store")
		return err
	}
	if wrapped.incomplete(r.Method) {
		wrapped.Header().Set("Cache-Control", "no-store")
		return ErrIncompleteResponse
	}
	responseCacheControl := cacheControlValue(wrapped.Header())
	mandatoryRevalidation := hasCacheDirective(responseCacheControl, "no-cache") ||
		hasCacheDirective(responseCacheControl, "must-revalidate") ||
		hasCacheDirective(responseCacheControl, "proxy-revalidate")
	if h.BackgroundRevalidate && !clientRequiresRevalidation && !mandatoryRevalidation &&
		wrapped.cacheState == "STALE" {
		_ = http.NewResponseController(w).Flush()
		if beginRevalidation(key) {
			if unlock != nil {
				unlock()
				unlock = nil
			}
			func() {
				defer finishRevalidation(key)
				revalidate(r, next)
			}()
		}
	}
	return nil
}

func serveDownstream(w http.ResponseWriter, r *http.Request, next caddyhttp.Handler) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			if recoveredErr, ok := recovered.(error); ok && errors.Is(recoveredErr, http.ErrAbortHandler) {
				err = ErrIncompleteResponse
				return
			}
			if rangeWasNotSatisfiable(r, w.Header()) {
				err = errRangeProcessing
				return
			}
			panic(recovered)
		}
	}()
	return next.ServeHTTP(w, r)
}

func rangeWasNotSatisfiable(request *http.Request, header http.Header) bool {
	return request.Header.Get("Range") != "" &&
		strings.HasPrefix(strings.ToLower(strings.TrimSpace(header.Get("Content-Range"))), "bytes */")
}

func writeRangeNotSatisfiable(w http.ResponseWriter) {
	header := w.Header()
	header.Del("Content-Length")
	header.Del("Cache-Status")
	header.Del("X-Cache")
	header.Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
}

func serveValidated(w http.ResponseWriter, r *http.Request, next caddyhttp.Handler) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			if recoveredErr, ok := recovered.(error); ok && errors.Is(recoveredErr, http.ErrAbortHandler) {
				err = ErrIncompleteResponse
				return
			}
			panic(recovered)
		}
	}()
	return next.ServeHTTP(w, r)
}

func writeUpstreamFailure(w http.ResponseWriter, buffer *bytes.Buffer) {
	buffer.Reset()
	header := w.Header()
	header.Del("Content-Length")
	header.Del("Content-Range")
	header.Del("Etag")
	header.Del("Last-Modified")
	header.Del("X-Goveto-Origin-Content-Length")
	header.Del("X-Goveto-Origin-Method")
	header.Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusBadGateway)
}

func (h Handler) coalescingKey(request *http.Request) string {
	var key strings.Builder
	key.WriteString(h.SiteID)
	key.WriteByte('\x00')
	key.WriteString(request.Method)
	key.WriteByte('\x00')
	key.WriteString(request.Host)
	key.WriteByte('\x00')
	key.WriteString(request.URL.RequestURI())
	for _, header := range h.CoalesceHeaders {
		key.WriteByte('\x00')
		key.WriteString(http.CanonicalHeaderKey(header))
		key.WriteByte('=')
		key.WriteString(strings.Join(request.Header.Values(header), "\x00"))
	}
	return key.String()
}

func acquireRequestLock(key string) func() {
	requestLocks.Lock()
	lock := requestLocks.values[key]
	if lock == nil {
		lock = new(requestLock)
		requestLocks.values[key] = lock
	}
	lock.refs++
	requestLocks.Unlock()

	lock.mu.Lock()
	return func() {
		lock.mu.Unlock()
		requestLocks.Lock()
		lock.refs--
		if lock.refs == 0 {
			delete(requestLocks.values, key)
		}
		requestLocks.Unlock()
	}
}

func beginRevalidation(key string) bool {
	requestLocks.Lock()
	defer requestLocks.Unlock()
	if _, ok := requestLocks.revalidating[key]; ok {
		return false
	}
	requestLocks.revalidating[key] = struct{}{}
	return true
}

func finishRevalidation(key string) {
	requestLocks.Lock()
	delete(requestLocks.revalidating, key)
	requestLocks.Unlock()
}

type responseWriter struct {
	*caddyhttp.ResponseWriterWrapper
	policy         Handler
	wroteHeader    bool
	status         int
	written        int64
	cacheState     string
	expectedLength int64
	requestMethod  string
}

func (w *responseWriter) WriteHeader(status int) {
	if w.wroteHeader {
		return
	}
	w.wroteHeader = true
	w.status = status
	header := w.Header()
	if w.policy.ValidateUpstream {
		header.Del("X-Goveto-Origin-Content-Length")
		header.Del("X-Goveto-Origin-Method")
		if expected, err := strconv.ParseInt(header.Get("Content-Length"), 10, 64); err == nil {
			w.expectedLength = expected
			header.Set("X-Goveto-Origin-Content-Length", strconv.FormatInt(expected, 10))
		}
		header.Set("X-Goveto-Origin-Method", w.requestMethod)
		if value := withoutCacheDirective(cacheControlValue(header), "stale-while-revalidate"); value == "" {
			header.Del("Cache-Control")
		} else {
			header.Set("Cache-Control", value)
		}
	} else {
		header.Del("X-Goveto-Origin-Content-Length")
		header.Del("X-Goveto-Origin-Method")
	}
	if status == http.StatusPartialContent {
		normalizeContentRange(header)
	}
	if header.Get("Set-Cookie") != "" {
		header.Set("Cache-Control", "private, no-store")
	} else if cacheControlValue(header) == "" {
		ttl := w.policy.DefaultTTL
		if configured, ok := w.policy.StatusTTL[strconv.Itoa(status)]; ok {
			ttl = configured
		}
		if ttl > 0 {
			cacheControl := "public, max-age=" + strconv.Itoa(ttl)
			if w.policy.StaleWhileTTL > 0 {
				cacheControl += ", stale-while-revalidate=" + strconv.Itoa(w.policy.StaleWhileTTL)
			}
			if w.policy.StaleIfErrorTTL > 0 {
				cacheControl += ", stale-if-error=" + strconv.Itoa(w.policy.StaleIfErrorTTL)
			}
			header.Set("Cache-Control", cacheControl)
		}
	}
	cacheControl := cacheControlValue(header)
	if w.policy.BackgroundRevalidate && w.policy.StaleWhileTTL > 0 && cacheControl != "" &&
		!hasCacheDirective(cacheControl, "private") &&
		!hasCacheDirective(cacheControl, "no-store") &&
		!hasCacheDirective(cacheControl, "no-cache") &&
		!hasCacheDirective(cacheControl, "must-revalidate") &&
		!hasCacheDirective(cacheControl, "proxy-revalidate") &&
		!hasCacheDirective(cacheControl, "stale-while-revalidate") {
		header.Set("Cache-Control", cacheControl+", stale-while-revalidate="+
			strconv.Itoa(w.policy.StaleWhileTTL))
	}
	if w.policy.XCache {
		result := cacheResult(header.Get("Cache-Status"))
		if result == "HIT" && responseIsStale(header) {
			result = "STALE"
		}
		header.Set("X-Cache", result)
		w.cacheState = result
	}
	if !w.policy.Age {
		header.Del("Age")
	}
	w.ResponseWriterWrapper.WriteHeader(status)
}

func responseIsStale(header http.Header) bool {
	age, err := strconv.Atoi(header.Get("Age"))
	if err != nil {
		return false
	}
	maxAge := -1
	for _, part := range splitCacheControl(cacheControlValue(header)) {
		pair := strings.SplitN(strings.TrimSpace(part), "=", 2)
		if len(pair) != 2 {
			continue
		}
		name := strings.ToLower(pair[0])
		if name != "max-age" && name != "s-maxage" {
			continue
		}
		if value, parseErr := strconv.Atoi(strings.Trim(pair[1], `"`)); parseErr == nil &&
			(name == "s-maxage" || maxAge < 0) {
			maxAge = value
		}
	}
	return maxAge >= 0 && age >= maxAge
}

func normalizeContentRange(header http.Header) {
	value := header.Get("Content-Range")
	if !strings.HasPrefix(value, "bytes ") {
		return
	}
	parts := strings.SplitN(strings.TrimPrefix(value, "bytes "), "/", 2)
	positions := strings.SplitN(parts[0], "-", 2)
	if len(parts) != 2 || len(positions) != 2 {
		return
	}
	start, startErr := strconv.ParseInt(positions[0], 10, 64)
	end, endErr := strconv.ParseInt(positions[1], 10, 64)
	length, lengthErr := strconv.ParseInt(header.Get("Content-Length"), 10, 64)
	if startErr == nil && endErr == nil && lengthErr == nil && length > 0 && end-start == length {
		header.Set("Content-Range", "bytes "+strconv.FormatInt(start, 10)+"-"+
			strconv.FormatInt(end-1, 10)+"/"+parts[1])
	}
}

func hasCacheDirective(value, directive string) bool {
	for _, part := range splitCacheControl(value) {
		name := strings.TrimSpace(strings.SplitN(part, "=", 2)[0])
		if strings.EqualFold(name, directive) {
			return true
		}
	}
	return false
}

func requestRequiresRevalidation(request *http.Request) bool {
	if hasCacheDirective(requestCacheControl(request), "no-cache") {
		return true
	}
	return requestCacheControl(request) == "" &&
		strings.EqualFold(strings.TrimSpace(request.Header.Get("Pragma")), "no-cache")
}

func requestCacheControl(request *http.Request) string {
	return cacheControlValue(request.Header)
}

func cacheControlValue(header http.Header) string {
	return strings.Join(header.Values("Cache-Control"), ",")
}

func withoutCacheDirective(value, directive string) string {
	kept := make([]string, 0, 4)
	for _, part := range splitCacheControl(value) {
		name := strings.TrimSpace(strings.SplitN(part, "=", 2)[0])
		if !strings.EqualFold(name, directive) {
			kept = append(kept, strings.TrimSpace(part))
		}
	}
	return strings.Join(kept, ", ")
}

func splitCacheControl(value string) []string {
	parts := make([]string, 0, 4)
	start := 0
	quoted := false
	escaped := false
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
			parts = append(parts, value[start:index])
			start = index + 1
		}
	}
	parts = append(parts, value[start:])
	return parts
}

func (w *responseWriter) Write(body []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	n, err := w.ResponseWriterWrapper.Write(body)
	w.written += int64(n)
	return n, err
}

func (w *responseWriter) incomplete(method string) bool {
	if method == http.MethodHead || w.status < http.StatusOK || w.status == http.StatusNoContent ||
		w.status == http.StatusNotModified {
		return false
	}
	declared := w.expectedLength
	var err error
	if declared < 0 {
		declared, err = strconv.ParseInt(w.Header().Get("Content-Length"), 10, 64)
	}
	return err == nil && declared >= 0 && w.written != declared
}

func revalidate(request *http.Request, next caddyhttp.Handler) {
	clone := request.Clone(request.Context())
	clone.Header = request.Header.Clone()
	clone.Header.Set("Cache-Control", "no-cache")
	_ = next.ServeHTTP(&discardResponseWriter{header: http.Header{}}, clone)
}

type discardResponseWriter struct{ header http.Header }

func (w *discardResponseWriter) Header() http.Header          { return w.header }
func (*discardResponseWriter) WriteHeader(int)                {}
func (*discardResponseWriter) Write(body []byte) (int, error) { return io.Discard.Write(body) }

func cacheResult(value string) string {
	lower := strings.ToLower(value)
	switch {
	case strings.Contains(lower, "fwd=stale"):
		return "STALE"
	case strings.Contains(lower, "hit"):
		return "HIT"
	case strings.Contains(lower, "uri-miss") || strings.Contains(lower, "stored"):
		return "MISS"
	default:
		return "BYPASS"
	}
}

var _ caddyhttp.MiddlewareHandler = (*Handler)(nil)
