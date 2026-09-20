package httpconditional

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

const testDate = "Wed, 16 Sep 2026 10:00:00 GMT"

func TestStatus(t *testing.T) {
	for _, test := range []struct {
		name    string
		method  string
		headers http.Header
		etag    string
		status  int
	}{
		{"none match", "GET", http.Header{"If-None-Match": {`"v1"`}}, `"v1"`, 304},
		{"none match head", "HEAD", http.Header{"If-None-Match": {`"v1"`}}, `"v1"`, 304},
		{"none match post", "POST", http.Header{"If-None-Match": {`"v1"`}}, `"v1"`, 412},
		{"none match list", "GET", http.Header{"If-None-Match": {`"other", "v1"`}}, `"v1"`, 304},
		{"none match wildcard", "GET", http.Header{"If-None-Match": {"*"}}, `"v1"`, 304},
		{"weak none match", "GET", http.Header{"If-None-Match": {`W/"v1"`}}, `"v1"`, 304},
		{"weak none match strong current", "GET", http.Header{"If-None-Match": {`"v1"`}}, `W/"v1"`, 304},
		{"weak if match", "GET", http.Header{"If-Match": {`W/"v1"`}}, `"v1"`, 412},
		{"if match", "GET", http.Header{"If-Match": {`"v1"`}}, `"v1"`, 0},
		{"if match list", "GET", http.Header{"If-Match": {`"other", "v1"`}}, `"v1"`, 0},
		{"if match mismatch", "GET", http.Header{"If-Match": {`"other"`}}, `"v1"`, 412},
		{"if match wildcard", "GET", http.Header{"If-Match": {"*"}}, `"v1"`, 0},
		// If-Match: * passes while a stored representation exists, even without an ETag (RFC 9110 section 13.1.1).
		{"if match wildcard no etag", "GET", http.Header{"If-Match": {"*"}}, "", 0},
		{"if match precedence over none match", "GET", http.Header{"If-Match": {`"other"`}, "If-None-Match": {`"v1"`}}, `"v1"`, 412},
		{"if match precedence over unmodified since", "GET", http.Header{"If-Match": {`"v1"`}, "If-Unmodified-Since": {"Tue, 15 Sep 2026 10:00:00 GMT"}}, `"v1"`, 0},
		{"unmodified since stale", "GET", http.Header{"If-Unmodified-Since": {"Tue, 15 Sep 2026 10:00:00 GMT"}}, `"v1"`, 412},
		{"unmodified since fresh", "GET", http.Header{"If-Unmodified-Since": {testDate}}, `"v1"`, 0},
		{"modified since", "GET", http.Header{"If-Modified-Since": {testDate}}, `"v1"`, 304},
		{"modified since older", "GET", http.Header{"If-Modified-Since": {"Tue, 15 Sep 2026 10:00:00 GMT"}}, `"v1"`, 0},
		{"modified since post", "POST", http.Header{"If-Modified-Since": {testDate}}, `"v1"`, 0},
		{"none match precedence over modified since", "GET", http.Header{"If-None-Match": {`"other"`}, "If-Modified-Since": {testDate}}, `"v1"`, 0},
		{"unclosed quote", "GET", http.Header{"If-None-Match": {`"v1`}}, `"v1"`, 0},
		{"bare token in list", "GET", http.Header{"If-None-Match": {`"other", oops`}}, `"v1"`, 0},
		{"bare token first", "GET", http.Header{"If-None-Match": {`oops, "v1"`}}, `"v1"`, 0},
		{"empty header", "GET", http.Header{"If-None-Match": {""}}, `"v1"`, 0},
		{"commas only", "GET", http.Header{"If-None-Match": {", ,"}}, `"v1"`, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(test.method, "http://example.test/asset", nil)
			for name, values := range test.headers {
				request.Header[name] = values
			}
			response := &http.Response{StatusCode: 200, Header: http.Header{"Last-Modified": {testDate}}}
			if test.etag != "" {
				response.Header.Set("ETag", test.etag)
			}
			if status := Status(request, response); status != test.status {
				t.Fatalf("status=%d want=%d", status, test.status)
			}
		})
	}
}

func TestStatusGuards(t *testing.T) {
	request := httptest.NewRequest("GET", "http://example.test/asset", nil)
	request.Header.Set("If-None-Match", `"v1"`)
	response := &http.Response{StatusCode: 200, Header: http.Header{"ETag": {`"v1"`}}}
	if status := Status(nil, response); status != 0 {
		t.Fatalf("nil request: status=%d", status)
	}
	if status := Status(request, nil); status != 0 {
		t.Fatalf("nil response: status=%d", status)
	}
	response.StatusCode = 404
	if status := Status(request, response); status != 0 {
		t.Fatalf("non-2xx response: status=%d", status)
	}
}

func TestRangeAllowed(t *testing.T) {
	for _, test := range []struct {
		name         string
		value        string
		lastModified string
		allowed      bool
	}{
		{"absent", "", testDate, true},
		{"etag", `"v1"`, testDate, true},
		{"etag mismatch", `"other"`, testDate, false},
		{"weak etag", `W/"v1"`, testDate, false},
		{"date equal", testDate, testDate, true},
		{"date older", "Tue, 15 Sep 2026 10:00:00 GMT", testDate, false},
		{"date invalid", "not a date", testDate, false},
		{"date without last modified", testDate, "", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest("GET", "http://example.test/asset", nil)
			if test.value != "" {
				request.Header.Set("If-Range", test.value)
			}
			header := http.Header{}
			header.Set("ETag", `"v1"`)
			if test.lastModified != "" {
				header.Set("Last-Modified", test.lastModified)
			}
			if allowed := RangeAllowed(request, header); allowed != test.allowed {
				t.Fatalf("allowed=%v want=%v", allowed, test.allowed)
			}
		})
	}
}
