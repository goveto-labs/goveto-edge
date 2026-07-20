package cacheheaders

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
)

type Handler struct {
	XCache          bool           `json:"x_cache"`
	Age             bool           `json:"age"`
	DefaultTTL      int            `json:"default_ttl"`
	StatusTTL       map[string]int `json:"status_ttl,omitempty"`
	StaleIfErrorTTL int            `json:"stale_if_error_ttl,omitempty"`
}

func init() { caddy.RegisterModule(Handler{}) }

func (Handler) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{ID: "http.handlers.goveto_cache_headers", New: func() caddy.Module { return new(Handler) }}
}

func (h Handler) ServeHTTP(w http.ResponseWriter, r *http.Request, next caddyhttp.Handler) error {
	return next.ServeHTTP(&responseWriter{ResponseWriterWrapper: &caddyhttp.ResponseWriterWrapper{ResponseWriter: w}, policy: h}, r)
}

type responseWriter struct {
	*caddyhttp.ResponseWriterWrapper
	policy      Handler
	wroteHeader bool
}

func (w *responseWriter) WriteHeader(status int) {
	if w.wroteHeader {
		return
	}
	w.wroteHeader = true
	header := w.Header()
	if header.Get("Cache-Control") == "" {
		if header.Get("Set-Cookie") != "" {
			header.Set("Cache-Control", "private, no-store")
		} else {
			ttl := w.policy.DefaultTTL
			if configured, ok := w.policy.StatusTTL[strconv.Itoa(status)]; ok {
				ttl = configured
			}
			if ttl > 0 {
				cacheControl := "public, max-age=" + strconv.Itoa(ttl)
				if w.policy.StaleIfErrorTTL > 0 {
					cacheControl += ", stale-if-error=" + strconv.Itoa(w.policy.StaleIfErrorTTL)
				}
				header.Set("Cache-Control", cacheControl)
			}
		}
	}
	if w.policy.XCache {
		header.Set("X-Cache", cacheResult(header.Get("Cache-Status")))
	}
	if !w.policy.Age {
		header.Del("Age")
	}
	w.ResponseWriterWrapper.WriteHeader(status)
}

func (w *responseWriter) Write(body []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriterWrapper.Write(body)
}

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
