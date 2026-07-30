package jobs

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"

	"goveto-edge/internal/jobqueue"
	"goveto-edge/internal/rbac"
)

func TestParseKind(t *testing.T) {
	kind, err := parseKind("publish")
	if err != nil || kind != jobqueue.Publish {
		t.Fatalf("parse publish = %q, %v", kind, err)
	}
	if _, err = parseKind("unknown"); err == nil {
		t.Fatal("unknown job kind was accepted")
	}
}

func TestPermissionForSensitiveJobs(t *testing.T) {
	if got := permissionFor(jobqueue.Install); got != rbac.PermissionNodeManage {
		t.Fatalf("install permission = %q", got)
	}
	if got := permissionFor(jobqueue.Certificate); got != rbac.PermissionCertificateManage {
		t.Fatalf("certificate permission = %q", got)
	}
}

func TestParseListParams(t *testing.T) {
	request := httptest.NewRequest("GET", "/jobs?kind=publish&status=failed&query=%20edge%20&page=3&page_size=50", nil)
	context := echo.NewContext(request, httptest.NewRecorder())
	params, err := parseListParams(context)
	if err != nil {
		t.Fatalf("parse list params: %v", err)
	}
	if params.Kind != "PUBLISH" || params.Status != "FAILED" || params.Query != "edge" {
		t.Fatalf("unexpected filters: %#v", params)
	}
	if params.Page != 3 || params.PageSize != 50 {
		t.Fatalf("unexpected pagination: %#v", params)
	}
}

func TestParseListParamsDefaultsAndValidation(t *testing.T) {
	request := httptest.NewRequest("GET", "/jobs?page=0&page_size=101", nil)
	params, err := parseListParams(echo.NewContext(request, httptest.NewRecorder()))
	if err != nil {
		t.Fatalf("parse defaults: %v", err)
	}
	if params.Page != 1 || params.PageSize != 25 {
		t.Fatalf("unexpected defaults: %#v", params)
	}

	tests := []string{
		"/jobs?kind=unknown",
		"/jobs?status=unknown",
		"/jobs?query=" + strings.Repeat("x", 257),
	}
	for _, target := range tests {
		request = httptest.NewRequest("GET", target, nil)
		if _, err = parseListParams(echo.NewContext(request, httptest.NewRecorder())); err == nil {
			t.Fatalf("invalid params were accepted: %s", target)
		}
	}
}

func TestRedactSensitiveInput(t *testing.T) {
	raw := json.RawMessage(`{
		"domains":["example.com"],
		"certificates":[{"certificate":"public","private_key":"private"}],
		"origin_policy":{"headers":{"Authorization":["Bearer value"],"X-Trace":["visible"]}},
		"waf":{"challenge_secret":"secret"}
	}`)
	if err := redactSensitiveInput(&raw); err != nil {
		t.Fatalf("redact input: %v", err)
	}
	text := string(raw)
	for _, secret := range []string{`:"private"`, "Bearer value", `"challenge_secret":"secret"`} {
		if strings.Contains(text, secret) {
			t.Fatalf("sensitive value %q remains in %s", secret, text)
		}
	}
	for _, visible := range []string{"example.com", "public", "X-Trace", "visible", "[REDACTED]"} {
		if !strings.Contains(text, visible) {
			t.Fatalf("expected value %q missing from %s", visible, text)
		}
	}
}
