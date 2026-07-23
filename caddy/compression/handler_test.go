package compression

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
)

type responseHandler struct {
	status int
	header http.Header
	body   []byte
	flush  bool
}

func (h responseHandler) ServeHTTP(w http.ResponseWriter, _ *http.Request) error {
	for key, values := range h.header {
		w.Header()[key] = append([]string(nil), values...)
	}
	if h.status != 0 {
		w.WriteHeader(h.status)
	}
	if len(h.body) > 0 {
		_, _ = w.Write(h.body)
	}
	if h.flush {
		http.NewResponseController(w).Flush()
	}
	return nil
}

func testHandler() Handler {
	h := Handler{
		Extensions:         []string{"html", "json"},
		ExcludedExtensions: []string{"jpg", "gz"},
		MIMETypes:          []string{"application/json", "text/*"},
		MinimumLength:      16,
		MaximumLength:      1024,
		ExcludedPaths:      []string{"/downloads", "/api/export"},
	}
	h.extensions = stringSet(h.Extensions)
	h.excludedExtensions = stringSet(h.ExcludedExtensions)
	return h
}

func serveResponse(t *testing.T, handler Handler, requestPath, acceptEncoding string, response responseHandler) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "http://example.test"+requestPath, nil)
	request.Header.Set("Accept-Encoding", acceptEncoding)
	if err := handler.ServeHTTP(recorder, request, caddyhttp.Handler(response)); err != nil {
		t.Fatal(err)
	}
	return recorder
}

func TestHandlerUsesConfiguredAlgorithmPriority(t *testing.T) {
	body := bytes.Repeat([]byte("compressible response "), 12)
	response := responseHandler{header: http.Header{"Content-Type": {"text/plain"}}, body: body}

	for _, test := range []struct {
		accept string
		want   string
	}{
		{"gzip, deflate, zstd, br", "br"},
		{"gzip, zstd", "gzip"},
		{"zstd, deflate", "zstd"},
		{"deflate", "deflate"},
		{"br;q=0.5, gzip;q=1", "gzip"},
	} {
		recorder := serveResponse(t, testHandler(), "/index.html", test.accept, response)
		if got := recorder.Header().Get("Content-Encoding"); got != test.want {
			t.Fatalf("Accept-Encoding %q: got %q want %q", test.accept, got, test.want)
		}
		decoded, err := decodeBody(test.want, recorder.Body.Bytes(), int64(len(body)))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(decoded, body) {
			t.Fatalf("Accept-Encoding %q: decoded body mismatch", test.accept)
		}
		if got := recorder.Header().Get("Vary"); got != "Accept-Encoding" {
			t.Fatalf("Vary=%q", got)
		}
	}
}

func TestHandlerMatchesExtensionOrMIMEAndHonorsExclusions(t *testing.T) {
	body := bytes.Repeat([]byte("response payload "), 8)
	tests := []struct {
		name        string
		path        string
		contentType string
		compressed  bool
	}{
		{"extension", "/assets/app.json", "application/octet-stream", true},
		{"MIME wildcard", "/resource", "text/css; charset=utf-8", true},
		{"excluded extension wins", "/photo.jpg", "text/plain", false},
		{"excluded exact path", "/api/export", "text/plain", false},
		{"excluded child path", "/downloads/archive.html", "text/html", false},
		{"path prefix boundary", "/downloads-v2/index.html", "text/html", true},
		{"no matcher", "/video.bin", "application/octet-stream", false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := serveResponse(t, testHandler(), test.path, "gzip", responseHandler{
				header: http.Header{"Content-Type": {test.contentType}},
				body:   body,
			})
			if got := recorder.Header().Get("Content-Encoding") != ""; got != test.compressed {
				t.Fatalf("compressed=%t want %t", got, test.compressed)
			}
		})
	}
}

func TestHandlerHonorsLengthNoTransformAndStreaming(t *testing.T) {
	handler := testHandler()
	for _, test := range []struct {
		name       string
		body       []byte
		header     http.Header
		flush      bool
		compressed bool
	}{
		{"below minimum", []byte("short"), http.Header{"Content-Type": {"text/plain"}}, false, false},
		{"above maximum", bytes.Repeat([]byte("x"), 1025), http.Header{"Content-Type": {"text/plain"}}, false, false},
		{"no transform", bytes.Repeat([]byte("x"), 32), http.Header{"Content-Type": {"text/plain"}, "Cache-Control": {"public, no-transform"}}, false, false},
		{"stream flush", bytes.Repeat([]byte("x"), 32), http.Header{"Content-Type": {"text/event-stream"}}, true, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := serveResponse(t, handler, "/index.html", "br", responseHandler{
				header: test.header,
				body:   test.body,
				flush:  test.flush,
			})
			if got := recorder.Header().Get("Content-Encoding") != ""; got != test.compressed {
				t.Fatalf("compressed=%t want %t", got, test.compressed)
			}
			if !test.compressed && !bytes.Equal(recorder.Body.Bytes(), test.body) {
				t.Fatal("uncompressed body changed")
			}
		})
	}
}

func TestHandlerRecompressesSupportedOriginEncoding(t *testing.T) {
	body := bytes.Repeat([]byte("origin compressed payload "), 12)
	gzipped, err := encodeBody("gzip", body)
	if err != nil {
		t.Fatal(err)
	}
	response := responseHandler{
		header: http.Header{
			"Content-Type":     {"text/plain"},
			"Content-Encoding": {"gzip"},
		},
		body: gzipped,
	}

	disabled := testHandler()
	recorder := serveResponse(t, disabled, "/index.html", "br, gzip", response)
	if got := recorder.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("recompression disabled: Content-Encoding=%q", got)
	}
	if !bytes.Equal(recorder.Body.Bytes(), gzipped) {
		t.Fatal("recompression disabled: origin body changed")
	}

	enabled := testHandler()
	enabled.Recompress = true
	enabled.MinimumLength = int64(len(gzipped) + 1)
	recorder = serveResponse(t, enabled, "/index.html", "br, gzip", response)
	if got := recorder.Header().Get("Content-Encoding"); got != "br" {
		t.Fatalf("recompression enabled: Content-Encoding=%q", got)
	}
	decoded, err := decodeBody("br", recorder.Body.Bytes(), int64(len(body)))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(decoded, body) {
		t.Fatal("recompressed body mismatch")
	}
}

func TestHandlerSkipsUnsupportedOrUnacceptedEncodings(t *testing.T) {
	body := bytes.Repeat([]byte("payload "), 8)
	response := responseHandler{header: http.Header{"Content-Type": {"text/plain"}}, body: body}
	for _, accept := range []string{"", "identity", "br;q=0, gzip;q=0", "compress"} {
		recorder := serveResponse(t, testHandler(), "/index.html", accept, response)
		if got := recorder.Header().Get("Content-Encoding"); got != "" {
			t.Fatalf("Accept-Encoding %q: Content-Encoding=%q", accept, got)
		}
		if !bytes.Equal(recorder.Body.Bytes(), body) {
			t.Fatalf("Accept-Encoding %q: body changed", accept)
		}
	}
}
