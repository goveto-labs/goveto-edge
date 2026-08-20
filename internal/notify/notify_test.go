package notify

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"testing"
	"time"

	"goveto-edge/internal/outboundhttp"
)

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestValidateURLRejectsUnsafeServicesAndGenericOptions(t *testing.T) {
	for _, rawURL := range []string{
		"googlechat://127.0.0.1/?key=value&token=value",
		"teams://tenant/a/b?host=127.0.0.1",
		"generic+https://example.com/hook?method=DELETE",
		"generic+https://example.com/hook?%40Host=internal.example",
	} {
		if _, err := ValidateURL(rawURL); err == nil {
			t.Fatalf("unsafe notification URL %q was accepted", rawURL)
		}
	}
}

// HTTP is permitted for public destinations; SSRF protection is enforced by
// the outbound policy at delivery time, not by rejecting the scheme here.
func TestValidateURLAcceptsHTTPForPublicHosts(t *testing.T) {
	for _, rawURL := range []string{
		"generic+http://example.com/hook",
		"generic+https://example.com/hook?disabletls=yes",
		"generic://example.com/hook",
	} {
		if _, err := ValidateURL(rawURL); err != nil {
			t.Fatalf("public HTTP notification URL %q rejected: %v", rawURL, err)
		}
	}
}

// A Gotify URL without a token must be rejected (not panic) at validation time.
func TestValidateURLRejectsGotifyWithoutToken(t *testing.T) {
	for _, raw := range []string{"gotify://gotify.example.com/", "gotify://gotify.example.com"} {
		if _, err := ValidateURL(raw); err == nil {
			t.Fatalf("gotify URL without token was accepted: %q", raw)
		}
	}
}

// Preset templates in frontend/src/utils/notificationTemplates.ts compose URLs
// of exactly these shapes; keep them accepted by the shared validator.
func TestValidateURLAcceptsConsolePresetTemplates(t *testing.T) {
	for service, rawURL := range map[string]string{
		"discord":  "discord://webhook-token@693853386302554172",
		"slack":    "slack://hook:T000000000-B000000000-XXXXXXXXXXXXXXXXXXXXXXXX@webhook",
		"telegram": "telegram://110201543:AAHdqTcvCH1vGWJxfSeofSAs0K5PALDsaw@telegram?chats=@alerts",
		"smtp":     "smtp://user:password@smtp.example.com:25/?from=alerts@example.com&to=ops@example.com",
		"ntfy":     "ntfy://ntfy.sh/goveto-alerts",
		"gotify":   "gotify://gotify.example.com/AzyoeNS.D4iJLVa",
		"pushover": "pushover://shoutrrr:api-token@user-key/",
		"bark":     "bark://:device-key@api.day.app",
		"generic":  "generic+https://example.com/api/v1/alerts",
	} {
		got, err := ValidateURL(rawURL)
		if err != nil {
			t.Fatalf("preset %s URL %q rejected: %v", service, rawURL, err)
		}
		if got != service {
			t.Fatalf("preset %s URL reported service %q", service, got)
		}
	}
}

func TestDeliverGenericRejectsPrivateDestination(t *testing.T) {
	target, err := url.Parse("generic+http://127.0.0.1/webhook")
	if err != nil {
		t.Fatal(err)
	}
	if err = deliverGeneric(context.Background(), target, "message", "title"); err == nil {
		t.Fatal("private webhook destination was accepted")
	}
}

// An operator-curated allowlist is the only way a private destination is
// accepted; verify the request then actually reaches the (stubbed) endpoint.
func TestDeliverGenericAllowsAllowlistedPrivateDestination(t *testing.T) {
	previousPolicy := outboundPolicy
	previousClient := httpClient
	t.Cleanup(func() {
		outboundPolicy = previousPolicy
		httpClient = previousClient
	})
	outboundPolicy = outboundhttp.NewPolicyWithAllowlist([]netip.Prefix{
		netip.MustParsePrefix("10.0.0.0/8"),
	})

	called := false
	stub := &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		called = true
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("ok")), Header: make(http.Header)}, nil
	})}
	target, err := url.Parse("generic+http://10.0.0.5/webhook")
	if err != nil {
		t.Fatal(err)
	}
	if err = deliverGenericWithClient(context.Background(), stub, target, "message", "title"); err != nil {
		t.Fatalf("allowlisted private destination rejected: %v", err)
	}
	if !called {
		t.Fatal("request was not dispatched to the allowlisted private destination")
	}
}

func TestDeliverGenericUsesBoundedClient(t *testing.T) {
	client := &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(strings.Repeat("x", ResponseLimit+1))),
		}, nil
	})}
	target, err := url.Parse("generic+https://8.8.8.8/hook")
	if err != nil {
		t.Fatal(err)
	}
	if err = deliverGenericWithClient(context.Background(), client, target, "message", "title"); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("oversized response error = %v", err)
	}
}

func TestDeliverGenericRejectsUnsafeHeader(t *testing.T) {
	target, err := url.Parse("generic+https://8.8.8.8/hook?%40Host=internal.example")
	if err != nil {
		t.Fatal(err)
	}
	if err = deliverGeneric(context.Background(), target, "message", "title"); err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("unsafe header error = %v", err)
	}
}

func TestCustomHostHTTPRequestDerivesScheme(t *testing.T) {
	tests := []struct {
		name       string
		service    string
		rawURL     string
		wantScheme string
	}{
		{name: "bark default https", service: "bark", rawURL: "bark://:device-key@api.day.app", wantScheme: "https"},
		{name: "gotify default https", service: "gotify", rawURL: "gotify://gotify.example.com/AzyoeNS.D4iJLVa", wantScheme: "https"},
		{name: "gotify plus http", service: "gotify", rawURL: "gotify+http://gotify.example.com/AzyoeNS.D4iJLVa", wantScheme: "http"},
		{name: "ntfy disabletls", service: "ntfy", rawURL: "ntfy://ntfy.sh/goveto-alerts?disabletls=yes", wantScheme: "http"},
		{name: "ntfy plus https", service: "ntfy", rawURL: "ntfy+https://ntfy.sh/goveto-alerts", wantScheme: "https"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			parsed, err := url.Parse(test.rawURL)
			if err != nil {
				t.Fatal(err)
			}
			target, _, _, err := customHostHTTPRequest(test.service, parsed, "message", "title")
			if err != nil {
				t.Fatal(err)
			}
			if target.Scheme != test.wantScheme || target.Hostname() != parsed.Hostname() {
				t.Fatalf("target = %s, want scheme %s", target, test.wantScheme)
			}
		})
	}
}

func TestGotifyRootPathUsesSingleSlash(t *testing.T) {
	parsed, err := url.Parse("gotify://gotify.example.com/secret-token")
	if err != nil {
		t.Fatal(err)
	}
	target, _, _, err := customHostHTTPRequest("gotify", parsed, "message", "title")
	if err != nil {
		t.Fatal(err)
	}
	if target.Path != "/message" {
		t.Fatalf("Gotify path = %q, want /message", target.Path)
	}
}

func TestSafeErrorMessageRedactsURLCredentials(t *testing.T) {
	secretURL := "https://gotify.example.com/message?token=top-secret-token"
	err := &url.Error{Op: "Post", URL: secretURL, Err: errors.New("connection refused")}
	message := SafeErrorMessage(fmt.Errorf("%w: %w", ErrDeliveryFailed, err))
	if strings.Contains(message, "top-secret-token") || strings.Contains(message, secretURL) {
		t.Fatalf("SafeErrorMessage leaked URL credentials: %q", message)
	}
	if message != "notification network request failed" {
		t.Fatalf("SafeErrorMessage = %q", message)
	}
}

func TestParseMailboxesRejectsHeaderInjection(t *testing.T) {
	if _, err := parseMailboxes("ops@example.com\r\nBcc: attacker@example.com"); err == nil {
		t.Fatal("SMTP recipient header injection was accepted")
	}
}

func TestSendWrapsInvalidURL(t *testing.T) {
	err := Send(context.Background(), "unknown://token", Message{Title: "t", Body: "b"})
	if !errors.Is(err, ErrInvalidURL) {
		t.Fatalf("invalid URL error = %v, want ErrInvalidURL", err)
	}
}

func TestSendCongestionPreservesSentinel(t *testing.T) {
	for range cap(sendSlots) {
		sendSlots <- struct{}{}
	}
	t.Cleanup(func() {
		for range cap(sendSlots) {
			<-sendSlots
		}
	})
	err := Send(context.Background(), "logger://", Message{Timeout: time.Millisecond})
	if !errors.Is(err, ErrDeliveryFailed) || !errors.Is(err, ErrSendCongestion) {
		t.Fatalf("congestion error = %v, want delivery and congestion sentinels", err)
	}
}

func TestBoundedSendTimeout(t *testing.T) {
	for _, test := range []struct {
		name      string
		requested time.Duration
		want      time.Duration
	}{
		{name: "default", requested: 0, want: SendTimeout},
		{name: "negative", requested: -time.Second, want: SendTimeout},
		{name: "shorter", requested: time.Second, want: time.Second},
		{name: "over limit", requested: SendTimeout + time.Second, want: SendTimeout},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := boundedSendTimeout(test.requested); got != test.want {
				t.Fatalf("boundedSendTimeout(%s) = %s, want %s", test.requested, got, test.want)
			}
		})
	}
}
