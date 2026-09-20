package waf

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"goveto-edge/internal/policy"
)

func TestBuiltinsInspectEncodedBodies(t *testing.T) {
	for _, test := range []struct{ name, contentType, body string }{
		{"form", "application/x-www-form-urlencoded", "q=%3Cscript%3Ealert%281%29%3C%2Fscript%3E"},
		{"json", "application/json", `{"q":"\u003cscript\u003ealert(1)\u003c/script\u003e"}`},
		{"nested json", "application/problem+json", `{"q":[{"value":"\u003cscript\u003e"}]}`},
		{"duplicate json keys", "application/json", `{"q":"\u003cscript\u003e","q":"safe"}`},
		{"multipart", "multipart/form-data; boundary=boundary", "--boundary\r\nContent-Disposition: form-data; name=\"q\"\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n=3Cscript=3Ealert(1)=3C/script=3E\r\n--boundary--\r\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := provisionHandler(t, "encoded-"+test.name, policy.DefaultWAFPolicy())
			request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(test.body))
			request.Header.Set("Content-Type", test.contentType)
			response, next := httptest.NewRecorder(), &bodyCaptureHandler{}
			if err := h.ServeHTTP(response, request, next); err != nil {
				t.Fatal(err)
			}
			if response.Code != http.StatusForbidden || next.calls != 0 {
				t.Fatalf("status=%d origin calls=%d", response.Code, next.calls)
			}
		})
	}
}

func TestBodyDecodingPreservesOriginBytes(t *testing.T) {
	h := provisionHandler(t, "preserve-body", policy.DefaultWAFPolicy())
	body := `{"q":"hello\u0020world"}`
	request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	next := &bodyCaptureHandler{}
	if err := h.ServeHTTP(httptest.NewRecorder(), request, next); err != nil {
		t.Fatal(err)
	}
	if next.calls != 1 || next.body != body {
		t.Fatalf("origin calls=%d body=%q", next.calls, next.body)
	}
}

func TestUnsupportedContentEncodingBlocked(t *testing.T) {
	h := provisionHandler(t, "unsupported-encoding", policy.DefaultWAFPolicy())
	for _, encoding := range []string{"gzip", "br"} {
		t.Run(encoding, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/submit", strings.NewReader("name=value"))
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			request.Header.Set("Content-Encoding", encoding)
			response, next := httptest.NewRecorder(), &bodyCaptureHandler{}
			if err := h.ServeHTTP(response, request, next); err != nil {
				t.Fatal(err)
			}
			if response.Code != http.StatusUnsupportedMediaType || next.calls != 0 {
				t.Fatalf("status=%d origin calls=%d", response.Code, next.calls)
			}
			header := response.Header()
			if header.Get("X-Goveto-WAF") != "BLOCK" || header.Get("X-Goveto-WAF-Rule") != "request-body-encoding" ||
				header.Get("X-Goveto-WAF-Source") != "body_inspection" || header.Get("X-Goveto-WAF-Match") != "unsupported_content_encoding" {
				t.Fatalf("headers=%v", header)
			}
			if header.Get("Cache-Control") != "private, no-store" || header.Get("X-Content-Type-Options") != "nosniff" {
				t.Fatalf("headers=%v", header)
			}
		})
	}

	t.Run("identity passes through", func(t *testing.T) {
		request := httptest.NewRequest(http.MethodPost, "/submit", strings.NewReader("name=value"))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		request.Header.Set("Content-Encoding", "identity")
		response, next := httptest.NewRecorder(), &bodyCaptureHandler{}
		if err := h.ServeHTTP(response, request, next); err != nil {
			t.Fatal(err)
		}
		if response.Code != http.StatusOK || next.calls != 1 {
			t.Fatalf("status=%d origin calls=%d", response.Code, next.calls)
		}
	})
}

func TestBodyConditionScopesDecodedCandidatesToFieldName(t *testing.T) {
	body := "safe=ok&other=blocked"
	request := httptest.NewRequest(http.MethodPost, "/submit", strings.NewReader(body))
	data := requestData{
		request:    request,
		body:       body,
		bodyValues: decodedBodyCandidates("application/x-www-form-urlencoded", body),
	}
	scoped := requestValues(policy.WAFCondition{Field: "BODY", FieldName: "safe"}, data)
	for _, candidate := range scoped {
		if candidate.name != "BODY:safe" {
			t.Fatalf("unscoped candidate %q", candidate.name)
		}
		if strings.Contains(candidate.value, "blocked") {
			t.Fatalf("scoped candidates include %q", candidate.value)
		}
	}
	if len(scoped) == 0 {
		t.Fatal("named field produced no candidates")
	}
	unscoped := requestValues(policy.WAFCondition{Field: "BODY"}, data)
	if len(unscoped) <= len(scoped) {
		t.Fatalf("unscoped=%d scoped=%d", len(unscoped), len(scoped))
	}
}

func TestDecodedBodyCandidatesMalformedInput(t *testing.T) {
	for _, test := range []struct {
		name, contentType, body string
	}{
		{"multipart missing boundary", "multipart/form-data", "--boundary\r\nContent-Disposition: form-data; name=\"q\"\r\n\r\nvalue\r\n--boundary--\r\n"},
		{"multipart truncated mid part", "multipart/form-data; boundary=boundary", "--boundary\r\nContent-Disposition: form-data; name=\"q\"\r\n\r\npartial"},
		{"multipart truncated headers", "multipart/form-data; boundary=boundary", "--boundary\r\nContent-Dispos"},
		{"deeply nested json", "application/json", strings.Repeat(`{"a":`, 5000) + `"v"` + strings.Repeat("}", 5000)},
		{"truncated json", "application/json", `{"a":["unterminated`},
	} {
		t.Run(test.name, func(t *testing.T) {
			// Malformed input must degrade to whatever decoded, never panic.
			_ = decodedBodyCandidates(test.contentType, test.body)
		})
	}
}

func TestDecodedBodyCandidatesSensitivityFollowsFieldName(t *testing.T) {
	candidates := decodedBodyCandidates("application/x-www-form-urlencoded", "password=hunter2&note=hello")
	sensitiveByName := map[string]bool{}
	for _, candidate := range candidates {
		sensitiveByName[candidate.name] = sensitiveByName[candidate.name] || candidate.sensitive
	}
	if !sensitiveByName["BODY:password"] {
		t.Fatal("password field was not marked sensitive")
	}
	if sensitiveByName["BODY:note"] {
		t.Fatal("note field was marked sensitive")
	}
}
