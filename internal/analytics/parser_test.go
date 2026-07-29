package analytics

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestParseAccess(t *testing.T) {
	payload := []byte(`{
		"ts": 1710000000.25,
		"request": {
			"remote_ip": "192.0.2.1",
			"proto": "HTTP/2.0",
			"method": "GET",
			"host": "example.com",
			"uri": "/assets/app.js?v=1",
			"headers": {
				"User-Agent": ["test"],
				"X-Request-Id": ["req-1"]
			}
		},
		"duration": 0.125,
		"size": 512,
		"status": 200,
		"upstream_address": "origin.example:443",
		"upstream_status": 404,
		"handler_error": "  dial tcp 192.0.2.10:443: connect: connection refused  ",
		"resp_headers": {
			"X-Cache": ["HIT"],
			"Content-Type": ["application/javascript"],
			"X-Goveto-WAF": ["BLOCK"],
			"X-Goveto-WAF-Rule": ["preset:XSS"],
			"X-Goveto-WAF-Source": ["GOVETO_COMPAT:2026.07.1"],
			"X-Goveto-WAF-Match": ["XSS"]
		}
	}`)
	event, err := ParseAccess(payload, "cluster", "node", "site")
	if err != nil {
		t.Fatal(err)
	}
	if event.Path != "/assets/app.js" || event.QueryString != "v=1" || event.FileExtension != "js" {
		t.Fatalf("unexpected URL fields: %#v", event)
	}
	if event.CacheStatus != "HIT" || event.ClientIP.String() != "192.0.2.1" || event.ResponseBodyBytes != 512 {
		t.Fatalf("unexpected event: %#v", event)
	}
	if event.UpstreamAddress != "origin.example:443" || event.UpstreamStatus != 404 ||
		event.HandlerError != "dial tcp 192.0.2.10:443: connect: connection refused" {
		t.Fatalf("upstream diagnostics were not parsed: %#v", event)
	}
	if event.RequestHeaderBytes == 0 || event.ResponseHeaderBytes == 0 {
		t.Fatal("header traffic was not counted")
	}
	if event.WAFAction != "BLOCK" || event.WAFRuleID != "preset:XSS" || event.WAFMatch != "XSS" {
		t.Fatalf("WAF event fields were not parsed: %#v", event)
	}
}

func TestNormalizeHandlerErrorTruncatesValidUTF8(t *testing.T) {
	value := strings.Repeat("a", 2047) + "界"
	got := normalizeHandlerError(value)
	if len(got) > 2048 || !utf8.ValidString(got) {
		t.Fatalf("invalid truncated handler error: bytes=%d valid=%t", len(got), utf8.ValidString(got))
	}
}

func TestParseAccessUnmapsIPv4MappedClientAddress(t *testing.T) {
	payload := []byte(`{
		"request":{"client_ip":"::ffff:192.168.4.23","method":"GET","host":"example.com","uri":"/"},
		"status":200
	}`)
	event, err := ParseAccess(payload, "cluster", "node", "site")
	if err != nil {
		t.Fatal(err)
	}
	if got := event.ClientIP.String(); got != "192.168.4.23" {
		t.Fatalf("client IP = %q, want native IPv4", got)
	}
}
