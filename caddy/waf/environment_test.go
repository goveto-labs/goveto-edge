package waf

import "testing"

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
