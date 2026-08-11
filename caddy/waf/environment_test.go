package waf

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func captchaRequest(target, address string) *http.Request {
	request := httptest.NewRequest(http.MethodGet, target, nil)
	request.RemoteAddr = address + ":1234"
	request.Header.Set("User-Agent", "Mozilla/5.0 Chrome/136.0 Safari/537.36")
	return request
}

func testBrowserEnvironment(request *http.Request) browserEnvironment {
	return browserEnvironment{
		Version: browserEnvironmentVersion, UserAgent: request.UserAgent(), Platform: "MacIntel", Vendor: "Google Inc.",
		Languages: []string{"en-US"}, Timezone: "UTC", Cookies: true, Worker: true, WebAssembly: true, Crypto: true,
		Canvas: true, WebGL: true, WebGLRenderer: "Apple GPU", LocalStorage: true, SessionStorage: true, IndexedDB: true,
		HardwareConcurrency: 8, ScreenWidth: 1440, ScreenHeight: 900, ColorDepth: 24, OuterWidth: 1440, OuterHeight: 900,
	}
}

func TestBrowserEnvironmentRejectsHighConfidenceAutomation(t *testing.T) {
	request := captchaRequest("http://example.test/protected", "192.0.2.1")
	for _, mutate := range []func(*browserEnvironment){
		func(environment *browserEnvironment) { environment.Webdriver = true },
		func(environment *browserEnvironment) { environment.AutomationSignals = []string{"domAutomation"} },
		func(environment *browserEnvironment) { environment.UserAgent = "Mozilla/5.0 HeadlessChrome/136.0" },
	} {
		environment := testBrowserEnvironment(request)
		mutate(&environment)
		if result := assessBrowserEnvironment(request, environment); result.Accepted {
			t.Fatalf("automation environment was accepted: %#v", result)
		}
	}
}

func TestBrowserEnvironmentUsesRiskScoreForPrivacyRestrictions(t *testing.T) {
	request := captchaRequest("http://example.test/protected", "192.0.2.1")
	environment := testBrowserEnvironment(request)
	environment.WebGL = false
	environment.WebGLRenderer = ""
	environment.LocalStorage = false
	environment.SessionStorage = false
	environment.IndexedDB = false
	result := assessBrowserEnvironment(request, environment)
	if !result.Accepted || result.Score == 0 {
		t.Fatalf("moderately restricted browser should be accepted with risk: %#v", result)
	}
}

func TestBrowserEnvironmentRejectsRequestMismatch(t *testing.T) {
	request := captchaRequest("http://example.test/protected", "192.0.2.1")
	environment := testBrowserEnvironment(request)
	environment.UserAgent = "different"
	if result := assessBrowserEnvironment(request, environment); result.Accepted {
		t.Fatalf("mismatched environment accepted: %#v", result)
	}
}
