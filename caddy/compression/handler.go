package compression

import (
	"bytes"
	"compress/zlib"
	"fmt"
	"io"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"

	"github.com/andybalholm/brotli"
	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp/encode"
	"github.com/klauspost/compress/gzip"
	"github.com/klauspost/compress/zstd"
)

var algorithmPriority = []string{"br", "gzip", "zstd", "deflate"}

type Handler struct {
	Extensions         []string `json:"extensions,omitempty"`
	ExcludedExtensions []string `json:"excluded_extensions,omitempty"`
	MIMETypes          []string `json:"mime_types,omitempty"`
	Recompress         bool     `json:"recompress,omitempty"`
	MinimumLength      int64    `json:"minimum_length,omitempty"`
	MaximumLength      int64    `json:"maximum_length,omitempty"`
	ExcludedPaths      []string `json:"excluded_paths,omitempty"`

	extensions         map[string]struct{}
	excludedExtensions map[string]struct{}
}

func init() { caddy.RegisterModule(Handler{}) }

func (Handler) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{
		ID:  "http.handlers.goveto_compression",
		New: func() caddy.Module { return new(Handler) },
	}
}

func (h *Handler) Provision(caddy.Context) error {
	h.extensions = stringSet(h.Extensions)
	h.excludedExtensions = stringSet(h.ExcludedExtensions)
	return nil
}

func (h Handler) Validate() error {
	if h.MinimumLength < 0 || h.MaximumLength < 1 || h.MinimumLength > h.MaximumLength {
		return fmt.Errorf("invalid compression length range %d-%d", h.MinimumLength, h.MaximumLength)
	}
	return nil
}

func (h Handler) ServeHTTP(w http.ResponseWriter, r *http.Request, next caddyhttp.Handler) error {
	algorithm := preferredAlgorithm(r)
	if algorithm == "" || requestBypassesCompression(r, h.ExcludedPaths, h.excludedExtensions) {
		return next.ServeHTTP(w, r)
	}
	stripEncodingETag(r.Header, algorithm)

	writer := &bufferedResponseWriter{
		ResponseWriter: w,
		maximumLength:  h.MaximumLength,
		status:         http.StatusOK,
	}
	err := next.ServeHTTP(writer, r)
	if err != nil {
		return err
	}
	return writer.finish(r, h, algorithm)
}

type bufferedResponseWriter struct {
	http.ResponseWriter
	buffer        bytes.Buffer
	maximumLength int64
	status        int
	wroteHeader   bool
	passthrough   bool
	writeErr      error
}

func (w *bufferedResponseWriter) WriteHeader(status int) {
	if w.wroteHeader {
		return
	}
	if status >= 100 && status < 200 {
		w.ResponseWriter.WriteHeader(status)
		return
	}
	w.status = status
	w.wroteHeader = true
}

func (w *bufferedResponseWriter) Write(body []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	if w.passthrough {
		return w.ResponseWriter.Write(body)
	}
	if w.buffer.Len() == 0 {
		if contentLength, err := strconv.ParseInt(w.Header().Get("Content-Length"), 10, 64); err == nil && contentLength > w.maximumLength {
			if err = w.startPassthrough(); err != nil {
				return 0, err
			}
			return w.ResponseWriter.Write(body)
		}
	}
	if int64(w.buffer.Len()+len(body)) > w.maximumLength {
		if err := w.startPassthrough(); err != nil {
			return 0, err
		}
		return w.ResponseWriter.Write(body)
	}
	return w.buffer.Write(body)
}

func (w *bufferedResponseWriter) ReadFrom(reader io.Reader) (int64, error) {
	return io.Copy(struct{ io.Writer }{w}, reader)
}

func (w *bufferedResponseWriter) Flush() {
	_ = w.FlushError()
}

func (w *bufferedResponseWriter) FlushError() error {
	if err := w.startPassthrough(); err != nil {
		return err
	}
	return http.NewResponseController(w.ResponseWriter).Flush()
}

func (w *bufferedResponseWriter) startPassthrough() error {
	if w.passthrough {
		return w.writeErr
	}
	w.passthrough = true
	w.ResponseWriter.WriteHeader(w.status)
	if w.buffer.Len() > 0 {
		_, w.writeErr = w.ResponseWriter.Write(w.buffer.Bytes())
		w.buffer.Reset()
	}
	return w.writeErr
}

func (w *bufferedResponseWriter) finish(r *http.Request, h Handler, algorithm string) error {
	if w.passthrough {
		return w.writeErr
	}
	body := w.buffer.Bytes()
	if !responseCanHaveBody(r.Method, w.status) {
		return w.writeResponse(body)
	}

	header := w.Header()
	if strings.Contains(strings.ToLower(header.Get("Cache-Control")), "no-transform") || header.Get("Content-Range") != "" {
		return w.writeResponse(body)
	}

	existingEncoding := strings.ToLower(strings.TrimSpace(header.Get("Content-Encoding")))
	if existingEncoding != "" {
		if !h.Recompress || existingEncoding == algorithm || strings.Contains(existingEncoding, ",") {
			return w.writeResponse(body)
		}
		decoded, err := decodeBody(existingEncoding, body, h.MaximumLength)
		if err != nil {
			return w.writeResponse(body)
		}
		body = decoded
		if int64(len(body)) < h.MinimumLength || int64(len(body)) > h.MaximumLength {
			return w.writeResponse(w.buffer.Bytes())
		}
	} else if int64(len(body)) < h.MinimumLength {
		return w.writeResponse(body)
	}

	contentType := header.Get("Content-Type")
	if contentType == "" && len(body) > 0 {
		contentType = http.DetectContentType(body)
		header.Set("Content-Type", contentType)
	}
	if !h.matches(r.URL.Path, contentType) {
		return w.writeResponse(w.buffer.Bytes())
	}

	compressed, err := encodeBody(algorithm, body)
	if err != nil {
		return err
	}
	header.Set("Content-Encoding", algorithm)
	header.Set("Content-Length", strconv.Itoa(len(compressed)))
	header.Del("Accept-Ranges")
	header.Del("Content-Md5")
	addVary(header, "Accept-Encoding")
	if etag := header.Get("Etag"); etag != "" && !strings.HasPrefix(etag, "W/") {
		if existingEncoding != "" {
			etag = strings.TrimSuffix(strings.TrimSuffix(etag, `"`), "-"+existingEncoding) + `"`
		}
		header.Set("Etag", fmt.Sprintf(`%s-%s"`, strings.TrimSuffix(etag, `"`), algorithm))
	}
	return w.writeResponse(compressed)
}

func (w *bufferedResponseWriter) writeResponse(body []byte) error {
	if w.Header().Get("Content-Length") == "" && responseCanHaveBody(http.MethodGet, w.status) {
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	}
	w.ResponseWriter.WriteHeader(w.status)
	if len(body) == 0 {
		return nil
	}
	_, err := w.ResponseWriter.Write(body)
	return err
}

func (h Handler) matches(requestPath, contentType string) bool {
	extension := strings.TrimPrefix(strings.ToLower(path.Ext(requestPath)), ".")
	if extension != "" {
		if _, excluded := h.excludedExtensions[extension]; excluded {
			return false
		}
		if _, supported := h.extensions[extension]; supported {
			return true
		}
	}
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return false
	}
	mediaType = strings.ToLower(mediaType)
	for _, allowed := range h.MIMETypes {
		if allowed == mediaType || (strings.HasSuffix(allowed, "/*") && strings.HasPrefix(mediaType, strings.TrimSuffix(allowed, "*"))) {
			return true
		}
	}
	return false
}

func requestBypassesCompression(r *http.Request, excludedPaths []string, excludedExtensions map[string]struct{}) bool {
	if r.Method == http.MethodConnect ||
		r.Header.Get("Sec-WebSocket-Key") != "" ||
		strings.Contains(strings.ToLower(r.Header.Get("Connection")), "upgrade") ||
		strings.Contains(strings.ToLower(r.Header.Get("Cache-Control")), "no-transform") {
		return true
	}
	for _, prefix := range excludedPaths {
		if r.URL.Path == prefix || strings.HasPrefix(r.URL.Path, strings.TrimSuffix(prefix, "/")+"/") {
			return true
		}
	}
	extension := strings.TrimPrefix(strings.ToLower(path.Ext(r.URL.Path)), ".")
	_, excluded := excludedExtensions[extension]
	return extension != "" && excluded
}

func stripEncodingETag(header http.Header, algorithm string) {
	etag := header.Get("If-None-Match")
	if etag == "" || strings.HasPrefix(etag, "W/") {
		return
	}
	if value, ok := strings.CutSuffix(etag, "-"+algorithm+`"`); ok {
		header.Set("If-None-Match", value+`"`)
	}
}

func preferredAlgorithm(r *http.Request) string {
	for _, algorithm := range encode.AcceptedEncodings(r, algorithmPriority) {
		for _, supported := range algorithmPriority {
			if algorithm == supported {
				return algorithm
			}
		}
	}
	return ""
}

func responseCanHaveBody(method string, status int) bool {
	return method != http.MethodHead && status != http.StatusNoContent && status != http.StatusNotModified && (status < 100 || status >= 200)
}

func encodeBody(algorithm string, body []byte) ([]byte, error) {
	var output bytes.Buffer
	var writer io.WriteCloser
	switch algorithm {
	case "br":
		writer = brotli.NewWriterLevel(&output, 5)
	case "gzip":
		value, err := gzip.NewWriterLevel(&output, 5)
		if err != nil {
			return nil, err
		}
		writer = value
	case "zstd":
		value, err := zstd.NewWriter(&output, zstd.WithEncoderConcurrency(1), zstd.WithEncoderLevel(zstd.SpeedDefault))
		if err != nil {
			return nil, err
		}
		writer = value
	case "deflate":
		value, err := zlib.NewWriterLevel(&output, 5)
		if err != nil {
			return nil, err
		}
		writer = value
	default:
		return nil, fmt.Errorf("unsupported compression algorithm %q", algorithm)
	}
	if _, err := writer.Write(body); err != nil {
		return nil, err
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func decodeBody(algorithm string, body []byte, maximumLength int64) ([]byte, error) {
	var reader io.ReadCloser
	switch algorithm {
	case "br":
		reader = io.NopCloser(brotli.NewReader(bytes.NewReader(body)))
	case "gzip":
		value, err := gzip.NewReader(bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		reader = value
	case "zstd":
		value, err := zstd.NewReader(bytes.NewReader(body), zstd.WithDecoderConcurrency(1))
		if err != nil {
			return nil, err
		}
		reader = value.IOReadCloser()
	case "deflate":
		value, err := zlib.NewReader(bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		reader = value
	default:
		return nil, fmt.Errorf("unsupported existing content encoding %q", algorithm)
	}
	defer reader.Close()
	decoded, err := io.ReadAll(io.LimitReader(reader, maximumLength+1))
	if err != nil {
		return nil, err
	}
	if int64(len(decoded)) > maximumLength {
		return nil, fmt.Errorf("decoded body exceeds maximum length")
	}
	return decoded, nil
}

func addVary(header http.Header, value string) {
	for _, current := range header.Values("Vary") {
		for item := range strings.SplitSeq(current, ",") {
			if strings.EqualFold(strings.TrimSpace(item), value) {
				return
			}
		}
	}
	header.Add("Vary", value)
}

func stringSet(values []string) map[string]struct{} {
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		result[strings.ToLower(value)] = struct{}{}
	}
	return result
}

var (
	_ caddy.Provisioner           = (*Handler)(nil)
	_ caddy.Validator             = (*Handler)(nil)
	_ caddyhttp.MiddlewareHandler = (*Handler)(nil)
)
