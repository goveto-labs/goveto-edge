package captcha

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func TestVerifierUsesFixedProviderEndpointAndForm(t *testing.T) {
	for _, test := range []struct {
		provider string
		host     string
		path     string
	}{
		{provider: ProviderCloudflare, host: "challenges.cloudflare.com", path: "/turnstile/v0/siteverify"},
		{provider: ProviderRecaptcha, host: "www.google.com", path: "/recaptcha/api/siteverify"},
	} {
		t.Run(test.provider, func(t *testing.T) {
			verifier := &Verifier{client: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
				if request.Method != http.MethodPost || request.URL.Host != test.host || request.URL.Path != test.path {
					t.Fatalf("verification request = %s %s", request.Method, request.URL)
				}
				body, err := io.ReadAll(request.Body)
				if err != nil {
					t.Fatal(err)
				}
				form, err := url.ParseQuery(string(body))
				if err != nil {
					t.Fatal(err)
				}
				if form.Get("secret") != "secret" || form.Get("response") != "token" || form.Get("remoteip") != "192.0.2.1" {
					t.Fatalf("verification form = %v", form)
				}
				return captchaResponse(http.StatusOK, `{"success":true}`), nil
			})}}

			valid, err := verifier.Verify(context.Background(), test.provider, "secret", "token", "192.0.2.1")
			if err != nil || !valid {
				t.Fatalf("Verify() = (%v, %v), want (true, nil)", valid, err)
			}
		})
	}
}

func TestVerifierRejectsInvalidAndOversizedResponses(t *testing.T) {
	for _, test := range []struct {
		name       string
		provider   string
		statusCode int
		body       string
	}{
		{name: "unsupported provider", provider: "custom"},
		{name: "provider error", provider: ProviderCloudflare, statusCode: http.StatusBadGateway},
		{name: "oversized response", provider: ProviderCloudflare, statusCode: http.StatusOK, body: `{"padding":"` + strings.Repeat("x", maxResponseBytes) + `","success":true}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			verifier := &Verifier{client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return captchaResponse(test.statusCode, test.body), nil
			})}}
			if valid, err := verifier.Verify(context.Background(), test.provider, "secret", "token", ""); err == nil || valid {
				t.Fatalf("Verify() = (%v, %v), want (false, error)", valid, err)
			}
		})
	}
}

func captchaResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Status:     http.StatusText(status),
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
	}
}
