package certmanager

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"goveto-edge/internal/outboundhttp"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func TestValidateACMEDirectoryRejectsUnsafeDestinations(t *testing.T) {
	service := New(nil, nil, nil)
	for _, directory := range []string{
		"http://8.8.8.8/directory",
		"https://127.0.0.1/directory",
		"https://169.254.169.254/latest/meta-data",
		"https://[::1]/directory",
		"https://user:password@8.8.8.8/directory",
	} {
		t.Run(directory, func(t *testing.T) {
			if err := service.ValidateACMEDirectory(context.Background(), directory); err == nil {
				t.Fatalf("unsafe ACME directory %q was accepted", directory)
			}
		})
	}
	if err := service.ValidateACMEDirectory(context.Background(), "https://8.8.8.8/directory"); err != nil {
		t.Fatalf("public HTTPS directory rejected: %v", err)
	}
}

func TestACMERoundTripperRejectsPrivateAdvertisedEndpoint(t *testing.T) {
	called := false
	transport := acmeRoundTripper{
		policy: outboundhttp.NewPolicy(),
		next: roundTripFunc(func(*http.Request) (*http.Response, error) {
			called = true
			return nil, nil
		}),
	}
	request, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "https://127.0.0.1/new-order", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = transport.RoundTrip(request); err == nil {
		t.Fatal("private ACME endpoint was accepted")
	}
	if called {
		t.Fatal("request reached the underlying transport")
	}
}

func TestACMERoundTripperLimitsResponseBody(t *testing.T) {
	transport := acmeRoundTripper{
		policy: outboundhttp.NewPolicy(),
		next: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(strings.Repeat("x", maxACMEResponseBytes+1))),
				Header:     make(http.Header),
			}, nil
		}),
	}
	request, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://8.8.8.8/directory", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := transport.RoundTrip(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if _, err = io.ReadAll(response.Body); err == nil {
		t.Fatal("oversized ACME response body was accepted")
	}
}
