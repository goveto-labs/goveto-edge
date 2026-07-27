package splitmatch

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHeaderAndCookieConditions(t *testing.T) {
	request := httptest.NewRequest("GET", "http://example.test/", nil)
	request.Header.Set("X-Variant", "canary")
	request.AddCookie(&http.Cookie{Name: "cohort", Value: "canary"})
	matcher := Matcher{HeaderName: "X-Variant", CookieName: "cohort", Value: "canary"}
	if !matcher.Match(request) {
		t.Fatal("expected matching header and cookie to select split")
	}
	request.Header.Set("X-Variant", "stable")
	if matcher.Match(request) {
		t.Fatal("mismatched header selected split")
	}
}

func TestPercentageIsStable(t *testing.T) {
	matcher := Matcher{Percentage: 35, Salt: "site:experiment"}
	request := httptest.NewRequest("GET", "http://example.test/", nil)
	request.RemoteAddr = "192.0.2.10:1234"
	first := matcher.Match(request)
	for range 20 {
		if matcher.Match(request) != first {
			t.Fatal("percentage selection changed for one identity")
		}
	}
}

func TestClientIdentityDoesNotDependOnSourcePort(t *testing.T) {
	first := httptest.NewRequest("GET", "http://example.test/", nil)
	first.RemoteAddr = "192.0.2.10:1234"
	first.Header.Set("User-Agent", "test-agent")
	second := first.Clone(first.Context())
	second.RemoteAddr = "192.0.2.10:5678"
	if clientIdentity(first) != clientIdentity(second) {
		t.Fatalf("source port changed client identity: %q != %q", clientIdentity(first), clientIdentity(second))
	}
}

func TestClientIdentityUsesFirstForwardedAddress(t *testing.T) {
	request := httptest.NewRequest("GET", "http://example.test/", nil)
	request.RemoteAddr = "192.0.2.10:1234"
	request.Header.Set("X-Forwarded-For", " 198.51.100.8, 192.0.2.20 ")
	if identity := clientIdentity(request); identity != "198.51.100.8\x00" {
		t.Fatalf("forwarded client identity = %q", identity)
	}
}
